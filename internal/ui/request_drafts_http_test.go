package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/store"
)

func TestSQLiteDraftSurvivesSaveFailureAndRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	col := &store.Collection{Name: "Drafts"}
	if err = db.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	req := &store.Request{CollectionID: col.ID, Spec: &dsl.Request{Name: "Secret request", Method: "GET", URL: "https://old.example.test", Headers: map[string]string{"Authorization": "Bearer recover-me"}}}
	if err = db.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: db, Engine: engine.NewOptions()}
	getRequest := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRequest, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(req.ID), nil)))
	var current requestAPI
	if getRequest.Code != 200 || json.Unmarshal(getRequest.Body.Bytes(), &current) != nil || current.ContentHash == "" {
		t.Fatalf("request GET status=%d body=%s", getRequest.Code, getRequest.Body)
	}
	draftReq := *req
	draftReq.Spec = &dsl.Request{Name: "Secret request", Method: "GET", URL: "https://draft.example.test", Headers: map[string]string{"Authorization": "Bearer local-draft-secret"}}
	writer := newTestDraftWriter(t)
	putDraft := func(method string, revision, edit int64, value store.Request) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"draft": value, "baseContentHash": current.ContentHash, "writerId": writer, "expectedRevision": revision, "editRevision": edit})
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, prepareLocalRequest(httptest.NewRequest(method, "/api/requests/"+itoa(req.ID)+"/draft", bytes.NewReader(body))))
		return w
	}
	saved := putDraft(http.MethodPut, 0, 1, draftReq)
	if saved.Code != 200 {
		t.Fatalf("draft PUT status=%d body=%s", saved.Code, saved.Body)
	}
	var first struct {
		Revision int64 `json:"revision"`
	}
	if json.Unmarshal(saved.Body.Bytes(), &first) != nil || first.Revision == 0 {
		t.Fatalf("draft PUT response=%s", saved.Body)
	}
	changed := *req
	changed.Spec = &dsl.Request{Name: "Secret request", Method: "GET", URL: "https://external.example.test"}
	if err = db.UpdateRequest(&changed); err != nil {
		t.Fatal(err)
	}
	staleBody, _ := json.Marshal(map[string]any{"collectionId": col.ID, "contentHash": current.ContentHash, "spec": draftReq.Spec})
	failedSave := httptest.NewRecorder()
	srv.Handler().ServeHTTP(failedSave, prepareLocalRequest(httptest.NewRequest(http.MethodPut, "/api/requests/"+itoa(req.ID), bytes.NewReader(staleBody))))
	if failedSave.Code != http.StatusConflict || strings.Contains(failedSave.Body.String(), "local-draft-secret") {
		t.Fatalf("failed save status=%d body=%s", failedSave.Code, failedSave.Body)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv = &Server{DB: db, Engine: engine.NewOptions()}
	draftGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(draftGet, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(req.ID)+"/draft", nil)))
	var restored struct {
		Exists   bool          `json:"exists"`
		Draft    store.Request `json:"draft"`
		Base     string        `json:"baseContentHash"`
		Revision int64         `json:"revision"`
	}
	if draftGet.Code != 200 || json.Unmarshal(draftGet.Body.Bytes(), &restored) != nil || !restored.Exists || restored.Draft.Spec.URL != "https://draft.example.test" || restored.Base != current.ContentHash {
		t.Fatalf("restored draft status=%d body=%s", draftGet.Code, draftGet.Body)
	}
	beacon := putDraft(http.MethodPost, restored.Revision, 2, draftReq)
	if beacon.Code != 200 {
		t.Fatalf("pagehide POST status=%d body=%s", beacon.Code, beacon.Body)
	}
	staleDelete := httptest.NewRecorder()
	srv.Handler().ServeHTTP(staleDelete, prepareLocalRequest(httptest.NewRequest(http.MethodDelete, "/api/requests/"+itoa(req.ID)+"/draft", strings.NewReader(`{"revision":1}`))))
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("stale draft cleanup status=%d body=%s", staleDelete.Code, staleDelete.Body)
	}
	latestDraft := httptest.NewRecorder()
	srv.Handler().ServeHTTP(latestDraft, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(req.ID)+"/draft", nil)))
	var retained struct {
		Revision int64 `json:"revision"`
	}
	if json.Unmarshal(latestDraft.Body.Bytes(), &retained) != nil || retained.Revision <= restored.Revision {
		t.Fatalf("newer draft was lost after stale cleanup: %s", latestDraft.Body)
	}
	deleteBody, _ := json.Marshal(map[string]int64{"revision": retained.Revision})
	deleteLatest := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteLatest, prepareLocalRequest(httptest.NewRequest(http.MethodDelete, "/api/requests/"+itoa(req.ID)+"/draft", bytes.NewReader(deleteBody))))
	if deleteLatest.Code != 200 {
		t.Fatalf("matching cleanup status=%d body=%s", deleteLatest.Code, deleteLatest.Body)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFileDraftStaleBaseIsKeptUntilExplicitDelete(t *testing.T) {
	root := t.TempDir()
	cid := "00000000-0000-4000-8000-000000000011"
	rid := "00000000-0000-4000-8000-000000000012"
	wid := "00000000-0000-4000-8000-000000000013"
	collectionDir := filepath.Join(root, "collections", "demo--"+cid)
	if err := os.MkdirAll(collectionDir, 0700); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(collectionDir, "01-read.req.toml")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "workspace.toml"), "schema_version = 2\nworkspace_id = \""+wid+"\"\ncollections = [\""+cid+"\"]\n")
	write(filepath.Join(collectionDir, "collection.toml"), "id = \""+cid+"\"\nname = \"Demo\"\n")
	write(requestPath, "id = \""+rid+"\"\nname = \"Read\"\nmethod = \"GET\"\nurl = \"https://old.example.test\"\n")
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err = srv.Prepare(); err != nil {
		t.Fatal(err)
	}
	indexed, err := db.Requests(1)
	if err != nil || len(indexed) != 1 {
		t.Fatalf("requests=%v err=%v", indexed, err)
	}
	id := indexed[0].ID
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(id), nil)))
	var latest requestAPI
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &latest) != nil {
		t.Fatalf("request GET status=%d body=%s", get.Code, get.Body)
	}
	writer := newTestDraftWriter(t)
	draft := map[string]any{"id": id, "collectionId": latest.CollectionID, "spec": dsl.Request{Name: "Read", Method: "GET", URL: "https://draft.example.test"}, "contentHash": latest.ContentHash}
	body, _ := json.Marshal(map[string]any{"draft": draft, "baseContentHash": latest.ContentHash, "writerId": writer, "expectedRevision": 0, "editRevision": 1})
	put := httptest.NewRecorder()
	srv.Handler().ServeHTTP(put, prepareLocalRequest(httptest.NewRequest(http.MethodPut, "/api/requests/"+itoa(id)+"/draft", bytes.NewReader(body))))
	if put.Code != 200 {
		t.Fatalf("draft PUT status=%d body=%s", put.Code, put.Body)
	}
	write(requestPath, "id = \""+rid+"\"\nname = \"Read changed outside Relay\"\nmethod = \"GET\"\nurl = \"https://new.example.test\"\n")
	staleSave, _ := json.Marshal(map[string]any{"collectionId": latest.CollectionID, "contentHash": latest.ContentHash, "spec": dsl.Request{Name: "Read", Method: "GET", URL: "https://draft.example.test"}})
	conflict := httptest.NewRecorder()
	srv.Handler().ServeHTTP(conflict, prepareLocalRequest(httptest.NewRequest(http.MethodPut, "/api/requests/"+itoa(id), bytes.NewReader(staleSave))))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("stale file save status=%d body=%s", conflict.Code, conflict.Body)
	}
	draftGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(draftGet, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(id)+"/draft", nil)))
	if draftGet.Code != 200 || !strings.Contains(draftGet.Body.String(), "https://draft.example.test") {
		t.Fatalf("stale draft was lost: status=%d body=%s", draftGet.Code, draftGet.Body)
	}
}

func newTestDraftWriter(t *testing.T) string {
	t.Helper()
	return "00000000-0000-4000-8000-000000000099"
}
