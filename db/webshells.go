package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// WebShell 是一条已登记的目标 WebShell 连接。password 等连接凭据只在详情
// 接口按需返回，列表接口通过 json:"-" 保证不会泄露。
type WebShell struct {
	ID             int64             `json:"id"`
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Type           string            `json:"type"`
	Lang           string            `json:"lang"`
	Mode           string            `json:"mode"`
	Password       string            `json:"-"`
	PasswordSet    bool              `json:"password_set"`
	Method         string            `json:"method"`
	Param          string            `json:"param"`
	Headers        map[string]string `json:"headers"`
	BodyTemplate   string            `json:"body_template"`
	ResponseRegex  string            `json:"response_regex"`
	ResponseB64    bool              `json:"response_b64"`
	Encoding       string            `json:"encoding"`
	Transport      string            `json:"transport"`
	Insecure       bool              `json:"insecure"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	OSType         string            `json:"os_type"`
	TaskID         *int64            `json:"task_id,omitempty"`
	TaskName       string            `json:"task_name,omitempty"`
	Target         string            `json:"target"`
	Notes          string            `json:"notes"`
	Status         string            `json:"status"`
	LastError      string            `json:"last_error"`
	LastCheckedAt  *time.Time        `json:"last_checked_at,omitempty"`
	LastUsedAt     *time.Time        `json:"last_used_at,omitempty"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// WebShellDetail 在详情接口里额外带上连接密码，供编辑表单回填。
type WebShellDetail struct {
	*WebShell
	Password string `json:"password"`
}

type WebShellFilter struct {
	TaskID *int64
	Query  string
	Status string
	Limit  int
}

const webShellCols = `ws.id, ws.name, ws.url, ws.type, ws.lang, ws.mode, ws.password,
       ws.method, ws.param, ws.headers, ws.body_template, ws.response_regex, ws.response_b64,
       ws.encoding, ws.transport, ws.insecure, ws.timeout_seconds, ws.os_type,
       ws.task_id, COALESCE(NULLIF(t.name,''), left(t.description, 80), ''),
       ws.target, ws.notes, ws.status, ws.last_error, ws.last_checked_at, ws.last_used_at,
       ws.created_by, ws.created_at, ws.updated_at`

func scanWebShell(row interface{ Scan(...any) error }) (*WebShell, error) {
	var ws WebShell
	var headers []byte
	var taskID sql.NullInt64
	var checked, used sql.NullTime
	if err := row.Scan(
		&ws.ID, &ws.Name, &ws.URL, &ws.Type, &ws.Lang, &ws.Mode, &ws.Password,
		&ws.Method, &ws.Param, &headers, &ws.BodyTemplate, &ws.ResponseRegex, &ws.ResponseB64,
		&ws.Encoding, &ws.Transport, &ws.Insecure, &ws.TimeoutSeconds, &ws.OSType,
		&taskID, &ws.TaskName, &ws.Target, &ws.Notes, &ws.Status, &ws.LastError, &checked, &used,
		&ws.CreatedBy, &ws.CreatedAt, &ws.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		_ = json.Unmarshal(headers, &ws.Headers)
	}
	if ws.Headers == nil {
		ws.Headers = map[string]string{}
	}
	if taskID.Valid {
		ws.TaskID = &taskID.Int64
	}
	if checked.Valid {
		ws.LastCheckedAt = &checked.Time
	}
	if used.Valid {
		ws.LastUsedAt = &used.Time
	}
	ws.PasswordSet = ws.Password != ""
	return &ws, nil
}

func (d *DB) ListWebShells(f WebShellFilter) ([]*WebShell, error) {
	where := []string{"1=1"}
	args := []any{}
	// addArg returns a generated placeholder; user-supplied values only ever travel
	// as driver parameters, never as SQL text.
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.TaskID != nil {
		where = append(where, "ws.task_id="+addArg(*f.TaskID))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := addArg("%" + q + "%")
		where = append(where, fmt.Sprintf("(ws.name ILIKE %s OR ws.url ILIKE %s OR ws.target ILIKE %s OR ws.notes ILIKE %s)", p, p, p, p))
	}
	if s := strings.TrimSpace(f.Status); s != "" && s != "all" {
		where = append(where, "ws.status="+addArg(s))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	args = append(args, limit)
	query := `SELECT ` + webShellCols + `
FROM webshells ws
LEFT JOIN tasks t ON t.id = ws.task_id
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ws.created_at DESC, ws.id DESC
LIMIT $` + fmt.Sprint(len(args))
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WebShell{}
	for rows.Next() {
		ws, err := scanWebShell(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (d *DB) GetWebShell(id int64) (*WebShell, error) {
	ws, err := scanWebShell(d.QueryRow(`SELECT `+webShellCols+`
FROM webshells ws LEFT JOIN tasks t ON t.id = ws.task_id WHERE ws.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ws, err
}

// GetWebShellByURL finds an existing registration for the same URL under the same
// task (or the same "no task" scope), so an agent calling webshell_save twice
// updates the existing row instead of creating duplicates.
func (d *DB) GetWebShellByURL(taskID *int64, url string) (*WebShell, error) {
	var task any
	if taskID != nil {
		task = *taskID
	}
	ws, err := scanWebShell(d.QueryRow(`SELECT `+webShellCols+`
FROM webshells ws LEFT JOIN tasks t ON t.id = ws.task_id
WHERE ws.url=$1 AND ws.task_id IS NOT DISTINCT FROM $2
ORDER BY ws.id DESC LIMIT 1`, url, task))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ws, err
}

func webShellTaskArg(taskID *int64) any {
	if taskID == nil {
		return nil
	}
	return *taskID
}

func (d *DB) CreateWebShell(ws *WebShell) error {
	headers, err := json.Marshal(ws.Headers)
	if err != nil {
		return err
	}
	return d.QueryRow(`
INSERT INTO webshells(name, url, type, lang, mode, password, method, param, headers,
                     body_template, response_regex, response_b64, encoding, transport,
                     insecure, timeout_seconds, os_type, task_id, target, notes,
                     status, last_error, created_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
RETURNING id, created_at, updated_at`,
		ws.Name, ws.URL, ws.Type, ws.Lang, ws.Mode, ws.Password, ws.Method, ws.Param, headers,
		ws.BodyTemplate, ws.ResponseRegex, ws.ResponseB64, ws.Encoding, ws.Transport,
		ws.Insecure, ws.TimeoutSeconds, ws.OSType, webShellTaskArg(ws.TaskID), ws.Target, ws.Notes,
		ws.Status, ws.LastError, ws.CreatedBy,
	).Scan(&ws.ID, &ws.CreatedAt, &ws.UpdatedAt)
}

func (d *DB) UpdateWebShell(ws *WebShell) error {
	headers, err := json.Marshal(ws.Headers)
	if err != nil {
		return err
	}
	_, err = d.Exec(`
UPDATE webshells SET
    name=$2, url=$3, type=$4, lang=$5, mode=$6, password=$7, method=$8, param=$9,
    headers=$10, body_template=$11, response_regex=$12, response_b64=$13,
    encoding=$14, transport=$15, insecure=$16, timeout_seconds=$17, os_type=$18,
    task_id=$19, target=$20, notes=$21, updated_at=now()
WHERE id=$1`,
		ws.ID, ws.Name, ws.URL, ws.Type, ws.Lang, ws.Mode, ws.Password, ws.Method, ws.Param,
		headers, ws.BodyTemplate, ws.ResponseRegex, ws.ResponseB64, ws.Encoding, ws.Transport,
		ws.Insecure, ws.TimeoutSeconds, ws.OSType, webShellTaskArg(ws.TaskID), ws.Target, ws.Notes)
	return err
}

func (d *DB) DeleteWebShell(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM webshells WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkWebShellCheck records the outcome of a connection test. Empty osType /
// transport leave the previously detected values untouched.
func (d *DB) MarkWebShellCheck(id int64, status, lastErr, osType, transport string) error {
	_, err := d.Exec(`
UPDATE webshells SET
    status=$2, last_error=$3, last_checked_at=now(), updated_at=now(),
    os_type=CASE WHEN $4<>'' THEN $4 ELSE os_type END,
    transport=CASE WHEN $5<>'' THEN $5 ELSE transport END
WHERE id=$1`, id, status, lastErr, osType, transport)
	return err
}

func (d *DB) TouchWebShell(id int64) error {
	_, err := d.Exec(`UPDATE webshells SET last_used_at=now() WHERE id=$1`, id)
	return err
}

// ResetWebShellProbe clears cached connection facts after the URL, shell type or
// password changes: the old detected transport/OS no longer apply.
func (d *DB) ResetWebShellProbe(id int64) error {
	_, err := d.Exec(`UPDATE webshells SET transport='', os_type='', status='unknown', last_error='' WHERE id=$1`, id)
	return err
}
