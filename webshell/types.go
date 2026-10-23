// Package webshell implements outbound clients for webshells obtained during a
// pentest engagement. It is an HTTP request/response client only: every operation
// is sent to a user-registered target URL. Commands are executed on the target
// through the shell's own execution capability.
//
// The package deliberately has no dependencies outside the Go standard library so
// it can be unit-tested with httptest without a database.
package webshell

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	TypeSimple   = "simple"
	TypeBehinder = "behinder"
	TypeGodzilla = "godzilla"
	TypeCustom   = "custom"

	LangPHP  = "php"
	LangJSP  = "jsp"
	LangASP  = "asp"
	LangASPX = "aspx"

	// Simple shell execution modes.
	ModeRaw      = "raw"       // parameter receives the raw command
	ModePHPEval  = "php_eval"  // parameter receives PHP code: system(base64_decode(...))
	ModeJSPEval  = "jsp_eval"  // parameter receives Java code evaluated by the shell
	ModeASPEval  = "asp_eval"  // parameter receives VBScript evaluated by the shell
	ModeASPXEval = "aspx_eval" // parameter receives C# code (only some aspx shells)

	EncodingAuto = "auto"
)

// Config is the connection contract shared by the API layer and the client.
type Config struct {
	URL            string
	Type           string
	Lang           string
	Mode           string
	Password       string
	Method         string
	Param          string
	Headers        map[string]string
	BodyTemplate   string
	ResponseRegex  string
	ResponseB64    bool
	Encoding       string
	Transport      string
	Insecure       bool
	TimeoutSeconds int
	OSType         string
}

// Normalized applies defaults and returns the config the client will actually use.
// The API layer persists this result so the stored row matches runtime behaviour.
func (c Config) Normalized() Config { return c.normalized() }

func (c Config) normalized() Config {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	c.Lang = strings.ToLower(strings.TrimSpace(c.Lang))
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	c.Param = strings.TrimSpace(c.Param)
	c.Encoding = strings.ToLower(strings.TrimSpace(c.Encoding))
	c.Transport = strings.TrimSpace(c.Transport)
	c.OSType = strings.ToLower(strings.TrimSpace(c.OSType))
	if c.Type == "" {
		c.Type = TypeSimple
	}
	if c.Lang == "" {
		c.Lang = LangPHP
	}
	if c.Method == "" {
		c.Method = "POST"
	}
	if c.Encoding == "" {
		c.Encoding = EncodingAuto
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 15
	}
	if c.TimeoutSeconds > 120 {
		c.TimeoutSeconds = 120
	}
	if c.TimeoutSeconds < 3 {
		c.TimeoutSeconds = 3
	}
	if c.Param == "" {
		if c.Type == TypeBehinder || c.Type == TypeGodzilla {
			c.Param = "pass"
		} else {
			c.Param = "cmd"
		}
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	if c.Type == TypeBehinder || c.Type == TypeGodzilla {
		c.Lang = LangPHP
		c.Mode = LangPHP
	}
	return c
}

// Validate checks the user-editable connection settings before they are stored.
func (c Config) Validate() error {
	c = c.normalized()
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("URL 不是合法的 http/https 地址")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL 仅支持 http/https")
	}
	switch c.Type {
	case TypeSimple, TypeBehinder, TypeGodzilla, TypeCustom:
	default:
		return fmt.Errorf("不支持的 WebShell 类型: %s", c.Type)
	}
	switch c.Lang {
	case LangPHP, LangJSP, LangASP, LangASPX:
	default:
		return fmt.Errorf("不支持的语言: %s", c.Lang)
	}
	if c.Type == TypeSimple {
		switch c.Mode {
		case ModeRaw, ModePHPEval, ModeJSPEval, ModeASPEval, ModeASPXEval:
		default:
			return fmt.Errorf("不支持的简单马执行方式: %s", c.Mode)
		}
	}
	if c.Type == TypeBehinder || c.Type == TypeGodzilla {
		if strings.TrimSpace(c.Password) == "" {
			return errors.New("冰蝎/哥斯拉必须填写连接密码")
		}
	}
	if c.Type == TypeCustom && c.Method == "POST" && strings.TrimSpace(c.BodyTemplate) != "" {
		if !strings.Contains(c.BodyTemplate, "{{cmd}}") &&
			!strings.Contains(c.BodyTemplate, "{{cmd_b64}}") &&
			!strings.Contains(c.BodyTemplate, "{{cmd_url}}") {
			return errors.New("自定义请求模板必须包含 {{cmd}} / {{cmd_b64}} / {{cmd_url}} 之一")
		}
	}
	if c.Method != "GET" && c.Method != "POST" {
		return errors.New("请求方法仅支持 GET/POST")
	}
	if len(c.Password) > 512 {
		return errors.New("密码过长")
	}
	if len(c.Param) > 128 {
		return errors.New("参数名过长")
	}
	if len(c.BodyTemplate) > 64<<10 {
		return errors.New("请求模板过大（最多 64KB）")
	}
	if len(c.ResponseRegex) > 4096 {
		return errors.New("响应正则过长")
	}
	if len(c.Headers) > 64 {
		return errors.New("请求头数量过多")
	}
	for k, v := range c.Headers {
		if len(k) > 256 || len(v) > 8192 {
			return errors.New("请求头过大")
		}
		if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
			return errors.New("请求头不能包含换行")
		}
	}
	if c.ResponseRegex != "" {
		if _, err := compileRegex(c.ResponseRegex); err != nil {
			return fmt.Errorf("响应正则无效: %w", err)
		}
	}
	return nil
}

// ExecResult is one command execution over the shell.
type ExecResult struct {
	Output      string `json:"output"`
	HTTPStatus  int    `json:"http_status"`
	DurationMS  int64  `json:"duration_ms"`
	OutputBytes int    `json:"output_bytes"`
	Truncated   bool   `json:"truncated"`
	Transport   string `json:"transport,omitempty"`
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK         bool   `json:"ok"`
	Output     string `json:"output,omitempty"`
	OSType     string `json:"os_type,omitempty"`
	Transport  string `json:"transport,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// InfoResult is the basic target information panel.
type InfoResult struct {
	OSType     string `json:"os_type"`
	User       string `json:"user"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Pwd        string `json:"pwd"`
	IP         string `json:"ip"`
	Raw        string `json:"raw"`
	Transport  string `json:"transport,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// FileEntry is one directory entry returned by the remote file manager.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix seconds
}

type FileListing struct {
	Path      string      `json:"path"`
	Parent    string      `json:"parent"`
	OSType    string      `json:"os_type"`
	Transport string      `json:"transport,omitempty"`
	Entries   []FileEntry `json:"entries"`
	Raw       string      `json:"raw,omitempty"`
}

type FileData struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Offset    int64  `json:"offset"`
	Length    int    `json:"length"`
	DataB64   string `json:"data_b64"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	OSType    string `json:"os_type"`
}

func nowMS() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }
