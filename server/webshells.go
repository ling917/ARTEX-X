package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/webshell"
)

const (
	wsMaxCommandBytes  = 16 << 10
	wsMaxWriteBytes    = 8 << 20
	wsDefaultReadBytes = 256 << 10
	wsMaxReadBytes     = 4 << 20
	wsJSONBodyLimit    = 1 << 20
	wsWriteBodyLimit   = 12 << 20
	wsTestHTTPTimeout  = 90 * time.Second
	wsFileHTTPTimeout  = 3 * time.Minute
	wsWriteHTTPTimeout = 5 * time.Minute
)

// wsCachedClient keeps the persistent client for one webshell together with a
// fingerprint of the config it was built from, so a stored edit transparently
// replaces the connection on the next request.
type wsCachedClient struct {
	key    string
	client *webshell.Client
}

func (s *Server) registerWebShellRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/webshells", s.listWebShells)
	mux.HandleFunc("POST /api/webshells", s.createWebShell)
	mux.HandleFunc("GET /api/webshells/{id}", s.getWebShell)
	mux.HandleFunc("PATCH /api/webshells/{id}", s.updateWebShell)
	mux.HandleFunc("DELETE /api/webshells/{id}", s.deleteWebShell)
	mux.HandleFunc("POST /api/webshells/{id}/test", s.testWebShell)
	mux.HandleFunc("POST /api/webshells/{id}/exec", s.execWebShell)
	mux.HandleFunc("GET /api/webshells/{id}/info", s.infoWebShell)
	mux.HandleFunc("GET /api/webshells/{id}/files", s.listWebShellFiles)
	mux.HandleFunc("GET /api/webshells/{id}/files/read", s.readWebShellFile)
	mux.HandleFunc("POST /api/webshells/{id}/files/write", s.writeWebShellFile)
	mux.HandleFunc("POST /api/webshells/{id}/files/delete", s.deleteWebShellFile)
	mux.HandleFunc("POST /api/webshells/{id}/files/mkdir", s.mkdirWebShell)
	mux.HandleFunc("POST /api/webshells/{id}/files/rename", s.renameWebShellFile)
}

// webShellConfig converts a stored row into the client contract and applies the
// same defaults the client uses, so callers can compare/persist normalized values.
func webShellConfig(ws *db.WebShell) webshell.Config {
	return webshell.Config{
		URL:            ws.URL,
		Type:           ws.Type,
		Lang:           ws.Lang,
		Mode:           ws.Mode,
		Password:       ws.Password,
		Method:         ws.Method,
		Param:          ws.Param,
		Headers:        ws.Headers,
		BodyTemplate:   ws.BodyTemplate,
		ResponseRegex:  ws.ResponseRegex,
		ResponseB64:    ws.ResponseB64,
		Encoding:       ws.Encoding,
		Transport:      ws.Transport,
		Insecure:       ws.Insecure,
		TimeoutSeconds: ws.TimeoutSeconds,
		OSType:         ws.OSType,
	}.Normalized()
}

func (s *Server) webShellClient(ws *db.WebShell) (*webshell.Client, error) {
	cfg := webShellConfig(ws)
	keyBytes, _ := json.Marshal(cfg)
	key := string(keyBytes)

	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if cached := s.wsClients[ws.ID]; cached != nil {
		if cached.key == key {
			return cached.client, nil
		}
		cached.client.Close()
		delete(s.wsClients, ws.ID)
	}
	client, err := webshell.New(cfg)
	if err != nil {
		return nil, err
	}
	s.wsClients[ws.ID] = &wsCachedClient{key: key, client: client}
	return client, nil
}

func (s *Server) dropWebShellClient(id int64) {
	s.wsMu.Lock()
	if cached := s.wsClients[id]; cached != nil {
		cached.client.Close()
		delete(s.wsClients, id)
	}
	s.wsMu.Unlock()
}

// ---------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------

