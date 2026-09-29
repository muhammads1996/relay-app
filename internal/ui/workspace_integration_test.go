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
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

func TestCanonicalRequestEditConflictAndFileSource(t *testing.T) {
	root := t.TempDir()
	cid, rid := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	collectionDir := filepath.Join(root, "collections", "demo--"+cid)
	if err := os.MkdirAll(collectionDir, 0755); err != nil {
		t.Fatal(err)
	}
	put := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(root, "workspace.toml"), "schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = [\""+cid+"\"]\n")
	put(filepath.Join(collectionDir, "collection.toml"), "id = \""+cid+"\"\nname = \"Demo\"\n")
	requestPath := filepath.Join(collectionDir, "01-read.req.toml")
	put(requestPath, "id = \""+rid+"\"\nname = \"Read\"\nmethod = \"GET\"\nurl = \"https://old.example.test\"\n")
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err := srv.Prepare(); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.Requests(1)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("requests=%v err=%v", reqs, err)
	}
	id := reqs[0].ID
	get := httptest.NewRecorder()
	getReq := prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(id), nil))
	srv.Handler().ServeHTTP(get, getReq)
	if get.Code != 200 {
		t.Fatalf("GET status %d: %s", get.Code, get.Body)
	}
	var current requestAPI
	if err := json.Unmarshal(get.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.ContentHash == "" {
		t.Fatal("GET did not return contentHash")
	}
	putRequest := func(hash, target string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"collectionId": current.CollectionID, "folderId": current.FolderID, "contentHash": hash, "spec": dsl.Request{Name: "Read", Method: "GET", URL: target}})
		r := httptest.NewRequest(http.MethodPut, "/api/requests/"+itoa(id), bytes.NewReader(body))
		r.Host = "127.0.0.1:7717"
		r.RemoteAddr = "127.0.0.1:54321"
		r.Header.Set("Origin", "http://127.0.0.1:7717")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	conflict := putRequest("stale", "https://evil.example.test")
	if conflict.Code != 409 {
		t.Fatalf("stale PUT status %d: %s", conflict.Code, conflict.Body)
	}
	success := putRequest(current.ContentHash, "https://new.example.test")
	if success.Code != 200 {
		t.Fatalf("PUT status %d: %s", success.Code, success.Body)
	}
	loaded, err := dsl.LoadRequest(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.URL != "https://new.example.test" {
		t.Fatalf("canonical file URL %q", loaded.URL)
	}
	folderBody, _ := json.Marshal(store.Folder{CollectionID: current.CollectionID, Name: "smoke-folder"})
	folderReq := prepareLocalRequest(httptest.NewRequest(http.MethodPost, "/api/folders", bytes.NewReader(folderBody)))
	folderResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(folderResp, folderReq)
	if folderResp.Code != 200 {
		t.Fatalf("create folder status %d: %s", folderResp.Code, folderResp.Body)
	}
	var folder canonicalFolderAPI
	if err := json.Unmarshal(folderResp.Body.Bytes(), &folder); err != nil {
		t.Fatal(err)
	}
	getAfter := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getAfter, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(id), nil)))
	var latest requestAPI
	if err := json.Unmarshal(getAfter.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	moveBody, _ := json.Marshal(map[string]any{"collectionId": latest.CollectionID, "folderId": folder.ID, "contentHash": latest.ContentHash, "spec": latest.Spec})
	moveReq := httptest.NewRequest(http.MethodPut, "/api/requests/"+itoa(id), bytes.NewReader(moveBody))
	moveReq.Host = "127.0.0.1:7717"
	moveReq.RemoteAddr = "127.0.0.1:54321"
	moveReq.Header.Set("Origin", "http://127.0.0.1:7717")
	moveResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(moveResp, moveReq)
	if moveResp.Code != 200 {
		t.Fatalf("move request status %d: %s", moveResp.Code, moveResp.Body)
	}
	opened, err := workspacepkg.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Requests) != 1 || opened.Requests[0].FolderID != folder.FileID {
		t.Fatalf("workspace request folder link after move: %+v", opened.Requests)
	}
	if _, err := dsl.LoadRequest(opened.Requests[0].Path); err != nil {
		t.Fatalf("CLI-loadable moved request: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	restarted := &Server{DB: reopenedDB, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err = restarted.Prepare(); err != nil {
		t.Fatal(err)
	}
	restartedRequests, err := reopenedDB.Requests(current.CollectionID)
	if err != nil || len(restartedRequests) != 1 || restartedRequests[0].FolderID == nil || *restartedRequests[0].FolderID != folder.ID {
		t.Fatalf("SQLite index after restart: requests=%+v err=%v", restartedRequests, err)
	}
	folderPatchBody, _ := json.Marshal(map[string]any{"id": folder.ID, "collectionId": current.CollectionID, "name": "renamed-folder", "headers": map[string]string{"X-Folder": "yes"}, "contentHash": folder.ContentHash})
	folderPatch := prepareLocalRequest(httptest.NewRequest(http.MethodPatch, "/api/folders/"+itoa(folder.ID), bytes.NewReader(folderPatchBody)))
	folderPatchResp := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(folderPatchResp, folderPatch)
	if folderPatchResp.Code != 200 {
		t.Fatalf("rename folder status %d: %s", folderPatchResp.Code, folderPatchResp.Body)
	}
	if err = json.Unmarshal(folderPatchResp.Body.Bytes(), &folder); err != nil {
		t.Fatal(err)
	}
	if files, err := filepath.Glob(filepath.Join(root, "collections", "demo--*", "renamed-folder", "*.req.toml")); err != nil || len(files) != 1 {
		t.Fatalf("folder rename canonical request paths=%v err=%v", files, err)
	}
	folderDeleteBody, _ := json.Marshal(map[string]string{"contentHash": folder.ContentHash})
	folderDelete := prepareLocalRequest(httptest.NewRequest(http.MethodDelete, "/api/folders/"+itoa(folder.ID), bytes.NewReader(folderDeleteBody)))
	folderDeleteResp := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(folderDeleteResp, folderDelete)
	if folderDeleteResp.Code != 200 {
		t.Fatalf("delete folder status %d: %s", folderDeleteResp.Code, folderDeleteResp.Body)
	}
	if rows, err := reopenedDB.Requests(current.CollectionID); err != nil || len(rows) != 0 {
		t.Fatalf("request index after folder delete: %v err=%v", rows, err)
	}
	stateResp := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(stateResp, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/state", nil)))
	var state struct {
		Collections []struct {
			store.Collection
			ContentHash   string `json:"contentHash"`
			WorkspaceHash string `json:"workspaceHash"`
		} `json:"collections"`
	}
	if err = json.Unmarshal(stateResp.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	var original stateCollection
	for _, c := range state.Collections {
		if c.ID == current.CollectionID {
			original = stateCollection{Collection: c.Collection, ContentHash: c.ContentHash, WorkspaceHash: c.WorkspaceHash}
		}
	}
	if original.ID == 0 {
		t.Fatal("original collection missing from canonical state")
	}
	createCollectionBody, _ := json.Marshal(map[string]any{"name": "Auxiliary", "workspaceHash": original.WorkspaceHash})
	createCollectionReq := prepareLocalRequest(httptest.NewRequest(http.MethodPost, "/api/collections", bytes.NewReader(createCollectionBody)))
	createCollectionResp := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(createCollectionResp, createCollectionReq)
	if createCollectionResp.Code != 200 {
		t.Fatalf("create collection status %d: %s", createCollectionResp.Code, createCollectionResp.Body)
	}
	var auxiliary canonicalCollectionAPI
	if err = json.Unmarshal(createCollectionResp.Body.Bytes(), &auxiliary); err != nil {
		t.Fatal(err)
	}
	updateCollectionBody, _ := json.Marshal(map[string]any{"id": auxiliary.ID, "name": "Renamed Auxiliary", "contentHash": auxiliary.ContentHash})
	updateCollectionReq := prepareLocalRequest(httptest.NewRequest(http.MethodPatch, "/api/collections/"+itoa(auxiliary.ID), bytes.NewReader(updateCollectionBody)))
	updateCollectionResp := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(updateCollectionResp, updateCollectionReq)
	if updateCollectionResp.Code != 200 {
		t.Fatalf("rename collection status %d: %s", updateCollectionResp.Code, updateCollectionResp.Body)
	}
	if err = json.Unmarshal(updateCollectionResp.Body.Bytes(), &auxiliary); err != nil {
		t.Fatal(err)
	}
	deleteCollectionBody, _ := json.Marshal(map[string]any{"contentHash": auxiliary.ContentHash, "workspaceHash": auxiliary.WorkspaceHash})
	deleteCollectionReq := prepareLocalRequest(httptest.NewRequest(http.MethodDelete, "/api/collections/"+itoa(auxiliary.ID), bytes.NewReader(deleteCollectionBody)))
	deleteCollectionResp := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(deleteCollectionResp, deleteCollectionReq)
	if deleteCollectionResp.Code != 200 {
		t.Fatalf("delete collection status %d: %s", deleteCollectionResp.Code, deleteCollectionResp.Body)
	}
}

func TestExplicitRefreshDetectsSameSizePreservedTimestampAndBlocksStaleSave(t *testing.T) {
	root := t.TempDir()
	cid, rid := "00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000102"
	collectionDir := filepath.Join(root, "collections", "demo--"+cid)
	if err := os.MkdirAll(collectionDir, 0755); err != nil {
		t.Fatal(err)
	}
	put := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(root, "workspace.toml"), "schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000110\"\ncollections = [\""+cid+"\"]\n")
	put(filepath.Join(collectionDir, "collection.toml"), "id = \""+cid+"\"\nname = \"Demo\"\n")
	requestPath := filepath.Join(collectionDir, "read.req.toml")
	put(requestPath, "id = \""+rid+"\"\nname = \"Read\"\nmethod = \"GET\"\nurl = \"https://old.example.test\"\n")
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err := srv.Prepare(); err != nil {
		t.Fatal(err)
	}
	requests, err := db.Requests(1)
	if err != nil || len(requests) != 1 {
		t.Fatalf("indexed requests=%d err=%v", len(requests), err)
	}
	id := requests[0].ID
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(id), nil)))
	if get.Code != http.StatusOK {
		t.Fatalf("GET request status %d: %s", get.Code, get.Body)
	}
	var before requestAPI
	if err := json.Unmarshal(get.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(contents, []byte("https://old.example.test"), []byte("https://new.example.test"), 1)
	if len(changed) != len(contents) || bytes.Equal(changed, contents) {
		t.Fatal("test edit must change contents without changing file size")
	}
	if err := os.WriteFile(requestPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(requestPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterInfo.Size() != info.Size() || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatalf("edit did not preserve size and mtime: before=(%d,%s) after=(%d,%s)", info.Size(), info.ModTime(), afterInfo.Size(), afterInfo.ModTime())
	}
	unsyncedGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unsyncedGet, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/requests/"+itoa(id), nil)))
	if unsyncedGet.Code != http.StatusOK {
		t.Fatalf("GET before full refresh status %d: %s", unsyncedGet.Code, unsyncedGet.Body)
	}
	var currentDisk requestAPI
	if err := json.Unmarshal(unsyncedGet.Body.Bytes(), &currentDisk); err != nil {
		t.Fatal(err)
	}
	if currentDisk.Spec.URL != "https://new.example.test" || currentDisk.ContentHash != workspacepkg.Hash(changed) {
		t.Fatalf("GET did not parse and hash the same current file bytes: %+v", currentDisk)
	}
	refresh := httptest.NewRecorder()
	srv.Handler().ServeHTTP(refresh, prepareLocalRequest(httptest.NewRequest(http.MethodPost, "/api/workspace/refresh", nil)))
	if refresh.Code != http.StatusOK {
		t.Fatalf("explicit refresh status %d: %s", refresh.Code, refresh.Body)
	}
	var scan workspaceRefreshStatus
	if err := json.Unmarshal(refresh.Body.Bytes(), &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Generation < 2 || scan.Error != "" {
		t.Fatalf("refresh status did not publish changed workspace: %+v", scan)
	}
	staleSpec := *before.Spec
	staleSpec.URL = "https://evil.example.test"
	body, _ := json.Marshal(map[string]any{"collectionId": before.CollectionID, "folderId": before.FolderID, "contentHash": before.ContentHash, "spec": staleSpec})
	staleSave := httptest.NewRecorder()
	putRequest := prepareLocalRequest(httptest.NewRequest(http.MethodPut, "/api/requests/"+itoa(id), bytes.NewReader(body)))
	srv.Handler().ServeHTTP(staleSave, putRequest)
	if staleSave.Code != http.StatusConflict {
		t.Fatalf("stale save status %d, want 409: %s", staleSave.Code, staleSave.Body)
	}
	state := httptest.NewRecorder()
	srv.Handler().ServeHTTP(state, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/state", nil)))
	var snapshot struct {
		WorkspaceGeneration uint64 `json:"workspaceGeneration"`
		Collections         []struct {
			Requests []requestMeta `json:"requests"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkspaceGeneration != scan.Generation || len(snapshot.Collections) != 1 || snapshot.Collections[0].Requests[0].URL != "https://new.example.test" {
		t.Fatalf("state did not publish refreshed generation: %+v", snapshot)
	}
}

func TestEmptyV2WorkspaceCanCreateFirstCollection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte("schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err = srv.Prepare(); err != nil {
		t.Fatal(err)
	}
	state := httptest.NewRecorder()
	srv.Handler().ServeHTTP(state, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/state", nil)))
	var snapshot struct{ StorageMode, WorkspaceHash string }
	if err = json.Unmarshal(state.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.StorageMode != "files" || snapshot.WorkspaceHash == "" {
		t.Fatalf("empty v2 state lacks file revision: %+v", snapshot)
	}
	body, _ := json.Marshal(map[string]string{"name": "First", "workspaceHash": snapshot.WorkspaceHash})
	req := prepareLocalRequest(httptest.NewRequest(http.MethodPost, "/api/collections", bytes.NewReader(body)))
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != 200 {
		t.Fatalf("create first collection status %d: %s", resp.Code, resp.Body)
	}
	opened, err := workspacepkg.Open(root)
	if err != nil || len(opened.Collections) != 1 {
		t.Fatalf("workspace after first collection: %+v err=%v", opened, err)
	}
}

func TestCanonicalCurlImportPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	cid := "00000000-0000-4000-8000-000000000001"
	collectionDir := filepath.Join(root, "collections", "demo--"+cid)
	if err := os.MkdirAll(collectionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte("schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = [\""+cid+"\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collectionDir, "collection.toml"), []byte("id = \""+cid+"\"\nname = \"Demo\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "relay.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err = srv.Prepare(); err != nil {
		t.Fatal(err)
	}
	state := httptest.NewRecorder()
	srv.Handler().ServeHTTP(state, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/state", nil)))
	var stateBody struct {
		WorkspaceHash string `json:"workspaceHash"`
	}
	if err = json.Unmarshal(state.Body.Bytes(), &stateBody); err != nil || stateBody.WorkspaceHash == "" {
		t.Fatalf("state workspaceHash: body=%s err=%v", state.Body, err)
	}
	post := func(path, hash string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"collectionId": 1, "curl": "curl -X POST https://example.test/imported -H 'X-Trace: yes' -d '{\"ok\":true}'", "workspaceHash": hash})
		resp := httptest.NewRecorder()
		req := prepareLocalRequest(httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		srv.Handler().ServeHTTP(resp, req)
		return resp
	}
	preview := post("/api/import/curl?preview=1", stateBody.WorkspaceHash)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body)
	}
	rows, err := db.Requests(1)
	if err != nil || len(rows) != 0 {
		t.Fatalf("preview mutated request index: requests=%v err=%v", rows, err)
	}
	stale := post("/api/import/curl", "stale")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale import status=%d body=%s", stale.Code, stale.Body)
	}
	committed := post("/api/import/curl", stateBody.WorkspaceHash)
	if committed.Code != http.StatusOK {
		t.Fatalf("commit status=%d body=%s", committed.Code, committed.Body)
	}
	var response requestAPI
	if err = json.Unmarshal(committed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.FileID == "" || response.ContentHash == "" || response.Spec == nil || response.Spec.Method != "POST" {
		t.Fatalf("import response = %+v", response)
	}
	paths, err := filepath.Glob(filepath.Join(collectionDir, "*.req.toml"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("imported request files=%v err=%v", paths, err)
	}
	canonicalPath := paths[0]
	loaded, err := dsl.LoadRequest(canonicalPath)
	if err != nil || loaded.URL != "https://example.test/imported" || loaded.Method != "POST" {
		t.Fatalf("canonical request=%+v err=%v", loaded, err)
	}
	indexed, err := db.Requests(1)
	if err != nil || len(indexed) != 1 || indexed[0].ID != response.ID {
		t.Fatalf("indexed import=%+v err=%v", indexed, err)
	}
	duplicate := post("/api/import/curl", stateBody.WorkspaceHash)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate-name import status=%d body=%s", duplicate.Code, duplicate.Body)
	}
	var duplicateResponse requestAPI
	if err = json.Unmarshal(duplicate.Body.Bytes(), &duplicateResponse); err != nil {
		t.Fatal(err)
	}
	paths, err = filepath.Glob(filepath.Join(collectionDir, "*.req.toml"))
	if err != nil || len(paths) != 2 || duplicateResponse.FileID == response.FileID {
		t.Fatalf("duplicate import paths=%v response=%+v err=%v", paths, duplicateResponse, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := &Server{DB: reopened, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err = restarted.Prepare(); err != nil {
		t.Fatal(err)
	}
	indexed, err = reopened.Requests(1)
	if err != nil || len(indexed) != 2 || indexed[0].Spec.URL != "https://example.test/imported" || indexed[1].Spec.URL != "https://example.test/imported" {
		t.Fatalf("request after restart=%+v err=%v", indexed, err)
	}
	loadedWorkspace, err := workspacepkg.Open(root)
	if err != nil || len(loadedWorkspace.Requests) != 2 {
		t.Fatalf("CLI workspace load requests=%+v err=%v", loadedWorkspace.Requests, err)
	}
}

func TestCanonicalBulkImportPreviewCommitAndRestart(t *testing.T) {
	fixtures := []struct {
		name  string
		path  string
		body  string
		count int
	}{
		{name: "postman", path: "/api/import/postman", body: `{"info":{"name":"Imported Postman"},"item":[{"name":"Get status","request":{"method":"GET","url":"https://example.test/status"}}]}`, count: 1},
		{name: "openapi", path: "/api/import/openapi", body: `{"openapi":"3.0.0","info":{"title":"Imported API","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/health":{"get":{"operationId":"health","summary":"Health"}}}}`, count: 1},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte("schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = []\n"), 0600); err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(root, "relay.db")
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
			if err = srv.Prepare(); err != nil {
				t.Fatal(err)
			}
			state := httptest.NewRecorder()
			srv.Handler().ServeHTTP(state, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/state", nil)))
			var workspaceState struct {
				WorkspaceHash string `json:"workspaceHash"`
			}
			if err = json.Unmarshal(state.Body.Bytes(), &workspaceState); err != nil || workspaceState.WorkspaceHash == "" {
				t.Fatalf("workspace state: %s err=%v", state.Body, err)
			}
			preview := httptest.NewRecorder()
			srv.Handler().ServeHTTP(preview, prepareLocalRequest(httptest.NewRequest(http.MethodPost, fixture.path+"?preview=1", strings.NewReader(fixture.body))))
			if preview.Code != http.StatusOK {
				t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body)
			}
			collections, err := db.Collections()
			if err != nil || len(collections) != 0 {
				t.Fatalf("preview mutated workspace index: collections=%+v err=%v", collections, err)
			}
			stale := httptest.NewRecorder()
			srv.Handler().ServeHTTP(stale, prepareLocalRequest(httptest.NewRequest(http.MethodPost, fixture.path+"?workspaceHash=stale", strings.NewReader(fixture.body))))
			if stale.Code != http.StatusConflict {
				t.Fatalf("stale import status=%d body=%s", stale.Code, stale.Body)
			}
			committed := httptest.NewRecorder()
			srv.Handler().ServeHTTP(committed, prepareLocalRequest(httptest.NewRequest(http.MethodPost, fixture.path+"?workspaceHash="+workspaceState.WorkspaceHash, strings.NewReader(fixture.body))))
			if committed.Code != http.StatusOK {
				t.Fatalf("commit status=%d body=%s", committed.Code, committed.Body)
			}
			var result struct {
				CollectionID int64  `json:"collectionId"`
				FileID       string `json:"fileId"`
				SourceID     string `json:"sourceId"`
				Requests     int    `json:"requests"`
			}
			if err = json.Unmarshal(committed.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.CollectionID == 0 || result.FileID == "" || result.SourceID == "" || result.Requests != fixture.count {
				t.Fatalf("commit result=%+v", result)
			}
			opened, err := workspacepkg.Open(root)
			if err != nil || len(opened.Collections) != 1 || len(opened.Requests) != fixture.count {
				t.Fatalf("canonical files collections=%+v requests=%+v err=%v", opened.Collections, opened.Requests, err)
			}
			for _, request := range opened.Requests {
				if _, err := dsl.LoadRequest(request.Path); err != nil {
					t.Fatalf("CLI request load %s: %v", request.Path, err)
				}
			}
			download := httptest.NewRecorder()
			srv.Handler().ServeHTTP(download, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/import/source/"+result.SourceID, nil)))
			if download.Code != http.StatusOK || download.Body.String() != fixture.body {
				t.Fatalf("source retrieval status=%d source=%q", download.Code, download.Body.String())
			}
			report := httptest.NewRecorder()
			srv.Handler().ServeHTTP(report, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/import/source/"+result.SourceID+"/report", nil)))
			var reportValue map[string]any
			if report.Code != http.StatusOK || json.Unmarshal(report.Body.Bytes(), &reportValue) != nil || reportValue["format"] != fixture.name {
				t.Fatalf("retained report status=%d body=%s", report.Code, report.Body)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedDB, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopenedDB.Close()
			restarted := &Server{DB: reopenedDB, Engine: engine.NewOptions(), WorkspaceRoot: root}
			if err = restarted.Prepare(); err != nil {
				t.Fatal(err)
			}
			rows, err := reopenedDB.Requests(result.CollectionID)
			if err != nil || len(rows) != fixture.count {
				t.Fatalf("SQLite after restart requests=%+v err=%v", rows, err)
			}
		})
	}
}

func TestCanonicalBulkImportFailureRetainsSourceWithoutPublishing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte("schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := &Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: root}
	if err = srv.Prepare(); err != nil {
		t.Fatal(err)
	}
	state := httptest.NewRecorder()
	srv.Handler().ServeHTTP(state, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/state", nil)))
	var stateValue struct {
		WorkspaceHash string `json:"workspaceHash"`
	}
	if err = json.Unmarshal(state.Body.Bytes(), &stateValue); err != nil {
		t.Fatal(err)
	}
	secretImport := `{"info":{"name":"Credential import"},"item":[{"name":"Secret","request":{"method":"GET","url":"https://example.test","header":[{"key":"Authorization","value":"Bearer literal-secret"}]}}]}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, prepareLocalRequest(httptest.NewRequest(http.MethodPost, "/api/import/postman?workspaceHash="+stateValue.WorkspaceHash, strings.NewReader(secretImport))))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe import status=%d body=%s", rec.Code, rec.Body)
	}
	var failure map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure["sourceId"] != nil {
		t.Fatalf("secret-bearing rejected source was retained: %#v", failure)
	}
	collections, err := db.Collections()
	if err != nil || len(collections) != 0 {
		t.Fatalf("failed import published collection: %+v err=%v", collections, err)
	}
	// A non-secret import that passes validation but fails to create its staging
	// directory must keep its source ID so the caller can recover the upload.
	if err = os.MkdirAll(filepath.Join(root, ".relay"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".relay", "imports"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	validImport := `{"info":{"name":"Recoverable import"},"item":[{"name":"Ping","request":{"method":"GET","url":"https://example.test/ping"}}]}`
	failed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(failed, prepareLocalRequest(httptest.NewRequest(http.MethodPost, "/api/import/postman?workspaceHash="+stateValue.WorkspaceHash, strings.NewReader(validImport))))
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected stage failure status=%d body=%s", failed.Code, failed.Body)
	}
	var failedBody struct {
		SourceID string `json:"sourceId"`
	}
	if err = json.Unmarshal(failed.Body.Bytes(), &failedBody); err != nil || failedBody.SourceID == "" {
		t.Fatalf("non-secret failed import lacks recovery source ID: body=%s err=%v", failed.Body, err)
	}
	recoveredSource := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recoveredSource, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/import/source/"+failedBody.SourceID, nil)))
	if recoveredSource.Code != http.StatusOK || recoveredSource.Body.String() != validImport {
		t.Fatalf("recoverable source status=%d body=%q", recoveredSource.Code, recoveredSource.Body.String())
	}
	recoveredReport := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recoveredReport, prepareLocalRequest(httptest.NewRequest(http.MethodGet, "/api/import/source/"+failedBody.SourceID+"/report", nil)))
	var retainedReport map[string]any
	if recoveredReport.Code != http.StatusOK || json.Unmarshal(recoveredReport.Body.Bytes(), &retainedReport) != nil || retainedReport["format"] != "postman" {
		t.Fatalf("retained failure report status=%d body=%s", recoveredReport.Code, recoveredReport.Body)
	}
}

type stateCollection struct {
	store.Collection
	ContentHash, WorkspaceHash string
}
