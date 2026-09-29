package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/secretstore"
)

const testManagementSchema = `
CREATE TABLE IF NOT EXISTS test_folders (
	id      INTEGER PRIMARY KEY,
	name    TEXT NOT NULL,
	xray_key TEXT NOT NULL DEFAULT '',
	test_plan_key TEXT NOT NULL DEFAULT '',
	sort    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS test_cases (
	id            INTEGER PRIMARY KEY,
	request_id    INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
	folder_id     INTEGER REFERENCES test_folders(id) ON DELETE SET NULL,
	name          TEXT NOT NULL,
	enabled       INTEGER NOT NULL DEFAULT 1,
	tags          TEXT NOT NULL DEFAULT '[]',
	owner         TEXT NOT NULL DEFAULT '',
	priority      TEXT NOT NULL DEFAULT '',
	xray_key      TEXT NOT NULL DEFAULT '',
	requirements  TEXT NOT NULL DEFAULT '[]',
	test_plan_key TEXT NOT NULL DEFAULT '',
	request_spec  TEXT NOT NULL DEFAULT '{}',
	assertions    TEXT NOT NULL DEFAULT '[]',
	script_tests  TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS test_cases_request_id ON test_cases(request_id);
CREATE TABLE IF NOT EXISTS test_sets (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL,
	description   TEXT NOT NULL DEFAULT '',
	xray_key      TEXT NOT NULL DEFAULT '',
	test_plan_key TEXT NOT NULL DEFAULT '',
	sort          INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS test_set_tests (
	test_set_id INTEGER NOT NULL REFERENCES test_sets(id) ON DELETE CASCADE,
	test_id     INTEGER NOT NULL REFERENCES test_cases(id) ON DELETE CASCADE,
	PRIMARY KEY (test_set_id, test_id)
);
CREATE TABLE IF NOT EXISTS test_runs (
	id          INTEGER PRIMARY KEY,
	test_id     INTEGER REFERENCES test_cases(id) ON DELETE SET NULL,
	request_id  INTEGER REFERENCES requests(id) ON DELETE SET NULL,
	name        TEXT NOT NULL,
	status      TEXT NOT NULL,
	duration_ms REAL NOT NULL DEFAULT 0,
	steps       TEXT NOT NULL DEFAULT '[]',
	error       TEXT NOT NULL DEFAULT '',
	executed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS test_runs_test_id ON test_runs(test_id, id DESC);
CREATE TABLE IF NOT EXISTS test_executions (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL,
	description   TEXT NOT NULL DEFAULT '',
	env           TEXT NOT NULL DEFAULT '',
	test_ids      TEXT NOT NULL DEFAULT '[]',
	folder_ids    TEXT NOT NULL DEFAULT '[]',
	test_set_ids  TEXT NOT NULL DEFAULT '[]',
	tags          TEXT NOT NULL DEFAULT '[]',
	priorities    TEXT NOT NULL DEFAULT '[]',
	xray_key      TEXT NOT NULL DEFAULT '',
	test_plan_key TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'DRAFT',
	last_summary  TEXT NOT NULL DEFAULT '{}',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS xray_credentials (
	id   INTEGER PRIMARY KEY CHECK (id = 1),
	data TEXT NOT NULL
);
`

type TestFolder struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	XrayKey     string `json:"xrayKey,omitempty"`
	TestPlanKey string `json:"testPlanKey,omitempty"`
}

type TestSet struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	XrayKey     string  `json:"xrayKey,omitempty"`
	TestPlanKey string  `json:"testPlanKey,omitempty"`
	TestIDs     []int64 `json:"testIds,omitempty"`
}

type TestCase struct {
	ID           int64           `json:"id"`
	RequestID    int64           `json:"requestId"`
	FolderID     *int64          `json:"folderId"`
	Name         string          `json:"name"`
	Enabled      bool            `json:"enabled"`
	Tags         []string        `json:"tags,omitempty"`
	Owner        string          `json:"owner,omitempty"`
	Priority     string          `json:"priority,omitempty"`
	XrayKey      string          `json:"xrayKey,omitempty"`
	Requirements []string        `json:"requirements,omitempty"`
	TestPlanKey  string          `json:"testPlanKey,omitempty"`
	Request      *dsl.Request    `json:"request,omitempty"`
	Assertions   []dsl.Assertion `json:"assertions,omitempty"`
	ScriptTests  string          `json:"scriptTests,omitempty"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

type TestRun struct {
	ID         int64            `json:"id"`
	TestID     *int64           `json:"testId,omitempty"`
	RequestID  *int64           `json:"requestId,omitempty"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	DurationMs float64          `json:"durationMs"`
	Steps      []TestStepResult `json:"steps,omitempty"`
	Error      string           `json:"error,omitempty"`
	ExecutedAt time.Time        `json:"executedAt"`
}

