package ui

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

// Prepare reconciles the rebuildable SQLite index from a marked v2 workspace.
// Unmarked legacy workspaces and legacy DB-only workspaces keep their old path.
func (s *Server) Prepare() error {
	if s.WorkspaceRoot == "" {
		return nil
	}
	return s.refreshWorkspace()
}

func (s *Server) refreshWorkspaceOnce() error {
	w, err := workspacepkg.Open(s.WorkspaceRoot)
	if err != nil {
		return err
	}
	if w.Legacy {
		s.workspaceMu.Lock()
		s.workspace = nil
		s.collectionIndexes = nil
		s.folderIndexes = nil
		s.requestIndexes = nil
		s.environmentIndexes = nil
		s.workspaceFingerprint = ""
		s.workspaceMu.Unlock()
		return nil
	}
	collections, err := s.DB.Collections()
	if err != nil {
		return err
	}
	collectionFileRows, err := s.DB.WorkspaceFiles("collection")
	if err != nil {
		return err
	}
	collectionFileByID := make(map[string]store.WorkspaceFile, len(collectionFileRows))
	for _, item := range collectionFileRows {
		collectionFileByID[item.FileID] = item
	}
	collectionByID := map[int64]store.Collection{}
	for _, c := range collections {
		collectionByID[c.ID] = c
	}
	collectionSQL := map[string]int64{}
	for i := range w.Collections {
		c := &w.Collections[i]
		id, ok := indexedIDFromCache("collection", c.ID, collectionByID, collectionFileByID)
		if err != nil {
			return err
		}
		value := store.Collection{Name: c.Name, Headers: c.Headers, Vars: c.Vars}
		if ok {
			value.ID = id
			if err = s.DB.UpdateCollection(&value); err != nil {
				return err
			}
		} else {
			if err = s.DB.CreateCollection(&value); err != nil {
				return err
			}
		}
		collectionSQL[c.ID] = value.ID
		if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "collection", FileID: c.ID, SQLiteID: value.ID, Path: c.Path, Hash: c.Hash}); err != nil {
			return err
		}
	}
	folderSQL := map[string]int64{}
	folderFileRows, err := s.DB.WorkspaceFiles("folder")
	if err != nil {
		return err
	}
	folderFileByID := make(map[string]store.WorkspaceFile, len(folderFileRows))
	for _, item := range folderFileRows {
		folderFileByID[item.FileID] = item
	}
	existingFolders := map[int64][]store.Folder{}
	for i := range w.Folders {
		f := &w.Folders[i]
		collectionID := collectionSQL[f.CollectionID]
		folders, loaded := existingFolders[collectionID]
		if !loaded {
			var err error
			folders, err = s.DB.Folders(collectionID)
			if err != nil {
				return err
			}
			existingFolders[collectionID] = folders
		}
		id, ok := indexedIDFromCache("folder", f.ID, folders, folderFileByID)
		collectionRoot := filepath.Dir(w.Collections[0].Path)
		for _, c := range w.Collections {
			if c.ID == f.CollectionID {
				collectionRoot = filepath.Dir(c.Path)
				break
			}
		}
		rel, err := filepath.Rel(collectionRoot, filepath.Dir(f.Path))
		if err != nil {
			return err
		}
		value := store.Folder{CollectionID: collectionID, Name: filepath.ToSlash(rel), Headers: f.Headers, Vars: f.Vars}
		if ok {
			value.ID = id
			if err = s.DB.UpdateFolder(&value); err != nil {
				return err
			}
		} else {
			if err = s.DB.CreateFolder(&value); err != nil {
				return err
			}
			existingFolders[collectionID] = append(existingFolders[collectionID], value)
		}
		folderSQL[f.ID] = value.ID
		if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "folder", FileID: f.ID, SQLiteID: value.ID, Path: f.Path}); err != nil {
			return err
		}
	}
	requestIndexes := map[int64]store.WorkspaceFile{}
	existingRequests := map[int64][]store.Request{}
	requestFileRows, err := s.DB.WorkspaceFiles("request")
	if err != nil {
		return err
	}
	requestFileByID := make(map[string]store.WorkspaceFile, len(requestFileRows))
	for _, item := range requestFileRows {
		requestFileByID[item.FileID] = item
	}
	requestChanges := make([]store.WorkspaceRequestIndex, 0, len(w.Requests))
	for i := range w.Requests {
		req := &w.Requests[i]
		cid := collectionSQL[req.CollectionID]
		var folderID *int64
		if req.FolderID != "" {
			id, ok := folderSQL[req.FolderID]
			if !ok {
				return fmt.Errorf("request %s references unknown folder %s", req.ID, req.FolderID)
			}
			folderID = &id
		}
		mapped, mappedExists := requestFileByID[req.ID]
		id, ok := mapped.SQLiteID, mappedExists
		if !ok {
			existing, loaded := existingRequests[cid]
			if !loaded {
				existing, err = s.DB.Requests(cid)
				if err != nil {
					return err
				}
				existingRequests[cid] = existing
			}
			id, ok = indexedIDFromCache("request", req.ID, existing, nil)
		}
		value := store.Request{CollectionID: cid, FolderID: folderID, Spec: &req.Request}
		unchanged := ok && mappedExists && mapped.Hash == req.Hash && mapped.Path == req.Path
		if ok {
			value.ID = id
		}
		item := store.WorkspaceFile{Kind: "request", FileID: req.ID, SQLiteID: value.ID, Path: req.Path, Hash: req.Hash}
		requestChanges = append(requestChanges, store.WorkspaceRequestIndex{Request: value, File: item, ExistingID: value.ID, Update: ok && !unchanged, BindFile: !unchanged})
	}
	requestIDs, err := s.DB.ReconcileWorkspaceRequests(requestChanges)
	if err != nil {
		return err
	}
	for i, change := range requestChanges {
		item := change.File
		item.SQLiteID = requestIDs[i]
		requestIndexes[item.SQLiteID] = item
	}
	envIndexes := map[string]store.WorkspaceFile{}
	for i := range w.Environments {
		e := &w.Environments[i]
		stored, err := s.DB.Environment(e.Name)
		if errors.Is(err, sql.ErrNoRows) {
			value := &store.Environment{Name: e.Name, Vars: e.Vars, Secrets: e.Secrets}
			if err = s.DB.UpsertEnvironment(value); err != nil {
				return err
			}
			stored, err = s.DB.Environment(e.Name)
		}
		if err != nil {
			return err
		}
		value := &store.Environment{ID: stored.ID, Name: e.Name, Vars: e.Vars, Secrets: e.Secrets}
		if err = s.DB.UpsertEnvironment(value); err != nil {
			return err
		}
		item := store.WorkspaceFile{Kind: "environment", FileID: e.ID, SQLiteID: stored.ID, Path: e.Path, Hash: e.Hash}
		if err = s.DB.BindWorkspaceFile(item); err != nil {
			return err
		}
		envIndexes[e.Name] = item
	}
	s.workspaceMu.Lock()
	s.workspace = w
	s.collectionIndexes = collectionSQL
	s.folderIndexes = folderSQL
	s.requestIndexes = requestIndexes
	s.environmentIndexes = envIndexes
	fingerprint := workspaceFingerprint(w)
	if fingerprint != s.workspaceFingerprint {
		s.workspaceGeneration++
		s.workspaceFingerprint = fingerprint
		s.workspaceScanMu.Lock()
		s.workspaceScan.Generation = s.workspaceGeneration
		s.workspaceScanMu.Unlock()
	}
	s.workspaceMu.Unlock()
	return nil
}

