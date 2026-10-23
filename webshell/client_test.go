package webshell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCommandShell emulates a one-line command shell whose parameter receives the
// raw command. It understands the two commands the client itself issues during
// detection and echoes everything else.
func fakeCommandShell(t *testing.T, method, param string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cmd string
		if method == http.MethodGet {
			cmd = r.URL.Query().Get(param)
		} else {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			cmd = r.PostForm.Get(param)
		}
		switch {
		case strings.HasPrefix(cmd, "echo "):
			w.Write([]byte(strings.TrimPrefix(cmd, "echo ")))
		case strings.Contains(cmd, "uname"):
			w.Write([]byte("Linux target 6.1.0 x86_64 GNU/Linux\n"))
		default:
			w.Write([]byte(cmd))
		}
	}))
}

func TestSimpleShellExecuteAndDetect(t *testing.T) {
	srv := fakeCommandShell(t, http.MethodPost, "cmd")
	defer srv.Close()

	c, err := New(Config{URL: srv.URL + "/shell.php", Type: TypeSimple, Lang: LangPHP, Mode: ModeRaw, Param: "cmd"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	res := c.Test(ctx)
	if !res.OK {
		t.Fatalf("Test failed: %+v", res)
	}
	if res.OSType != "linux" {
		t.Errorf("OSType = %q, want linux", res.OSType)
	}

	exec, err := c.Execute(ctx, "whoami")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exec.Output != "whoami" {
		t.Errorf("Output = %q, want whoami", exec.Output)
	}
	if exec.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d", exec.HTTPStatus)
	}
}

func TestSimpleShellGET(t *testing.T) {
	srv := fakeCommandShell(t, http.MethodGet, "c")
	defer srv.Close()

	c, err := New(Config{URL: srv.URL + "/shell.jsp", Type: TypeSimple, Lang: LangJSP, Mode: ModeRaw, Method: "GET", Param: "c"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	res := c.Test(context.Background())
	if !res.OK {
		t.Fatalf("Test failed: %+v", res)
	}
	if res.OSType != "linux" {
		t.Errorf("OSType = %q, want linux", res.OSType)
	}
}

func TestCustomJSONTemplate(t *testing.T) {
	const pass = "s3cret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Pass string `json:"pass"`
			Cmd  string `json:"cmd_b64"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if body.Pass != pass {
			t.Errorf("pass = %q, want %q", body.Pass, pass)
		}
		raw, err := base64.StdEncoding.DecodeString(body.Cmd)
		if err != nil {
			t.Errorf("cmd base64: %v", err)
			return
		}
		cmd := string(raw)
		if strings.HasPrefix(cmd, "echo ") {
			w.Write([]byte(strings.TrimPrefix(cmd, "echo ")))
			return
		}
		if strings.Contains(cmd, "uname") {
			w.Write([]byte("Linux"))
			return
		}
		w.Write([]byte(cmd))
	}))
	defer srv.Close()

	c, err := New(Config{
		URL:          srv.URL + "/x.php",
		Type:         TypeCustom,
		Lang:         LangPHP,
		Password:     pass,
		Method:       "POST",
		Param:        "ignored",
		BodyTemplate: `{"pass":"{{pass}}","cmd_b64":"{{cmd_b64}}"}`,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	if res := c.Test(ctx); !res.OK {
		t.Fatalf("Test failed: %+v", res)
	}
	exec, err := c.Execute(ctx, "id")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exec.Output != "id" {
		t.Errorf("Output = %q, want id", exec.Output)
	}
}

func TestResponseRegexAndBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cmd := r.PostForm.Get("cmd")
		if strings.Contains(cmd, "uname") {
			w.Write([]byte("<pre>Linux</pre>"))
			return
		}
		w.Write([]byte("<pre>" + base64.StdEncoding.EncodeToString([]byte(cmd)) + "</pre>"))
	}))
	defer srv.Close()

	c, err := New(Config{
		URL:           srv.URL,
		Type:          TypeSimple,
		Mode:          ModeRaw,
		Param:         "cmd",
		ResponseRegex: `<pre>(.*?)</pre>`,
		ResponseB64:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	exec, err := c.Execute(context.Background(), "pwd")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exec.Output != "pwd" {
		t.Errorf("Output = %q, want pwd", exec.Output)
	}
}

func TestValidateRejectsHeaderNewlines(t *testing.T) {
	cfg := Config{
		URL:     "http://127.0.0.1/shell.php",
		Type:    TypeSimple,
		Headers: map[string]string{"X-Test": "a\r\nInjected: 1"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected header newline validation error")
	}
}

func TestParseListingUnix(t *testing.T) {
	raw := "warning: something\n__WS_PWD__/var/www/html\n" +
		"d\t0\t1700000000\tsub\n" +
		"f\t1234\t1700000001\tshell.php\n"
	listing, err := parseListing(raw, ".", "linux")
	if err != nil {
		t.Fatalf("parseListing: %v", err)
	}
	if listing.Path != "/var/www/html" {
		t.Errorf("Path = %q", listing.Path)
	}
	if listing.Parent != "/var/www" {
		t.Errorf("Parent = %q, want /var/www", listing.Parent)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(listing.Entries))
	}
	if !listing.Entries[0].Dir || listing.Entries[0].Name != "sub" {
		t.Errorf("entry0 = %+v", listing.Entries[0])
	}
	if listing.Entries[1].Dir || listing.Entries[1].Size != 1234 {
		t.Errorf("entry1 = %+v", listing.Entries[1])
	}
	if listing.Entries[1].Path != "/var/www/html/shell.php" {
		t.Errorf("entry1 path = %q", listing.Entries[1].Path)
	}
}

func TestParseListingError(t *testing.T) {
	if _, err := parseListing("__WS_ERR__:目录不存在", "/nope", "linux"); err == nil {
		t.Fatal("expected error for __WS_ERR__")
	}
}

func TestClassifyOS(t *testing.T) {
	cases := map[string]string{
		"MINGW64_NT-10.0-19045 DESKTOP 3.4.10 x86_64":   "windows",
		"MSYS_NT-10.0-19045":                            "windows",
		"CYGWIN_NT-10.0-19045":                          "windows",
		"Microsoft Windows [Version 10.0.19045.3803]":   "windows",
		"Linux target 6.1.0 x86_64 GNU/Linux":           "linux",
		"Darwin host 23.0.0 Darwin Kernel Version 23.0": "linux",
		"": "unknown",
	}
	for out, want := range cases {
		if got := classifyOS(out); got != want {
			t.Errorf("classifyOS(%q) = %q, want %q", out, got, want)
		}
	}
}

func TestStripPowerShellNoise(t *testing.T) {
	raw := "\ufeff__WS_SIZE__16\n#< CLIXML\r\n<Objs Version=\"1.1.0.1\"><Obj S=\"progress\">x</Obj></Objs>\r\naGVsbG8="
	got := stripPowerShellNoise(raw)
	if strings.Contains(got, "CLIXML") || strings.Contains(got, "\ufeff") {
		t.Fatalf("noise not stripped: %q", got)
	}
	if !strings.Contains(got, "__WS_SIZE__16") || !strings.HasSuffix(strings.TrimSpace(got), "aGVsbG8=") {
		t.Fatalf("payload damaged: %q", got)
	}
}
