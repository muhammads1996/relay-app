package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

func atoi64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func pathID(r *http.Request) (int64, error) {
	id, err := atoi64(r.PathValue("id"))
	if err != nil {
		return 0, fmt.Errorf("bad id %q", r.PathValue("id"))
	}
	return id, nil
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, limit))
}

func decode[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// requestMeta is the listing shape for the sidebar tree.
type requestMeta struct {
	ID       int64  `json:"id"`
	FolderID *int64 `json:"folderId"`
	Name     string `json:"name"`
	Method   string `json:"method"`
	URL      string `json:"url"`
}

type requestAPI struct {
	store.Request
	FileID      string `json:"fileId,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	ws, indexes := s.workspaceStateSnapshot()
	if ws != nil {
		s.scheduleWorkspaceRefresh()
		s.writeCanonicalState(w, ws, indexes)
		return
	}
	if s.WorkspaceRoot != "" {
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
		ws, indexes = s.workspaceStateSnapshot()
		if ws != nil {
			s.writeCanonicalState(w, ws, indexes)
			return
		}
	}
	cols, err := s.DB.Collections()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	type colState struct {
		store.Collection
		Folders       []store.Folder `json:"folders"`
		Requests      []requestMeta  `json:"requests"`
		ContentHash   string         `json:"contentHash,omitempty"`
		WorkspaceHash string         `json:"workspaceHash,omitempty"`
	}
	out := struct {
		Collections         []colState          `json:"collections"`
		Environments        []store.Environment `json:"environments"`
		Presets             []store.Preset      `json:"presets"`
		StorageMode         string              `json:"storageMode"`
		WorkspaceHash       string              `json:"workspaceHash,omitempty"`
		WorkspaceID         string              `json:"workspaceId"`
		WorkspaceGeneration uint64              `json:"workspaceGeneration"`
	}{Collections: []colState{}, Environments: []store.Environment{}, Presets: []store.Preset{}, StorageMode: "sqlite"}
	out.WorkspaceID, err = s.DB.Identity()
	if err != nil {
		httpError(w, 500, err)
		return
	}

	for _, c := range cols {
		cs := colState{Collection: c, Folders: []store.Folder{}, Requests: []requestMeta{}}
		folders, err := s.DB.Folders(c.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if folders != nil {
			cs.Folders = folders
		}
		reqs, err := s.DB.Requests(c.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		for _, q := range reqs {
			cs.Requests = append(cs.Requests, requestMeta{
				ID: q.ID, FolderID: q.FolderID,
				Name: q.Spec.Name, Method: q.Spec.Method, URL: q.Spec.URL,
			})
		}
		out.Collections = append(out.Collections, cs)
	}
	envs, err := s.DB.Environments()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if envs != nil {
		out.Environments = envs
	}
	presets, err := s.DB.Presets()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if presets != nil {
		out.Presets = presets
	}
	// Secret preset values never reach the browser.
	for pi := range out.Presets {
		for hi := range out.Presets[pi].Headers {
			if out.Presets[pi].Headers[hi].Secret {
				out.Presets[pi].Headers[hi].Value = ""
			}
		}
	}
	writeJSON(w, out)
}

func (s *Server) writeCanonicalState(w http.ResponseWriter, ws *workspacepkg.Workspace, indexes workspaceStateIndexes) {
	type folderAPI struct {
		store.Folder
		ContentHash string `json:"contentHash"`
	}
	type colState struct {
		store.Collection
		Folders       []folderAPI   `json:"folders"`
		Requests      []requestMeta `json:"requests"`
		ContentHash   string        `json:"contentHash,omitempty"`
		WorkspaceHash string        `json:"workspaceHash,omitempty"`
	}
	out := struct {
		Collections         []colState          `json:"collections"`
		Environments        []store.Environment `json:"environments"`
		Presets             []store.Preset      `json:"presets"`
		StorageMode         string              `json:"storageMode"`
		WorkspaceHash       string              `json:"workspaceHash"`
		WorkspaceID         string              `json:"workspaceId"`
		WorkspaceGeneration uint64              `json:"workspaceGeneration"`
	}{Collections: []colState{}, Environments: []store.Environment{}, Presets: []store.Preset{}, StorageMode: "files", WorkspaceHash: ws.Manifest.Hash, WorkspaceID: ws.Manifest.ID, WorkspaceGeneration: indexes.generation}
	folderIDs := map[string]int64{}
	foldersByCollection := make(map[string][]workspacepkg.Folder, len(ws.Collections))
	requestsByCollection := make(map[string][]workspacepkg.Request, len(ws.Collections))
	for _, f := range ws.Folders {
		foldersByCollection[f.CollectionID] = append(foldersByCollection[f.CollectionID], f)
	}
	for _, q := range ws.Requests {
		requestsByCollection[q.CollectionID] = append(requestsByCollection[q.CollectionID], q)
	}
	for _, c := range ws.Collections {
		collectionID, ok := indexes.collections[c.ID]
		if !ok {
			httpError(w, 500, fmt.Errorf("collection %s is not indexed", c.ID))
			return
		}
		cs := colState{Collection: store.Collection{ID: collectionID, Name: c.Name, Headers: c.Headers, Vars: c.Vars}, Folders: []folderAPI{}, Requests: []requestMeta{}, ContentHash: c.Hash, WorkspaceHash: ws.Manifest.Hash}
		for _, f := range foldersByCollection[c.ID] {
			folderID, ok := indexes.folders[f.ID]
			if !ok {
				httpError(w, 500, fmt.Errorf("folder %s is not indexed", f.ID))
				return
			}
			folderIDs[f.ID] = folderID
			rel, _ := filepath.Rel(filepath.Dir(c.Path), filepath.Dir(f.Path))
			cs.Folders = append(cs.Folders, folderAPI{Folder: store.Folder{ID: folderID, CollectionID: collectionID, Name: filepath.ToSlash(rel), Headers: f.Headers, Vars: f.Vars}, ContentHash: f.Hash})
		}
		for _, q := range requestsByCollection[c.ID] {
			var fid *int64
			if q.FolderID != "" {
				v := folderIDs[q.FolderID]
				fid = &v
			}
			ri, ok := indexes.requests[q.ID]
			if !ok {
				httpError(w, 500, fmt.Errorf("request %s is not indexed", q.ID))
				return
			}
			cs.Requests = append(cs.Requests, requestMeta{ID: ri, FolderID: fid, Name: q.Name, Method: q.Method, URL: q.URL})
		}
		out.Collections = append(out.Collections, cs)
	}
	for _, e := range ws.Environments {
		if id, ok := indexes.environments[e.ID]; ok {
			out.Environments = append(out.Environments, store.Environment{ID: id, Name: e.Name, Vars: e.Vars, Secrets: e.Secrets})
		}
	}
	ps, err := s.DB.Presets()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	out.Presets = ps
	for pi := range out.Presets {
		for hi := range out.Presets[pi].Headers {
			if out.Presets[pi].Headers[hi].Secret {
				out.Presets[pi].Headers[hi].Value = ""
			}
		}
	}
	writeJSON(w, out)
}

// --- collections ---

func (s *Server) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalCollectionCreate(w, r)
		return
	}
	c, err := decode[store.Collection](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if c.Name == "" {
		httpError(w, 422, fmt.Errorf("collection needs a name"))
		return
	}
	if err := s.DB.CreateCollection(c); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) handleCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalCollectionUpdate(w, r)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	c, err := decode[store.Collection](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	c.ID = id
	if err := s.DB.UpdateCollection(c); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalCollectionDelete(w, r)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteCollection(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- folders ---

func (s *Server) handleFolderCreate(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalFolderCreate(w, r)
		return
	}
	f, err := decode[store.Folder](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if f.Name == "" || f.CollectionID == 0 {
		httpError(w, 422, fmt.Errorf("folder needs a name and collectionId"))
		return
	}
	if err := s.DB.CreateFolder(f); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) handleFolderUpdate(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalFolderUpdate(w, r)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	f, err := decode[store.Folder](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	f.ID = id
	if err := s.DB.UpdateFolder(f); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) handleFolderDelete(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalFolderDelete(w, r)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteFolder(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- requests ---

func (s *Server) handleRequestCreate(w http.ResponseWriter, r *http.Request) {
	req, err := decode[store.Request](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if req.CollectionID == 0 {
		httpError(w, 422, fmt.Errorf("request needs a collectionId"))
		return
	}
	if req.Spec == nil {
		req.Spec = &dsl.Request{Name: "Untitled", Method: "GET", URL: "{{baseUrl}}/"}
	}
	if !s.isVersioned() {
		if err := s.DB.CreateRequest(req); err != nil {
			httpError(w, 422, err)
			return
		}
		hash, err := s.DB.RequestContentHash(req.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, requestAPI{Request: *req, ContentHash: hash})
		return
	}
	if err := s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	collection, err := s.DB.WorkspaceFileBySQLiteID("collection", req.CollectionID)
	if err != nil {
		httpError(w, 422, fmt.Errorf("collection is not indexed from workspace files"))
		return
	}
	folderStable := ""
	if req.FolderID != nil {
		folder, err := s.DB.WorkspaceFileBySQLiteID("folder", *req.FolderID)
		if err != nil {
			httpError(w, 422, fmt.Errorf("folder is not indexed from workspace files"))
			return
		}
		folderStable = folder.FileID
	}
	if err = s.DB.CreateRequest(req); err != nil {
		httpError(w, 422, err)
		return
	}
	stableID := workspacepkg.NewID()
	path, err := s.currentWorkspace().NewRequestPath(collection.FileID, folderStable, req.Spec.Name)
	if err != nil {
		_ = s.DB.DeleteRequest(req.ID)
		httpError(w, 422, err)
		return
	}
	fileReq := workspacepkg.Request{ID: stableID, CollectionID: collection.FileID, FolderID: folderStable, Path: path, Request: *req.Spec}
	hash, err := s.currentWorkspace().SaveRequest(fileReq, "")
	if err != nil {
		_ = s.DB.DeleteRequest(req.ID)
		httpError(w, 422, err)
		return
	}
	if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "request", FileID: stableID, SQLiteID: req.ID, Path: path, Hash: hash}); err != nil {
		httpError(w, 500, err)
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, requestAPI{Request: *req, FileID: stableID, ContentHash: hash})
}

func (s *Server) handleRequestGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if s.isVersioned() {
		s.scheduleWorkspaceRefresh()
	}
	req, err := s.DB.Request(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", id))
		return
	}
	if file, item, collectionID, folderID, ok := s.canonicalRequestWithLocation(id); ok {
		if !workspacepkg.SafePath(s.WorkspaceRoot, file.Path) {
			httpError(w, http.StatusConflict, fmt.Errorf("request file path changed; refresh files"))
			return
		}
		data, err := os.ReadFile(file.Path)
		if err != nil {
			httpError(w, http.StatusServiceUnavailable, fmt.Errorf("request file is unavailable; refresh files"))
			return
		}
		var identity struct {
			ID string `toml:"id"`
		}
		if _, err = toml.Decode(string(data), &identity); err != nil || identity.ID != file.ID {
			httpError(w, http.StatusConflict, fmt.Errorf("request file identity changed; refresh files"))
			return
		}
		spec, err := dsl.ParseRequest(file.Path, data)
		if err != nil {
			httpError(w, http.StatusUnprocessableEntity, fmt.Errorf("request file is invalid; refresh files"))
			return
		}
		req.Spec = spec
		req.CollectionID = collectionID
		req.FolderID = folderID
		item.Hash = workspacepkg.Hash(data)
		writeJSON(w, requestAPI{Request: *req, FileID: file.ID, ContentHash: item.Hash})
		return
	}
	if s.isVersioned() {
		httpError(w, 404, fmt.Errorf("request %d is no longer present in workspace files", id))
		return
	}
	hash, err := s.DB.RequestContentHash(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, requestAPI{Request: *req, ContentHash: hash})
}

func (s *Server) handleRequestUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var input struct {
		store.Request
		ContentHash string `json:"contentHash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		httpError(w, 400, err)
		return
	}
	req := &input.Request
	existing, err := s.DB.Request(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", id))
		return
	}
	req.ID = id
	if req.CollectionID == 0 {
		req.CollectionID = existing.CollectionID
	}
	if s.isVersioned() {
		file, item, ok := s.canonicalRequest(id)
		if !ok {
			httpError(w, 404, fmt.Errorf("request is not indexed from workspace files"))
			return
		}
		if input.ContentHash == "" {
			httpError(w, 409, fmt.Errorf("contentHash is required; reload the definition before saving"))
			return
		}
		moving := req.CollectionID != existing.CollectionID || !sameInt64(req.FolderID, existing.FolderID)
		if input.ContentHash != item.Hash {
			httpError(w, 409, fmt.Errorf("request changed on disk; keep this draft and compare with the current file"))
			return
		}
		if req.Spec == nil {
			httpError(w, 422, fmt.Errorf("request definition is required"))
			return
		}
		if moving {
			if !bytes.Equal(dsl.Marshal(req.Spec), dsl.Marshal(&file.Request)) {
				httpError(w, 422, fmt.Errorf("move the request without editing its definition, then save edits separately"))
				return
			}
			collectionFile, err := s.DB.WorkspaceFileBySQLiteID("collection", req.CollectionID)
			if err != nil {
				httpError(w, 422, fmt.Errorf("target collection is not indexed from workspace files"))
				return
			}
			targetDir, err := s.currentWorkspace().RequestDirectory(collectionFile.FileID, "")
			if req.FolderID != nil {
				folderFile, e := s.DB.WorkspaceFileBySQLiteID("folder", *req.FolderID)
				if e != nil {
					httpError(w, 422, fmt.Errorf("target folder is not indexed from workspace files"))
					return
				}
				targetDir, err = s.currentWorkspace().RequestDirectory(collectionFile.FileID, folderFile.FileID)
			}
			if err != nil {
				httpError(w, 422, err)
				return
			}
			newPath, err := s.currentWorkspace().MoveRequest(file.ID, file.Path, targetDir, input.ContentHash)
			if err != nil {
				if errors.Is(err, workspacepkg.ErrConflict) {
					httpError(w, 409, err)
				} else {
					httpError(w, 422, err)
				}
				return
			}
			req.Spec = &file.Request
			req.CollectionID = collectionFile.SQLiteID
			if req.FolderID != nil {
				folderFile, _ := s.DB.WorkspaceFileBySQLiteID("folder", *req.FolderID)
				folderID := folderFile.SQLiteID
				req.FolderID = &folderID
			}
			if err = s.DB.UpdateRequest(req); err != nil {
				httpError(w, 500, fmt.Errorf("request moved to %s but SQLite index rebuild is required: %w", newPath, err))
				return
			}
			_ = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "request", FileID: file.ID, SQLiteID: id, Path: newPath, Hash: item.Hash})
			if err = s.refreshWorkspace(); err != nil {
				httpError(w, 500, err)
				return
			}
			fresh, _ := s.DB.WorkspaceFileByStableID("request", file.ID)
			writeJSON(w, requestAPI{Request: *req, FileID: file.ID, ContentHash: fresh.Hash})
			return
		}
		file.Request = *req.Spec
		newHash, err := s.currentWorkspace().SaveRequest(file, input.ContentHash)
		if err != nil {
			if errors.Is(err, workspacepkg.ErrConflict) {
				httpError(w, 409, fmt.Errorf("request changed on disk; keep this draft and compare with the current file"))
				return
			}
			httpError(w, 422, err)
			return
		}
		if err = s.DB.UpdateRequest(req); err != nil {
			httpError(w, 500, fmt.Errorf("file saved but SQLite index update failed: %w", err))
			return
		}
		if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "request", FileID: file.ID, SQLiteID: id, Path: file.Path, Hash: newHash}); err != nil {
			httpError(w, 500, fmt.Errorf("file saved but SQLite index update failed: %w", err))
			return
		}
		if err = s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, requestAPI{Request: *req, FileID: file.ID, ContentHash: newHash})
		return
	}
	var updateErr error
	if input.ContentHash != "" {
		updateErr = s.DB.UpdateRequestIfHash(req, input.ContentHash)
	} else {
		updateErr = s.DB.UpdateRequest(req)
	}
	if err := updateErr; err != nil {
		if errors.Is(err, store.ErrRequestNotFound) {
			httpError(w, 404, fmt.Errorf("request %d not found", id))
			return
		}
		if errors.Is(err, store.ErrRequestConflict) {
			httpError(w, 409, fmt.Errorf("request changed since it was loaded; keep your draft and compare with the current definition"))
			return
		}
		httpError(w, 422, err)
		return
	}
	hash, err := s.DB.RequestContentHash(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, requestAPI{Request: *req, ContentHash: hash})
}