func indexedIDFromCache(kind, fileID string, rows any, indexed map[string]store.WorkspaceFile) (int64, bool) {
	if item, ok := indexed[fileID]; ok {
		return item.SQLiteID, true
	}
	oldID := int64(0)
	switch list := rows.(type) {
	case map[int64]store.Collection:
		for id := range list {
			if workspacepkg.LegacyID(kind, id) == fileID {
				oldID = id
				break
			}
		}
	case []store.Folder:
		for _, v := range list {
			if workspacepkg.LegacyID(kind, v.ID) == fileID {
				oldID = v.ID
				break
			}
		}
	case []store.Request:
		for _, v := range list {
			if workspacepkg.LegacyID(kind, v.ID) == fileID {
				oldID = v.ID
				break
			}
		}
	}
	if oldID > 0 {
		return oldID, true
	}
	return 0, false
}

func (s *Server) canonicalRequest(id int64) (workspacepkg.Request, store.WorkspaceFile, bool) {
	r, item, _, _, ok := s.canonicalRequestWithLocation(id)
	return r, item, ok
}

func (s *Server) canonicalRequestWithLocation(id int64) (workspacepkg.Request, store.WorkspaceFile, int64, *int64, bool) {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	if s.workspace == nil {
		return workspacepkg.Request{}, store.WorkspaceFile{}, 0, nil, false
	}
	item, ok := s.requestIndexes[id]
	if !ok {
		return workspacepkg.Request{}, store.WorkspaceFile{}, 0, nil, false
	}
	for _, r := range s.workspace.Requests {
		if r.ID == item.FileID {
			collectionID, ok := s.collectionIndexes[r.CollectionID]
			if !ok {
				return workspacepkg.Request{}, item, 0, nil, false
			}
			var folderID *int64
			if r.FolderID != "" {
				value, ok := s.folderIndexes[r.FolderID]
				if !ok {
					return workspacepkg.Request{}, item, 0, nil, false
				}
				folderID = &value
			}
			return r, item, collectionID, folderID, true
		}
	}
	return workspacepkg.Request{}, item, 0, nil, false
}

type workspaceStateIndexes struct {
	generation   uint64
	requests     map[string]int64
	collections  map[string]int64
	folders      map[string]int64
	environments map[string]int64
}