type webShellInput struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Type           string            `json:"type"`
	Lang           string            `json:"lang"`
	Mode           string            `json:"mode"`
	Password       *string           `json:"password"`
	Method         string            `json:"method"`
	Param          string            `json:"param"`
	Headers        map[string]string `json:"headers"`
	BodyTemplate   string            `json:"body_template"`
	ResponseRegex  string            `json:"response_regex"`
	ResponseB64    bool              `json:"response_b64"`
	Encoding       string            `json:"encoding"`
	Insecure       *bool             `json:"insecure"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	TaskID         *int64            `json:"task_id"`
	Target         string            `json:"target"`
	Notes          string            `json:"notes"`
}

func decodeWSBody(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// applyWebShellInput copies the full connection form into ws, normalizes defaults
// and validates. An empty password on update keeps the stored credential.
func applyWebShellInput(ws *db.WebShell, in *webShellInput, create bool) error {
	ws.Name = strings.TrimSpace(in.Name)
	ws.URL = strings.TrimSpace(in.URL)
	ws.Type = in.Type
	ws.Lang = in.Lang
	ws.Mode = in.Mode
	ws.Method = in.Method
	ws.Param = in.Param
	ws.Headers = in.Headers
	ws.BodyTemplate = in.BodyTemplate
	ws.ResponseRegex = in.ResponseRegex
	ws.ResponseB64 = in.ResponseB64
	ws.Encoding = in.Encoding
	// 编辑表单不带 task_id：更新时保留原有归属，避免手动编辑把 Agent 登记的任务关联清掉。
	if create || in.TaskID != nil {
		ws.TaskID = in.TaskID
	}
	ws.Target = strings.TrimSpace(in.Target)
	ws.Notes = strings.TrimSpace(in.Notes)
	ws.Insecure = true
	if in.Insecure != nil {
		ws.Insecure = *in.Insecure
	}
	ws.TimeoutSeconds = in.TimeoutSeconds
	if create {
		if in.Password != nil {
			ws.Password = *in.Password
		}
	} else if in.Password != nil && *in.Password != "" {
		ws.Password = *in.Password
	}

	cfg := webShellConfig(ws)
	if err := cfg.Validate(); err != nil {
		return err
	}
	ws.Type, ws.Lang, ws.Mode = cfg.Type, cfg.Lang, cfg.Mode
	ws.Method, ws.Param, ws.Encoding = cfg.Method, cfg.Param, cfg.Encoding
	ws.TimeoutSeconds = cfg.TimeoutSeconds
	ws.Insecure = cfg.Insecure
	if ws.Headers == nil {
		ws.Headers = map[string]string{}
	}
	if ws.Name == "" {
		ws.Name = defaultWebShellName(ws.URL)
	}
	if ws.Target == "" {
		ws.Target = webShellHost(ws.URL)
	}
	return nil
}

func (s *Server) checkWebShellTask(taskID *int64) error {
	if taskID == nil {
		return nil
	}
	if *taskID <= 0 {
		return errors.New("关联任务 ID 无效")
	}
	var exists bool
	if err := s.m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1)`, *taskID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("关联任务不存在")
	}
	return nil
}

// webShellConnKey covers the fields whose change invalidates the detected
// transport, cached OS and last test status.
func webShellConnKey(ws *db.WebShell) string {
	return strings.Join([]string{ws.URL, ws.Type, ws.Lang, ws.Mode, ws.Encoding, ws.Password}, "\x00")
}

func defaultWebShellName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" || p == "/" {
		return u.Host
	}
	return u.Host + p
}

func webShellHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// loadWebShell resolves {id} and returns nil after writing an error response.
func (s *Server) loadWebShell(w http.ResponseWriter, r *http.Request) *db.WebShell {
	if s.m.pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return nil
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的 WebShell ID")
		return nil
	}
	ws, err := s.m.pg.GetWebShell(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if ws == nil {
		writeErr(w, http.StatusNotFound, "WebShell 不存在")
		return nil
	}
	return ws
}

