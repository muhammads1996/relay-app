package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

type canonicalCollectionAPI struct {
	store.Collection
	FileID        string `json:"fileId"`
	ContentHash   string `json:"contentHash"`
	WorkspaceHash string `json:"workspaceHash"`
}

type canonicalFolderAPI struct {
	store.Folder
	FileID      string `json:"fileId"`
	ContentHash string `json:"contentHash"`
}

func (s *Server) handleCanonicalCollectionCreate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		store.Collection
		WorkspaceHash string `json:"workspaceHash"`
	}
	if err := decodeBody(r, &input); err != nil {
		httpError(w, 400, err)
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		httpError(w, 422, fmt.Errorf("collection needs a name"))
		return
	}
	if input.WorkspaceHash == "" {
		httpError(w, 409, fmt.Errorf("workspaceHash is required; reload before creating a collection"))
		return
	}
	stable := workspacepkg.NewID()
	definition := workspacepkg.Collection{ID: stable, Config: dsl.Config{Name: input.Name, Headers: input.Headers, Vars: input.Vars}}
	created, err := s.currentWorkspace().CreateCollection(definition, input.WorkspaceHash)
	if err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, err)
		} else {
			httpError(w, 422, err)
		}
		return
	}
	row := &store.Collection{Name: created.Name, Headers: created.Headers, Vars: created.Vars}
	if err = s.DB.CreateCollection(row); err != nil {
		httpError(w, 500, fmt.Errorf("file created but SQLite index rebuild is required: %w", err))
		return
	}
	if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "collection", FileID: stable, SQLiteID: row.ID, Path: created.Path}); err != nil {
		httpError(w, 500, fmt.Errorf("file created but SQLite index rebuild is required: %w", err))
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	ws := s.currentWorkspace()
	file, _ := s.DB.WorkspaceFileByStableID("collection", stable)
	writeJSON(w, canonicalCollectionAPI{Collection: *row, FileID: stable, ContentHash: file.Hash, WorkspaceHash: ws.Manifest.Hash})
}

func (s *Server) handleCanonicalCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var input struct {
		store.Collection
		ContentHash string `json:"contentHash"`
	}
	if err = decodeBody(r, &input); err != nil {
		httpError(w, 400, err)
		return
	}
	item, err := s.DB.WorkspaceFileBySQLiteID("collection", id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("collection is not indexed from workspace files"))
		return
	}
	if input.ContentHash == "" {
		httpError(w, 409, fmt.Errorf("contentHash is required; reload the collection before saving"))
		return
	}
	if input.ID != 0 && input.ID != id {
		httpError(w, 422, fmt.Errorf("collection ID cannot be changed"))
		return
	}
	def := workspacepkg.Collection{ID: item.FileID, Config: dsl.Config{Name: input.Name, Headers: input.Headers, Vars: input.Vars}}
	hash, err := s.currentWorkspace().SaveCollection(def, input.ContentHash)
	if err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, err)
		} else {
			httpError(w, 422, err)
		}
		return
	}
	row := &store.Collection{ID: id, Name: input.Name, Headers: input.Headers, Vars: input.Vars}
	if err = s.DB.UpdateCollection(row); err != nil {
		httpError(w, 500, fmt.Errorf("file saved but SQLite index rebuild is required: %w", err))
		return
	}
	if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "collection", FileID: item.FileID, SQLiteID: id, Path: item.Path, Hash: hash}); err != nil {
		httpError(w, 500, err)
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	fresh, _ := s.DB.WorkspaceFileByStableID("collection", item.FileID)
	writeJSON(w, canonicalCollectionAPI{Collection: *row, FileID: item.FileID, ContentHash: fresh.Hash, WorkspaceHash: s.currentWorkspace().Manifest.Hash})
}

func (s *Server) handleCanonicalCollectionDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var input struct {
		ContentHash   string `json:"contentHash"`
		WorkspaceHash string `json:"workspaceHash"`
	}
	if err = decodeBody(r, &input); err != nil {
		httpError(w, 400, err)
		return
	}
	item, err := s.DB.WorkspaceFileBySQLiteID("collection", id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("collection is not indexed from workspace files"))
		return
	}
	if input.ContentHash == "" || input.WorkspaceHash == "" {
		httpError(w, 409, fmt.Errorf("contentHash and workspaceHash are required; reload before deleting"))
		return
	}
	if err = s.currentWorkspace().DeleteCollection(item.FileID, input.ContentHash, input.WorkspaceHash); err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, err)
		} else {
			httpError(w, 422, err)
		}
		return
	}
	requests, _ := s.DB.Requests(id)
	for _, q := range requests {
		_ = s.DB.DeleteWorkspaceFile("request", q.ID)
		_ = s.DB.DeleteRequest(q.ID)
	}
	folders, _ := s.DB.Folders(id)
	for _, f := range folders {
		_ = s.DB.DeleteWorkspaceFile("folder", f.ID)
		_ = s.DB.DeleteFolder(f.ID)
	}
	_ = s.DB.DeleteWorkspaceFile("collection", id)
	if err = s.DB.DeleteCollection(id); err != nil {
		httpError(w, 500, fmt.Errorf("collection moved to recoverable trash; SQLite cleanup failed: %w", err))
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleCanonicalFolderCreate(w http.ResponseWriter, r *http.Request) {
	var input store.Folder
	if err := decodeBody(r, &input); err != nil {
		httpError(w, 400, err)
		return
	}
	collection, err := s.DB.WorkspaceFileBySQLiteID("collection", input.CollectionID)
	if err != nil {
		httpError(w, 422, fmt.Errorf("collection is not indexed from workspace files"))
		return
	}
	folder := workspacepkg.Folder{ID: workspacepkg.NewID(), CollectionID: collection.FileID, Config: dsl.Config{Name: input.Name, Headers: input.Headers, Vars: input.Vars}}
	created, err := s.currentWorkspace().CreateFolder(folder)
	if err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, err)
		} else {
			httpError(w, 422, err)
		}
		return
	}
	row := &store.Folder{CollectionID: input.CollectionID, Name: input.Name, Headers: input.Headers, Vars: input.Vars}
	if err = s.DB.CreateFolder(row); err != nil {
		httpError(w, 500, fmt.Errorf("folder created but SQLite index rebuild is required: %w", err))
		return
	}
	if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "folder", FileID: created.ID, SQLiteID: row.ID, Path: created.Path, Hash: created.Hash}); err != nil {
		httpError(w, 500, err)
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, canonicalFolderAPI{Folder: *row, FileID: created.ID, ContentHash: created.Hash})
}

