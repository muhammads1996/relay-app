package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	maxRequestDraftBytes = 1 << 20
	maxRequestDrafts     = 50
	requestDraftTTL      = 30 * 24 * time.Hour
)

var ErrRequestDraftTooLarge = errors.New("request draft exceeds the 1 MiB limit")
var ErrRequestDraftConflict = errors.New("a newer local recovery draft already exists")

// RequestDraft stores a local recovery copy separately from request history.
// Payload may contain secrets and must never be added to logs or status output.
type RequestDraft struct {
	WorkspaceID  string    `json:"workspaceId"`
	RequestKey   string    `json:"requestKey"`
	Payload      []byte    `json:"draft"`
	BaseHash     string    `json:"baseContentHash"`
	Revision     int64     `json:"revision"`
	WriterID     string    `json:"writerId"`
	EditRevision int64     `json:"editRevision"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Identity returns an opaque, persistent identity for this local SQLite DB.
func (s *Store) Identity() (string, error) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO relay_meta(key, value) VALUES ('database_id', ?)`, uuid.NewString()); err != nil {
		return "", err
	}
	var id string
	if err := s.db.QueryRow(`SELECT value FROM relay_meta WHERE key='database_id'`).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// RequestDraft returns an unexpired local recovery copy. Callers must keep its
// payload out of status, history, diagnostics, and logs.
func (s *Store) RequestDraft(workspaceID, requestKey string) (RequestDraft, error) {
	if workspaceID == "" || requestKey == "" {
		return RequestDraft{}, fmt.Errorf("workspace and request identities are required")
	}
	s.purgeRequestDrafts(time.Now())
	var d RequestDraft
	var updated string
	err := s.db.QueryRow(`SELECT workspace_id, request_key, payload, base_hash, revision, writer_id, edit_revision, updated_at FROM request_drafts WHERE workspace_id=? AND request_key=? AND updated_at>=?`, workspaceID, requestKey, draftTimestamp(time.Now().Add(-requestDraftTTL))).Scan(&d.WorkspaceID, &d.RequestKey, &d.Payload, &d.BaseHash, &d.Revision, &d.WriterID, &d.EditRevision, &updated)
	if err != nil {
		return RequestDraft{}, err
	}
	d.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return d, err
}

// SaveRequestDraft replaces the single current draft for an identity and
// returns its new revision. The revision guards late deletes from older tabs.
func (s *Store) SaveRequestDraft(workspaceID, requestKey string, payload []byte, baseHash, writerID string, expectedRevision, editRevision int64) (RequestDraft, error) {
	if workspaceID == "" || requestKey == "" || writerID == "" || expectedRevision < 0 || editRevision <= 0 {
		return RequestDraft{}, fmt.Errorf("workspace, request, writer identities and valid revisions are required")
	}
	if len(payload) > maxRequestDraftBytes {
		return RequestDraft{}, ErrRequestDraftTooLarge
	}
	now := time.Now().UTC()
	stamp := draftTimestamp(now)
	tx, err := s.db.Begin()
	if err != nil {
		return RequestDraft{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM request_drafts WHERE updated_at < ?`, draftTimestamp(now.Add(-requestDraftTTL))); err != nil {
		return RequestDraft{}, err
	}
	var currentRevision, currentEdit int64
	var currentWriter string
	queryErr := tx.QueryRow(`SELECT revision, writer_id, edit_revision FROM request_drafts WHERE workspace_id=? AND request_key=?`, workspaceID, requestKey).Scan(&currentRevision, &currentWriter, &currentEdit)
	if errors.Is(queryErr, sql.ErrNoRows) {
		if expectedRevision != 0 {
			return RequestDraft{}, ErrRequestDraftConflict
		}
		if _, err = tx.Exec(`INSERT INTO request_drafts(workspace_id, request_key, payload, base_hash, revision, writer_id, edit_revision, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`, workspaceID, requestKey, payload, baseHash, writerID, editRevision, stamp); err != nil {
			return RequestDraft{}, err
		}
		currentRevision = 1
	} else if queryErr != nil {
		return RequestDraft{}, queryErr
	} else {
		newerWriterEdit := currentWriter == writerID && editRevision > currentEdit
		if expectedRevision != currentRevision && !newerWriterEdit {
			return RequestDraft{}, ErrRequestDraftConflict
		}
		if currentWriter == writerID && editRevision <= currentEdit {
			return RequestDraft{}, ErrRequestDraftConflict
		}
		if _, err = tx.Exec(`UPDATE request_drafts SET payload=?, base_hash=?, revision=revision+1, writer_id=?, edit_revision=?, updated_at=? WHERE workspace_id=? AND request_key=? AND revision=?`, payload, baseHash, writerID, editRevision, stamp, workspaceID, requestKey, currentRevision); err != nil {
			return RequestDraft{}, err
		}
		currentRevision++
	}
	if _, err = tx.Exec(`DELETE FROM request_drafts WHERE workspace_id=? AND request_key IN (SELECT request_key FROM request_drafts WHERE workspace_id=? ORDER BY updated_at DESC, request_key LIMIT -1 OFFSET ?)`, workspaceID, workspaceID, maxRequestDrafts); err != nil {
		return RequestDraft{}, err
	}
	if _, err = tx.Exec(`DELETE FROM request_drafts WHERE rowid IN (SELECT rowid FROM request_drafts ORDER BY updated_at DESC, workspace_id, request_key LIMIT -1 OFFSET ?)`, maxRequestDrafts*10); err != nil {
		return RequestDraft{}, err
	}
	if err = tx.Commit(); err != nil {
		return RequestDraft{}, err
	}
	return RequestDraft{WorkspaceID: workspaceID, RequestKey: requestKey, Payload: append([]byte(nil), payload...), BaseHash: baseHash, Revision: currentRevision, WriterID: writerID, EditRevision: editRevision, UpdatedAt: now}, nil
}

// DeleteRequestDraft deletes only the draft revision the caller observed.
func (s *Store) DeleteRequestDraft(workspaceID, requestKey string, revision int64) (bool, error) {
	if workspaceID == "" || requestKey == "" || revision <= 0 {
		return false, fmt.Errorf("workspace, request, and positive draft revision are required")
	}
	result, err := s.db.Exec(`DELETE FROM request_drafts WHERE workspace_id=? AND request_key=? AND revision=?`, workspaceID, requestKey, revision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) DeleteRequestDraftsForRequest(workspaceID, requestKey string) error {
	_, err := s.db.Exec(`DELETE FROM request_drafts WHERE workspace_id=? AND request_key=?`, workspaceID, requestKey)
	return err
}

func (s *Store) purgeRequestDrafts(now time.Time) {
	_, _ = s.db.Exec(`DELETE FROM request_drafts WHERE updated_at < ?`, draftTimestamp(now.Add(-requestDraftTTL)))
}

func draftTimestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