func (s *Server) listWebShells(w http.ResponseWriter, r *http.Request) {
	if s.m.pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	q := r.URL.Query()
	filter := db.WebShellFilter{
		Query:  q.Get("q"),
		Status: q.Get("status"),
		Limit:  atoiDefault(q.Get("limit"), 200),
	}
	if v := strings.TrimSpace(q.Get("task_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "无效的 task_id")
			return
		}
		filter.TaskID = &id
	}
	items, err := s.m.pg.ListWebShells(filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) getWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	writeJSON(w, http.StatusOK, db.WebShellDetail{WebShell: ws, Password: ws.Password})
}

func (s *Server) createWebShell(w http.ResponseWriter, r *http.Request) {
	if s.m.pg == nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	var in webShellInput
	if !decodeWSBody(w, r, &in, wsJSONBodyLimit) {
		return
	}
	ws := &db.WebShell{CreatedBy: "user", Status: "unknown"}
	if err := applyWebShellInput(ws, &in, true); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkWebShellTask(ws.TaskID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.m.pg.CreateWebShell(ws); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, db.WebShellDetail{WebShell: ws, Password: ws.Password})
}

func (s *Server) updateWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	var in webShellInput
	if !decodeWSBody(w, r, &in, wsJSONBodyLimit) {
		return
	}
	before := webShellConnKey(ws)
	if err := applyWebShellInput(ws, &in, false); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkWebShellTask(ws.TaskID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	connChanged := webShellConnKey(ws) != before
	if connChanged {
		ws.Transport = ""
		ws.OSType = ""
	}
	if err := s.m.pg.UpdateWebShell(ws); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if connChanged {
		_ = s.m.pg.ResetWebShellProbe(ws.ID)
	}
	s.dropWebShellClient(ws.ID)
	writeJSON(w, http.StatusOK, db.WebShellDetail{WebShell: ws, Password: ws.Password})
}

func (s *Server) deleteWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	deleted, err := s.m.pg.DeleteWebShell(ws.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.dropWebShellClient(ws.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": deleted, "id": ws.ID})
}

// ---------------------------------------------------------------------
// Connection / command / info
// ---------------------------------------------------------------------

func (s *Server) testWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wsTestHTTPTimeout)
	defer cancel()
	res := client.Test(ctx)
	status := "fail"
	if res.OK {
		status = "ok"
	}
	_ = s.m.pg.MarkWebShellCheck(ws.ID, status, res.Error, res.OSType, res.Transport)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) execWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	var req struct {
		Command string `json:"command"`
	}
	if !decodeWSBody(w, r, &req, wsJSONBodyLimit) {
		return
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		writeErr(w, http.StatusBadRequest, "命令不能为空")
		return
	}
	if len(cmd) > wsMaxCommandBytes {
		writeErr(w, http.StatusBadRequest, "命令过长（最多 16KB）")
		return
	}
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := client.Execute(r.Context(), cmd)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.persistWebShellTransport(ws, client)
	_ = s.m.pg.TouchWebShell(ws.ID)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) infoWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wsFileHTTPTimeout)
	defer cancel()
	res, err := client.Info(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.persistWebShellTransport(ws, client)
	_ = s.m.pg.TouchWebShell(ws.ID)
	writeJSON(w, http.StatusOK, res)
}

// persistWebShellTransport stores a transport detected during an exec/info call so
// later requests (and the UI) can see what variant worked.
func (s *Server) persistWebShellTransport(ws *db.WebShell, client *webshell.Client) {
	tr := client.Transport()
	if tr == "" || tr == ws.Transport {
		return
	}
	if err := s.m.pg.MarkWebShellCheck(ws.ID, "ok", "", client.OSType(), tr); err == nil {
		ws.Transport = tr
		if osType := client.OSType(); osType != "" {
			ws.OSType = osType
		}
	}
}

// ---------------------------------------------------------------------
// Remote file manager
// ---------------------------------------------------------------------