func (s *Server) handleCanonicalFolderUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var input struct {
		store.Folder
		ContentHash string `json:"contentHash"`
	}
	if err = decodeBody(r, &input); err != nil {
		httpError(w, 400, err)
		return
	}
	item, err := s.DB.WorkspaceFileBySQLiteID("folder", id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("folder is not indexed from workspace files"))
		return
	}
	if input.ContentHash == "" {
		httpError(w, 409, fmt.Errorf("contentHash is required; reload the folder before saving"))
		return
	}
	collection, err := s.DB.WorkspaceFileBySQLiteID("collection", input.CollectionID)
	if err != nil || input.CollectionID == 0 {
		httpError(w, 422, fmt.Errorf("folder collection cannot be changed to an unindexed collection"))
		return
	}
	folder := workspacepkg.Folder{ID: item.FileID, CollectionID: collection.FileID, Config: dsl.Config{Name: input.Name, Headers: input.Headers, Vars: input.Vars}}
	saved, err := s.currentWorkspace().SaveFolder(folder, input.ContentHash)
	if err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, err)
		} else {
			httpError(w, 422, err)
		}
		return
	}
	rel, err := filepath.Rel(filepath.Dir(collection.Path), filepath.Dir(saved.Path))
	if err != nil {
		httpError(w, 500, err)
		return
	}
	row := &store.Folder{ID: id, CollectionID: input.CollectionID, Name: filepath.ToSlash(rel), Headers: input.Headers, Vars: input.Vars}
	if err = s.DB.UpdateFolder(row); err != nil {
		httpError(w, 500, fmt.Errorf("folder file moved but SQLite index rebuild is required: %w", err))
		return
	}
	if err = s.DB.BindWorkspaceFile(store.WorkspaceFile{Kind: "folder", FileID: item.FileID, SQLiteID: id, Path: saved.Path, Hash: saved.Hash}); err != nil {
		httpError(w, 500, err)
		return
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, canonicalFolderAPI{Folder: *row, FileID: item.FileID, ContentHash: saved.Hash})
}

func (s *Server) handleCanonicalFolderDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var input struct {
		ContentHash string `json:"contentHash"`
	}
	if err = decodeBody(r, &input); err != nil {
		httpError(w, 400, err)
		return
	}
	item, err := s.DB.WorkspaceFileBySQLiteID("folder", id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("folder is not indexed from workspace files"))
		return
	}
	if input.ContentHash == "" {
		httpError(w, 409, fmt.Errorf("contentHash is required; reload before deleting"))
		return
	}
	ws := s.currentWorkspace()
	deletedFolders := map[string]bool{}
	deletedRequests := map[string]bool{}
	sourceDir := ""
	for _, f := range ws.Folders {
		if f.ID == item.FileID {
			sourceDir = filepath.Dir(f.Path)
			break
		}
	}
	if sourceDir != "" {
		for _, f := range ws.Folders {
			if withinPath(sourceDir, filepath.Dir(f.Path)) {
				deletedFolders[f.ID] = true
			}
		}
		for _, q := range ws.Requests {
			if withinPath(sourceDir, filepath.Dir(q.Path)) {
				deletedRequests[q.ID] = true
			}
		}
	}
	target, err := ws.DeleteFolder(item.FileID, input.ContentHash)
	if err != nil {
		if errors.Is(err, workspacepkg.ErrConflict) {
			httpError(w, 409, err)
		} else {
			httpError(w, 422, err)
		}
		return
	}
	for stable := range deletedRequests {
		mapped, e := s.DB.WorkspaceFileByStableID("request", stable)
		if e == nil {
			if e = s.DB.DeleteRequest(mapped.SQLiteID); e != nil {
				httpError(w, 500, fmt.Errorf("folder moved to recoverable trash, but request index cleanup failed: %w", e))
				return
			}
			_ = s.DB.DeleteWorkspaceFile("request", mapped.SQLiteID)
		}
	}
	for stable := range deletedFolders {
		mapped, e := s.DB.WorkspaceFileByStableID("folder", stable)
		if e == nil {
			if e = s.DB.DeleteFolder(mapped.SQLiteID); e != nil {
				httpError(w, 500, fmt.Errorf("folder moved to recoverable trash, but folder index cleanup failed: %w", e))
				return
			}
			_ = s.DB.DeleteWorkspaceFile("folder", mapped.SQLiteID)
		}
	}
	if err = s.refreshWorkspace(); err != nil {
		httpError(w, 500, fmt.Errorf("folder moved to recoverable trash at %s; refresh failed: %w", target, err))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func withinPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func decodeBody(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
