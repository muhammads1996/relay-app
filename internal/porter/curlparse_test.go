package porter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCurlDevToolsStyle(t *testing.T) {
	cmd := `curl 'https://api.example.com/aml/v2/verify' \
  -X POST \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer eyJtok' \
  -H 'X-Correlation-Id: abc-123' \
  --data-raw '{"idNumber":"8001015009087"}' \
  --compressed`
	req, err := ParseCurl(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL != "https://api.example.com/aml/v2/verify" {
		t.Errorf("method/url = %s %s", req.Method, req.URL)
	}
	if req.Headers["Content-Type"] != "application/json" || req.Headers["X-Correlation-Id"] != "abc-123" {
		t.Errorf("headers = %v", req.Headers)
	}
	// Bearer header becomes the auth helper.
	if req.Auth == nil || req.Auth.Type != "bearer" || req.Auth.Token != "eyJtok" {
		t.Errorf("auth = %+v", req.Auth)
	}
	if _, ok := req.Headers["Authorization"]; ok {
		t.Error("Authorization should move into auth")
	}
	if req.Body == nil || req.Body.Type != "json" || req.Body.Content != `{"idNumber":"8001015009087"}` {
		t.Errorf("body = %+v", req.Body)
	}
	if req.Name != "POST /aml/v2/verify" {
		t.Errorf("name = %q", req.Name)
	}
}

func TestParseCurlForms(t *testing.T) {
	cases := []struct {
		cmd    string
		method string
		url    string
		body   string
		btype  string
	}{
		{`curl https://x.test/ping`, "GET", "https://x.test/ping", "", ""},
		{`curl -d 'a=1' -d 'b=2' https://x.test/form`, "POST", "https://x.test/form", "a=1&b=2", "urlencoded"},
		{`curl -G -d 'q=term' https://x.test/search`, "GET", "https://x.test/search?q=term", "", ""},
		{`curl --request PUT --url https://x.test/item "-H" "Accept: */*"`, "PUT", "https://x.test/item", "", ""},
		{"curl -s -L -k https://x.test/redirect -o /dev/null", "GET", "https://x.test/redirect", "", ""},
	}
	for _, c := range cases {
		req, err := ParseCurl(c.cmd)
		if err != nil {
			t.Errorf("%s: %v", c.cmd, err)
			continue
		}
		if req.Method != c.method || req.URL != c.url {
			t.Errorf("%s:\n got %s %s", c.cmd, req.Method, req.URL)
		}
		if c.body == "" && req.Body != nil {
			t.Errorf("%s: unexpected body %+v", c.cmd, req.Body)
		}
		if c.body != "" && (req.Body == nil || req.Body.Content != c.body || req.Body.Type != c.btype) {
			t.Errorf("%s: body = %+v", c.cmd, req.Body)
		}
	}
}

func TestParseCurlBasicAuthAndErrors(t *testing.T) {
	req, err := ParseCurl(`curl -u admin:s3cret https://x.test/`)
	if err != nil {
		t.Fatal(err)
	}
	if req.Auth == nil || req.Auth.Type != "basic" || req.Auth.Password != "s3cret" {
		t.Errorf("auth = %+v", req.Auth)
	}
	for _, bad := range []string{"", "wget https://x.test", "curl", "curl -H", `curl 'unterminated`} {
		if _, err := ParseCurl(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

// A parsed curl command round-trips through our own curl exporter.
func TestParseCurlRoundTrip(t *testing.T) {
	req, err := ParseCurl(`curl -X POST -H 'Content-Type: application/json' --data-raw '{"a":1}' 'https://x.test/v1/do?x=a b'`)
	if err != nil {
		t.Fatal(err)
	}
	// Marshal → reparse as TOML to prove it lands in the DSL cleanly.
	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), `"json"`) {
		t.Errorf("spec json = %s", b)
	}
}

func TestParseCurlPostmanStyle(t *testing.T) {
	cmd := `curl --location --request POST --url 'https://x.test/v1/do?x=a b' --header 'Content-Type: application/json' --header 'Authorization: Bearer token123' --data-raw '{"a":1}'`
	req, err := ParseCurl(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL != "https://x.test/v1/do?x=a b" {
		t.Fatalf("method/url = %s %s", req.Method, req.URL)
	}
	if req.Auth == nil || req.Auth.Type != "bearer" || req.Auth.Token != "token123" {
		t.Fatalf("auth = %+v", req.Auth)
	}
	if req.Body == nil || req.Body.Type != "json" || req.Body.Content != `{"a":1}` {
		t.Fatalf("body = %+v", req.Body)
	}
}

func TestParseCurlUserInsuranceSample(t *testing.T) {
	cmd := `curl --location 'https://dev.nonprod.agw.qa.omapps.net/insurance/life/party/aml/certify/v2/person' \
--header 'X-IBM-Client-Id: 165239a3-1596-4ff8-9239-a315967ff8e3' \
--header 'useCache: true' \
--data '{
    "SourceSystem": "IOP",
    "User": "x525719",
    "IdentityNumber": "8202250196080",
    "FirstName": "HELGADIA",
    "LastName": "KLEIN-SMIT",
    "AddressLine1": "121 EAGLE STREET",
    "AddressLine2": "PESCODIA",
    "AddressLine3": "KIMBERLEY",
    "PostalCode": "8309"
}'`

	req, err := ParseCurl(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://dev.nonprod.agw.qa.omapps.net/insurance/life/party/aml/certify/v2/person" {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %q", req.Method)
	}
	if req.Headers["X-IBM-Client-Id"] != "165239a3-1596-4ff8-9239-a315967ff8e3" {
		t.Fatalf("X-IBM-Client-Id header = %q", req.Headers["X-IBM-Client-Id"])
	}
	if req.Headers["useCache"] != "true" {
		t.Fatalf("useCache header = %q", req.Headers["useCache"])
	}
	if req.Body == nil {
		t.Fatal("body is nil")
	}
	if req.Body.Type != "json" {
		t.Fatalf("body type = %q", req.Body.Type)
	}
	if !strings.Contains(req.Body.Content, `"IdentityNumber": "8202250196080"`) {
		t.Fatalf("body content not imported correctly: %s", req.Body.Content)
	}
}

func TestParseCurlWithTerminalPrefix(t *testing.T) {
	cmd := `PS C:\Apps\relay> curl --location 'https://x.test/v1/do' --header 'Content-Type: application/json' --data '{"a":1}'`
	req, err := ParseCurl(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL != "https://x.test/v1/do" {
		t.Fatalf("method/url = %s %s", req.Method, req.URL)
	}
	if req.Body == nil || req.Body.Type != "json" {
		t.Fatalf("body = %+v", req.Body)
	}
}

func TestParseCurlWindowsCaretContinuation(t *testing.T) {
	cmd := "curl --location 'https://x.test/v1/do' ^\n--header 'X-IBM-Client-Id: abc' ^\n--data '{\"a\":1}'"
	req, err := ParseCurl(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %q", req.Method)
	}
	if req.Headers["X-IBM-Client-Id"] != "abc" {
		t.Fatalf("headers = %+v", req.Headers)
	}
	if req.Body == nil || req.Body.Content != `{"a":1}` {
		t.Fatalf("body = %+v", req.Body)
	}
}

func TestExportPostmanRoundTrip(t *testing.T) {
	root := exportWorkspace(t) // collection.toml + root request + verify/ folder
	out, err := ExportPostman(root)
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Info     struct{ Name, Schema string }
		Variable []struct{ Key, Value string }
		Item     []json.RawMessage
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid postman json: %v\n%s", err, out)
	}
	if doc.Info.Name != "demo" || !strings.Contains(doc.Info.Schema, "v2.1.0") {
		t.Errorf("info = %+v", doc.Info)
	}
	s := string(out)
	for _, want := range []string{
		`"{{baseUrl}}/aml/v2/verify"`,     // variables stay raw
		`"X-Correlation-Id"`,              // inherited collection header flattened in
		`pm.response.to.have.status(200)`, // assertions → pm tests
		`"name": "verify"`,                // folder preserved
	} {
		if !strings.Contains(s, want) {
			t.Errorf("postman export missing %s\n%s", want, s)
		}
	}
	// Decode the folder item's test script and check the jsonpath expect
	// line (raw substring search would miss it: JSON escapes the quotes).
	var full struct {
		Item []struct {
			Name string
			Item []struct {
				Event []struct {
					Script struct{ Exec []string }
				}
			}
		}
	}
	if err := json.Unmarshal(out, &full); err != nil {
		t.Fatal(err)
	}
	var exec []string
	for _, top := range full.Item {
		for _, sub := range top.Item {
			for _, ev := range sub.Event {
				exec = append(exec, ev.Script.Exec...)
			}
		}
	}
	wantLine := `pm.expect(pm.response.json().result.items[0].status).to.eql("VERIFIED")`
	if !strings.Contains(strings.Join(exec, "\n"), wantLine) {
		t.Errorf("pm test script missing %s\ngot: %v", wantLine, exec)
	}

	// Full circle: our own importer reads the export back losslessly enough
	// to run.
	dir := t.TempDir()
	n, err := ImportPostman(out, dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("re-import = %d requests", n)
	}
}
