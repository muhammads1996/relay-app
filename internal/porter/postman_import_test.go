package porter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/runner"
)

func TestImportPostmanInheritedAuthAndScripts(t *testing.T) {
	data := []byte(`{
  "info":{"name":"Inherited"},
  "auth":{"type":"bearer","bearer":[{"key":"token","value":"collection-token"}]},
  "event":[{"listen":"prerequest","script":{"type":"text/javascript","exec":["pm.environment.set('a', '1');"]}},
            {"listen":"test","script":{"type":"text/javascript","exec":["pm.test('collection', function() {});"]}}],
  "item":[{"name":"Outer","event":[{"listen":"prerequest","script":{"exec":["pm.environment.set('b', '2');"]}}],"item":[
    {"name":"Inner","auth":{"type":"basic","basic":[{"key":"username","value":"folder-user"},{"key":"password","value":"folder-pass"}]},"item":[
      {"name":"Inherited basic","request":{"url":"https://example.test"}},
      {"name":"Override","request":{"url":"https://example.test","auth":{"type":"apikey","apikey":[{"key":"key","value":"X-Key"},{"key":"value","value":"secret"},{"key":"in","value":"query"}]},"event":[{"listen":"test","script":{"exec":["pm.test('request', function() {});", "pm.sendRequest('https://unsupported');"]}}]}}
    ]}
  ]}]}`)
	out := filepath.Join(t.TempDir(), "import")
	report, err := ImportPostmanWithReport(data, out)
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 {
		t.Fatalf("requests=%d", report.Requests)
	}

	inherited, err := dsl.LoadRequest(filepath.Join(out, "outer", "inner", "01-inherited-basic.req.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if inherited.Auth == nil || inherited.Auth.Type != "basic" || inherited.Auth.Username != "folder-user" || inherited.Auth.Password != "folder-pass" {
		t.Fatalf("inherited auth = %+v", inherited.Auth)
	}
	if inherited.Scripts == nil || !strings.Contains(inherited.Scripts.PreRequest, "set('a'") || !strings.Contains(inherited.Scripts.PreRequest, "set('b'") || !strings.Contains(inherited.Scripts.Tests, "collection") {
		t.Fatalf("inherited scripts = %+v", inherited.Scripts)
	}
	override, err := dsl.LoadRequest(filepath.Join(out, "outer", "inner", "02-override.req.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if override.Auth == nil || override.Auth.Type != "apikey" || override.Auth.In != "query" || override.Auth.Key != "X-Key" || override.Auth.Value != "secret" {
		t.Fatalf("request override auth = %+v", override.Auth)
	}
	if override.Scripts == nil || !strings.Contains(override.Scripts.Tests, "collection") || !strings.Contains(override.Scripts.Tests, "request") || !strings.Contains(override.Scripts.Tests, "pm.sendRequest") {
		t.Fatalf("request scripts were not preserved: %+v", override.Scripts)
	}
	found := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning.Message, "pm.sendRequest") && warning.Location == "request: Outer / Inner / Override" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing located unsupported API warning: %+v", report.Warnings)
	}
}

