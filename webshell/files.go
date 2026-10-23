package webshell

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	fileChunkSize = 128 << 10
	maxReadBytes  = 4 << 20
)

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// powershellCommand encodes the script as UTF-16LE/base64 so no cmd.exe quoting
// rules can mangle paths or base64 payloads. The prefix suppresses progress
// records (which otherwise surface as "#< CLIXML" on stderr and, once a shell
// merges stderr into stdout, corrupt the base64 file payloads) and forces UTF-8
// console output so non-ASCII paths survive the round trip.
func powershellCommand(script string) string {
	script = "$ProgressPreference='SilentlyContinue';[Console]::OutputEncoding=[Text.Encoding]::UTF8;" + script
	u16 := utf16.Encode([]rune(script))
	buf := make([]byte, len(u16)*2)
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	return "powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -OutputFormat Text -EncodedCommand " + base64.StdEncoding.EncodeToString(buf)
}

func (c *Client) List(ctx context.Context, dir string) (*FileListing, error) {
	if err := c.EnsureOS(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	osType := c.osType
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "."
	}
	var cmd string
	if osType == "windows" {
		script := fmt.Sprintf(`$ErrorActionPreference='SilentlyContinue'
$p=%s
if (-not (Test-Path -LiteralPath $p)) { Write-Output '__WS_ERR__:路径不存在'; exit }
Set-Location -LiteralPath $p
Write-Output ('__WS_PWD__' + (Get-Location).Path)
Get-ChildItem -LiteralPath $p -Force | ForEach-Object {
  $t = if ($_.PSIsContainer) { 'd' } else { 'f' }
  $s = if ($_.PSIsContainer) { 0 } else { $_.Length }
  $m = [int64]([DateTimeOffset]$_.LastWriteTimeUtc).ToUnixTimeSeconds()
  Write-Output ($t + [char]9 + $s + [char]9 + $m + [char]9 + $_.Name)
}`, psQuote(dir))
		cmd = powershellCommand(script)
	} else {
		script := fmt.Sprintf(`cd %s 2>/dev/null || { echo '__WS_ERR__:目录不存在或无权限'; exit 0; }
printf '__WS_PWD__%%s\n' "$(pwd)"
for f in * .[!.]* ..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  if [ -d "$f" ]; then t=d; else t=f; fi
  s=$(wc -c < "$f" 2>/dev/null || echo 0)
  m=$(date -r "$f" +%%s 2>/dev/null || echo 0)
  printf '%%s\t%%s\t%%s\t%%s\n' "$t" "$s" "$m" "$f"
done`, shQuote(dir))
		cmd = script
	}
	res, err := c.executeLocked(ctx, cmd)
	if err != nil {
		return nil, err
	}
	listing, err := parseListing(res.Output, dir, osType)
	if err != nil {
		return nil, err
	}
	listing.Transport = c.transport
	return listing, nil
}

func parseListing(raw, requested, osType string) (*FileListing, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := &FileListing{Path: requested, OSType: osType, Entries: []FileEntry{}, Raw: trimForDisplay(raw)}
	seenPwd := false
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "__WS_ERR__") {
			msg := strings.TrimPrefix(line, "__WS_ERR__")
			msg = strings.TrimPrefix(msg, ":")
			if msg == "" {
				msg = "目录不存在或无权限"
			}
			return nil, errors.New(msg)
		}
		if strings.HasPrefix(line, "__WS_PWD__") {
			out.Path = strings.TrimSpace(strings.TrimPrefix(line, "__WS_PWD__"))
			seenPwd = true
			continue
		}
		if !seenPwd {
			// Ignore shell warning text emitted before the pwd marker; only the
			// marker line is authoritative for the current directory.
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		mtime, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		name := parts[3]
		if name == "" || name == "." || name == ".." {
			continue
		}
		out.Entries = append(out.Entries, FileEntry{
			Name:  name,
			Path:  joinRemote(out.Path, name, osType),
			Dir:   parts[0] == "d",
			Size:  size,
			MTime: mtime,
		})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Dir != out.Entries[j].Dir {
			return out.Entries[i].Dir
		}
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	out.Parent = parentRemote(out.Path, osType)
	return out, nil
}