func (s *Server) handleRequestDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	draftIdentity, err := s.requestDraftIdentity(id)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	if s.isVersioned() {
		file, item, ok := s.canonicalRequest(id)
		if !ok {
			httpError(w, 404, fmt.Errorf("request is not indexed from workspace files"))
			return
		}
		if _, err := s.currentWorkspace().TrashRequest(file.ID, file.Path, item.Hash); err != nil {
			if errors.Is(err, workspacepkg.ErrConflict) {
				httpError(w, 409, fmt.Errorf("request changed on disk; reload before deleting"))
				return
			}
			httpError(w, 422, err)
			return
		}
		if err := s.DB.DeleteRequest(id); err != nil {
			httpError(w, 500, err)
			return
		}
		if err := s.DB.DeleteRequestDraftsForRequest(draftIdentity.workspace, draftIdentity.request); err != nil {
			httpError(w, 500, fmt.Errorf("request deleted but local recovery cleanup failed"))
			return
		}
		_ = s.DB.DeleteWorkspaceFile("request", id)
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if err := s.DB.DeleteRequest(id); err != nil {
		httpError(w, 500, err)
		return
	}
	if err := s.DB.DeleteRequestDraftsForRequest(draftIdentity.workspace, draftIdentity.request); err != nil {
		httpError(w, 500, fmt.Errorf("request deleted but local recovery cleanup failed"))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRequestStats(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	stats, err := s.DB.RequestStats(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, stats)
}

// --- environments ---

func (s *Server) handleEnvList(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
		out := []environmentAPI{}
		for _, e := range s.currentWorkspace().Environments {
			stored, err := s.DB.Environment(e.Name)
			if err != nil {
				httpError(w, 500, err)
				return
			}
			_, item, _ := s.canonicalEnv(e.Name)
			out = append(out, environmentAPI{Environment: *stored, FileID: e.ID, ContentHash: item.Hash})
		}
		writeJSON(w, out)
		return
	}
	envs, err := s.DB.Environments()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if envs == nil {
		envs = []store.Environment{}
	}
	writeJSON(w, envs)
}

func (s *Server) handleEnvPut(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		s.handleCanonicalEnvPut(w, r)
		return
	}
	e, err := decode[store.Environment](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	e.Name = r.PathValue("name")
	if err := s.DB.UpsertEnvironment(e); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, e)
}

