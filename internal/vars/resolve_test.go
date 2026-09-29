package vars

import (
	"os"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

func TestResolveInheritanceAndOverride(t *testing.T) {
	req := &dsl.Request{
		Method: "POST",
		URL:    "{{baseUrl}}/verify",
		Headers: map[string]string{
			"X-Api-Version": "2", // overrides inherited
			"X-Trace":       "",  // disables inherited
		},
		Body: &dsl.Body{Type: "json", Content: `{"id":"{{id}}"}`},
	}
	inherited := map[string]string{
		"X-Api-Version": "1",
		"X-Trace":       "on",
		"X-Team":        "qe",
	}
	scope := NewScope(map[string]string{"baseUrl": "https://sit.example.com", "id": "9001"})

	r, err := Resolve(req, inherited, scope)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "https://sit.example.com/verify" {
		t.Errorf("url = %q", r.URL)
	}
	if got := r.Headers.Get("X-Api-Version"); got != "2" {
		t.Errorf("override failed: %q", got)
	}
	if r.HeaderOrigin["X-Api-Version"] != "request" {
		t.Errorf("origin = %q", r.HeaderOrigin["X-Api-Version"])
	}
	if got := r.Headers.Get("X-Trace"); got != "" {
		t.Errorf("disable failed: %q", got)
	}
	if got := r.Headers.Get("X-Team"); got != "qe" {
		t.Errorf("inherit failed: %q", got)
	}
	if r.HeaderOrigin["X-Team"] != "inherited" {
		t.Errorf("origin = %q", r.HeaderOrigin["X-Team"])
	}
	if string(r.Body) != `{"id":"9001"}` {
		t.Errorf("body = %s", r.Body)
	}
	// json body type sets Content-Type when absent
	if got := r.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
}

func TestResolveQueryAndAuth(t *testing.T) {
	req := &dsl.Request{
		Method: "GET",
		URL:    "https://api.example.com/search",
		Query:  map[string]string{"q": "{{term}}", "page": "1"},
		Auth:   &dsl.Auth{Type: "bearer", Token: "{{token}}"},
	}
	scope := NewScope(map[string]string{"term": "two words", "token": "abc123"})
	r, err := Resolve(req, nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "https://api.example.com/search?page=1&q=two+words" {
		t.Errorf("url = %q", r.URL)
	}
	if got := r.Headers.Get("Authorization"); got != "Bearer abc123" {
		t.Errorf("auth = %q", got)
	}
	if r.HeaderOrigin["Authorization"] != "auth" {
		t.Errorf("origin = %q", r.HeaderOrigin["Authorization"])
	}
}

func TestResolveOrderedEntries(t *testing.T) {
	req := &dsl.Request{
		Method: "GET", URL: "https://api.example.test/search?base=one",
		QueryEntries: []dsl.Entry{
			{Key: "tag", Value: "{{first}}"},
			{Key: "tag", Value: "disabled", Disabled: true},
			{Key: "tag", Value: "last"},
		},
		HeaderEntries: []dsl.Entry{
			{Key: "X-Repeat", Value: "{{first}}"},
			{Key: "X-Repeat", Value: "last"},
			{Key: "X-Off", Value: "ignored", Disabled: true},
		},
	}
	resolved, err := Resolve(req, map[string]string{"X-Repeat": "inherited", "X-Team": "qa"}, NewScope(map[string]string{"first": "one two"}))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.URL != "https://api.example.test/search?base=one&tag=one+two&tag=last" {
		t.Errorf("ordered query = %q", resolved.URL)
	}
	if got := resolved.Headers.Values("X-Repeat"); len(got) != 2 || got[0] != "one two" || got[1] != "last" {
		t.Errorf("ordered header values = %#v", got)
	}
	if got := resolved.Headers.Get("X-Team"); got != "qa" {
		t.Errorf("inherited header = %q", got)
	}
	if got := resolved.Headers.Get("X-Off"); got != "" {
		t.Errorf("disabled header = %q", got)
	}
	if len(resolved.HeaderEntries) != 2 || resolved.HeaderEntries[0].Value != "one two" || resolved.HeaderEntries[1].Value != "last" {
		t.Errorf("resolved header entries = %#v", resolved.HeaderEntries)
	}
}

func TestResolveOrderedEntriesReplaceMirroredURLKeys(t *testing.T) {
	req := &dsl.Request{
		Method: "GET",
		URL:    "https://api.example.test/search?source=raw&tag=a&tag=b",
		QueryEntries: []dsl.Entry{
			{Key: "tag", Value: "a"},
			{Key: "tag", Value: "b"},
		},
		Query: map[string]string{"tag": "b"},
	}
	resolved, err := Resolve(req, nil, NewScope(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://api.example.test/search?source=raw&tag=a&tag=b"; resolved.URL != want {
		t.Fatalf("ordered query duplicated URL values: got %q, want %q", resolved.URL, want)
	}

	req.QueryEntries = []dsl.Entry{{Key: "tag", Value: "b", Disabled: true}}
	resolved, err = Resolve(req, nil, NewScope(nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://api.example.test/search?source=raw"; resolved.URL != want {
		t.Fatalf("disabled ordered key remained in URL: got %q, want %q", resolved.URL, want)
	}
}

func TestResolveDisabledEntriesSuppressLegacyMapValues(t *testing.T) {
	resolved, err := Resolve(&dsl.Request{
		Method: "GET", URL: "https://api.example.test/search?base=one",
		Query:         map[string]string{"tag": "legacy-query"},
		Headers:       map[string]string{"X-Disabled": "legacy-header"},
		QueryEntries:  []dsl.Entry{{Key: "tag", Value: "disabled", Disabled: true}},
		HeaderEntries: []dsl.Entry{{Key: "X-Disabled", Value: "disabled", Disabled: true}},
	}, nil, NewScope(nil))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.URL != "https://api.example.test/search?base=one" {
		t.Errorf("disabled query leaked map value: %q", resolved.URL)
	}
	if got := resolved.Headers.Get("X-Disabled"); got != "" {
		t.Errorf("disabled header leaked map value: %q", got)
	}
}

func TestResolveBasicAndAPIKey(t *testing.T) {
	scope := NewScope()
	r, err := Resolve(&dsl.Request{
		Method: "GET", URL: "https://x.test",
		Auth: &dsl.Auth{Type: "basic", Username: "u", Password: "p"},
	}, nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Headers.Get("Authorization"); got != "Basic dTpw" {
		t.Errorf("basic = %q", got)
	}

	r, err = Resolve(&dsl.Request{
		Method: "GET", URL: "https://x.test/p",
		Auth: &dsl.Auth{Type: "apikey", Key: "key", Value: "v1", In: "query"},
	}, nil, scope)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "https://x.test/p?key=v1" {
		t.Errorf("apikey query = %q", r.URL)
	}
}

func TestResolveFormData(t *testing.T) {
	root := t.TempDir()
	filePath := root + "/doc.txt"
	if err := os.WriteFile(filePath, []byte("hello file"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := &dsl.Request{
		Method: "POST",
		URL:    "https://api.example.com/upload",
		Path:   root + "/upload.req.toml",
		Body: &dsl.Body{Type: "formdata", FormData: []dsl.FormField{
			{Key: "name", Value: "{{name}}", Type: "text"},
			{Key: "file", File: "doc.txt", Type: "file"},
			{Key: "disabled", Value: "no", Type: "text", Disabled: true},
		}},
	}
	r, err := Resolve(req, nil, NewScope(map[string]string{"name": "Ada"}))
	if err != nil {
		t.Fatal(err)
	}
	ct := r.Headers.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Fatalf("content-type = %q", ct)
	}
	body := string(r.Body)
	for _, want := range []string{`name="name"`, "Ada", `name="file"`, `filename="doc.txt"`, "hello file"} {
		if !strings.Contains(body, want) {
			t.Errorf("multipart body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "disabled") {
		t.Errorf("disabled field was included:\n%s", body)
	}
}

func TestResolveUnresolvedVariableFails(t *testing.T) {
	_, err := Resolve(&dsl.Request{Method: "GET", URL: "{{nope}}/x"}, nil, NewScope())
	if err == nil {
		t.Fatal("expected error")
	}
}
