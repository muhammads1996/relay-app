package ui

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/porter"
	"github.com/muhaymien96/relay/internal/store"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

// Environment exports contain credentials, so only their converted variables
// and secret names are persisted. The original upload stays in the browser.
func (s *Server) importPostmanEnvironment(w http.ResponseWriter, r *http.Request, data []byte) {
	env, err := porter.ParsePostmanEnvironment(data)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	warnings := env.Warnings
	if warnings == nil {
		warnings = []porter.ImportWarning{}
	}
	response := map[string]any{
		"kind": "environment", "environmentName": env.Name, "requests": 0,
		"variables": env.Variables, "secrets": len(env.Secrets), "warnings": warnings,
	}
	if s.isVersioned() {
		s.importCanonicalPostmanEnvironment(w, r, env, response)
		return
	}
	stored, err := s.DB.Environment(env.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpError(w, 500, err)
		return
	}
	hash := ""
	if stored != nil {
		hash = workspacepkg.Hash(store.MarshalEnvironment(*stored))
	}
	response["contentHash"] = hash
	if r.URL.Query().Get("preview") == "1" {
		writeJSON(w, response)
		return
	}
	if r.URL.Query().Has("contentHash") && r.URL.Query().Get("contentHash") != hash {
		httpError(w, 409, fmt.Errorf("environment changed; preview again before importing"))
		return
	}
	definition := &store.Environment{Name: env.Name, Vars: env.Vars, Secrets: env.Secrets}
	if r.URL.Query().Has("contentHash") {
		changed, err := s.DB.CompareAndSwapEnvironment(definition, stored)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if !changed {
			httpError(w, 409, fmt.Errorf("environment changed; preview again before importing"))
			return
		}
	} else if err := s.DB.UpsertEnvironment(definition); err != nil {
		httpError(w, 500, err)
		return
	}
	indexed, err := s.DB.Environment(env.Name)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	response["environmentId"] = indexed.ID
	response["contentHash"] = workspacepkg.Hash(store.MarshalEnvironment(*definition))
	writeJSON(w, response)
}

func (s *Server) importCanonicalPostmanEnvironment(w http.ResponseWriter, r *http.Request, env porter.PostmanEnvironment, response map[string]any) {
	if err := s.refreshWorkspace(); err != nil {
		httpError(w, 500, err)
		return
	}
	ws := s.currentWorkspace()
	file, item, exists := s.canonicalEnv(env.Name)
	collectionID := file.CollectionID
	if raw := r.URL.Query().Get("collectionId"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			httpError(w, 422, fmt.Errorf("invalid collectionId"))
			return
		}
		selected, err := s.DB.WorkspaceFileBySQLiteID("collection", id)
		if err != nil {
			httpError(w, 422, fmt.Errorf("select a collection from the current workspace before importing an environment"))
			return
		}
		if !exists {
			collectionID = selected.FileID
		}
	}
	if collectionID == "" {
		if len(ws.Collections) != 1 {
			httpError(w, 422, fmt.Errorf("select one collection before importing an environment"))
			return
		}
		collectionID = ws.Collections[0].ID
	}
	// An existing environment remains in its original collection on reimport.
	collection, err := s.DB.WorkspaceFileByStableID("collection", collectionID)
	if err != nil {
		httpError(w, 422, fmt.Errorf("environment collection is not indexed from workspace files"))
		return
	}
	hash := ""
	if exists {
		hash = item.Hash
	}
	response["workspaceHash"] = ws.Manifest.Hash
	response["collectionId"] = collection.SQLiteID
	response["contentHash"] = hash
	if r.URL.Query().Get("preview") == "1" {
		writeJSON(w, response)
		return
	}
	expected := r.URL.Query().Get("workspaceHash")
	if expected == "" {
		expected = r.Header.Get("X-Workspace-Hash")
	}
	if expected == "" || expected != ws.Manifest.Hash || r.URL.Query().Get("contentHash") != hash {
		httpError(w, 409, fmt.Errorf("workspace or environment changed; preview again before importing"))
		return
	}
	definition := workspacepkg.Environment{
		ID: file.ID, CollectionID: collectionID, Name: env.Name, Path: file.Path,
		Environment: dsl.Environment{Vars: env.Vars, Secrets: env.Secrets},
	}
	if !exists {
		definition.ID = workspacepkg.NewID()
	}
	newHash, err := ws.SaveEnvironment(definition, hash)
	if err != nil {
		status := 422
		if errors.Is(err, workspacepkg.ErrConflict) {
			status = 409
		}
		httpError(w, status, err)
		return
	}
	if err := s.refreshWorkspace(); err != nil {
		httpError(w, 500, fmt.Errorf("environment file committed; index refresh required: %w", err))
		return
	}
	indexed, err := s.DB.WorkspaceFileByStableID("environment", definition.ID)
	if err != nil {
		httpError(w, 500, fmt.Errorf("environment file committed; index refresh required: %w", err))
		return
	}
	response["environmentId"] = indexed.SQLiteID
	response["fileId"] = definition.ID
	response["contentHash"] = newHash
	writeJSON(w, response)
}
