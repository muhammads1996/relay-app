package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const postmanWarningFixture = `{"info":{"name":"Warnings"},"event":[{"listen":"prerequest","script":{"type":"text/javascript","exec":["pm.sendRequest('https://example.test');"]}}],"item":[{"name":"Ping","request":{"url":"https://example.test/ping"}}]}`

func TestPostmanPreviewReportsWarningsWithoutMutation(t *testing.T) {
	s, _ := newServer(t)
	before, err := s.DB.Collections()
	if err != nil {
		t.Fatal(err)
	}
	rec, doc := call(t, s, "POST", "/api/import/postman?preview=1", postmanWarningFixture)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	if got := doc["requests"]; got != float64(1) {
		t.Fatalf("requests = %v", got)
	}
	warnings, ok := doc["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v", doc["warnings"])
	}
	warning := warnings[0].(map[string]any)
	if warning["location"] != "collection: Warnings" || !strings.Contains(warning["message"].(string), "pm.sendRequest") {
		t.Fatalf("warning = %#v", warning)
	}
	if _, ok := doc["collectionId"]; ok {
		t.Fatal("preview returned a committed collection ID")
	}
	if _, ok := doc["sourceId"]; ok {
		t.Fatal("preview retained a source backup")
	}
	after, err := s.DB.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("preview mutated collection count: before=%d after=%d", len(before), len(after))
	}
}

func TestPostmanCommitRetainsAndServesOriginalSource(t *testing.T) {
	s, _ := newServer(t)
	rec, doc := call(t, s, "POST", "/api/import/postman", postmanWarningFixture)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	if doc["requests"] != float64(1) || doc["collectionId"] == nil || doc["sourceId"] == nil {
		t.Fatalf("commit response = %#v", doc)
	}
	if warnings, ok := doc["warnings"].([]any); !ok || len(warnings) != 1 {
		t.Fatalf("commit warnings = %#v", doc["warnings"])
	}
	sourceID := doc["sourceId"].(string)
	req := httptest.NewRequest(http.MethodGet, "/api/import/postman/source/"+sourceID, nil)
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("source download: %d %s", out.Code, out.Body.String())
	}
	if out.Header().Get("Content-Type") != "application/json; charset=utf-8" || !strings.Contains(out.Header().Get("Content-Disposition"), "postman-original.json") {
		t.Fatalf("download headers = %v", out.Header())
	}
	if out.Body.String() != postmanWarningFixture {
		t.Fatal("retrieved source differs from original upload")
	}
	collections, err := s.DB.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(collections) != 2 {
		t.Fatalf("committed collections=%d, want 2", len(collections))
	}
}

func TestPostmanSourceUnknownIDIsNotFound(t *testing.T) {
	s, _ := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/import/postman/source/missing", nil)
	out := httptest.NewRecorder()
	prepareLocalRequest(req)
	s.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusNotFound {
		t.Fatalf("unknown source status=%d body=%s", out.Code, out.Body.String())
	}
}

func TestPostmanFailedCommitKeepsBackupAndLeavesNoCollection(t *testing.T) {
	s, _ := newServer(t)
	before, err := s.DB.Collections()
	if err != nil {
		t.Fatal(err)
	}
	// The valid root request is traversed before the malformed nested one.
	source := `{"info":{"name":"Broken import"},"item":[{"name":"Valid","request":{"url":"https://example.test/ok"}},{"name":"outer","item":[{"name":"inner","item":[{"name":"Broken","request":{"url":"https://example.test/broken"}}]}]}]}`
	// Make the second request's generated DSL invalid after conversion by
	// removing its required URL from the source. This still leaves one valid
	// request earlier in traversal order.
	source = strings.Replace(source, `"url":"https://example.test/broken"`, `"url":""`, 1)
	rec, doc := call(t, s, "POST", "/api/import/postman", source)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	sourceID, ok := doc["sourceId"].(string)
	if !ok || sourceID == "" {
		t.Fatalf("failed commit did not return recoverable source ID: %#v", doc)
	}
	backup := httptest.NewRecorder()
	sourceReq := prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/import/postman/source/"+sourceID, nil))
	s.Handler().ServeHTTP(backup, sourceReq)
	if backup.Code != http.StatusOK || backup.Body.String() != source {
		t.Fatalf("failed import backup status=%d body=%q", backup.Code, backup.Body.String())
	}
	after, err := s.DB.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed seed left a collection: before=%d after=%d", len(before), len(after))
	}
}