func joinRemote(base, name, osType string) string {
	if base == "" || base == "." {
		return name
	}
	if osType == "windows" {
		if strings.HasSuffix(base, `\`) || strings.HasSuffix(base, "/") {
			return base + name
		}
		return base + `\` + name
	}
	if base == "/" {
		return "/" + name
	}
	return strings.TrimRight(base, "/") + "/" + name
}

func parentRemote(p, osType string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || p == "/" || p == `\` {
		return ""
	}
	if osType == "windows" {
		p = strings.TrimRight(p, `\/`)
		if len(p) == 2 && p[1] == ':' {
			return ""
		}
		for i := len(p) - 1; i >= 0; i-- {
			if p[i] == '\\' || p[i] == '/' {
				if i == 2 && p[1] == ':' {
					return p[:3]
				}
				return p[:i]
			}
		}
		return ""
	}
	parent := path.Dir(p)
	if parent == "." || parent == "/" {
		return ""
	}
	return parent
}

// ReadFile returns a base64 chunk of a remote file. offset is in bytes.
func (c *Client) ReadFile(ctx context.Context, file string, offset int64, length int) (*FileData, error) {
	if err := c.EnsureOS(ctx); err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if length <= 0 || length > maxReadBytes {
		length = maxReadBytes
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	osType := c.osType
	var cmd string
	if osType == "windows" {
		script := fmt.Sprintf(`$ErrorActionPreference='SilentlyContinue'
$p=%s
if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { Write-Output '__WS_ERR__:不是文件'; exit }
$b=[IO.File]::ReadAllBytes($p)
Write-Output ('__WS_SIZE__' + $b.Length)
$s=%d
$l=[Math]::Min(%d,$b.Length-$s)
if ($l -gt 0) { [Convert]::ToBase64String($b,$s,$l) }`, psQuote(file), offset, length)
		cmd = powershellCommand(script)
	} else {
		cmd = fmt.Sprintf(`[ -f %s ] || { echo '__WS_ERR__:不是文件'; exit 0; }
s=$(wc -c < %s 2>/dev/null || echo 0)
echo "__WS_SIZE__$s"
tail -c +%d %s | head -c %d | base64`, shQuote(file), shQuote(file), offset+1, shQuote(file), length)
	}
	res, err := c.executeLocked(ctx, cmd)
	if err != nil {
		return nil, err
	}
	out := &FileData{Path: file, Offset: offset, OSType: osType}
	var data strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(res.Output, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "__WS_ERR__") {
			return nil, errors.New(strings.TrimPrefix(trimmed, "__WS_ERR__:"))
		}
		if strings.HasPrefix(trimmed, "__WS_SIZE__") {
			out.Size, _ = strconv.ParseInt(strings.TrimPrefix(trimmed, "__WS_SIZE__"), 10, 64)
			continue
		}
		if trimmed != "" {
			data.WriteString(trimmed)
		}
	}
	out.DataB64 = data.String()
	if raw, ok := b64decodeLoose([]byte(out.DataB64)); ok {
		out.Length = len(raw)
		out.Binary = !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0)
		// Return canonical base64 so the UI can decode it uniformly.
		out.DataB64 = base64.StdEncoding.EncodeToString(raw)
	}
	out.Truncated = out.Size > 0 && offset+int64(out.Length) < out.Size
	return out, nil
}

// WriteFile replaces a remote file with data, sending it in bounded chunks.
func (c *Client) WriteFile(ctx context.Context, file string, data []byte) error {
	if err := c.EnsureOS(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	osType := c.osType
	for offset := 0; offset < len(data); offset += fileChunkSize {
		end := offset + fileChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		b64 := base64.StdEncoding.EncodeToString(chunk)
		var cmd string
		if osType == "windows" {
			mode := "Create"
			if offset > 0 {
				mode = "Append"
			}
			script := fmt.Sprintf(`$b=[Convert]::FromBase64String('%s')
$fs=[IO.File]::Open(%s,[IO.FileMode]::%s,[IO.FileAccess]::Write)
$fs.Write($b,0,$b.Length)
$fs.Close()`, b64, psQuote(file), mode)
			cmd = powershellCommand(script)
		} else {
			redir := ">"
			if offset > 0 {
				redir = ">>"
			}
			cmd = fmt.Sprintf(`printf '%%s' '%s' | base64 -d %s %s`, b64, redir, shQuote(file))
		}
		res, err := c.executeLocked(ctx, cmd)
		if err != nil {
			return fmt.Errorf("写入第 %d 字节失败: %w", offset, err)
		}
		if msg := commandError(res.Output); msg != "" {
			return fmt.Errorf("写入第 %d 字节失败: %s", offset, msg)
		}
	}
	if len(data) == 0 {
		cmd := "printf '' > " + shQuote(file)
		if osType == "windows" {
			cmd = powershellCommand("$fs=[IO.File]::Open(" + psQuote(file) + ",[IO.FileMode]::Create,[IO.FileAccess]::Write);$fs.Close()")
		}
		if _, err := c.executeLocked(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) DeleteFile(ctx context.Context, file string) (string, error) {
	return c.fileOp(ctx, func(osType string) string {
		if osType == "windows" {
			return powershellCommand("Remove-Item -LiteralPath " + psQuote(file) + " -Recurse -Force -ErrorAction SilentlyContinue")
		}
		return "rm -rf " + shQuote(file)
	})
}

func (c *Client) Mkdir(ctx context.Context, dir string) (string, error) {
	return c.fileOp(ctx, func(osType string) string {
		if osType == "windows" {
			return powershellCommand("New-Item -ItemType Directory -Force -Path " + psQuote(dir) + " | Out-Null")
		}
		return "mkdir -p " + shQuote(dir)
	})
}

func (c *Client) Rename(ctx context.Context, from, to string) (string, error) {
	return c.fileOp(ctx, func(osType string) string {
		if osType == "windows" {
			return powershellCommand("Move-Item -LiteralPath " + psQuote(from) + " -Destination " + psQuote(to) + " -Force")
		}
		return "mv " + shQuote(from) + " " + shQuote(to)
	})
}

func (c *Client) fileOp(ctx context.Context, build func(osType string) string) (string, error) {
	if err := c.EnsureOS(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.executeLocked(ctx, build(c.osType))
	if err != nil {
		return "", err
	}
	if msg := commandError(res.Output); msg != "" {
		return res.Output, errors.New(msg)
	}
	return res.Output, nil
}

func commandError(out string) string {
	low := strings.ToLower(out)
	for _, needle := range []string{"not found", "no such file", "permission denied", "cannot find", "拒绝访问", "找不到", "没有那个文件", "权限不够"} {
		if strings.Contains(low, needle) {
			return strings.TrimSpace(out)
		}
	}
	return ""
}
