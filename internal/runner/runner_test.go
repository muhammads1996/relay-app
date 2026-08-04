package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

// newWorkspace builds a collection with inheritance and an env, against a
// live httptest server:
//
//	col/
//	  collection.toml        (header X-Team, var basePath)
//	  01-ok.req.toml         (passes)
//	  sub/
//	    folder.toml          (header X-Sub)
//	    02-echo.req.toml     (asserts inherited headers echoed back)
//	  03-fail.req.toml       (assertion fails)
func newWorkspace(t *testing.T) (root string, srv *httptest.Server) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"status":"VERIFIED"}}`))
		case "/api/echo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"team":   r.Header.Get("X-Team"),
				"sub":    r.Header.Get("X-Sub"),
				"secret": r.Header.Get("X-Api-Key"),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	root = t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("col/collection.toml", "name = \"demo\"\n[headers]\nX-Team = \"qe\"\n[vars]\nbasePath = \"/api\"\n")
	write("col/01-ok.req.toml", `name = "ok"
url = "{{baseUrl}}{{basePath}}/ok"
[[assertions]]
type = "status"
equals = 200
[[assertions]]
type = "jsonpath"
path = "$.result.status"
equals = "VERIFIED"
`)
	write("col/sub/folder.toml", "[headers]\nX-Sub = \"yes\"\n")
	write("col/sub/02-echo.req.toml", `name = "echo"
url = "{{baseUrl}}{{basePath}}/echo"
[headers]
X-Api-Key = "{{apiKey}}"
[[assertions]]
type = "jsonpath"
path = "$.team"
equals = "qe"
[[assertions]]
type = "jsonpath"
path = "$.sub"
equals = "yes"
[[assertions]]
type = "jsonpath"
path = "$.secret"
equals = "s3cret"
`)
	write("col/03-fail.req.toml", `name = "fail"
url = "{{baseUrl}}{{basePath}}/ok"
[[assertions]]
type = "jsonpath"
path = "$.result.status"
equals = "REJECTED"
`)
	return root, srv
}

func env(srvURL string) *dsl.Environment {
	return &dsl.Environment{
		Vars:    map[string]string{"baseUrl": srvURL},
		Secrets: []string{"apiKey"},
	}
}

func getenv(k string) string {
	if k == "RELAY_SECRET_APIKEY" {
		return "s3cret"
	}
	return ""
}

func TestRun(t *testing.T) {
	root, srv := newWorkspace(t)
	rep, err := Run(context.Background(), filepath.Join(root, "col"), Options{
		Env: env(srv.URL), Getenv: getenv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("results = %d", len(rep.Results))
	}
	// Lexical order: 01-ok, 03-fail, sub/02-echo (files sort before dirs here
	// by full path: col/01… col/03… col/sub/02…).
	byName := map[string]RequestResult{}
	for _, r := range rep.Results {
		byName[r.Name] = r
	}
	if byName["ok"].Failed() {
		t.Errorf("ok failed: %+v", byName["ok"])
	}
	if byName["echo"].Failed() {
		t.Errorf("echo (inheritance+secret) failed: %+v", byName["echo"].Assertions)
	}
	if !byName["fail"].Failed() {
		t.Error("fail should fail")
	}
	if rep.Failures() != 1 {
		t.Errorf("failures = %d", rep.Failures())
	}
}

func TestRunBail(t *testing.T) {
	root, srv := newWorkspace(t)
	// Make the first request fail so bail stops the run.
	first := filepath.Join(root, "col", "01-ok.req.toml")
	data, _ := os.ReadFile(first)
	if err := os.WriteFile(first, []byte(strings.Replace(string(data), `"VERIFIED"`, `"NOPE"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), filepath.Join(root, "col"), Options{
		Env: env(srv.URL), Getenv: getenv, Bail: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 {
		t.Errorf("bail should stop after first failure, got %d results", len(rep.Results))
	}
}

