package ui

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/pack"
	"github.com/muhaymien96/relay/internal/store"
)

// newServer seeds a store with one collection (folder + preset + secret)
// pointed at a live echo API.
func newServer(t *testing.T) (*Server, int64) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"auth":    r.Header.Get("Authorization"),
			"channel": r.Header.Get("X-Channel"),
			"team":    r.Header.Get("X-Team"),
		})
	}))
	t.Cleanup(api.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	col := &store.Collection{Name: "AML", Headers: map[string]string{"X-Team": "qe"},
		Vars: map[string]string{"baseUrl": api.URL}}
	if err := db.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	req := &store.Request{CollectionID: col.ID, Spec: &dsl.Request{
		Name: "Echo", Method: "GET", URL: "{{baseUrl}}/echo",
		Auth: &dsl.Auth{Type: "bearer", Token: "{{apiToken}}"},
		Assertions: []dsl.Assertion{
			{Type: "status", Equals: float64(200)}, // JSON round-trip makes numbers float64
			{Type: "jsonpath", Path: "$.team", Equals: "qe"},
		},
	}}
	if err := db.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEnvironment(&store.Environment{Name: "local",
		Vars: map[string]string{}, Secrets: []string{"apiToken"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreatePreset(&store.Preset{Name: "gw",
		Headers:     []store.PresetHeader{{Key: "X-Channel", Value: "MOBILE_IOS"}, {Key: "X-Gw-Key", Value: "topsecret", Secret: true}},
		Attachments: []store.Attachment{{CollectionID: &col.ID}}}); err != nil {
		t.Fatal(err)
	}

	return &Server{
		DB: db, Engine: engine.NewOptions(),
		Getenv: func(k string) string {
			if k == "RELAY_SECRET_APITOKEN" {
				return "hunter2"
			}
			return ""
		},
	}, req.ID
}

func call(t *testing.T, s *Server, method, url, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, url, rd)
	prepareLocalRequest(req)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	return rec, doc
}

func TestState(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "GET", "/api/state", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	cols := doc["collections"].([]any)
	if len(cols) != 1 {
		t.Fatalf("collections = %d", len(cols))
	}
	col := cols[0].(map[string]any)
	if col["name"] != "AML" || len(col["requests"].([]any)) != 1 {
		t.Errorf("col = %v", col)
	}
	// Secret preset values must not appear in state.
	if strings.Contains(rec.Body.String(), "topsecret") {
		t.Error("secret preset value leaked in /api/state")
	}
	presets := doc["presets"].([]any)
	if len(presets) != 1 {
		t.Errorf("presets = %v", presets)
	}
}

func TestRequestCRUDOverHTTP(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "POST", "/api/requests",
		`{"collectionId":1,"spec":{"name":"New","method":"POST","url":"{{baseUrl}}/x"}}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %v", rec.Code, doc)
	}
	id := int64(doc["id"].(float64))

	rec, doc = call(t, s, "PUT", "/api/requests/"+itoa(id),
		`{"spec":{"name":"Renamed","method":"PUT","url":"{{baseUrl}}/y"}}`)
	if rec.Code != 200 {
		t.Fatalf("update: %d %v", rec.Code, doc)
	}
	rec, doc = call(t, s, "GET", "/api/requests/"+itoa(id), "")
	spec := doc["spec"].(map[string]any)
	if spec["name"] != "Renamed" || spec["method"] != "PUT" {
		t.Errorf("spec = %v", spec)
	}
	rec, _ = call(t, s, "DELETE", "/api/requests/"+itoa(id), "")
	if rec.Code != 200 {
		t.Errorf("delete: %d", rec.Code)
	}
	rec, _ = call(t, s, "GET", "/api/requests/"+itoa(id), "")
	if rec.Code != 404 {
		t.Errorf("get deleted: %d", rec.Code)
	}
}

func TestSendWithPresetsAndSecrets(t *testing.T) {
	s, reqID := newServer(t)
	rec, doc := call(t, s, "POST", "/api/send", `{"requestId":`+itoa(reqID)+`,"env":"local"}`)
	if rec.Code != 200 {
		t.Fatalf("send: %d %v", rec.Code, doc)
	}
	body := doc["body"].(string)
	// Upstream got the real bearer token and the preset header.
	if !strings.Contains(body, "Bearer hunter2") || !strings.Contains(body, "MOBILE_IOS") {
		t.Errorf("upstream body = %s", body)
	}
	// Display headers are masked: env secret and secret preset value.
	rh := doc["requestHeaders"].(map[string]any)
	if strings.Contains(rh["Authorization"].(string), "hunter2") {
		t.Errorf("env secret leaked: %v", rh["Authorization"])
	}
	if got := rh["X-Gw-Key"].(string); strings.Contains(got, "topsecret") {
		t.Errorf("preset secret leaked: %v", got)
	}
	// Collection header inherited.
	if rh["X-Team"].(string) != "qe" {
		t.Errorf("inherited header missing: %v", rh)
	}
	// Assertions evaluated.
	asserts := doc["assertions"].([]any)
	if len(asserts) != 2 {
		t.Fatalf("assertions = %v", asserts)
	}
	for _, a := range asserts {
		if !a.(map[string]any)["passed"].(bool) {
			t.Errorf("assertion failed: %v", a)
		}
	}

	// History recorded; stored body retrievable; list carries no body.
	_, list := call(t, s, "GET", "/api/history", "")
	_ = list
	rec, _ = call(t, s, "GET", "/api/history", "")
	var entries []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &entries)
	if len(entries) != 1 {
		t.Fatalf("history = %v", entries)
	}
	id := int64(entries[0]["id"].(float64))
	rec, doc = call(t, s, "GET", "/api/history/"+itoa(id), "")
	if rec.Code != 200 || !strings.Contains(doc["body"].(string), "MOBILE_IOS") {
		t.Errorf("history entry: %d %v", rec.Code, doc)
	}

	// Stats reflect the send.
	rec, doc = call(t, s, "GET", "/api/requests/"+itoa(reqID)+"/stats", "")
	if rec.Code != 200 || doc["count"].(float64) != 1 || doc["successRate"].(float64) != 1 {
		t.Errorf("stats: %d %v", rec.Code, doc)
	}
}

func TestRunCollection(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "POST", "/api/run", `{"collectionId":1,"env":"local"}`)
	if rec.Code != 200 {
		t.Fatalf("run: %d %v", rec.Code, doc)
	}
	if doc["executed"].(float64) != 1 || doc["passed"].(float64) != 1 || doc["failed"].(float64) != 0 {
		t.Errorf("summary = %v", doc)
	}
}

func TestImportPostmanAndExportK6(t *testing.T) {
	s, _ := newServer(t)
	postman := `{"info":{"name":"Imported"},"item":[{"name":"Ping","request":{"method":"GET","url":"https://x.test/ping"}}]}`
	rec, doc := call(t, s, "POST", "/api/import/postman", postman)
	if rec.Code != 200 || doc["requests"].(float64) != 1 {
		t.Fatalf("import: %d %v", rec.Code, doc)
	}
	colID := int64(doc["collectionId"].(float64))

	req := httptest.NewRequest("GET", "/api/export?format=k6&collection="+itoa(colID), nil)
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("export: %d %s", out.Code, out.Body.String())
	}
	script := out.Body.String()
	if !strings.Contains(script, "k6/http") || !strings.Contains(script, "https://x.test/ping") {
		t.Errorf("k6 script missing content:\n%s", script)
	}
}

func TestEnvironmentAndPresetEndpoints(t *testing.T) {
	s, _ := newServer(t)
	rec, _ := call(t, s, "PUT", "/api/environments/sit", `{"vars":{"baseUrl":"https://sit"},"secrets":["k"]}`)
	if rec.Code != 200 {
		t.Fatalf("env put: %d", rec.Code)
	}
	rec, _ = call(t, s, "GET", "/api/environments", "")
	if !strings.Contains(rec.Body.String(), `"sit"`) {
		t.Error("env list missing sit")
	}
	rec, _ = call(t, s, "DELETE", "/api/environments/sit", "")
	if rec.Code != 200 {
		t.Errorf("env delete: %d", rec.Code)
	}

	rec, doc := call(t, s, "POST", "/api/presets", `{"name":"json-defaults","headers":[{"key":"Accept","value":"application/json"}]}`)
	if rec.Code != 200 {
		t.Fatalf("preset create: %d %v", rec.Code, doc)
	}
	id := int64(doc["id"].(float64))
	// Updating a secret header with empty value keeps the stored value.
	rec, _ = call(t, s, "PUT", "/api/presets/1",
		`{"name":"gw","headers":[{"key":"X-Gw-Key","value":"","secret":true}],"attachments":[{"collectionId":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("preset update: %d", rec.Code)
	}
	ps, _ := s.DB.Presets()
	for _, p := range ps {
		if p.Name == "gw" && p.Headers[0].Value != "topsecret" {
			t.Errorf("secret wiped on masked round-trip: %+v", p.Headers)
		}
	}
	rec, _ = call(t, s, "DELETE", "/api/presets/"+itoa(id), "")
	if rec.Code != 200 {
		t.Errorf("preset delete: %d", rec.Code)
	}
}

func TestIndexServed(t *testing.T) {
	s, _ := newServer(t)
	rec := httptest.NewRecorder()
	rootReq := prepareLocalRequest(httptest.NewRequest("GET", "/", nil))
	s.Handler().ServeHTTP(rec, rootReq)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "API Workbench") {
		t.Errorf("index: %d", rec.Code)
	}
	for _, rule := range []string{
		"background: Highlight; color: HighlightText; -webkit-text-fill-color: HighlightText;",
		".varMirror .var-token {",
		"padding: 0; border: 0; border-radius: 3px; font-weight: inherit;",
	} {
		if !strings.Contains(body, rule) {
			t.Errorf("index is missing variable editor rule %q", rule)
		}
	}
}

func itoa(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestRequestCurl(t *testing.T) {
	s, reqID := newServer(t)
	rec, doc := call(t, s, "GET", "/api/requests/"+itoa(reqID)+"/curl?env=local", "")
	if rec.Code != 200 {
		t.Fatalf("curl: %d %v", rec.Code, doc)
	}
	cmd := doc["curl"].(string)
	if !strings.HasPrefix(cmd, "curl") || !strings.Contains(cmd, "/echo") {
		t.Errorf("curl = %s", cmd)
	}
	// Env secret becomes a shell reference; preset secret value is masked.
	if strings.Contains(cmd, "hunter2") || !strings.Contains(cmd, "$RELAY_SECRET_APITOKEN") {
		t.Errorf("env secret handling wrong: %s", cmd)
	}
	if strings.Contains(cmd, "topsecret") {
		t.Errorf("preset secret leaked: %s", cmd)
	}
	// Inherited preset/collection headers present.
	if !strings.Contains(cmd, "MOBILE_IOS") || !strings.Contains(cmd, "X-Team: qe") {
		t.Errorf("inherited headers missing: %s", cmd)
	}
}

func TestImportCurlEndpoint(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "POST", "/api/import/curl",
		`{"collectionId":1,"curl":"curl -X POST -H 'Content-Type: application/json' --data-raw '{\"a\":1}' https://x.test/v1/do"}`)
	if rec.Code != 200 {
		t.Fatalf("import curl: %d %v", rec.Code, doc)
	}
	spec := doc["spec"].(map[string]any)
	if spec["method"] != "POST" || spec["url"] != "https://x.test/v1/do" {
		t.Errorf("spec = %v", spec)
	}
	if spec["body"].(map[string]any)["type"] != "json" {
		t.Errorf("body = %v", spec["body"])
	}

	rec, doc = call(t, s, "POST", "/api/import/curl", `{"collectionId":1,"curl":"wget https://x.test"}`)
	if rec.Code != 422 {
		t.Errorf("non-curl should 422, got %d %v", rec.Code, doc)
	}
}

func TestExportPostmanEndpoint(t *testing.T) {
	s, _ := newServer(t)
	req := httptest.NewRequest("GET", "/api/export?format=postman&collection=1", nil)
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("export postman: %d %s", out.Code, out.Body.String())
	}
	var doc struct {
		Info struct{ Name, Schema string }
		Item []struct{ Name string }
	}
	if err := json.Unmarshal(out.Body.Bytes(), &doc); err != nil {
		t.Fatalf("invalid postman json: %v", err)
	}
	if doc.Info.Name != "AML" || len(doc.Item) != 1 || doc.Item[0].Name != "Echo" {
		t.Errorf("doc = %+v", doc)
	}
	// The secret preset value must not appear in the exported collection.
	if strings.Contains(out.Body.String(), "topsecret") {
		t.Error("preset secret leaked into postman export")
	}
	// Non-secret preset + collection headers are flattened in.
	if !strings.Contains(out.Body.String(), "MOBILE_IOS") || !strings.Contains(out.Body.String(), "X-Team") {
		t.Error("inherited headers missing from postman export")
	}
}

func TestExportCurlEndpoint(t *testing.T) {
	s, reqID := newServer(t)
	req := httptest.NewRequest("GET", "/api/export?format=curl&request="+itoa(reqID)+"&env=local", nil)
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("export curl: %d %s", out.Code, out.Body.String())
	}
	cmd := out.Body.String()
	if !strings.Contains(cmd, "curl --location --request") || !strings.Contains(cmd, "--url") {
		t.Fatalf("not postman-style curl: %s", cmd)
	}
	if strings.Contains(cmd, "hunter2") || !strings.Contains(cmd, "$RELAY_SECRET_APITOKEN") {
		t.Fatalf("secret handling wrong: %s", cmd)
	}
}

func TestExportRelayPackageCanSelectRequestsAndEnvironments(t *testing.T) {
	s, reqID := newServer(t)
	if err := s.DB.CreateRequest(&store.Request{CollectionID: 1, Spec: &dsl.Request{
		Name: "Other", Method: "POST", URL: "https://example.test/other",
		Body: &dsl.Body{Type: "json", Content: `{"skip":true}`},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.UpsertEnvironment(&store.Environment{
		Name: "sit", Vars: map[string]string{"baseUrl": "https://sit.example.test"},
	}); err != nil {
		t.Fatal(err)
	}

	url := "/api/export?format=relay&collection=1&requests=" + itoa(reqID) + "&environments=local"
	out := httptest.NewRecorder()
	exportReq := prepareLocalRequest(httptest.NewRequest("GET", url, nil))
	s.Handler().ServeHTTP(out, exportReq)
	if out.Code != 200 {
		t.Fatalf("export relay: %d %s", out.Code, out.Body.String())
	}
	if got := out.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type = %q", got)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Body.Bytes()), int64(out.Body.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(b)
	}
	var requestFiles int
	for name, body := range files {
		if strings.HasSuffix(name, ".req.toml") {
			requestFiles++
			if !strings.Contains(body, `name = "Echo"`) || strings.Contains(body, "Other") {
				t.Fatalf("unexpected selected request file %s: %s", name, body)
			}
		}
	}
	if requestFiles != 1 {
		t.Fatalf("request files = %d; files=%v", requestFiles, files)
	}
	if _, ok := files["environments/local.toml"]; !ok {
		t.Fatalf("selected environment missing: files=%v", files)
	}
	if _, ok := files["environments/sit.toml"]; ok {
		t.Fatal("unselected environment was exported")
	}
	joined := strings.Join(mapValues(files), "\n")
	if strings.Contains(joined, "topsecret") || strings.Contains(joined, "hunter2") {
		t.Fatal("secret value leaked into Relay package")
	}
}

func TestExportEnvironmentTOML(t *testing.T) {
	s, _ := newServer(t)
	out := httptest.NewRecorder()
	envReq := prepareLocalRequest(httptest.NewRequest("GET", "/api/export?format=environment&env=local", nil))
	s.Handler().ServeHTTP(out, envReq)
	if out.Code != 200 {
		t.Fatalf("export environment: %d %s", out.Code, out.Body.String())
	}
	body := out.Body.String()
	if !strings.Contains(body, `secrets = ["apiToken"]`) {
		t.Fatalf("environment TOML = %q", body)
	}
	if strings.Contains(body, "hunter2") {
		t.Fatal("environment secret value leaked")
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, value := range m {
		out = append(out, value)
	}
	return out
}

func TestXrayCredentialsAllowPartialUpdates(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "PUT", "/api/xray/credentials", `{"jiraEmail":"me@example.com","jiraApiKey":"tok"}`)
	if rec.Code != 200 {
		t.Fatalf("jira-only credentials: %d %v", rec.Code, doc)
	}
	if doc["hasJiraEmail"] != true || doc["hasJiraApiKey"] != true {
		t.Fatalf("jira status = %v", doc)
	}
	if doc["source"] != "stored" {
		t.Fatalf("jira source = %v", doc)
	}
	rec, doc = call(t, s, "PUT", "/api/xray/credentials", `{"clientId":"cid","clientSecret":"sec"}`)
	if rec.Code != 200 {
		t.Fatalf("xray credentials: %d %v", rec.Code, doc)
	}
	if doc["hasClientId"] != true || doc["hasClientSecret"] != true || doc["hasJiraEmail"] != true || doc["hasJiraApiKey"] != true {
		t.Fatalf("merged status = %v", doc)
	}
}

func TestXrayRequirementLinkUsesJiraOnlyConfig(t *testing.T) {
	s, reqID := newServer(t)
	if err := s.DB.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	tests, err := s.DB.TestCasesForRequest(reqID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) == 0 {
		t.Fatal("no test case for request")
	}
	tc := tests[0]
	tc.XrayKey = "AML-T1"
	tc.Requirements = []string{"AML-1", " AML-1 ", "AML-2"}
	if err := s.DB.UpdateTestCase(&tc); err != nil {
		t.Fatal(err)
	}
	var links int
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		links++
		if r.URL.Path != "/rest/api/3/issueLink" {
			t.Errorf("path = %s", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "me@example.com" || pass != "tok" {
			t.Errorf("auth = %s/%s", user, pass)
		}
		w.WriteHeader(201)
	}))
	defer jiraSrv.Close()
	if err := s.DB.SaveXraySettings(store.XraySettings{JiraBaseURL: jiraSrv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SaveXrayCredentials(store.XrayCredentials{JiraEmail: "me@example.com", JiraAPIKey: "tok"}); err != nil {
		t.Fatal(err)
	}
	rec, doc := call(t, s, "POST", "/api/xray/tests/"+itoa(tc.ID)+"/link-requirements", `{}`)
	if rec.Code != 200 {
		t.Fatalf("link requirements: %d %v", rec.Code, doc)
	}
	if links != 2 {
		t.Fatalf("expected 2 de-duplicated Jira links, got %d", links)
	}
}

func TestXrayTestCreateReturnsRequirementLinkWarning(t *testing.T) {
	s, reqID := newServer(t)
	if err := s.DB.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	tests, err := s.DB.TestCasesForRequest(reqID)
	if err != nil {
		t.Fatal(err)
	}
	tc := tests[0]
	tc.Requirements = []string{"AML-1"}
	if err := s.DB.UpdateTestCase(&tc); err != nil {
		t.Fatal(err)
	}
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"token"`))
	}))
	defer authSrv.Close()
	var createVariables map[string]any
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		createVariables, _ = body["variables"].(map[string]any)
		_, _ = w.Write([]byte(`{"data":{"createTest":{"test":{"jira":{"key":"AML-T1"}},"warnings":[]}}}`))
	}))
	defer gqlSrv.Close()
	if err := s.DB.SaveXraySettings(store.XraySettings{ProjectKey: "AML", AuthURL: authSrv.URL, CloudURL: gqlSrv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SaveXrayCredentials(store.XrayCredentials{ClientID: "id", ClientSecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	rec, doc := call(t, s, "POST", "/api/xray/tests/"+itoa(tc.ID)+"/create", `{}`)
	if rec.Code != 200 {
		t.Fatalf("create test: %d %v", rec.Code, doc)
	}
	warnings, _ := doc["warnings"].([]any)
	if len(warnings) == 0 || !strings.Contains(warnings[0].(string), "requirements not linked") {
		t.Fatalf("expected requirement warning, got %v", doc["warnings"])
	}
	testType, _ := createVariables["testType"].(map[string]any)
	if testType["name"] != "Generic" {
		t.Fatalf("Xray test type = %v, want Generic", testType)
	}
	if _, ok := createVariables["unstructured"].(string); !ok {
		t.Fatalf("Xray create payload is missing its Generic definition: %v", createVariables)
	}
}

func TestExportTestManagementPackZip(t *testing.T) {
	s, _ := newServer(t)
	if err := s.DB.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/tests/export", strings.NewReader(`{"scope":"collection","collectionId":1}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("export pack: %d %s", out.Code, out.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Body.Bytes()), int64(out.Body.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(b)
	}
	if !strings.Contains(files["relay.json"], `"defaultPlan": "all"`) {
		t.Fatalf("manifest missing default plan: %s", files["relay.json"])
	}
	if len(files["plans/all.plan.json"]) == 0 {
		t.Fatalf("plan missing from zip: %v", files)
	}
	if !strings.Contains(files["environments/local.toml"], `secrets = ["apiToken"]`) {
		t.Fatalf("environment missing from zip: %q", files["environments/local.toml"])
	}
	foundReq := false
	for name, body := range files {
		if strings.HasSuffix(name, ".req.toml") && strings.Contains(body, `Echo - default`) {
			foundReq = true
			break
		}
	}
	if !foundReq {
		t.Fatalf("materialized request with test metadata missing: %v", files)
	}
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := pack.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if errs := p.Validate(); len(errs) > 0 {
		t.Fatalf("exported pack invalid: %v", errs)
	}
	spec, err := p.Resolve(pack.Target{Root: root, Kind: "pack"}, "local", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := p.Materialize(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if matches, _ := filepath.Glob(filepath.Join(dir, "collections", "*", "*.req.toml")); len(matches) == 0 {
		t.Fatalf("pack did not materialize runnable requests under %s", dir)
	}
}

func TestExportTestManagementPlaywrightZip(t *testing.T) {
	s, _ := newServer(t)
	if err := s.DB.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/tests/export", strings.NewReader(`{"scope":"collection","collectionId":1,"format":"playwright","env":"local"}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("export playwright: %d %s", out.Code, out.Body.String())
	}
	if cd := out.Header().Get("Content-Disposition"); !strings.Contains(cd, ".playwright.zip") {
		t.Fatalf("content disposition = %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Body.Bytes()), int64(out.Body.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(b)
	}
	for _, name := range []string{
		"package.json",
		"playwright.config.ts",
		"tests/models/relay.model.ts",
		"tests/services/api.service.ts",
		"tests/fixtures/index.ts",
		"tests/fixtures/test-data.ts",
		"reporters/relay-xray-reporter.ts",
		"tests/specs/relay-generated-suite.spec.ts",
	} {
		if files[name] == "" {
			t.Fatalf("%s missing from Playwright zip: %v", name, files)
		}
	}
	if !strings.Contains(files["tests/specs/relay-generated-suite.spec.ts"], "Assertion 1: status") {
		t.Fatalf("generated test missing assertion steps:\n%s", files["tests/specs/relay-generated-suite.spec.ts"])
	}
	if !strings.Contains(files[".env.example"], "RELAY_SECRET_APITOKEN") {
		t.Fatalf("env example missing secret: %q", files[".env.example"])
	}
	if !strings.Contains(files[".env"], "Environment=local") ||
		!strings.Contains(files[".env"], "RELAY_SECRET_APITOKEN=") ||
		!strings.Contains(files["tests/fixtures/test-data.ts"], `"local"`) {
		t.Fatalf("selected environment not exported: .env=%q test-data=%q", files[".env"], files["tests/fixtures/test-data.ts"])
	}
	if strings.Contains(files[".env"], "hunter2") {
		t.Fatalf("secret leaked into Playwright project .env: %q", files[".env"])
	}
}

func TestExportSourceFolderAsPlaywrightProject(t *testing.T) {
	s, reqID := newServer(t)
	folder := &store.Folder{CollectionID: 1, Name: "Identity", Headers: map[string]string{"X-Folder": "identity"}, Vars: map[string]string{}}
	if err := s.DB.CreateFolder(folder); err != nil {
		t.Fatal(err)
	}
	storedReq, err := s.DB.Request(reqID)
	if err != nil {
		t.Fatal(err)
	}
	storedReq.FolderID = &folder.ID
	if err := s.DB.UpdateRequest(storedReq); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"scope":"source-folder","sourceFolderId":%d,"format":"playwright","env":"local"}`, folder.ID)
	req := httptest.NewRequest("POST", "/api/tests/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("export source folder: %d %s", out.Code, out.Body.String())
	}
	if cd := out.Header().Get("Content-Disposition"); !strings.Contains(cd, "identity.playwright.zip") {
		t.Fatalf("content disposition = %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Body.Bytes()), int64(out.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var spec string
	for _, file := range zr.File {
		if file.Name != "tests/specs/relay-generated-suite.spec.ts" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		spec = string(data)
	}
	if !strings.Contains(spec, "Echo") {
		t.Fatalf("folder request missing from generated spec:\n%s", spec)
	}
}

func TestManagedBatchRunPersistsScriptVariables(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"token":"ui-token"}`))
		case "/use":
			if r.URL.Query().Get("token") != "ui-token" {
				http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	col := &store.Collection{Name: "Chain", Vars: map[string]string{"baseUrl": api.URL}}
	if err := db.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []*dsl.Request{
		{Name: "Get token", Method: "GET", URL: "{{baseUrl}}/token"},
		{Name: "Use token", Method: "GET", URL: "{{baseUrl}}/use?token={{token}}", Assertions: []dsl.Assertion{{Type: "status", Equals: float64(200)}}},
	} {
		if err := db.CreateRequest(&store.Request{CollectionID: col.ID, Spec: spec}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	tests, err := db.TestCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 {
		t.Fatalf("tests = %d", len(tests))
	}
	tests[0].ScriptTests = `
pm.test("capture token", function() {
  var body = pm.response.json();
  pm.collectionVariables.set("token", body.token);
  pm.expect(body.token).to.equal("ui-token");
});
`
	if err := db.UpdateTestCase(&tests[0]); err != nil {
		t.Fatal(err)
	}

	s := &Server{DB: db, Engine: engine.NewOptions()}
	rec, doc := call(t, s, "POST", "/api/tests/run", `{}`)
	if rec.Code != 200 {
		t.Fatalf("run: %d %v", rec.Code, doc)
	}
	if doc["failed"].(float64) != 0 || doc["passed"].(float64) != 2 {
		t.Fatalf("summary = %v", doc)
	}
}

func TestManagedExecutionRetainsResultSnapshot(t *testing.T) {
	server, requestID := newServer(t)
	if err := server.DB.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	tests, err := server.DB.TestCases()
	if err != nil || len(tests) != 1 {
		t.Fatalf("tests = %v, err = %v", tests, err)
	}
	execution := &store.TestExecution{Name: "API smoke", Env: "local", TestIDs: []int64{tests[0].ID}}
	if err := server.DB.CreateTestExecution(execution); err != nil {
		t.Fatal(err)
	}
	response, document := call(t, server, "POST", "/api/test-executions/"+itoa(execution.ID)+"/run", `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("run: %d %v", response.Code, document)
	}
	tests[0].Assertions = []dsl.Assertion{{Type: "status", Equals: float64(500)}}
	if err := server.DB.UpdateTestCase(&tests[0]); err != nil {
		t.Fatal(err)
	}
	response, document = call(t, server, "POST", "/api/tests/"+itoa(tests[0].ID)+"/run", `{"env":"local"}`)
	if response.Code != http.StatusOK || document["passed"] != false {
		t.Fatalf("later run: %d %v", response.Code, document)
	}
	response, document = call(t, server, "GET", "/api/test-executions/"+itoa(execution.ID), "")
	if response.Code != http.StatusOK {
		t.Fatalf("load: %d %v", response.Code, document)
	}
	summary := document["lastSummary"].(map[string]any)
	results := summary["results"].([]any)
	if summary["env"] != "local" || len(results) != 1 || summary["passed"] != float64(1) {
		t.Fatalf("snapshot summary = %v", summary)
	}
	result := results[0].(map[string]any)
	if result["passed"] != true || result["status"] != float64(200) || result["requestId"] != float64(requestID) || len(result["steps"].([]any)) == 0 {
		t.Fatalf("snapshot result = %v", result)
	}
	if _, exists := result["send"]; exists {
		t.Fatal("execution snapshot must not store complete response payloads")
	}
}

func TestManagedEmptySetDoesNotRunOtherTests(t *testing.T) {
	server, _ := newServer(t)
	if err := server.DB.EnsureDefaultTestCases(); err != nil {
		t.Fatal(err)
	}
	set := &store.TestSet{Name: "Empty set"}
	if err := server.DB.CreateTestSet(set); err != nil {
		t.Fatal(err)
	}
	response, document := call(t, server, "POST", "/api/tests/run", `{"testSetId":`+itoa(set.ID)+`}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty set run: %d %v", response.Code, document)
	}
	runs, err := server.DB.LastTestRuns()
	if err != nil || len(runs) != 0 {
		t.Fatalf("unexpected runs: %v, err = %v", runs, err)
	}
}

func TestMoveRequestToFolder(t *testing.T) {
	s, reqID := newServer(t)
	rec, doc := call(t, s, "POST", "/api/folders", `{"collectionId":1,"name":"verify","headers":{},"vars":{}}`)
	if rec.Code != 200 {
		t.Fatalf("folder create: %d %v", rec.Code, doc)
	}
	folderID := int64(doc["id"].(float64))

	rec, _ = call(t, s, "PUT", "/api/requests/"+itoa(reqID),
		`{"folderId":`+itoa(folderID)+`,"spec":{"name":"Echo","method":"GET","url":"{{baseUrl}}/echo"}}`)
	if rec.Code != 200 {
		t.Fatalf("move: %d", rec.Code)
	}
	rec, doc = call(t, s, "GET", "/api/requests/"+itoa(reqID), "")
	if rec.Code != 200 || doc["folderId"] == nil || int64(doc["folderId"].(float64)) != folderID {
		t.Errorf("folderId after move = %v", doc["folderId"])
	}
	// Folder rename via PATCH.
	rec, _ = call(t, s, "PATCH", "/api/folders/"+itoa(folderID), `{"collectionId":1,"name":"renamed","headers":{"X-F":"1"},"vars":{}}`)
	if rec.Code != 200 {
		t.Errorf("folder patch: %d", rec.Code)
	}
}

func TestMatchesTagExpr(t *testing.T) {
	cases := []struct {
		tags []string
		expr string
		want bool
	}{
		{nil, "", true},
		{[]string{"regression"}, "", true},
		{[]string{"regression", "flaky"}, "regression,!flaky", false},
		{[]string{"regression"}, "regression,!flaky", true},
		{[]string{"smoke"}, "regression", false},
		{[]string{"Regression"}, "regression", true}, // case-insensitive
	}
	for _, c := range cases {
		if got := matchesTagExpr(c.tags, c.expr); got != c.want {
			t.Errorf("matchesTagExpr(%v, %q) = %v, want %v", c.tags, c.expr, got, c.want)
		}
	}
}

func TestScopedRequests(t *testing.T) {
	all := []store.Request{
		{ID: 1, Spec: &dsl.Request{Name: "A", Tags: []string{"smoke"}}},
		{ID: 2, Spec: &dsl.Request{Name: "B", Tags: []string{"regression"}}},
		{ID: 3, Spec: &dsl.Request{Name: "C", Tags: []string{"regression", "flaky"}}},
	}
	out := scopedRequests(all, nil, "")
	if len(out) != 3 {
		t.Fatalf("no filter: got %d, want 3", len(out))
	}
	out = scopedRequests(all, []int64{1, 3}, "")
	if len(out) != 2 || out[0].ID != 1 || out[1].ID != 3 {
		t.Errorf("id filter: %+v", out)
	}
	out = scopedRequests(all, nil, "regression,!flaky")
	if len(out) != 1 || out[0].ID != 2 {
		t.Errorf("tag filter: %+v", out)
	}
}

// newXrayCollection adds a second collection with one request, for the Xray
// handler tests that don't need a live upstream.
func newXrayCollection(t *testing.T, s *Server) (colID, reqID int64) {
	t.Helper()
	col := &store.Collection{Name: "Trace"}
	if err := s.DB.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	req := &store.Request{CollectionID: col.ID, Spec: &dsl.Request{Name: "Verify", Method: "GET", URL: "https://x.test/v"}}
	if err := s.DB.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	return col.ID, req.ID
}

func TestXrayPushWithoutConfig(t *testing.T) {
	s, _ := newServer(t)
	colID, _ := newXrayCollection(t, s)
	rec, doc := call(t, s, "POST", "/api/xray/push", `{"collectionId":`+itoa(colID)+`}`)
	if rec.Code != 422 {
		t.Fatalf("expected 422 when xray not configured, got %d %v", rec.Code, doc)
	}
}

func TestXrayPushEmptyScope(t *testing.T) {
	s, _ := newServer(t)
	colID, _ := newXrayCollection(t, s)
	if err := s.DB.SaveXraySettings(store.XraySettings{ProjectKey: "AML"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_XRAY_CLIENT_ID", "id")
	t.Setenv("RELAY_XRAY_CLIENT_SECRET", "secret")
	rec, doc := call(t, s, "POST", "/api/xray/push", `{"collectionId":`+itoa(colID)+`,"tags":"nope-matches-nothing"}`)
	if rec.Code != 422 {
		t.Fatalf("expected 422 for empty scope, got %d %v", rec.Code, doc)
	}
}

func TestXrayTestGetRequiresKey(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "GET", "/api/xray/test", "")
	if rec.Code != 400 {
		t.Fatalf("expected 400 without key, got %d %v", rec.Code, doc)
	}
}

func TestXrayTestGetRequiresConfig(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "GET", "/api/xray/test?key=AML-T1", "")
	if rec.Code != 422 {
		t.Fatalf("expected 422 when xray not configured, got %d %v", rec.Code, doc)
	}
}

func TestXrayTestCreateMissingRequest(t *testing.T) {
	s, _ := newServer(t)
	if err := s.DB.SaveXraySettings(store.XraySettings{ProjectKey: "AML"}); err != nil {
		t.Fatal(err)
	}
	rec, doc := call(t, s, "POST", "/api/xray/test", `{"requestId":99999,"summary":"x"}`)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for missing request, got %d %v", rec.Code, doc)
	}
}

func TestXrayRequirementsLinkNeedsExistingTest(t *testing.T) {
	s, _ := newServer(t)
	_, reqID := newXrayCollection(t, s)
	rec, doc := call(t, s, "POST", "/api/xray/requirements/link",
		`{"requestId":`+itoa(reqID)+`,"requirementKeys":["AML-1"]}`)
	if rec.Code != 422 {
		t.Fatalf("expected 422 without a linked Xray test, got %d %v", rec.Code, doc)
	}
}

func TestSettingsControlTLSAndTimeout(t *testing.T) {
	s, _ := newServer(t)

	// Defaults come back without any row existing.
	rec, doc := call(t, s, "GET", "/api/settings", "")
	if rec.Code != 200 || doc["timeoutSeconds"].(float64) != 30 || doc["followRedirects"] != true {
		t.Fatalf("defaults: %d %v", rec.Code, doc)
	}
	// Validation rejects nonsense.
	rec, _ = call(t, s, "PUT", "/api/settings", `{"timeoutSeconds":0,"followRedirects":true,"insecure":false}`)
	if rec.Code != 422 {
		t.Errorf("invalid timeout accepted: %d", rec.Code)
	}

	// A self-signed TLS upstream fails with default settings...
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tls.Close()
	req := &store.Request{CollectionID: 1, Spec: &dsl.Request{Name: "TLS", Method: "GET", URL: tls.URL}}
	if err := s.DB.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, s, "POST", "/api/send", `{"requestId":`+itoa(req.ID)+`}`)
	if rec.Code != 502 {
		t.Fatalf("self-signed should fail with verification on, got %d", rec.Code)
	}

	// ...and succeeds after enabling the insecure setting.
	rec, _ = call(t, s, "PUT", "/api/settings", `{"timeoutSeconds":15,"followRedirects":true,"insecure":true}`)
	if rec.Code != 200 {
		t.Fatalf("settings put: %d", rec.Code)
	}
	rec, doc = call(t, s, "POST", "/api/send", `{"requestId":`+itoa(req.ID)+`}`)
	if rec.Code != 200 || doc["status"].(float64) != 200 {
		t.Errorf("send with insecure=true: %d %v", rec.Code, doc)
	}
}
