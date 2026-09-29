package dsl

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const sample = `name = "Verify Individual"
method = "POST"
url = "{{baseUrl}}/aml/v2/verify"

[headers]
Content-Type = "application/json"
X-Correlation-Id = "{{$uuid}}"

[body]
type = "json"
content = '''
{ "idNumber": "{{testIdNumber}}", "channel": "API" }
'''

[[assertions]]
type = "status"
equals = 200

[[assertions]]
type = "jsonpath"
path = "$.result.status"
equals = "VERIFIED"
`

func writeReq(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.req.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRequest(t *testing.T) {
	r, err := LoadRequest(writeReq(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "Verify Individual" || r.Method != "POST" {
		t.Errorf("got name=%q method=%q", r.Name, r.Method)
	}
	if r.Headers["Content-Type"] != "application/json" {
		t.Errorf("headers = %v", r.Headers)
	}
	if r.Body == nil || r.Body.Type != "json" {
		t.Fatalf("body = %+v", r.Body)
	}
	if len(r.Assertions) != 2 {
		t.Fatalf("assertions = %+v", r.Assertions)
	}
	if r.Assertions[0].Type != "status" {
		t.Errorf("first assertion = %+v", r.Assertions[0])
	}
	if got := r.Assertions[1].Equals; got != "VERIFIED" {
		t.Errorf("jsonpath equals = %v", got)
	}
}

// Marshal → Load → Marshal must be byte-identical (deterministic output is
// what keeps collections git-diffable).
func TestMarshalRoundTrip(t *testing.T) {
	r, err := LoadRequest(writeReq(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	first := Marshal(r)
	r2, err := LoadRequest(writeReq(t, string(first)))
	if err != nil {
		t.Fatalf("re-parse failed: %v\n%s", err, first)
	}
	second := Marshal(r2)
	if !bytes.Equal(first, second) {
		t.Errorf("round trip not stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestOrderedEntriesRoundTrip(t *testing.T) {
	r := &Request{
		Name: "ordered", Method: "GET", URL: "https://example.test/path",
		Query: map[string]string{"legacy": "query"}, Headers: map[string]string{"X-Legacy": "header"},
		QueryEntries:  []Entry{{Key: "tag", Value: "first"}, {Key: "tag", Value: "skip", Disabled: true}, {Key: "tag", Value: "last"}},
		HeaderEntries: []Entry{{Key: "X-Repeat", Value: "one"}, {Key: "X-Repeat", Value: "two"}},
	}
	first := Marshal(r)
	r2, err := LoadRequest(writeReq(t, string(first)))
	if err != nil {
		t.Fatalf("ordered request did not parse: %v\n%s", err, first)
	}
	if len(r2.QueryEntries) != 3 || r2.QueryEntries[0].Value != "first" || !r2.QueryEntries[1].Disabled || r2.QueryEntries[2].Value != "last" {
		t.Fatalf("query entries did not round-trip: %#v", r2.QueryEntries)
	}
	if len(r2.HeaderEntries) != 2 || r2.HeaderEntries[0].Value != "one" || r2.HeaderEntries[1].Value != "two" {
		t.Fatalf("header entries did not round-trip: %#v", r2.HeaderEntries)
	}
	if r2.Query["legacy"] != "query" || r2.Headers["X-Legacy"] != "header" {
		t.Fatalf("legacy maps did not round-trip alongside entries: query=%v headers=%v", r2.Query, r2.Headers)
	}
	if second := Marshal(r2); !bytes.Equal(first, second) {
		t.Errorf("ordered marshal is unstable:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestMarshalEscaping(t *testing.T) {
	r := &Request{
		Name:    `has "quotes" and \backslash`,
		Method:  "POST",
		URL:     "https://example.com",
		Headers: map[string]string{"X-Weird Key": "tab\there"},
		Body:    &Body{Type: "raw", Content: "contains ''' triple quotes\nand newline"},
	}
	out := Marshal(r)
	r2, err := LoadRequest(writeReq(t, string(out)))
	if err != nil {
		t.Fatalf("escaped output failed to parse: %v\n%s", err, out)
	}
	if r2.Name != r.Name {
		t.Errorf("name: got %q want %q", r2.Name, r.Name)
	}
	if r2.Headers["X-Weird Key"] != "tab\there" {
		t.Errorf("header: got %q", r2.Headers["X-Weird Key"])
	}
	if r2.Body.Content != r.Body.Content {
		t.Errorf("body: got %q want %q", r2.Body.Content, r.Body.Content)
	}
}

// Body content must survive Marshal → Load byte-exactly: no trailing
// newline may be added or removed.
func TestMarshalBodyFidelity(t *testing.T) {
	for _, content := range []string{
		`{"no": "trailing newline"}`,
		"{\"with\": \"trailing newline\"}\n",
		"line1\nline2",
		"ends with quote'",
		"tab\tis fine",
	} {
		r := &Request{Method: "POST", URL: "https://x.test", Body: &Body{Type: "json", Content: content}}
		r2, err := LoadRequest(writeReq(t, string(Marshal(r))))
		if err != nil {
			t.Fatalf("%q: %v", content, err)
		}
		if r2.Body.Content != content {
			t.Errorf("body changed:\n got %q\nwant %q", r2.Body.Content, content)
		}
	}
}

func TestMarshalFormDataRoundTrip(t *testing.T) {
	r := &Request{
		Name:   "upload",
		Method: "POST",
		URL:    "https://api.example.com/upload",
		Body: &Body{Type: "formdata", FormData: []FormField{
			{Key: "metadata", Value: `{"id":"{{id}}"}`, Type: "text"},
			{Key: "document", File: "./fixtures/doc.pdf", Type: "file"},
			{Key: "skip", Value: "off", Type: "text", Disabled: true},
		}},
	}
	r2, err := LoadRequest(writeReq(t, string(Marshal(r))))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if r2.Body == nil || r2.Body.Type != "formdata" || len(r2.Body.FormData) != 3 {
		t.Fatalf("form-data body = %+v", r2.Body)
	}
	if got := r2.Body.FormData[1].File; got != "./fixtures/doc.pdf" {
		t.Errorf("file field = %q", got)
	}
	if !r2.Body.FormData[2].Disabled {
		t.Errorf("disabled field was not preserved: %+v", r2.Body.FormData[2])
	}
}

func TestLoadRequestErrors(t *testing.T) {
	if _, err := LoadRequest(writeReq(t, `name = "no url"`)); err == nil {
		t.Error("expected error for missing url")
	}
	if _, err := LoadRequest(writeReq(t, `url = "x" method`)); err == nil {
		t.Error("expected error for invalid toml")
	}
}

func TestDefaultMethod(t *testing.T) {
	r, err := LoadRequest(writeReq(t, `url = "https://example.com"`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "GET" {
		t.Errorf("default method = %q", r.Method)
	}
}

func TestScriptsRoundTrip(t *testing.T) {
	r := &Request{
		Name:   "with scripts",
		Method: "POST",
		URL:    "https://api.example.com/test",
		Scripts: &Scripts{
			PreRequest: "pm.environment.set(\"x\", \"1\");",
			Tests: `pm.test("status ok", function() {
  pm.expect(pm.response.code).to.equal(200);
});
`,
		},
	}
	out := Marshal(r)
	r2, err := LoadRequest(writeReq(t, string(out)))
	if err != nil {
		t.Fatalf("parse failed: %v\n%s", err, out)
	}
	if r2.Scripts == nil {
		t.Fatal("scripts nil after round-trip")
	}
	if r2.Scripts.PreRequest != r.Scripts.PreRequest {
		t.Errorf("pre_request: got %q want %q", r2.Scripts.PreRequest, r.Scripts.PreRequest)
	}
	if r2.Scripts.Tests != r.Scripts.Tests {
		t.Errorf("tests: got %q want %q", r2.Scripts.Tests, r.Scripts.Tests)
	}
}

func TestMarshalModernAssertionsAndXrayRoundTrip(t *testing.T) {
	r := &Request{
		Name:         "modern",
		Method:       "GET",
		URL:          "{{baseUrl}}/modern",
		Tags:         []string{"smoke", "release"},
		Owner:        "risk-platform",
		Priority:     "critical",
		XrayKey:      "AML-T142",
		XrayPlan:     "AML-P1",
		XraySet:      "AML-S1",
		Requirements: []string{"AML-REQ-1"},
		Assertions: []Assertion{
			{Type: "status", Op: "oneof", Exp: "200,204"},
			{Type: "json", Path: "$.items", Op: "lengthGt", Exp: int64(0)},
			{Type: "forall", Path: "$.items", Field: "status", Sub: "==", Exp: "ACTIVE"},
			{Type: "script", Code: "pm.test(\"ok\", () => pm.expect(true).to.be.true);"},
		},
	}
	out := Marshal(r)
	r2, err := LoadRequest(writeReq(t, string(out)))
	if err != nil {
		t.Fatalf("parse failed: %v\n%s", err, out)
	}
	if r2.XrayPlan != r.XrayPlan || r2.XraySet != r.XraySet || r2.XrayKey != r.XrayKey {
		t.Fatalf("xray fields changed: %+v", r2)
	}
	if len(r2.Assertions) != len(r.Assertions) {
		t.Fatalf("assertions = %+v", r2.Assertions)
	}
	if r2.Assertions[0].Op != "oneof" || r2.Assertions[0].Exp != "200,204" {
		t.Errorf("status assertion = %+v", r2.Assertions[0])
	}
	if r2.Assertions[1].Op != "lengthGt" || r2.Assertions[1].Exp != int64(0) {
		t.Errorf("json assertion = %+v", r2.Assertions[1])
	}
	if r2.Assertions[2].Field != "status" || r2.Assertions[2].Sub != "==" || r2.Assertions[2].Exp != "ACTIVE" {
		t.Errorf("forall assertion = %+v", r2.Assertions[2])
	}
	if r2.Assertions[3].Code != r.Assertions[3].Code {
		t.Errorf("script code = %q", r2.Assertions[3].Code)
	}
}