func TestReports(t *testing.T) {
	root, srv := newWorkspace(t)
	rep, err := Run(context.Background(), filepath.Join(root, "col"), Options{
		Env: env(srv.URL), Getenv: getenv,
	})
	if err != nil {
		t.Fatal(err)
	}

	var junit bytes.Buffer
	if err := WriteJUnit(&junit, rep); err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Tests    int `xml:"tests,attr"`
		Failures int `xml:"failures,attr"`
		Cases    []struct {
			Name    string `xml:"name,attr"`
			Failure *struct {
				Message string `xml:"message,attr"`
			} `xml:"failure"`
		} `xml:"testcase"`
	}
	if err := xml.Unmarshal(junit.Bytes(), &suite); err != nil {
		t.Fatalf("junit output invalid: %v\n%s", err, junit.String())
	}
	// The reporter emits one JUnit testcase per assertion (not per request),
	// so: 01-ok (2) + sub/02-echo (3) + 03-fail (1) = 6 total, 1 failure.
	if suite.Tests != 6 || suite.Failures != 1 {
		t.Errorf("junit tests=%d failures=%d", suite.Tests, suite.Failures)
	}

	var js bytes.Buffer
	if err := WriteJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tests    int `json:"tests"`
		Failures int `json:"failures"`
	}
	if err := json.Unmarshal(js.Bytes(), &doc); err != nil {
		t.Fatalf("json output invalid: %v", err)
	}
	if doc.Tests != 3 || doc.Failures != 1 {
		t.Errorf("json tests=%d failures=%d", doc.Tests, doc.Failures)
	}
}

func TestRunTransportError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.req.toml"),
		[]byte("url = \"http://127.0.0.1:1/unreachable\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), root, Options{Getenv: os.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Results[0].Failed() || rep.Results[0].Err == nil {
		t.Errorf("expected transport error, got %+v", rep.Results[0])
	}
}

func TestDataDrivenRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": r.URL.Query().Get("id")})
	}))
	defer srv.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.req.toml"), []byte(`name = "lookup"
url = "{{baseUrl}}/item?id={{rowId}}"
[[assertions]]
type = "jsonpath"
path = "$.id"
equals = "{{rowId}}"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(context.Background(), root, Options{
		Env:    &dsl.Environment{Vars: map[string]string{"baseUrl": srv.URL}},
		Getenv: os.Getenv,
		Data: []map[string]string{
			{"rowId": "100"},
			{"rowId": "200"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results = %d", len(rep.Results))
	}
	for i, want := range []string{"lookup [1]", "lookup [2]"} {
		if rep.Results[i].Name != want {
			t.Errorf("name[%d] = %q, want %q", i, rep.Results[i].Name, want)
		}
		if rep.Results[i].Iteration != i+1 {
			t.Errorf("iteration[%d] = %d", i, rep.Results[i].Iteration)
		}
	}
	if rep.Failures() != 0 {
		for _, r := range rep.Results {
			t.Logf("%+v", r)
		}
		t.Errorf("failures = %d", rep.Failures())
	}
}

func TestChainedRunPersistsScriptVariables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"token":"abc123"}`))
		case "/use":
			if r.URL.Query().Get("token") != "abc123" {
				http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("01-token.req.toml", `name = "get token"
url = "{{baseUrl}}/token"

[scripts]
tests = '''
pm.test("captures token", function() {
  var body = pm.response.json();
  pm.collectionVariables.set("token", body.token);
  pm.expect(body.token).to.equal("abc123");
});
'''
`)
	write("02-use.req.toml", `name = "use token"
url = "{{baseUrl}}/use?token={{token}}"

[[assertions]]
type = "status"
equals = 200
`)

	rep, err := Run(context.Background(), root, Options{
		Env:    &dsl.Environment{Vars: map[string]string{"baseUrl": srv.URL}},
		Getenv: os.Getenv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results = %d", len(rep.Results))
	}
	for _, res := range rep.Results {
		if res.Failed() {
			t.Fatalf("%s failed: err=%v assertions=%+v scripts=%+v", res.Name, res.Err, res.Assertions, res.ScriptTests)
		}
	}
}
