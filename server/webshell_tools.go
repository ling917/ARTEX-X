package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	wsToolOutputLimit = 100 << 10
	wsToolTestTimeout = 60 * time.Second
)

// webShellTools are host tools: they live in the server package because saving a
// shell needs the database and executing one needs the same persistent client pool
// the WebShell page uses. They are injected into every agent then filtered by the
// tools-table binding (worker/pentest/auto/mainagent by default).
func (s *Server) webShellTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolWebShellSave(),
		s.toolWebShellExec(),
	}
}

func (s *Server) toolWebShellSave() actool.CoreTool {
	schema := objSchema(map[string]any{
		"url": map[string]any{
			"type":        "string",
			"description": "WebShell 的完整 URL（如 http://target/upload/shell.php）",
		},
		"type": map[string]any{
			"type": "string",
			"enum": []string{"simple", "behinder", "godzilla", "custom"},
			"description": "马的类型：simple=一句话命令马(默认)，behinder=冰蝎，godzilla=哥斯拉，" +
				"custom=自定义请求模板。",
		},
		"password": map[string]any{
			"type":        "string",
			"description": "连接密码（冰蝎/哥斯拉必填；一句话马通常是参数值，可留空）",
		},
		"lang": map[string]any{
			"type": "string", "enum": []string{"php", "jsp", "asp", "aspx"},
			"description": "目标脚本语言，默认 php",
		},
		"mode": map[string]any{
			"type": "string", "enum": []string{"raw", "php_eval", "jsp_eval", "asp_eval", "aspx_eval"},
			"description": "simple 类型的执行方式：raw=参数直接当命令(默认)，php_eval=参数传 PHP 代码，" +
				"jsp_eval/asp_eval/aspx_eval 同理。冰蝎/哥斯拉忽略此项。",
		},
		"method": map[string]any{
			"type": "string", "enum": []string{"GET", "POST"},
			"description": "请求方法，默认 POST",
		},
		"param": map[string]any{
			"type":        "string",
			"description": "命令参数名。simple 默认 cmd，冰蝎/哥斯拉默认 pass",
		},
		"headers": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "额外请求头（如 Cookie），可选",
		},
		"body_template": map[string]any{
			"type": "string",
			"description": "仅 custom：完整请求体模板，用 {{cmd}}/{{cmd_b64}}/{{cmd_url}} 代表命令，" +
				"{{pass}} 代表密码，{{param}} 代表参数名",
		},
		"response_regex": map[string]any{
			"type":        "string",
			"description": "可选：从响应中提取输出的正则（取第 1 个分组）",
		},
		"response_b64": map[string]any{
			"type":        "boolean",
			"description": "响应体是否整体 base64 编码",
		},
		"encoding": map[string]any{
			"type":        "string",
			"description": "传输编码：auto(默认自动探测)。探测失败时可填 aes_cbc_iv2/aes_cbc_iv1/aes_ecb/xor/base64 等已知变体",
		},
		"insecure": map[string]any{
			"type":        "boolean",
			"description": "是否跳过 HTTPS 证书校验，默认 true",
		},
		"timeout_seconds": map[string]any{
			"type":        "integer",
			"description": "单次请求超时秒数，默认 15，最多 120",
		},
		"name":   strParam("显示名称，默认取 URL 主机+路径"),
		"target": strParam("目标标识（域名/IP），默认取 URL 主机"),
		"notes":  strParam("备注，可选"),
		"verify": map[string]any{
			"type":        "boolean",
			"description": "登记后是否立即测试连接（默认 true）。测试会探测加密传输变体。",
		},
	}, "url")

	return wrTool("webshell_save",
		"登记一个已经拿到执行权限的目标 WebShell，登记后用户可在「WebShell」页面直接连接（终端/文件管理/信息面板），"+
			"你也可以用返回的 id 调 webshell_exec 继续在该目标上执行命令。同一个任务下相同 URL 会更新已有记录而不是重复登记。"+
			"适用：PHP/JSP/ASP 一句话、冰蝎、哥斯拉、自定义请求马。必须提供能实际执行命令的 URL 和正确类型/密码。",
		schema,
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if s.m.pg == nil {
				return actool.Errorf("数据库不可用"), nil
			}
			var a struct {
				URL            string            `json:"url"`
				Type           *string           `json:"type"`
				Lang           *string           `json:"lang"`
				Mode           *string           `json:"mode"`
				Password       *string           `json:"password"`
				Method         *string           `json:"method"`
				Param          *string           `json:"param"`
				Headers        map[string]string `json:"headers"`
				BodyTemplate   *string           `json:"body_template"`
				ResponseRegex  *string           `json:"response_regex"`
				ResponseB64    *bool             `json:"response_b64"`
				Encoding       *string           `json:"encoding"`
				Insecure       *bool             `json:"insecure"`
				TimeoutSeconds *int              `json:"timeout_seconds"`
				Name           *string           `json:"name"`
				Target         *string           `json:"target"`
				Notes          *string           `json:"notes"`
				Verify         *bool             `json:"verify"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			targetURL := strings.TrimSpace(a.URL)
			if targetURL == "" {
				return actool.Errorf("url 为必填"), nil
			}

			runInfo := agent.RunInfoFrom(ctx)
			var taskPtr *int64
			if runInfo.TaskID > 0 {
				id := runInfo.TaskID
				taskPtr = &id
			}
			existing, err := s.m.pg.GetWebShellByURL(taskPtr, targetURL)
			if err != nil {
				return actool.Errorf("查询已有登记失败: " + err.Error()), nil
			}
			if existing == nil && taskPtr != nil {
				// A shell registered globally (manual add) should be reused, not duplicated.
				existing, err = s.m.pg.GetWebShellByURL(nil, targetURL)
				if err != nil {
					return actool.Errorf("查询已有登记失败: " + err.Error()), nil
				}
			}

			create := existing == nil
			ws := existing
			if create {
				ws = &db.WebShell{CreatedBy: "agent", Status: "unknown", TaskID: taskPtr}
			}
			inp := webShellInputFromRow(ws)
			inp.URL = targetURL
			if a.Type != nil {
				inp.Type = *a.Type
			}
			if a.Lang != nil {
				inp.Lang = *a.Lang
			}
			if a.Mode != nil {
				inp.Mode = *a.Mode
			}
			if a.Method != nil {
				inp.Method = *a.Method
			}
			if a.Param != nil {
				inp.Param = *a.Param
			}
			if a.Headers != nil {
				inp.Headers = a.Headers
			}
			if a.BodyTemplate != nil {
				inp.BodyTemplate = *a.BodyTemplate
			}
			if a.ResponseRegex != nil {
				inp.ResponseRegex = *a.ResponseRegex
			}
			if a.ResponseB64 != nil {
				inp.ResponseB64 = *a.ResponseB64
			}
			if a.Encoding != nil {
				inp.Encoding = *a.Encoding
			}
			if a.Insecure != nil {
				inp.Insecure = a.Insecure
			}
			if a.TimeoutSeconds != nil {
				inp.TimeoutSeconds = *a.TimeoutSeconds
			}
			if a.Name != nil {
				inp.Name = *a.Name
			}
			if a.Target != nil {
				inp.Target = *a.Target
			}
			if a.Notes != nil {
				inp.Notes = *a.Notes
			}
			if a.Password != nil {
				inp.Password = a.Password
			}

			before := ""
			if existing != nil {
				before = webShellConnKey(existing)
			}
			if err := applyWebShellInput(ws, &inp, create); err != nil {
				return actool.Errorf("登记失败: " + err.Error()), nil
			}
			connChanged := create || webShellConnKey(ws) != before
			if connChanged {
				ws.Transport = ""
				ws.OSType = ""
			}

			if create {
				if err := s.m.pg.CreateWebShell(ws); err != nil {
					return actool.Errorf("写入数据库失败: " + err.Error()), nil
				}
			} else {
				if err := s.m.pg.UpdateWebShell(ws); err != nil {
					return actool.Errorf("更新数据库失败: " + err.Error()), nil
				}
				if connChanged {
					_ = s.m.pg.ResetWebShellProbe(ws.ID)
				}
			}
			s.dropWebShellClient(ws.ID)

			out := map[string]any{
				"id":           ws.ID,
				"name":         ws.Name,
				"url":          ws.URL,
				"type":         ws.Type,
				"status":       ws.Status,
				"os_type":      ws.OSType,
				"password_set": ws.Password != "",
				"created":      create,
				"task_id":      ws.TaskID,
			}
			verify := true
			if a.Verify != nil {
				verify = *a.Verify
			}
			if verify {
				client, err := s.webShellClient(ws)
				if err != nil {
					out["test_error"] = err.Error()
				} else {
					vctx, cancel := context.WithTimeout(ctx, wsToolTestTimeout)
					res := client.Test(vctx)
					cancel()
					status := "fail"
					if res.OK {
						status = "ok"
					}
					_ = s.m.pg.MarkWebShellCheck(ws.ID, status, res.Error, res.OSType, res.Transport)
					out["status"] = status
					out["os_type"] = res.OSType
					out["transport"] = res.Transport
					out["test"] = res
					if !res.OK {
						out["hint"] = "登记已保存，但连接测试未通过；请在「WebShell」页面修正类型/密码/参数后重试。"
					}
				}
			}
			if ws.TaskID != nil {
				out["task_id"] = *ws.TaskID
			}
			return jsonResult(out)
		})
}

func (s *Server) toolWebShellExec() actool.CoreTool {
	schema := objSchema(map[string]any{
		"id": map[string]any{
			"type":        "integer",
			"description": "已登记 WebShell 的 id（webshell_save 返回）",
		},
		"url":     strParam("也可以直接给 URL 定位已登记的 shell（与 id 二选一）"),
		"command": strParam("要在目标上执行的系统命令（Linux shell / Windows cmd）"),
	}, "command")

	return wrTool("webshell_exec",
		"在已登记的目标 WebShell 上执行一条命令并返回输出。命令在目标主机上执行，不是本机容器。"+
			"若 shell 尚未登记，先用 webshell_save 登记；也可以传 url 直接复用已登记的记录。",
		schema,
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if s.m.pg == nil {
				return actool.Errorf("数据库不可用"), nil
			}
			var a struct {
				ID      json.RawMessage `json:"id"`
				URL     string          `json:"url"`
				Command string          `json:"command"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			cmd := strings.TrimSpace(a.Command)
			if cmd == "" {
				return actool.Errorf("command 为必填"), nil
			}
			if len(cmd) > wsMaxCommandBytes {
				return actool.Errorf("命令过长（最多 16KB）"), nil
			}

			var ws *db.WebShell
			var err error
			if id := parseProfileID(a.ID); id > 0 {
				ws, err = s.m.pg.GetWebShell(id)
				if err != nil {
					return actool.Errorf("读取 WebShell 失败: " + err.Error()), nil
				}
			}
			if ws == nil && strings.TrimSpace(a.URL) != "" {
				var taskPtr *int64
				if runInfo := agent.RunInfoFrom(ctx); runInfo.TaskID > 0 {
					id := runInfo.TaskID
					taskPtr = &id
				}
				ws, err = s.m.pg.GetWebShellByURL(taskPtr, strings.TrimSpace(a.URL))
				if err != nil {
					return actool.Errorf("读取 WebShell 失败: " + err.Error()), nil
				}
				if ws == nil && taskPtr != nil {
					ws, _ = s.m.pg.GetWebShellByURL(nil, strings.TrimSpace(a.URL))
				}
			}
			if ws == nil {
				return actool.Errorf("未找到已登记的 WebShell" +
					"（先用 webshell_save 登记，或在「WebShell」页面手动添加）"), nil
			}

			client, err := s.webShellClient(ws)
			if err != nil {
				return actool.Errorf("连接配置无效: " + err.Error()), nil
			}
			res, err := client.Execute(ctx, cmd)
			if err != nil {
				return actool.Errorf("执行失败: " + err.Error()), nil
			}
			s.persistWebShellTransport(ws, client)
			_ = s.m.pg.TouchWebShell(ws.ID)

			output := res.Output
			truncated := res.Truncated
			if len(output) > wsToolOutputLimit {
				output = output[:wsToolOutputLimit] + "\n…(输出过长已截断)"
				truncated = true
			}
			return jsonResult(map[string]any{
				"shell_id":    ws.ID,
				"output":      output,
				"http_status": res.HTTPStatus,
				"duration_ms": res.DurationMS,
				"truncated":   truncated,
				"transport":   res.Transport,
			})
		})
}

// webShellInputFromRow pre-fills the edit form so an agent's partial update keeps
// every field it did not mention.
func webShellInputFromRow(ws *db.WebShell) webShellInput {
	in := webShellInput{
		Name:           ws.Name,
		URL:            ws.URL,
		Type:           ws.Type,
		Lang:           ws.Lang,
		Mode:           ws.Mode,
		Headers:        ws.Headers,
		BodyTemplate:   ws.BodyTemplate,
		ResponseRegex:  ws.ResponseRegex,
		ResponseB64:    ws.ResponseB64,
		Encoding:       ws.Encoding,
		Insecure:       boolPtr(ws.Insecure),
		TimeoutSeconds: ws.TimeoutSeconds,
		TaskID:         ws.TaskID,
		Target:         ws.Target,
		Notes:          ws.Notes,
	}
	if ws.ID == 0 {
		// New rows: let the stored defaults decide (insecure defaults to true).
		in.Insecure = nil
		in.TimeoutSeconds = 0
	}
	if ws.Method != "" {
		in.Method = ws.Method
	}
	if ws.Param != "" {
		in.Param = ws.Param
	}
	return in
}

func boolPtr(v bool) *bool { return &v }