type TestExecution struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Env         string         `json:"env,omitempty"`
	TestIDs     []int64        `json:"testIds,omitempty"`
	FolderIDs   []int64        `json:"folderIds,omitempty"`
	TestSetIDs  []int64        `json:"testSetIds,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Priorities  []string       `json:"priorities,omitempty"`
	XrayKey     string         `json:"xrayKey,omitempty"`
	TestPlanKey string         `json:"testPlanKey,omitempty"`
	Status      string         `json:"status"`
	LastSummary map[string]any `json:"lastSummary,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type TestStepResult struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Passed   bool   `json:"passed"`
	Message  string `json:"message,omitempty"`
}

type XrayCredentials struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	JiraEmail    string `json:"jiraEmail,omitempty"`
	JiraAPIKey   string `json:"jiraApiKey,omitempty"`
}

type XrayCredentialStatus struct {
	HasClientID         bool   `json:"hasClientId"`
	HasClientSecret     bool   `json:"hasClientSecret"`
	HasJiraEmail        bool   `json:"hasJiraEmail"`
	HasJiraAPIKey       bool   `json:"hasJiraApiKey"`
	LegacySQLitePresent bool   `json:"legacySQLitePresent"`
	Source              string `json:"source"`
}

func (s *Store) ensureTestManagement() error {
	if _, err := s.db.Exec(testManagementSchema); err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE test_cases ADD COLUMN request_spec TEXT NOT NULL DEFAULT '{}'`)
	_, _ = s.db.Exec(`ALTER TABLE test_folders ADD COLUMN xray_key TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE test_folders ADD COLUMN test_plan_key TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *Store) EnsureDefaultTestCases() error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT id, spec FROM requests WHERE id NOT IN (SELECT request_id FROM test_cases) ORDER BY id`)
	if err != nil {
		return err
	}
	var pending []TestCase
	for rows.Next() {
		var reqID int64
		var specJSON string
		if err := rows.Scan(&reqID, &specJSON); err != nil {
			rows.Close()
			return err
		}
		var spec dsl.Request
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			rows.Close()
			return err
		}
		tc := TestCase{
			RequestID:    reqID,
			Name:         defaultTestName(spec),
			Enabled:      true,
			Tags:         append([]string(nil), spec.Tags...),
			Owner:        spec.Owner,
			Priority:     spec.Priority,
			XrayKey:      spec.XrayKey,
			Requirements: append([]string(nil), spec.Requirements...),
			Request:      testRequestSpec(spec),
			Assertions:   append([]dsl.Assertion(nil), spec.Assertions...),
		}
		if spec.Scripts != nil {
			tc.ScriptTests = spec.Scripts.Tests
		}
		pending = append(pending, tc)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i := range pending {
		if err := s.CreateTestCase(&pending[i]); err != nil {
			return err
		}
	}
	return nil
}

func defaultTestName(spec dsl.Request) string {
	if strings.TrimSpace(spec.Name) == "" {
		return "Default test"
	}
	return spec.Name + " - default"
}

func testRequestSpec(spec dsl.Request) *dsl.Request {
	req := &dsl.Request{
		Name:    spec.Name,
		Method:  spec.Method,
		URL:     spec.URL,
		Query:   copyStringMap(spec.Query),
		Headers: copyStringMap(spec.Headers),
		Auth:    spec.Auth,
		Body:    spec.Body,
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	return req
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Store) TestFolders() ([]TestFolder, error) {
	if err := s.ensureTestManagement(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, name, xray_key, test_plan_key FROM test_folders ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestFolder
	for rows.Next() {
		var f TestFolder
		if err := rows.Scan(&f.ID, &f.Name, &f.XrayKey, &f.TestPlanKey); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) CreateTestFolder(f *TestFolder) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("test folder needs a name")
	}
	res, err := s.db.Exec(`INSERT INTO test_folders (name, xray_key, test_plan_key) VALUES (?, ?, ?)`, strings.TrimSpace(f.Name), f.XrayKey, f.TestPlanKey)
	if err != nil {
		return err
	}
	f.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateTestFolder(f *TestFolder) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("test folder needs a name")
	}
	_, err := s.db.Exec(`UPDATE test_folders SET name = ?, xray_key = ?, test_plan_key = ? WHERE id = ?`, strings.TrimSpace(f.Name), f.XrayKey, f.TestPlanKey, f.ID)
	return err
}

func (s *Store) DeleteTestFolder(id int64) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM test_folders WHERE id = ?`, id)
	return err
}