func TestPostmanImportedScriptsAuthAndDuplicateRowsExecute(t *testing.T) {
	var gotQuery, gotHeaders, gotAuth, gotDisabled string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotHeaders = strings.Join(r.Header.Values("X-Repeat"), ",")
		gotAuth = r.Header.Get("Authorization")
		gotDisabled = r.Header.Get("X-Disabled")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"source":"postman"}`))
	}))
	defer srv.Close()

	data := []byte(fmt.Sprintf(`{
  "info":{"name":"Executable migration"},
  "variable":[{"key":"base","value":%q}],
  "auth":{"type":"bearer","bearer":[{"key":"token","value":"inherited-token"}]},
  "event":[
    {"listen":"prerequest","script":{"type":"text/javascript","exec":["pm.collectionVariables.set('path','/check');"]}},
    {"listen":"test","script":{"type":"text/javascript","exec":["pm.test('collection script', function(){ pm.expect(pm.response.code).to.eql(200); });"]}}
  ],
  "item":[{"name":"folder","event":[{"listen":"test","script":{"type":"text/javascript","exec":["pm.test('folder script', function(){ pm.expect(pm.response.json().ok).to.eql(true); });"]}}],"item":[
    {"name":"request","request":{"method":"GET","url":{"raw":"{{base}}{{path}}?keep=1","query":[{"key":"tag","value":"one"},{"key":"tag","value":"two"},{"key":"off","value":"x","disabled":true}]},"header":[{"key":"X-Repeat","value":"first"},{"key":"X-Repeat","value":"second"},{"key":"X-Disabled","value":"no","disabled":true}],"event":[{"listen":"test","script":{"type":"text/javascript","exec":["pm.test('request script', function(){ pm.expect(pm.response.json().source).to.eql('postman'); });"]}}]}}
  ]}]
}`, srv.URL))
	out := filepath.Join(t.TempDir(), "import")
	report, err := ImportPostmanWithReport(data, out)
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 {
		t.Fatalf("requests = %d", report.Requests)
	}
	resp, err := runner.Run(context.Background(), out, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failures() != 0 || len(resp.Results) != 1 {
		t.Fatalf("imported request failed: %+v", resp.Results)
	}
	result := resp.Results[0]
	if len(result.ScriptTests) != 3 {
		t.Fatalf("collection/folder/request scripts did not all run: %+v", result.ScriptTests)
	}
	for _, test := range result.ScriptTests {
		if !test.Passed {
			t.Errorf("script test %q failed: %s", test.Name, test.Error)
		}
	}
	if gotQuery != "keep=1&tag=one&tag=two" {
		t.Errorf("query rows did not preserve order, duplicates, and disabled state: %q", gotQuery)
	}
	if gotHeaders != "first,second" {
		t.Errorf("duplicate headers were not preserved: %q", gotHeaders)
	}
	if gotAuth != "Bearer inherited-token" {
		t.Errorf("inherited auth was not executed: %q", gotAuth)
	}
	if gotDisabled != "" {
		t.Errorf("disabled header was sent: %q", gotDisabled)
	}
}

func TestPostmanUnsupportedAPIsAreLocatedAndScriptsPreserved(t *testing.T) {
	data := []byte(`{"info":{"name":"Warnings"},"event":[{"listen":"prerequest","script":{"exec":["pm.sendRequest('https://example.test');","pm.cookies.jar().set('x','y');"]}}],"item":[{"name":"one","request":{"url":"https://example.test","event":[{"listen":"test","script":{"exec":["pm.visualizer.set('x','y');"]}}]}}]}`)
	out := t.TempDir()
	report, err := ImportPostmanWithReport(data, out)
	if err != nil {
		t.Fatal(err)
	}
	req, err := dsl.LoadRequest(filepath.Join(out, "01-one.req.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"pm.sendRequest", "pm.cookies", "pm.visualizer"} {
		if req.Scripts == nil || !strings.Contains(req.Scripts.PreRequest+req.Scripts.Tests, source) {
			t.Errorf("unsupported source %q was not preserved in request scripts: %+v", source, req.Scripts)
		}
	}
	warnings := strings.Builder{}
	for _, warning := range report.Warnings {
		fmt.Fprintf(&warnings, "%s: %s\n", warning.Location, warning.Message)
	}
	for _, source := range []string{"pm.sendRequest", "pm.cookies", "pm.visualizer"} {
		if !strings.Contains(warnings.String(), source) {
			t.Errorf("missing warning for %q: %s", source, warnings.String())
		}
	}
}

func TestPostmanDisabledCollectionVariableIsOmittedWithWarning(t *testing.T) {
	data := []byte(`{"info":{"name":"Vars"},"variable":[{"key":"active","value":"ok"},{"key":"inactive","value":"secret","disabled":true}],"item":[]}`)
	out := t.TempDir()
	report, err := ImportPostmanWithReport(data, out)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := dsl.LoadConfig(filepath.Join(out, "collection.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vars["active"] != "ok" {
		t.Fatalf("active variable missing: %+v", cfg.Vars)
	}
	if _, exists := cfg.Vars["inactive"]; exists {
		t.Fatalf("disabled variable imported: %+v", cfg.Vars)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0].Message, `disabled collection variable "inactive"`) {
		t.Fatalf("disabled variable warning = %+v", report.Warnings)
	}
}

func TestImportPostmanNoAuthAndUnsupportedAuthWarning(t *testing.T) {
	data := []byte(`{"info":{"name":"Auth"},"auth":{"type":"bearer","bearer":[{"key":"token","value":"parent"}]},"item":[
{"name":"Disabled","request":{"url":"https://example.test","auth":{"type":"noauth"}}},
{"name":"Unsupported","request":{"url":"https://example.test","auth":{"type":"oauth2"}}}]}`)
	out := t.TempDir()
	report, err := ImportPostmanWithReport(data, out)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := dsl.LoadRequest(filepath.Join(out, "01-disabled.req.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Auth != nil {
		t.Fatalf("noauth override retained inherited auth: %+v", disabled.Auth)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0].Message, "oauth2") {
		t.Fatalf("warnings = %+v", report.Warnings)
	}
}

func TestImportPostmanLegacyAPIStillReturnsCount(t *testing.T) {
	out := filepath.Join(t.TempDir(), "legacy")
	n, err := ImportPostman([]byte(`{"info":{"name":"Compat"},"item":[{"name":"one","request":{"url":"https://example.test"}}]}`), out)
	if err != nil || n != 1 {
		t.Fatalf("ImportPostman()=(%d,%v)", n, err)
	}
	if _, err := os.Stat(filepath.Join(out, "01-one.req.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestImportPostmanReportsDroppedRequestContent(t *testing.T) {
	data := []byte(`{
  "info":{"name":"Dropped content"},
  "item":[
    {"name":"Examples","response":[{"name":"200 response"}]},
    {"name":"Fields","request":{"method":"POST","header":[{"key":"X-Off","value":"x","disabled":true}],"url":{"raw":"https://example.test/ping?a=1","query":[{"key":"b","value":"2","disabled":true}]},"body":{"mode":"binary","file":{"src":"./upload.bin"}}}},
    {"name":"Encoded","request":{"method":"POST","url":"https://example.test/form","body":{"mode":"urlencoded","urlencoded":[{"key":"off","value":"gone","disabled":true}]}}}
  ]
}`)
	out := t.TempDir()
	report, err := ImportPostmanWithReport(data, out)
	if err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(report.Warnings))
	for _, warning := range report.Warnings {
		joined = append(joined, warning.Location+": "+warning.Message)
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{"saved Postman response example was not imported", `Postman body mode "binary" was not imported`, `disabled URL encoded field "off" was dropped`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing warning %q in:\n%s", want, got)
		}
	}
	for _, stale := range []string{`disabled header "X-Off" was dropped`, `disabled query parameter "b" was dropped`} {
		if strings.Contains(got, stale) {
			t.Errorf("preserved disabled row was incorrectly reported as dropped: %q\n%s", stale, got)
		}
	}
	fields, err := dsl.LoadRequest(filepath.Join(out, "02-fields.req.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fields.HeaderEntries) != 1 || !fields.HeaderEntries[0].Disabled || fields.HeaderEntries[0].Key != "X-Off" {
		t.Errorf("disabled header row was not preserved: %+v", fields.HeaderEntries)
	}
	if len(fields.QueryEntries) != 1 || !fields.QueryEntries[0].Disabled || fields.QueryEntries[0].Key != "b" {
		t.Errorf("disabled query row was not preserved: %+v", fields.QueryEntries)
	}
}
