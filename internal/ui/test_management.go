package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/adapters/tm"
	"github.com/muhaymien96/relay/internal/adapters/xray"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
)

type testsState struct {
	Collections []testCollectionState   `json:"collections"`
	TestFolders []store.TestFolder      `json:"testFolders"`
	TestSets    []store.TestSet         `json:"testSets"`
	Executions  []store.TestExecution   `json:"executions"`
	Tests       []store.TestCase        `json:"tests"`
	LastRuns    map[int64]store.TestRun `json:"lastRuns"`
	Xray        xraySettingsResponse    `json:"xray"`
}

type testCollectionState struct {
	store.Collection
	Folders  []store.Folder `json:"folders"`
	Requests []requestMeta  `json:"requests"`
}

type xraySettingsResponse struct {
	ProjectKey  string                     `json:"projectKey"`
	TestPlanKey string                     `json:"testPlanKey"`
	TestSetKey  string                     `json:"testSetKey"`
	CloudURL    string                     `json:"cloudUrl"`
	AuthURL     string                     `json:"authUrl"`
	Labels      string                     `json:"labels"`
	Component   string                     `json:"component"`
	JiraBaseURL string                     `json:"jiraBaseUrl"`
	JiraEmail   string                     `json:"jiraEmail"`
	Credentials store.XrayCredentialStatus `json:"credentials"`
}

func xrayResponse(xs store.XraySettings, status store.XrayCredentialStatus) xraySettingsResponse {
	return xraySettingsResponse{
		ProjectKey: xs.ProjectKey, TestPlanKey: xs.TestPlanKey, TestSetKey: xs.TestSetKey, CloudURL: xs.CloudURL, AuthURL: xs.AuthURL,
		Labels: xs.Labels, Component: xs.Component, JiraBaseURL: xs.JiraBaseURL, JiraEmail: xs.JiraEmail, Credentials: status,
	}
}