func (s *Server) handleEnvDelete(w http.ResponseWriter, r *http.Request) {
	if s.isVersioned() {
		name := r.PathValue("name")
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
		if err := s.trashCanonicalEnvironment(name); err != nil {
			if errors.Is(err, workspacepkg.ErrConflict) {
				httpError(w, 409, fmt.Errorf("environment changed on disk; reload before deleting"))
				return
			}
			httpError(w, 422, err)
			return
		}
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if err := s.DB.DeleteEnvironment(r.PathValue("name")); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- presets ---

func (s *Server) handlePresetList(w http.ResponseWriter, r *http.Request) {
	ps, err := s.DB.Presets()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if ps == nil {
		ps = []store.Preset{}
	}
	for pi := range ps {
		for hi := range ps[pi].Headers {
			if ps[pi].Headers[hi].Secret {
				ps[pi].Headers[hi].Value = ""
			}
		}
	}
	writeJSON(w, ps)
}

func (s *Server) handlePresetCreate(w http.ResponseWriter, r *http.Request) {
	p, err := decode[store.Preset](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.CreatePreset(p); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handlePresetUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	p, err := decode[store.Preset](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	p.ID = id
	// Empty values on secret headers mean "keep the stored value" so the
	// masked UI round-trip doesn't wipe secrets.
	if existing, err := s.DB.Presets(); err == nil {
		for _, ex := range existing {
			if ex.ID != id {
				continue
			}
			for hi := range p.Headers {
				if p.Headers[hi].Secret && p.Headers[hi].Value == "" {
					for _, eh := range ex.Headers {
						if eh.Key == p.Headers[hi].Key {
							p.Headers[hi].Value = eh.Value
						}
					}
				}
			}
		}
	}
	if err := s.DB.UpdatePreset(p); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handlePresetDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeletePreset(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- history ---

func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.DB.History(limit)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if list == nil {
		list = []store.HistoryEntry{}
	}
	writeJSON(w, list)
}

func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	h, err := s.DB.HistoryEntry(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("history entry %d not found", id))
		return
	}
	writeJSON(w, map[string]any{
		"entry": h,
		"body":  string(h.RespBody),
	})
}