func (s *Store) TestCases() ([]TestCase, error) {
	if err := s.EnsureDefaultTestCases(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, request_id, folder_id, name, enabled, tags, owner, priority, xray_key, requirements, test_plan_key, request_spec, assertions, script_tests, updated_at FROM test_cases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestCase
	for rows.Next() {
		tc, err := scanTestCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

func (s *Store) TestCasesForRequest(requestID int64) ([]TestCase, error) {
	if err := s.EnsureDefaultTestCases(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, request_id, folder_id, name, enabled, tags, owner, priority, xray_key, requirements, test_plan_key, request_spec, assertions, script_tests, updated_at FROM test_cases WHERE request_id = ? ORDER BY id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestCase
	for rows.Next() {
		tc, err := scanTestCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

func (s *Store) TestCase(id int64) (*TestCase, error) {
	if err := s.EnsureDefaultTestCases(); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT id, request_id, folder_id, name, enabled, tags, owner, priority, xray_key, requirements, test_plan_key, request_spec, assertions, script_tests, updated_at FROM test_cases WHERE id = ?`, id)
	tc, err := scanTestCase(row)
	if err != nil {
		return nil, err
	}
	return &tc, nil
}

type testCaseScanner interface {
	Scan(dest ...any) error
}

func scanTestCase(row testCaseScanner) (TestCase, error) {
	var tc TestCase
	var folder sql.NullInt64
	var enabled int
	var tags, reqs, requestSpec, assertions, updated string
	if err := row.Scan(&tc.ID, &tc.RequestID, &folder, &tc.Name, &enabled, &tags, &tc.Owner, &tc.Priority, &tc.XrayKey, &reqs, &tc.TestPlanKey, &requestSpec, &assertions, &tc.ScriptTests, &updated); err != nil {
		return tc, err
	}
	if folder.Valid {
		tc.FolderID = &folder.Int64
	}
	tc.Enabled = enabled != 0
	tc.Tags = unj[[]string](tags)
	tc.Requirements = unj[[]string](reqs)
	req := unj[dsl.Request](requestSpec)
	if req.Method != "" || req.URL != "" || len(req.Headers) > 0 || req.Body != nil {
		tc.Request = &req
	}
	tc.Assertions = unj[[]dsl.Assertion](assertions)
	tc.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return tc, nil
}

func (s *Store) CreateTestCase(tc *TestCase) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if tc.RequestID == 0 {
		return fmt.Errorf("test case needs a requestId")
	}
	if strings.TrimSpace(tc.Name) == "" {
		tc.Name = "Untitled test"
	}
	res, err := s.db.Exec(`INSERT INTO test_cases (request_id, folder_id, name, enabled, tags, owner, priority, xray_key, requirements, test_plan_key, request_spec, assertions, script_tests, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.RequestID, tc.FolderID, strings.TrimSpace(tc.Name), boolInt(tc.Enabled), j(tc.Tags), tc.Owner, tc.Priority,
		tc.XrayKey, j(tc.Requirements), tc.TestPlanKey, j(tc.Request), j(tc.Assertions), tc.ScriptTests, now())
	if err != nil {
		return err
	}
	tc.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateTestCase(tc *TestCase) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if tc.ID == 0 {
		return fmt.Errorf("test case needs an id")
	}
	if tc.RequestID == 0 {
		return fmt.Errorf("test case needs a requestId")
	}
	if strings.TrimSpace(tc.Name) == "" {
		return fmt.Errorf("test case needs a name")
	}
	_, err := s.db.Exec(`UPDATE test_cases
		SET request_id = ?, folder_id = ?, name = ?, enabled = ?, tags = ?, owner = ?, priority = ?, xray_key = ?, requirements = ?, test_plan_key = ?, request_spec = ?, assertions = ?, script_tests = ?, updated_at = ?
		WHERE id = ?`,
		tc.RequestID, tc.FolderID, strings.TrimSpace(tc.Name), boolInt(tc.Enabled), j(tc.Tags), tc.Owner, tc.Priority,
		tc.XrayKey, j(tc.Requirements), tc.TestPlanKey, j(tc.Request), j(tc.Assertions), tc.ScriptTests, now(), tc.ID)
	return err
}

func (s *Store) DeleteTestCase(id int64) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM test_cases WHERE id = ?`, id)
	return err
}

func (s *Store) TestSets() ([]TestSet, error) {
	if err := s.ensureTestManagement(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, name, description, xray_key, test_plan_key FROM test_sets ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestSet
	for rows.Next() {
		var ts TestSet
		if err := rows.Scan(&ts.ID, &ts.Name, &ts.Description, &ts.XrayKey, &ts.TestPlanKey); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load the member test IDs only after the outer rows are fully drained.
	// Issuing a nested query while these rows are still open would deadlock
	// against the single-connection pool (SetMaxOpenConns(1)).
	for i := range out {
		if out[i].TestIDs, err = s.testSetIDs(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) TestSet(id int64) (*TestSet, error) {
	if err := s.ensureTestManagement(); err != nil {
		return nil, err
	}
	var ts TestSet
	err := s.db.QueryRow(`SELECT id, name, description, xray_key, test_plan_key FROM test_sets WHERE id = ?`, id).
		Scan(&ts.ID, &ts.Name, &ts.Description, &ts.XrayKey, &ts.TestPlanKey)
	if err != nil {
		return nil, err
	}
	ts.TestIDs, err = s.testSetIDs(ts.ID)
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

func (s *Store) testSetIDs(id int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT test_id FROM test_set_tests WHERE test_set_id = ? ORDER BY test_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var testID int64
		if err := rows.Scan(&testID); err != nil {
			return nil, err
		}
		ids = append(ids, testID)
	}
	return ids, rows.Err()
}

func (s *Store) CreateTestSet(ts *TestSet) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if strings.TrimSpace(ts.Name) == "" {
		return fmt.Errorf("test set needs a name")
	}
	// Insert the set and its membership in a single transaction so a failure
	// linking tests cannot leave an orphaned, empty test set behind.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO test_sets (name, description, xray_key, test_plan_key) VALUES (?, ?, ?, ?)`,
		strings.TrimSpace(ts.Name), ts.Description, ts.XrayKey, ts.TestPlanKey)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := replaceTestSetTestsTx(tx, id, ts.TestIDs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ts.ID = id
	return nil
}

func (s *Store) UpdateTestSet(ts *TestSet) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if strings.TrimSpace(ts.Name) == "" {
		return fmt.Errorf("test set needs a name")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE test_sets SET name = ?, description = ?, xray_key = ?, test_plan_key = ? WHERE id = ?`,
		strings.TrimSpace(ts.Name), ts.Description, ts.XrayKey, ts.TestPlanKey, ts.ID); err != nil {
		return err
	}
	if err := replaceTestSetTestsTx(tx, ts.ID, ts.TestIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteTestSet(id int64) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM test_sets WHERE id = ?`, id)
	return err
}

func (s *Store) TestExecutions() ([]TestExecution, error) {
	if err := s.ensureTestManagement(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, name, description, env, test_ids, folder_ids, test_set_ids, tags, priorities, xray_key, test_plan_key, status, last_summary, created_at, updated_at FROM test_executions ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestExecution
	for rows.Next() {
		ex, err := scanTestExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	return out, rows.Err()
}

func (s *Store) TestExecution(id int64) (*TestExecution, error) {
	if err := s.ensureTestManagement(); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT id, name, description, env, test_ids, folder_ids, test_set_ids, tags, priorities, xray_key, test_plan_key, status, last_summary, created_at, updated_at FROM test_executions WHERE id = ?`, id)
	ex, err := scanTestExecution(row)
	if err != nil {
		return nil, err
	}
	return &ex, nil
}

func scanTestExecution(row testCaseScanner) (TestExecution, error) {
	var ex TestExecution
	var testIDs, folderIDs, setIDs, tags, priorities, summary, created, updated string
	if err := row.Scan(&ex.ID, &ex.Name, &ex.Description, &ex.Env, &testIDs, &folderIDs, &setIDs, &tags, &priorities, &ex.XrayKey, &ex.TestPlanKey, &ex.Status, &summary, &created, &updated); err != nil {
		return ex, err
	}
	ex.TestIDs = unj[[]int64](testIDs)
	ex.FolderIDs = unj[[]int64](folderIDs)
	ex.TestSetIDs = unj[[]int64](setIDs)
	ex.Tags = unj[[]string](tags)
	ex.Priorities = unj[[]string](priorities)
	ex.LastSummary = unj[map[string]any](summary)
	ex.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	ex.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return ex, nil
}

func (s *Store) CreateTestExecution(ex *TestExecution) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if strings.TrimSpace(ex.Name) == "" {
		ex.Name = "New execution"
	}
	if ex.Status == "" {
		ex.Status = "DRAFT"
	}
	res, err := s.db.Exec(`INSERT INTO test_executions (name, description, env, test_ids, folder_ids, test_set_ids, tags, priorities, xray_key, test_plan_key, status, last_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(ex.Name), ex.Description, ex.Env, j(ex.TestIDs), j(ex.FolderIDs), j(ex.TestSetIDs), j(ex.Tags), j(ex.Priorities),
		ex.XrayKey, ex.TestPlanKey, ex.Status, j(ex.LastSummary), now(), now())
	if err != nil {
		return err
	}
	ex.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateTestExecution(ex *TestExecution) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	if ex.ID == 0 {
		return fmt.Errorf("test execution needs an id")
	}
	if strings.TrimSpace(ex.Name) == "" {
		return fmt.Errorf("test execution needs a name")
	}
	_, err := s.db.Exec(`UPDATE test_executions
		SET name = ?, description = ?, env = ?, test_ids = ?, folder_ids = ?, test_set_ids = ?, tags = ?, priorities = ?, xray_key = ?, test_plan_key = ?, status = ?, last_summary = ?, updated_at = ?
		WHERE id = ?`,
		strings.TrimSpace(ex.Name), ex.Description, ex.Env, j(ex.TestIDs), j(ex.FolderIDs), j(ex.TestSetIDs), j(ex.Tags), j(ex.Priorities),
		ex.XrayKey, ex.TestPlanKey, ex.Status, j(ex.LastSummary), now(), ex.ID)
	return err
}

func (s *Store) DeleteTestExecution(id int64) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM test_executions WHERE id = ?`, id)
	return err
}

func (s *Store) ReplaceTestSetTests(setID int64, testIDs []int64) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceTestSetTestsTx(tx, setID, testIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceTestSetTestsTx rewrites a test set's membership inside an existing
// transaction. The SELECT-guarded insert skips test IDs that don't exist
// instead of raising a foreign-key error, so stale IDs from the client are
// dropped gracefully rather than failing the whole operation.
func replaceTestSetTestsTx(tx *sql.Tx, setID int64, testIDs []int64) error {
	if _, err := tx.Exec(`DELETE FROM test_set_tests WHERE test_set_id = ?`, setID); err != nil {
		return err
	}
	for _, id := range testIDs {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO test_set_tests (test_set_id, test_id) SELECT ?, id FROM test_cases WHERE id = ?`,
			setID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddTestRun(run *TestRun) error {
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	when := run.ExecutedAt
	if when.IsZero() {
		when = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO test_runs (test_id, request_id, name, status, duration_ms, steps, error, executed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.TestID, run.RequestID, run.Name, run.Status, run.DurationMs, j(run.Steps), run.Error, when.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	run.ID, err = res.LastInsertId()
	_, _ = s.db.Exec(`DELETE FROM test_runs WHERE id NOT IN (SELECT id FROM test_runs ORDER BY id DESC LIMIT 1000)`)
	return err
}

func (s *Store) LastTestRuns() (map[int64]TestRun, error) {
	if err := s.ensureTestManagement(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT tr.id, tr.test_id, tr.request_id, tr.name, tr.status, tr.duration_ms, tr.steps, tr.error, tr.executed_at
		FROM test_runs tr
		JOIN (SELECT test_id, MAX(id) id FROM test_runs WHERE test_id IS NOT NULL GROUP BY test_id) last ON last.id = tr.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TestRun{}
	for rows.Next() {
		run, err := scanTestRun(rows)
		if err != nil {
			return nil, err
		}
		if run.TestID != nil {
			out[*run.TestID] = run
		}
	}
	return out, rows.Err()
}

func scanTestRun(row testCaseScanner) (TestRun, error) {
	var run TestRun
	var testID, requestID sql.NullInt64
	var steps, executed string
	if err := row.Scan(&run.ID, &testID, &requestID, &run.Name, &run.Status, &run.DurationMs, &steps, &run.Error, &executed); err != nil {
		return run, err
	}
	if testID.Valid {
		run.TestID = &testID.Int64
	}
	if requestID.Valid {
		run.RequestID = &requestID.Int64
	}
	run.Steps = unj[[]TestStepResult](steps)
	run.ExecutedAt, _ = time.Parse(time.RFC3339Nano, executed)
	return run, nil
}

func (s *Store) SaveXrayCredentials(creds XrayCredentials) error {
	if s.secretStore != nil {
		// One JSON record makes updates atomic at the backend boundary. Existing
		// SQLite credentials remain untouched as a recoverable legacy backup.
		data, err := json.Marshal(creds)
		if err != nil {
			return err
		}
		key, err := s.xrayCredentialSecretKey()
		if err != nil {
			return err
		}
		return s.secretStore.Set(key, string(data))
	}
	if err := s.ensureTestManagement(); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO xray_credentials (id, data) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data`, j(creds))
	return err
}

func (s *Store) XrayCredentials() (XrayCredentials, error) {
	creds, _, err := s.xrayCredentialsWithSource()
	return creds, err
}

func (s *Store) xrayCredentialsWithSource() (XrayCredentials, string, error) {
	if s.secretStore != nil {
		key, err := s.xrayCredentialSecretKey()
		if err != nil {
			return XrayCredentials{}, "", err
		}
		data, err := s.secretStore.Get(key)
		if err == nil {
			var creds XrayCredentials
			if err := json.Unmarshal([]byte(data), &creds); err != nil {
				return XrayCredentials{}, "", fmt.Errorf("decoding external Xray credentials: %w", err)
			}
			return creds, "secure store", nil
		}
		if !errors.Is(err, secretstore.ErrNotFound) {
			return XrayCredentials{}, "", err
		}
	}
	creds, err := s.legacyXrayCredentials()
	if err != nil {
		return XrayCredentials{}, "", err
	}
	if creds.ClientID == "" && creds.ClientSecret == "" && creds.JiraEmail == "" && creds.JiraAPIKey == "" {
		return creds, "none", nil
	}
	if s.secretStore != nil {
		return creds, "legacy SQLite", nil
	}
	return creds, "stored", nil
}

func (s *Store) legacyXrayCredentials() (XrayCredentials, error) {
	if err := s.ensureTestManagement(); err != nil {
		return XrayCredentials{}, err
	}
	var data string
	err := s.db.QueryRow(`SELECT data FROM xray_credentials WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return XrayCredentials{}, nil
	}
	if err != nil {
		return XrayCredentials{}, err
	}
	var creds XrayCredentials
	if err := json.Unmarshal([]byte(data), &creds); err != nil {
		return XrayCredentials{}, fmt.Errorf("decoding legacy Xray credentials: %w", err)
	}
	return creds, nil
}

func (s *Store) XrayCredentialStatus() (XrayCredentialStatus, error) {
	creds, source, err := s.xrayCredentialsWithSource()
	if err != nil {
		return XrayCredentialStatus{}, err
	}
	legacy, err := s.legacyXrayCredentials()
	if err != nil {
		return XrayCredentialStatus{}, err
	}
	return XrayCredentialStatus{
		HasClientID:         creds.ClientID != "",
		HasClientSecret:     creds.ClientSecret != "",
		HasJiraEmail:        creds.JiraEmail != "",
		HasJiraAPIKey:       creds.JiraAPIKey != "",
		LegacySQLitePresent: legacy.ClientID != "" || legacy.ClientSecret != "" || legacy.JiraEmail != "" || legacy.JiraAPIKey != "",
		Source:              source,
	}, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