func (s *Server) handleTestsState(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.EnsureDefaultTestCases(); err != nil {
		httpError(w, 500, err)
		return
	}
	cols, err := s.DB.Collections()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	out := testsState{Collections: []testCollectionState{}, LastRuns: map[int64]store.TestRun{}}
	for _, c := range cols {
		cs := testCollectionState{Collection: c, Folders: []store.Folder{}, Requests: []requestMeta{}}
		folders, err := s.DB.Folders(c.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		cs.Folders = folders
		reqs, err := s.DB.Requests(c.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		for _, q := range reqs {
			cs.Requests = append(cs.Requests, requestMeta{
				ID: q.ID, FolderID: q.FolderID, Name: q.Spec.Name, Method: q.Spec.Method, URL: q.Spec.URL,
			})
		}
		out.Collections = append(out.Collections, cs)
	}
	if out.TestFolders, err = s.DB.TestFolders(); err != nil {
		httpError(w, 500, err)
		return
	}
	if out.TestSets, err = s.DB.TestSets(); err != nil {
		httpError(w, 500, err)
		return
	}
	if out.Executions, err = s.DB.TestExecutions(); err != nil {
		httpError(w, 500, err)
		return
	}
	if out.Tests, err = s.DB.TestCases(); err != nil {
		httpError(w, 500, err)
		return
	}
	if out.LastRuns, err = s.DB.LastTestRuns(); err != nil {
		httpError(w, 500, err)
		return
	}
	xs, _ := s.DB.XraySettings()
	st, _ := s.xrayCredentialStatus()
	out.Xray = xrayResponse(xs, st)
	writeJSON(w, out)
}

func (s *Server) handleTestCreate(w http.ResponseWriter, r *http.Request) {
	tc, err := decode[store.TestCase](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if !tc.Enabled {
		tc.Enabled = true
	}
	if err := s.DB.CreateTestCase(tc); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, tc)
}

func (s *Server) handleTestGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tc, err := s.DB.TestCase(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	writeJSON(w, tc)
}

func (s *Server) handleTestUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tc, err := decode[store.TestCase](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tc.ID = id
	if err := s.DB.UpdateTestCase(tc); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, tc)
}

func (s *Server) handleTestDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteTestCase(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTestRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var in struct {
		Env string `json:"env"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	res, err := s.runTestCase(r.Context(), id, in.Env, true)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) handleTestsRun(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
	}
	var in struct {
		Env          string   `json:"env"`
		TestIDs      []int64  `json:"testIds"`
		FolderIDs    []int64  `json:"folderIds"`
		TestSetIDs   []int64  `json:"testSetIds"`
		Tags         []string `json:"tags"`
		Priorities   []string `json:"priorities"`
		RequestID    int64    `json:"requestId"`
		CollectionID int64    `json:"collectionId"`
		FolderID     *int64   `json:"folderId"`
		TestSetID    int64    `json:"testSetId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	testSetIDs := append([]int64(nil), in.TestSetIDs...)
	if in.TestSetID != 0 {
		testSetIDs = append(testSetIDs, in.TestSetID)
	}
	tests, err := s.selectedTests(testSelection{
		TestIDs: in.TestIDs, FolderIDs: in.FolderIDs, TestSetIDs: testSetIDs, Tags: in.Tags, Priorities: in.Priorities,
		RequestID: in.RequestID, CollectionID: in.CollectionID, RequestFolderID: in.FolderID,
	})
	if err != nil {
		httpError(w, 422, err)
		return
	}
	results := make([]testCaseRunResult, 0, len(tests))
	passed := 0
	start := time.Now()
	sessionVars := map[string]string{}
	for _, tc := range tests {
		if !tc.Enabled {
			continue
		}
		res, err := s.runTestCaseValueWithSession(r.Context(), tc, in.Env, true, sessionVars)
		if err != nil {
			res = &testCaseRunResult{TestID: tc.ID, RequestID: tc.RequestID, Name: tc.Name, Passed: false, Error: err.Error()}
		}
		if res.Passed {
			passed++
		}
		results = append(results, *res)
	}
	writeJSON(w, map[string]any{
		"results":    results,
		"executed":   len(results),
		"passed":     passed,
		"failed":     len(results) - passed,
		"durationMs": float64(time.Since(start).Microseconds()) / 1000,
		"finishedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

type testCaseRunResult struct {
	TestID     int64                  `json:"testId"`
	RequestID  int64                  `json:"requestId"`
	Name       string                 `json:"name"`
	XrayKey    string                 `json:"xrayKey,omitempty"`
	Status     int                    `json:"status"`
	DurationMs float64                `json:"durationMs"`
	Passed     bool                   `json:"passed"`
	Error      string                 `json:"error,omitempty"`
	Steps      []store.TestStepResult `json:"steps,omitempty"`
	Send       *sendResult            `json:"send,omitempty"`
}

func (s *Server) runTestCase(ctx context.Context, id int64, env string, record bool) (*testCaseRunResult, error) {
	tc, err := s.DB.TestCase(id)
	if err != nil {
		return nil, err
	}
	return s.runTestCaseValue(ctx, *tc, env, record)
}

func (s *Server) runTestCaseValue(ctx context.Context, tc store.TestCase, env string, record bool) (*testCaseRunResult, error) {
	return s.runTestCaseValueWithSession(ctx, tc, env, record, nil)
}

func (s *Server) runTestCaseValueWithSession(ctx context.Context, tc store.TestCase, env string, record bool, sessionVars map[string]string) (*testCaseRunResult, error) {
	req, err := s.DB.Request(tc.RequestID)
	if err != nil {
		return nil, fmt.Errorf("request %d: %w", tc.RequestID, err)
	}
	testReq := cloneRequestForTest(req, tc)
	out, _, execErr := s.executeWithSession(ctx, testReq, env, false, sessionVars)
	res := &testCaseRunResult{TestID: tc.ID, RequestID: tc.RequestID, Name: tc.Name, XrayKey: tc.XrayKey, Passed: true}
	if execErr != nil {
		res.Passed = false
		res.Error = execErr.Error()
	} else {
		res.Status = out.Status
		res.DurationMs = out.Timing["total"]
		res.Send = out
		res.Steps = stepsFromSend(tc, out)
		for _, st := range res.Steps {
			if !st.Passed {
				res.Passed = false
				break
			}
		}
	}
	if record {
		status := "PASSED"
		if !res.Passed {
			status = "FAILED"
		}
		testID, requestID := tc.ID, tc.RequestID
		_ = s.DB.AddTestRun(&store.TestRun{
			TestID: &testID, RequestID: &requestID, Name: tc.Name, Status: status,
			DurationMs: res.DurationMs, Steps: res.Steps, Error: res.Error, ExecutedAt: time.Now(),
		})
	}
	return res, execErr
}

func cloneRequestForTest(req *store.Request, tc store.TestCase) *store.Request {
	spec := *req.Spec
	if tc.Request != nil {
		if tc.Request.Name != "" {
			spec.Name = tc.Request.Name
		}
		if tc.Request.Method != "" {
			spec.Method = strings.ToUpper(tc.Request.Method)
		}
		if tc.Request.URL != "" {
			spec.URL = tc.Request.URL
		}
		if tc.Request.Query != nil {
			spec.Query = copyMap(tc.Request.Query)
		}
		if tc.Request.Headers != nil {
			spec.Headers = copyMap(tc.Request.Headers)
		}
		if tc.Request.Auth != nil {
			auth := *tc.Request.Auth
			spec.Auth = &auth
		}
		if tc.Request.Body != nil {
			body := *tc.Request.Body
			body.FormData = append([]dsl.FormField(nil), tc.Request.Body.FormData...)
			spec.Body = &body
		}
	}
	spec.Name = tc.Name
	spec.Tags = append([]string(nil), tc.Tags...)
	spec.Owner = tc.Owner
	spec.Priority = tc.Priority
	spec.XrayKey = tc.XrayKey
	spec.Requirements = append([]string(nil), tc.Requirements...)
	spec.Assertions = append([]dsl.Assertion(nil), tc.Assertions...)
	if spec.Scripts == nil {
		spec.Scripts = &dsl.Scripts{}
	} else {
		scripts := *spec.Scripts
		spec.Scripts = &scripts
	}
	spec.Scripts.Tests = tc.ScriptTests
	cp := *req
	cp.Spec = &spec
	return &cp
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stepsFromSend(tc store.TestCase, out *sendResult) []store.TestStepResult {
	steps := make([]store.TestStepResult, 0, len(out.Assertions)+len(out.ScriptTests))
	for i, a := range out.Assertions {
		expected := ""
		if i < len(tc.Assertions) {
			expected = assertionExpected(tc.Assertions[i])
		}
		steps = append(steps, store.TestStepResult{
			Name: fmt.Sprintf("Assertion %d: %s", i+1, a.Type), Type: a.Type, Expected: expected,
			Passed: a.Passed, Message: a.Message,
		})
	}
	for i, t := range out.ScriptTests {
		steps = append(steps, store.TestStepResult{
			Name: fmt.Sprintf("Script test %d: %s", i+1, t.Name), Type: "script", Passed: t.Passed, Message: firstNonEmpty(t.Error, t.Name),
		})
	}
	return steps
}

func assertionExpected(a dsl.Assertion) string {
	switch a.Type {
	case "status":
		return fmt.Sprintf("status equals %v", a.Equals)
	case "jsonpath":
		return fmt.Sprintf("%s equals %v", a.Path, a.Equals)
	case "header":
		if a.Contains != "" {
			return fmt.Sprintf("%s contains %s", a.Name, a.Contains)
		}
		return fmt.Sprintf("%s equals %v", a.Name, a.Equals)
	case "contains":
		return "body contains " + a.Contains
	case "max_ms":
		return fmt.Sprintf("duration <= %dms", a.MaxMs)
	default:
		return a.Type
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

type testSelection struct {
	TestIDs         []int64
	FolderIDs       []int64
	TestSetIDs      []int64
	Tags            []string
	Priorities      []string
	RequestID       int64
	CollectionID    int64
	RequestFolderID *int64
}

func (s *Server) selectedTests(sel testSelection) ([]store.TestCase, error) {
	all, err := s.DB.TestCases()
	if err != nil {
		return nil, err
	}
	setIDs := map[int64]bool{}
	for _, setID := range sel.TestSetIDs {
		ts, err := s.DB.TestSet(setID)
		if err != nil {
			return nil, err
		}
		for _, id := range ts.TestIDs {
			setIDs[id] = true
		}
	}
	explicit := map[int64]bool{}
	for _, id := range sel.TestIDs {
		explicit[id] = true
	}
	folderIDs := map[int64]bool{}
	for _, id := range sel.FolderIDs {
		folderIDs[id] = true
	}
	tags := lowerSet(sel.Tags)
	priorities := lowerSet(sel.Priorities)
	var out []store.TestCase
	for _, tc := range all {
		if len(explicit) > 0 && !explicit[tc.ID] {
			continue
		}
		if len(setIDs) > 0 && !setIDs[tc.ID] {
			continue
		}
		if len(folderIDs) > 0 {
			if tc.FolderID == nil || !folderIDs[*tc.FolderID] {
				continue
			}
		}
		if len(tags) > 0 && !hasAllLower(tc.Tags, tags) {
			continue
		}
		if len(priorities) > 0 && !priorities[strings.ToLower(strings.TrimSpace(tc.Priority))] {
			continue
		}
		if sel.RequestID != 0 && tc.RequestID != sel.RequestID {
			continue
		}
		if sel.CollectionID != 0 || sel.RequestFolderID != nil {
			req, err := s.DB.Request(tc.RequestID)
			if err != nil {
				return nil, err
			}
			if sel.CollectionID != 0 && req.CollectionID != sel.CollectionID {
				continue
			}
			if sel.RequestFolderID != nil {
				if req.FolderID == nil || *req.FolderID != *sel.RequestFolderID {
					continue
				}
			}
		}
		out = append(out, tc)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tests selected")
	}
	return out, nil
}

func lowerSet(vals []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range vals {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func hasAllLower(vals []string, want map[string]bool) bool {
	have := lowerSet(vals)
	for w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

func (s *Server) handleTestFolderCreate(w http.ResponseWriter, r *http.Request) {
	f, err := decode[store.TestFolder](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.CreateTestFolder(f); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) handleTestFolderUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	f, err := decode[store.TestFolder](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	f.ID = id
	if err := s.DB.UpdateTestFolder(f); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) handleTestFolderDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteTestFolder(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTestSetCreate(w http.ResponseWriter, r *http.Request) {
	ts, err := decode[store.TestSet](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.CreateTestSet(ts); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, ts)
}

func (s *Server) handleTestSetUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ts, err := decode[store.TestSet](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ts.ID = id
	if err := s.DB.UpdateTestSet(ts); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, ts)
}

func (s *Server) handleTestSetDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteTestSet(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTestExecutionCreate(w http.ResponseWriter, r *http.Request) {
	ex, err := decode[store.TestExecution](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.CreateTestExecution(ex); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, ex)
}

func (s *Server) handleTestExecutionGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ex, err := s.DB.TestExecution(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	writeJSON(w, ex)
}

func (s *Server) handleTestExecutionUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ex, err := decode[store.TestExecution](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ex.ID = id
	if err := s.DB.UpdateTestExecution(ex); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, ex)
}

func (s *Server) handleTestExecutionDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteTestExecution(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTestExecutionRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ex, err := s.DB.TestExecution(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	var in struct {
		Env string `json:"env"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Env != "" {
		ex.Env = in.Env
	}
	results, summary, err := s.runExecutionTests(r.Context(), *ex, true)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	ex.Status = "COMPLETED"
	if summaryFailed(summary) > 0 {
		ex.Status = "FAILED"
	}
	ex.LastSummary = summary
	_ = s.DB.UpdateTestExecution(ex)
	writeJSON(w, map[string]any{"execution": ex, "results": results, "summary": summary})
}

func (s *Server) handleTestExecutionXrayPush(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ex, err := s.DB.TestExecution(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	cfg, xs, err := s.xrayConfig()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	results, summary, err := s.runExecutionTests(r.Context(), *ex, true)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	tmResults := make([]tm.TestResult, 0, len(results))
	for _, res := range results {
		status := tm.StatusPASS
		if !res.Passed {
			status = tm.StatusFAIL
		}
		tmResults = append(tmResults, tm.TestResult{
			TestKey: res.XrayKey, Name: res.Name, Status: status, Comment: res.Error,
			DurationMs: res.DurationMs, Steps: tmStepsFromStore(res.Steps),
		})
	}
	projectKey := xs.ProjectKey
	if projectKey == "" {
		httpError(w, 422, fmt.Errorf("set Xray project key first"))
		return
	}
	plan := firstNonEmpty(ex.TestPlanKey, xs.TestPlanKey)
	name := ex.Name
	if name == "" {
		name = "Relay Test Management execution"
	}
	finished := time.Now()
	durationMs := summaryDurationMs(summary)
	key, err := xray.New(cfg).PushExecution(tm.Execution{
		ProjectKey: projectKey, TestPlanKey: plan, Summary: name,
		StartedAt:  finished.Add(-time.Duration(durationMs) * time.Millisecond),
		FinishedAt: finished, Results: tmResults,
	})
	if err != nil {
		httpError(w, 502, fmt.Errorf("xray push failed: %w", err))
		return
	}
	ex.XrayKey = key
	ex.Status = "PUSHED"
	ex.LastSummary = summary
	_ = s.DB.UpdateTestExecution(ex)
	writeJSON(w, map[string]any{"execution": ex, "executionKey": key, "summary": summary})
}

func (s *Server) runExecutionTests(ctx context.Context, ex store.TestExecution, record bool) ([]testCaseRunResult, map[string]any, error) {
	tests, err := s.selectedTests(testSelection{
		TestIDs: ex.TestIDs, FolderIDs: ex.FolderIDs, TestSetIDs: ex.TestSetIDs, Tags: ex.Tags, Priorities: ex.Priorities,
	})
	if err != nil {
		return nil, nil, err
	}
	results := make([]testCaseRunResult, 0, len(tests))
	passed := 0
	start := time.Now()
	sessionVars := map[string]string{}
	for _, tc := range tests {
		if !tc.Enabled {
			continue
		}
		res, err := s.runTestCaseValueWithSession(ctx, tc, ex.Env, record, sessionVars)
		if err != nil {
			res = &testCaseRunResult{TestID: tc.ID, RequestID: tc.RequestID, Name: tc.Name, XrayKey: tc.XrayKey, Passed: false, Error: err.Error()}
		}
		if res.Passed {
			passed++
		}
		results = append(results, *res)
	}
	if len(results) == 0 {
		return nil, nil, fmt.Errorf("no enabled tests selected")
	}
	dur := float64(time.Since(start).Microseconds()) / 1000
	summary := map[string]any{
		"executed":   len(results),
		"passed":     passed,
		"failed":     len(results) - passed,
		"durationMs": dur,
		"finishedAt": time.Now().UTC().Format(time.RFC3339),
	}
	return results, summary, nil
}

func summaryFailed(summary map[string]any) int {
	switch v := summary["failed"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func summaryDurationMs(summary map[string]any) float64 {
	switch v := summary["durationMs"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func (s *Server) handleXraySettingsGet(w http.ResponseWriter, r *http.Request) {
	set, err := s.DB.XraySettings()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	status, _ := s.xrayCredentialStatus()
	writeJSON(w, xrayResponse(set, status))
}

func (s *Server) handleXrayCredentialsPut(w http.ResponseWriter, r *http.Request) {
	var in store.XrayCredentials
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	existing, _ := s.DB.XrayCredentials()
	if strings.TrimSpace(in.ClientID) == "" {
		in.ClientID = existing.ClientID
	}
	if strings.TrimSpace(in.ClientSecret) == "" {
		in.ClientSecret = existing.ClientSecret
	}
	if strings.TrimSpace(in.JiraEmail) == "" {
		in.JiraEmail = existing.JiraEmail
	}
	if strings.TrimSpace(in.JiraAPIKey) == "" {
		in.JiraAPIKey = existing.JiraAPIKey
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.ClientSecret = strings.TrimSpace(in.ClientSecret)
	in.JiraEmail = strings.TrimSpace(in.JiraEmail)
	in.JiraAPIKey = strings.TrimSpace(in.JiraAPIKey)
	if in.ClientID == "" && in.ClientSecret == "" && in.JiraEmail == "" && in.JiraAPIKey == "" {
		httpError(w, 422, fmt.Errorf("enter at least one Xray or Jira credential value"))
		return
	}
	if err := s.DB.SaveXrayCredentials(in); err != nil {
		httpError(w, 500, err)
		return
	}
	status, _ := s.xrayCredentialStatus()
	writeJSON(w, status)
}

func (s *Server) handleXrayTestConnection(w http.ResponseWriter, r *http.Request) {
	client, err := s.xrayClient()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	if err := client.TestConnection(); err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Connected to Xray"})
}

func (s *Server) handleXrayTestValidate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tc, err := s.DB.TestCase(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	if tc.XrayKey == "" {
		httpError(w, 422, fmt.Errorf("test has no Xray key"))
		return
	}
	client, err := s.xrayClient()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	issue, err := client.GetTest(tc.XrayKey)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, issue)
}

func (s *Server) handleXrayTestCreate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tc, err := s.DB.TestCase(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	xs, _ := s.DB.XraySettings()
	if xs.ProjectKey == "" {
		httpError(w, 422, fmt.Errorf("set Xray project key first"))
		return
	}
	client, err := s.xrayClient()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	var warnings []string
	if tc.XrayKey != "" {
		ref, err := client.GetTest(tc.XrayKey)
		if err != nil {
			httpError(w, 502, err)
			return
		}
		if ref == nil {
			httpError(w, 404, fmt.Errorf("%s not found in Xray", tc.XrayKey))
			return
		}
		if err := client.UpdateTest(*ref, tm.NewTest{
			ProjectKey: xs.ProjectKey,
			Summary:    tc.Name,
			TestType:   "Generic",
			Steps:      testStepsText(tc.Assertions),
		}); err != nil {
			httpError(w, 502, err)
			return
		}
		if len(tc.Requirements) > 0 {
			if err := client.LinkRequirements(tc.XrayKey, tc.Requirements); err != nil {
				warnings = append(warnings, "requirements not linked: "+err.Error())
			}
		}
		if tc.TestPlanKey != "" {
			if err := client.AddTestsToTestPlan(tc.TestPlanKey, []string{tc.XrayKey}); err != nil {
				warnings = append(warnings, "test plan not updated: "+err.Error())
			}
		}
		writeJSON(w, map[string]any{"test": tc, "issue": xray.Issue{Key: tc.XrayKey, Summary: tc.Name}, "updated": true, "warnings": warnings})
		return
	}
	key, err := client.CreateTest(tm.NewTest{
		ProjectKey: xs.ProjectKey,
		Summary:    tc.Name,
		TestType:   "Generic",
		Steps:      testStepsText(tc.Assertions),
	})
	if err != nil {
		httpError(w, 502, err)
		return
	}
	tc.XrayKey = key
	if xs.TestPlanKey != "" && tc.TestPlanKey == "" {
		tc.TestPlanKey = xs.TestPlanKey
	}
	if err := s.DB.UpdateTestCase(tc); err != nil {
		httpError(w, 500, err)
		return
	}
	if len(tc.Requirements) > 0 {
		if err := client.LinkRequirements(tc.XrayKey, tc.Requirements); err != nil {
			warnings = append(warnings, "requirements not linked: "+err.Error())
		}
	}
	if tc.TestPlanKey != "" {
		if err := client.AddTestsToTestPlan(tc.TestPlanKey, []string{tc.XrayKey}); err != nil {
			warnings = append(warnings, "test plan not updated: "+err.Error())
		}
	}
	writeJSON(w, map[string]any{"test": tc, "issue": xray.Issue{Key: key, Summary: tc.Name}, "warnings": warnings})
}

func (s *Server) handleXrayLinkRequirements(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tc, err := s.DB.TestCase(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	if tc.XrayKey == "" {
		httpError(w, 422, fmt.Errorf("link or create an Xray test before linking requirements"))
		return
	}
	if len(tc.Requirements) == 0 {
		httpError(w, 422, fmt.Errorf("add at least one requirement key before linking"))
		return
	}
	cfg, err := s.jiraConfig()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	if err := xray.New(cfg).LinkRequirements(tc.XrayKey, tc.Requirements); err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleXrayTestSetCreate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	ts, err := s.DB.TestSet(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	xs, _ := s.DB.XraySettings()
	if xs.ProjectKey == "" {
		httpError(w, 422, fmt.Errorf("set Xray project key first"))
		return
	}
	tests, err := s.selectedTests(testSelection{TestIDs: ts.TestIDs})
	if err != nil {
		httpError(w, 422, err)
		return
	}
	var keys []string
	for _, tc := range tests {
		if tc.XrayKey == "" {
			httpError(w, 422, fmt.Errorf("test %q has no Xray key", tc.Name))
			return
		}
		keys = append(keys, tc.XrayKey)
	}
	client, err := s.xrayClient()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	var issue *xray.Issue
	if ts.XrayKey != "" {
		issue, err = client.AddTestsToTestSet(ts.XrayKey, keys)
		if issue != nil {
			issue.Summary = ts.Name
		}
	} else {
		issue, err = client.CreateTestSet(xs.ProjectKey, ts.Name, keys)
	}
	if err != nil {
		httpError(w, 502, err)
		return
	}
	ts.XrayKey = issue.Key
	if err := s.DB.UpdateTestSet(ts); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"testSet": ts, "issue": issue})
}

func (s *Server) xrayCredentialStatus() (store.XrayCredentialStatus, error) {
	st, err := s.DB.XrayCredentialStatus()
	if err != nil {
		return st, err
	}
	if os.Getenv("RELAY_XRAY_CLIENT_ID") != "" || os.Getenv("RELAY_XRAY_CLIENT_SECRET") != "" {
		st.HasClientID = os.Getenv("RELAY_XRAY_CLIENT_ID") != ""
		st.HasClientSecret = os.Getenv("RELAY_XRAY_CLIENT_SECRET") != ""
		st.Source = "environment"
	}
	if os.Getenv("RELAY_JIRA_EMAIL") != "" || os.Getenv("RELAY_JIRA_API_TOKEN") != "" || os.Getenv("JIRA_API_KEY") != "" {
		st.HasJiraEmail = os.Getenv("RELAY_JIRA_EMAIL") != ""
		st.HasJiraAPIKey = os.Getenv("RELAY_JIRA_API_TOKEN") != "" || os.Getenv("JIRA_API_KEY") != ""
		if st.Source == "none" {
			st.Source = "environment"
		}
	}
	return st, nil
}

func (s *Server) xrayClient() (*xray.Client, error) {
	cfg, _, err := s.xrayConfig()
	if err != nil {
		return nil, err
	}
	return xray.New(cfg), nil
}

func (s *Server) jiraConfig() (xray.Config, error) {
	xs, err := s.DB.XraySettings()
	if err != nil {
		return xray.Config{}, err
	}
	creds, _ := s.DB.XrayCredentials()
	cfg := xray.ConfigFromEnv()
	if xs.JiraBaseURL != "" {
		cfg.JiraBaseURL = xs.JiraBaseURL
	}
	cfg.JiraEmail = firstNonEmpty(cfg.JiraEmail, creds.JiraEmail, xs.JiraEmail)
	cfg.JiraAPIToken = firstNonEmpty(cfg.JiraAPIToken, creds.JiraAPIKey)
	if cfg.JiraBaseURL == "" {
		return xray.Config{}, fmt.Errorf("Jira base URL is missing")
	}
	if cfg.JiraEmail == "" || cfg.JiraAPIToken == "" {
		return xray.Config{}, fmt.Errorf("Jira email and API token are missing")
	}
	return cfg, nil
}

func tmStepsFromAssertions(assertions []dsl.Assertion) []tm.TestStep {
	steps := make([]tm.TestStep, 0, len(assertions))
	for i, a := range assertions {
		steps = append(steps, tm.TestStep{
			Name:     fmt.Sprintf("Assertion %d: %s", i+1, a.Type),
			Type:     a.Type,
			Expected: assertionExpected(a),
			Status:   tm.StatusPASS,
		})
	}
	return steps
}

func testStepsText(assertions []dsl.Assertion) string {
	if len(assertions) == 0 {
		return "Execute the Relay request and verify the response."
	}
	parts := make([]string, 0, len(assertions))
	for i, a := range assertions {
		parts = append(parts, fmt.Sprintf("%d. %s", i+1, assertionExpected(a)))
	}
	return strings.Join(parts, "\n")
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