func (s *Server) listWebShellFiles(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wsFileHTTPTimeout)
	defer cancel()
	listing, err := client.List(ctx, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.persistWebShellTransport(ws, client)
	_ = s.m.pg.TouchWebShell(ws.ID)
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) readWebShellFile(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	q := r.URL.Query()
	file := strings.TrimSpace(q.Get("path"))
	if file == "" {
		writeErr(w, http.StatusBadRequest, "缺少文件路径")
		return
	}
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	length := atoiDefault(q.Get("length"), wsDefaultReadBytes)
	if length <= 0 || length > wsMaxReadBytes {
		length = wsMaxReadBytes
	}
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wsFileHTTPTimeout)
	defer cancel()
	data, err := client.ReadFile(ctx, file, offset, length)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.persistWebShellTransport(ws, client)
	_ = s.m.pg.TouchWebShell(ws.ID)
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) writeWebShellFile(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	var req struct {
		Path    string `json:"path"`
		DataB64 string `json:"data_b64"`
	}
	if !decodeWSBody(w, r, &req, wsWriteBodyLimit) {
		return
	}
	file := strings.TrimSpace(req.Path)
	if file == "" {
		writeErr(w, http.StatusBadRequest, "缺少文件路径")
		return
	}
	data, err := decodeBase64Loose(req.DataB64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(data) > wsMaxWriteBytes {
		writeErr(w, http.StatusBadRequest, "文件过大（最多 8MB）")
		return
	}
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wsWriteHTTPTimeout)
	defer cancel()
	if err := client.WriteFile(ctx, file, data); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.persistWebShellTransport(ws, client)
	_ = s.m.pg.TouchWebShell(ws.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": file, "bytes": len(data)})
}

func (s *Server) deleteWebShellFile(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !decodeWSBody(w, r, &req, wsJSONBodyLimit) {
		return
	}
	file := strings.TrimSpace(req.Path)
	if unsafeRemotePath(file) {
		writeErr(w, http.StatusBadRequest, "拒绝删除该路径")
		return
	}
	s.webShellFileOp(w, r, ws, func(ctx context.Context, c *webshell.Client) (string, error) {
		return c.DeleteFile(ctx, file)
	})
}

func (s *Server) mkdirWebShell(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !decodeWSBody(w, r, &req, wsJSONBodyLimit) {
		return
	}
	dir := strings.TrimSpace(req.Path)
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "缺少目录路径")
		return
	}
	s.webShellFileOp(w, r, ws, func(ctx context.Context, c *webshell.Client) (string, error) {
		return c.Mkdir(ctx, dir)
	})
}

func (s *Server) renameWebShellFile(w http.ResponseWriter, r *http.Request) {
	ws := s.loadWebShell(w, r)
	if ws == nil {
		return
	}
	var req struct {
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if !decodeWSBody(w, r, &req, wsJSONBodyLimit) {
		return
	}
	from := strings.TrimSpace(req.Path)
	to := strings.TrimSpace(req.To)
	if unsafeRemotePath(from) || to == "" {
		writeErr(w, http.StatusBadRequest, "路径无效")
		return
	}
	s.webShellFileOp(w, r, ws, func(ctx context.Context, c *webshell.Client) (string, error) {
		return c.Rename(ctx, from, to)
	})
}

func (s *Server) webShellFileOp(w http.ResponseWriter, r *http.Request, ws *db.WebShell, op func(context.Context, *webshell.Client) (string, error)) {
	client, err := s.webShellClient(ws)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wsFileHTTPTimeout)
	defer cancel()
	out, err := op(ctx, client)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.persistWebShellTransport(ws, client)
	_ = s.m.pg.TouchWebShell(ws.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

// unsafeRemotePath blocks whole-filesystem/drive-root deletes and renames.
func unsafeRemotePath(p string) bool {
	switch p {
	case "", "/", `\`, ".", "..":
		return true
	}
	if len(p) == 2 && p[1] == ':' {
		return true
	}
	return false
}

func decodeBase64Loose(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ";base64,"); i >= 0 {
		s = s[i+len(";base64,"):]
	}
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("data_b64 不是合法的 base64")
}
