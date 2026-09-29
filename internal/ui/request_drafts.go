package ui

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/muhaymien96/relay/internal/store"
)

const maxRequestDraftPayload = 1 << 20

type requestDraftIdentity struct {
	workspace string
	request   string
}

func (s *Server) requestDraftIdentity(id int64) (requestDraftIdentity, error) {
	if s.isVersioned() {
		file, _, ok := s.canonicalRequest(id)
		if !ok {
			return requestDraftIdentity{}, fmt.Errorf("request is not present in the current workspace")
		}
		ws := s.currentWorkspace()
		if ws == nil || ws.Manifest.ID == "" || file.ID == "" {
			return requestDraftIdentity{}, fmt.Errorf("stable workspace and request identities are unavailable")
		}
		dbID, err := s.DB.Identity()
		if err != nil {
			return requestDraftIdentity{}, err
		}
		return requestDraftIdentity{workspace: dbID + ":" + ws.Manifest.ID, request: "file:" + file.ID}, nil
	}
	dbID, err := s.DB.Identity()
	if err != nil {
		return requestDraftIdentity{}, err
	}
	return requestDraftIdentity{workspace: dbID, request: "sqlite:" + strconv.FormatInt(id, 10)}, nil
}

func (s *Server) handleRequestDraftGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.DB.Request(id); err != nil {
		httpError(w, http.StatusNotFound, fmt.Errorf("request %d not found", id))
		return
	}
	identity, err := s.requestDraftIdentity(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	draft, err := s.DB.RequestDraft(identity.workspace, identity.request)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, map[string]any{"exists": false})
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Errorf("loading local recovery draft"))
		return
	}
	writeJSON(w, map[string]any{"exists": true, "draft": json.RawMessage(draft.Payload), "baseContentHash": draft.BaseHash, "revision": draft.Revision, "updatedAt": draft.UpdatedAt, "writerId": draft.WriterID, "editRevision": draft.EditRevision})
}

func (s *Server) handleRequestDraftPut(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.DB.Request(id); err != nil {
		httpError(w, http.StatusNotFound, fmt.Errorf("request %d not found", id))
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestDraftPayload+8192)
	var input struct {
		Draft            json.RawMessage `json:"draft"`
		BaseContentHash  string          `json:"baseContentHash"`
		WriterID         string          `json:"writerId"`
		ExpectedRevision int64           `json:"expectedRevision"`
		EditRevision     int64           `json:"editRevision"`
	}
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpError(w, http.StatusRequestEntityTooLarge, store.ErrRequestDraftTooLarge)
			return
		}
		httpError(w, http.StatusBadRequest, fmt.Errorf("invalid recovery draft"))
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		httpError(w, http.StatusBadRequest, fmt.Errorf("invalid recovery draft"))
		return
	}
	if len(input.WriterID) != 36 || strings.TrimSpace(input.WriterID) == "" || input.ExpectedRevision < 0 || input.EditRevision <= 0 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("writer identity and valid draft revisions are required"))
		return
	}
	if _, err := uuid.Parse(input.WriterID); err != nil {
		httpError(w, http.StatusBadRequest, fmt.Errorf("writer identity and valid draft revisions are required"))
		return
	}
	if len(input.Draft) == 0 || !json.Valid(input.Draft) {
		httpError(w, http.StatusBadRequest, fmt.Errorf("recovery draft is required"))
		return
	}
	if len(input.Draft) > maxRequestDraftPayload {
		httpError(w, http.StatusRequestEntityTooLarge, store.ErrRequestDraftTooLarge)
		return
	}
	if len(input.BaseContentHash) > 128 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("invalid base revision"))
		return
	}
	if input.BaseContentHash != "" {
		decoded, e := hex.DecodeString(input.BaseContentHash)
		if e != nil || len(decoded) != 32 {
			httpError(w, http.StatusBadRequest, fmt.Errorf("invalid base revision"))
			return
		}
	}
	identity, err := s.requestDraftIdentity(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	draft, err := s.DB.SaveRequestDraft(identity.workspace, identity.request, input.Draft, input.BaseContentHash, input.WriterID, input.ExpectedRevision, input.EditRevision)
	if errors.Is(err, store.ErrRequestDraftTooLarge) {
		httpError(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	if errors.Is(err, store.ErrRequestDraftConflict) {
		httpError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Errorf("saving local recovery draft"))
		return
	}
	writeJSON(w, map[string]any{"revision": draft.Revision, "updatedAt": draft.UpdatedAt})
}

func (s *Server) handleRequestDraftDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := decoder.Decode(&input); err != nil {
		httpError(w, http.StatusBadRequest, fmt.Errorf("draft revision is required"))
		return
	}
	if input.Revision <= 0 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("draft revision is required"))
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		httpError(w, http.StatusBadRequest, fmt.Errorf("invalid draft cleanup request"))
		return
	}
	identity, err := s.requestDraftIdentity(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	deleted, err := s.DB.DeleteRequestDraft(identity.workspace, identity.request, input.Revision)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Errorf("deleting local recovery draft"))
		return
	}
	if !deleted {
		httpError(w, http.StatusConflict, fmt.Errorf("a newer recovery draft exists; it was kept"))
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