func (s *Server) workspaceStateSnapshot() (*workspacepkg.Workspace, workspaceStateIndexes) {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	indexes := workspaceStateIndexes{
		generation:   s.workspaceGeneration,
		requests:     make(map[string]int64, len(s.requestIndexes)),
		collections:  make(map[string]int64, len(s.collectionIndexes)),
		folders:      make(map[string]int64, len(s.folderIndexes)),
		environments: make(map[string]int64, len(s.environmentIndexes)),
	}
	for id, item := range s.requestIndexes {
		indexes.requests[item.FileID] = id
	}
	for _, item := range s.environmentIndexes {
		indexes.environments[item.FileID] = item.SQLiteID
	}
	for id, sqliteID := range s.collectionIndexes {
		indexes.collections[id] = sqliteID
	}
	for id, sqliteID := range s.folderIndexes {
		indexes.folders[id] = sqliteID
	}
	return s.workspace, indexes
}

func (s *Server) canonicalEnv(name string) (workspacepkg.Environment, store.WorkspaceFile, bool) {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	if s.workspace == nil {
		return workspacepkg.Environment{}, store.WorkspaceFile{}, false
	}
	item, ok := s.environmentIndexes[name]
	if !ok {
		return workspacepkg.Environment{}, store.WorkspaceFile{}, false
	}
	for _, e := range s.workspace.Environments {
		if e.ID == item.FileID {
			return e, item, true
		}
	}
	return workspacepkg.Environment{}, item, false
}

func (s *Server) isVersioned() bool {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	return s.workspace != nil
}
func (s *Server) currentWorkspace() *workspacepkg.Workspace {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	return s.workspace
}
func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

type environmentAPI struct {
	store.Environment
	FileID      string `json:"fileId,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
}

func (s *Server) handleCanonicalEnvPut(w http.ResponseWriter, r *http.Request) {
	var input struct {
		store.Environment
		FileID       string `json:"fileId"`
		ContentHash  string `json:"contentHash"`
		CollectionID string `json:"collectionStableId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		httpError(w, 400, err)
		return
	}
	input.Name = r.PathValue("name")
	if input.Name == "" {
		httpError(w, 422, fmt.Errorf("environment name is required"))
		return
	}
	if err := s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	ws := s.currentWorkspace()
	if ws == nil {
		httpError(w, 500, fmt.Errorf("workspace is not in schema v2"))
		return
	}
	file, item, exists := s.canonicalEnv(input.Name)
	stableID := input.FileID
	if exists {
		if input.ContentHash == "" {
			httpError(w, 409, fmt.Errorf("contentHash is required; reload the environment before saving"))
			return
		}
		if input.ContentHash != item.Hash || stableID != "" && stableID != file.ID {
			httpError(w, 409, fmt.Errorf("environment changed on disk; keep this draft and compare with the current file"))
			return
		}
		stableID = file.ID
	} else {
		if input.ContentHash != "" {
			httpError(w, 409, fmt.Errorf("environment no longer exists on disk"))
			return
		}
		if input.CollectionID != "" {
			stableID = input.CollectionID
		}
		if len(ws.Collections) != 1 {
			httpError(w, 422, fmt.Errorf("select one collection before creating an environment in a multi-collection workspace"))
			return
		}
		if stableID == "" {
			stableID = ws.Collections[0].ID
		}
	}
	collectionID := file.CollectionID
	if !exists {
		collectionID = stableID
	}
	definition := workspacepkg.Environment{ID: file.ID, CollectionID: collectionID, Name: input.Name, Path: file.Path, Environment: dsl.Environment{Vars: input.Vars, Secrets: input.Secrets}}
	if !exists {
		definition.ID = workspacepkg.NewID()
	}
	newHash, err := ws.SaveEnvironment(definition, input.ContentHash)
	if err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, fmt.Errorf("environment changed on disk; keep this draft and compare with the current file"))
			return
		}
		httpError(w, 422, err)
		return
	}
	indexed := &store.Environment{Name: input.Name, Vars: input.Vars, Secrets: input.Secrets}
	if err = s.DB.UpsertEnvironment(indexed); err != nil {
		httpError(w, 500, fmt.Errorf("file saved but SQLite index update failed: %w", err))
		return
	}
	stored, err := s.DB.Environment(input.Name)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "environment", FileID: definition.ID, SQLiteID: stored.ID, Path: definition.Path, Hash: newHash}); err != nil {
		httpError(w, 500, err)
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, environmentAPI{Environment: *stored, FileID: definition.ID, ContentHash: newHash})
}

func (s *Server) trashCanonicalEnvironment(name string) error {
	file, item, ok := s.canonicalEnv(name)
	if !ok {
		return fmt.Errorf("environment %q is not indexed from workspace files", name)
	}
	if _, err := s.currentWorkspace().TrashEnvironment(file.ID, file.Path, item.Hash); err != nil {
		return err
	}
	if err := s.DB.DeleteEnvironment(name); err != nil {
		return err
	}
	return s.DB.DeleteWorkspaceFile("environment", item.SQLiteID)
}
