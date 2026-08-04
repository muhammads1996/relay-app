// Package store persists workspaces in SQLite (modernc.org/sqlite, pure
// Go): collections, folders, requests, environments, and send history. The
// request payload is the same dsl.Request the file format uses, stored as
// JSON, so the store and the .req.toml interchange format never drift.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/muhaymien96/relay/internal/dsl"
)

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS collections (
	id      INTEGER PRIMARY KEY,
	name    TEXT NOT NULL,
	headers TEXT NOT NULL DEFAULT '{}',
	vars    TEXT NOT NULL DEFAULT '{}',
	sort    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS folders (
	id            INTEGER PRIMARY KEY,
	collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	headers       TEXT NOT NULL DEFAULT '{}',
	vars          TEXT NOT NULL DEFAULT '{}',
	sort          INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS requests (
	id            INTEGER PRIMARY KEY,
	collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
	folder_id     INTEGER REFERENCES folders(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	method        TEXT NOT NULL,
	url           TEXT NOT NULL,
	spec          TEXT NOT NULL,
	sort          INTEGER NOT NULL DEFAULT 0,
	updated_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS environments (
	id      INTEGER PRIMARY KEY,
	name    TEXT NOT NULL UNIQUE,
	vars    TEXT NOT NULL DEFAULT '{}',
	secrets TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS history (
	id           INTEGER PRIMARY KEY,
	request_id   INTEGER,
	request_name TEXT NOT NULL,
	method       TEXT NOT NULL,
	url          TEXT NOT NULL,
	status       INTEGER NOT NULL,
	duration_ms  REAL NOT NULL,
	resp_headers TEXT NOT NULL DEFAULT '{}',
	resp_body    BLOB,
	timing       TEXT NOT NULL DEFAULT '{}',
	sent_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS history_sent_at ON history(sent_at DESC);
CREATE TABLE IF NOT EXISTS presets (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	headers     TEXT NOT NULL DEFAULT '[]',
	updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS preset_attachments (
	preset_id     INTEGER NOT NULL REFERENCES presets(id) ON DELETE CASCADE,
	collection_id INTEGER REFERENCES collections(id) ON DELETE CASCADE,
	folder_id     INTEGER REFERENCES folders(id) ON DELETE CASCADE
);
`

// Store is an open workspace database.
type Store struct {
	db *sql.DB
}

// Collection is a top-level group of requests with inheritable headers/vars.
type Collection struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Headers map[string]string `json:"headers"`
	Vars    map[string]string `json:"vars"`
}

// Folder groups requests inside a collection.
type Folder struct {
	ID           int64             `json:"id"`
	CollectionID int64             `json:"collectionId"`
	Name         string            `json:"name"`
	Headers      map[string]string `json:"headers"`
	Vars         map[string]string `json:"vars"`
}

// Request is a stored request: identity plus the dsl.Request payload.
type Request struct {
	ID           int64        `json:"id"`
	CollectionID int64        `json:"collectionId"`
	FolderID     *int64       `json:"folderId"`
	Spec         *dsl.Request `json:"spec"`
}

// Environment mirrors dsl.Environment with identity.
type Environment struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Vars    map[string]string `json:"vars"`
	Secrets []string          `json:"secrets"`
}

// HistoryEntry is one recorded send.
type HistoryEntry struct {
	ID          int64              `json:"id"`
	RequestID   *int64             `json:"requestId"`
	RequestName string             `json:"requestName"`
	Method      string             `json:"method"`
	URL         string             `json:"url"`
	Status      int                `json:"status"`
	DurationMs  float64            `json:"durationMs"`
	RespHeaders map[string]string  `json:"respHeaders,omitempty"`
	RespBody    []byte             `json:"respBody,omitempty"`
	Timing      map[string]float64 `json:"timing,omitempty"`
	SentAt      time.Time          `json:"sentAt"`
}

// Open opens (creating if needed) the database at path and applies the
// schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite serializes per connection; a single connection
	// avoids SQLITE_BUSY under the UI's concurrent handlers.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Empty reports whether the store has no collections yet (used to decide
// whether to seed from a directory on first launch).
func (s *Store) Empty() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM collections`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

func j(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func unj[T any](s string) T {
	var v T
	_ = json.Unmarshal([]byte(s), &v)
	return v
}

// --- collections ---

func (s *Store) CreateCollection(c *Collection) error {
	res, err := s.db.Exec(`INSERT INTO collections (name, headers, vars) VALUES (?, ?, ?)`,
		c.Name, j(c.Headers), j(c.Vars))
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateCollection(c *Collection) error {
	_, err := s.db.Exec(`UPDATE collections SET name = ?, headers = ?, vars = ? WHERE id = ?`,
		c.Name, j(c.Headers), j(c.Vars), c.ID)
	return err
}

func (s *Store) DeleteCollection(id int64) error {
	_, err := s.db.Exec(`DELETE FROM collections WHERE id = ?`, id)
	return err
}

func (s *Store) Collections() ([]Collection, error) {
	rows, err := s.db.Query(`SELECT id, name, headers, vars FROM collections ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var c Collection
		var h, v string
		if err := rows.Scan(&c.ID, &c.Name, &h, &v); err != nil {
			return nil, err
		}
		c.Headers, c.Vars = unj[map[string]string](h), unj[map[string]string](v)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Collection(id int64) (*Collection, error) {
	var c Collection
	var h, v string
	err := s.db.QueryRow(`SELECT id, name, headers, vars FROM collections WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &h, &v)
	if err != nil {
		return nil, err
	}
	c.Headers, c.Vars = unj[map[string]string](h), unj[map[string]string](v)
	return &c, nil
}

// --- folders ---

func (s *Store) CreateFolder(f *Folder) error {
	res, err := s.db.Exec(`INSERT INTO folders (collection_id, name, headers, vars) VALUES (?, ?, ?, ?)`,
		f.CollectionID, f.Name, j(f.Headers), j(f.Vars))
	if err != nil {
		return err
	}
	f.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateFolder(f *Folder) error {
	_, err := s.db.Exec(`UPDATE folders SET name = ?, headers = ?, vars = ? WHERE id = ?`,
		f.Name, j(f.Headers), j(f.Vars), f.ID)
	return err
}

func (s *Store) DeleteFolder(id int64) error {
	_, err := s.db.Exec(`DELETE FROM folders WHERE id = ?`, id)
	return err
}

func (s *Store) Folders(collectionID int64) ([]Folder, error) {
	rows, err := s.db.Query(`SELECT id, collection_id, name, headers, vars FROM folders WHERE collection_id = ? ORDER BY sort, id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		var h, v string
		if err := rows.Scan(&f.ID, &f.CollectionID, &f.Name, &h, &v); err != nil {
			return nil, err
		}
		f.Headers, f.Vars = unj[map[string]string](h), unj[map[string]string](v)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Folder(id int64) (*Folder, error) {
	var f Folder
	var h, v string
	err := s.db.QueryRow(`SELECT id, collection_id, name, headers, vars FROM folders WHERE id = ?`, id).
		Scan(&f.ID, &f.CollectionID, &f.Name, &h, &v)
	if err != nil {
		return nil, err
	}
	f.Headers, f.Vars = unj[map[string]string](h), unj[map[string]string](v)
	return &f, nil
}

// --- requests ---

func (s *Store) CreateRequest(r *Request) error {
	if err := normalizeSpec(r); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`INSERT INTO requests (collection_id, folder_id, name, method, url, spec, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.CollectionID, r.FolderID, r.Spec.Name, r.Spec.Method, r.Spec.URL, j(r.Spec), now())
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateRequest(r *Request) error {
	if err := normalizeSpec(r); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`UPDATE requests SET folder_id = ?, name = ?, method = ?, url = ?, spec = ?, updated_at = ? WHERE id = ?`,
		r.FolderID, r.Spec.Name, r.Spec.Method, r.Spec.URL, j(r.Spec), now(), r.ID)
	return err
}

func (s *Store) DeleteRequest(id int64) error {
	_, err := s.db.Exec(`DELETE FROM requests WHERE id = ?`, id)
	return err
}

func (s *Store) Request(id int64) (*Request, error) {
	var r Request
	var spec string
	err := s.db.QueryRow(`SELECT id, collection_id, folder_id, spec FROM requests WHERE id = ?`, id).
		Scan(&r.ID, &r.CollectionID, &r.FolderID, &spec)
	if err != nil {
		return nil, err
	}
	r.Spec = &dsl.Request{}
	if err := json.Unmarshal([]byte(spec), r.Spec); err != nil {
		return nil, fmt.Errorf("request %d: corrupt spec: %w", id, err)
	}
	return &r, nil
}

func (s *Store) Requests(collectionID int64) ([]Request, error) {
	rows, err := s.db.Query(`SELECT id, collection_id, folder_id, spec FROM requests WHERE collection_id = ? ORDER BY sort, id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var spec string
		if err := rows.Scan(&r.ID, &r.CollectionID, &r.FolderID, &spec); err != nil {
			return nil, err
		}
		r.Spec = &dsl.Request{}
		if err := json.Unmarshal([]byte(spec), r.Spec); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func normalizeSpec(r *Request) error {
	if r.Spec == nil {
		return fmt.Errorf("request has no spec")
	}
	if r.Spec.URL == "" {
		return fmt.Errorf("request needs a url")
	}
	if r.Spec.Method == "" {
		r.Spec.Method = "GET"
	}
	if r.Spec.Name == "" {
		r.Spec.Name = "Untitled"
	}
	return nil
}

// --- environments ---

func (s *Store) UpsertEnvironment(e *Environment) error {
	if e.Name == "" {
		return fmt.Errorf("environment needs a name")
	}
	res, err := s.db.Exec(`INSERT INTO environments (name, vars, secrets) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET vars = excluded.vars, secrets = excluded.secrets`,
		e.Name, j(e.Vars), j(e.Secrets))
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		e.ID = id
	}
	return nil
}

func (s *Store) DeleteEnvironment(name string) error {
	_, err := s.db.Exec(`DELETE FROM environments WHERE name = ?`, name)
	return err
}

func (s *Store) Environments() ([]Environment, error) {
	rows, err := s.db.Query(`SELECT id, name, vars, secrets FROM environments ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		var v, sec string
		if err := rows.Scan(&e.ID, &e.Name, &v, &sec); err != nil {
			return nil, err
		}
		e.Vars, e.Secrets = unj[map[string]string](v), unj[[]string](sec)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Environment(name string) (*Environment, error) {
	var e Environment
	var v, sec string
	err := s.db.QueryRow(`SELECT id, name, vars, secrets FROM environments WHERE name = ?`, name).
		Scan(&e.ID, &e.Name, &v, &sec)
	if err != nil {
		return nil, err
	}
	e.Vars, e.Secrets = unj[map[string]string](v), unj[[]string](sec)
	return &e, nil
}

// --- history ---

// MaxHistoryBody caps stored response bodies; bigger bodies are truncated.
const MaxHistoryBody = 256 << 10

func (s *Store) AddHistory(h *HistoryEntry) error {
	body := h.RespBody
	if len(body) > MaxHistoryBody {
		body = body[:MaxHistoryBody]
	}
	res, err := s.db.Exec(
		`INSERT INTO history (request_id, request_name, method, url, status, duration_ms, resp_headers, resp_body, timing, sent_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.RequestID, h.RequestName, h.Method, h.URL, h.Status, h.DurationMs,
		j(h.RespHeaders), body, j(h.Timing), h.SentAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	h.ID, err = res.LastInsertId()
	// Keep the table bounded.
	_, _ = s.db.Exec(`DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT 500)`)
	return err
}

// History lists recent sends, newest first, without bodies.
func (s *Store) History(limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, request_id, request_name, method, url, status, duration_ms, sent_at FROM history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var sent string
		if err := rows.Scan(&h.ID, &h.RequestID, &h.RequestName, &h.Method, &h.URL, &h.Status, &h.DurationMs, &sent); err != nil {
			return nil, err
		}
		h.SentAt, _ = time.Parse(time.RFC3339Nano, sent)
		out = append(out, h)
	}
	return out, rows.Err()
}

// HistoryEntry returns one send including the stored response.
func (s *Store) HistoryEntry(id int64) (*HistoryEntry, error) {
	var h HistoryEntry
	var sent, headers, timing string
	err := s.db.QueryRow(
		`SELECT id, request_id, request_name, method, url, status, duration_ms, resp_headers, resp_body, timing, sent_at
		 FROM history WHERE id = ?`, id).
		Scan(&h.ID, &h.RequestID, &h.RequestName, &h.Method, &h.URL, &h.Status, &h.DurationMs, &headers, &h.RespBody, &timing, &sent)
	if err != nil {
		return nil, err
	}
	h.RespHeaders = unj[map[string]string](headers)
	h.Timing = unj[map[string]float64](timing)
	h.SentAt, _ = time.Parse(time.RFC3339Nano, sent)
	return &h, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// RequestStats aggregates history for one request (powers the workbench's
// performance metrics card).
type RequestStats struct {
	Count       int     `json:"count"`
	AvgMs       float64 `json:"avgMs"`
	SuccessRate float64 `json:"successRate"` // 0..1, status < 400
}

func (s *Store) RequestStats(requestID int64) (*RequestStats, error) {
	var st RequestStats
	err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(AVG(duration_ms), 0),
		        COALESCE(AVG(CASE WHEN status > 0 AND status < 400 THEN 1.0 ELSE 0.0 END), 0)
		 FROM history WHERE request_id = ?`, requestID).
		Scan(&st.Count, &st.AvgMs, &st.SuccessRate)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// Settings are workspace-level engine preferences, stored as a single JSON
// row so the schema never needs migrating for new knobs.
type Settings struct {
	TimeoutSeconds  int  `json:"timeoutSeconds"`
	FollowRedirects bool `json:"followRedirects"`
	Insecure        bool `json:"insecure"` // skip TLS verification (self-signed SIT/UAT)
}

// DefaultSettings mirror engine.NewOptions.
func DefaultSettings() Settings {
	return Settings{TimeoutSeconds: 30, FollowRedirects: true, Insecure: false}
}

func (s *Store) Settings() (Settings, error) {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY CHECK (id = 1), data TEXT NOT NULL)`); err != nil {
		return DefaultSettings(), err
	}
	var data string
	err := s.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&data)
	if err != nil {
		return DefaultSettings(), nil // no row yet
	}
	out := DefaultSettings()
	_ = json.Unmarshal([]byte(data), &out)
	if out.TimeoutSeconds <= 0 {
		out.TimeoutSeconds = 30
	}
	return out, nil
}

func (s *Store) SaveSettings(set Settings) error {
	if set.TimeoutSeconds <= 0 || set.TimeoutSeconds > 600 {
		return fmt.Errorf("timeout must be 1-600 seconds")
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY CHECK (id = 1), data TEXT NOT NULL)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO settings (id, data) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data`, j(set))
	return err
}

// XraySettings holds Xray Cloud connection parameters (credentials come from
// environment variables — never stored here).
type XraySettings struct {
	ProjectKey  string `json:"projectKey"`
	TestPlanKey string `json:"testPlanKey"`
	TestSetKey  string `json:"testSetKey"`
	CloudURL    string `json:"cloudUrl"` // override default; empty = Xray Cloud default
	AuthURL     string `json:"authUrl"`  // override default auth endpoint
	Labels      string `json:"labels"`
	Component   string `json:"component"`

	// JiraBaseURL/JiraEmail are needed for requirement-linking (plain Jira
	// REST, not Xray's GraphQL API). Not secret — the API token that pairs
	// with JiraEmail is read from RELAY_JIRA_API_TOKEN only, never stored.
	JiraBaseURL string `json:"jiraBaseUrl"`
	JiraEmail   string `json:"jiraEmail"`
}

func (s *Store) XraySettings() (XraySettings, error) {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS xray_settings (id INTEGER PRIMARY KEY CHECK (id = 1), data TEXT NOT NULL)`); err != nil {
		return XraySettings{}, err
	}
	var data string
	err := s.db.QueryRow(`SELECT data FROM xray_settings WHERE id = 1`).Scan(&data)
	if err != nil {
		return XraySettings{}, nil // no row yet = not configured
	}
	var xs XraySettings
	_ = json.Unmarshal([]byte(data), &xs)
	return xs, nil
}

func (s *Store) SaveXraySettings(xs XraySettings) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS xray_settings (id INTEGER PRIMARY KEY CHECK (id = 1), data TEXT NOT NULL)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO xray_settings (id, data) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data`, j(xs))
	return err
}
