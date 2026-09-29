package store

import (
	"database/sql"
	"errors"
	"github.com/muhaymien96/relay/internal/dsl"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestDraftSurvivesStoreRestartAndUsesConditionalCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	one, err := s.SaveRequestDraft(identity, "sqlite:7", []byte(`{"spec":{"url":"https://example.test"}}`), "base-v1", "writer-a", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.SaveRequestDraft(identity, "sqlite:7", []byte(`{"spec":{"url":"https://example.test/new"}}`), "base-v1", "writer-a", one.Revision, 2)
	if err != nil {
		t.Fatal(err)
	}
	if two.Revision != one.Revision+1 {
		t.Fatalf("draft revisions = %d then %d", one.Revision, two.Revision)
	}
	if deleted, err := s.DeleteRequestDraft(identity, "sqlite:7", one.Revision); err != nil || deleted {
		t.Fatalf("stale cleanup deleted newer draft: deleted=%v err=%v", deleted, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	identityAfterRestart, err := s.Identity()
	if err != nil || identityAfterRestart != identity {
		t.Fatalf("DB identity after restart = %q, err=%v; want %q", identityAfterRestart, err, identity)
	}
	recovered, err := s.RequestDraft(identity, "sqlite:7")
	if err != nil {
		t.Fatal(err)
	}
	if string(recovered.Payload) != `{"spec":{"url":"https://example.test/new"}}` || recovered.BaseHash != "base-v1" || recovered.Revision != two.Revision {
		t.Fatalf("recovered draft = %#v", recovered)
	}
	if deleted, err := s.DeleteRequestDraft(identity, "sqlite:7", two.Revision); err != nil || !deleted {
		t.Fatalf("matching cleanup = deleted %v, err %v", deleted, err)
	}
	if _, err := s.RequestDraft(identity, "sqlite:7"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("draft after cleanup error = %v", err)
	}
}

func TestRequestDraftBoundsPayloadAndExpiresOldRows(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "drafts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	identity, _ := s.Identity()
	if _, err := s.SaveRequestDraft(identity, "file:request-a", make([]byte, maxRequestDraftBytes+1), "base", "writer", 0, 1); !errors.Is(err, ErrRequestDraftTooLarge) {
		t.Fatalf("oversize payload error = %v", err)
	}
	d, err := s.SaveRequestDraft(identity, "file:request-a", []byte("draft"), "base", "writer", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	old := draftTimestamp(time.Now().UTC().Add(-requestDraftTTL - time.Minute))
	if _, err := s.db.Exec(`UPDATE request_drafts SET updated_at=? WHERE workspace_id=? AND request_key=?`, old, identity, d.RequestKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestDraft(identity, d.RequestKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired draft lookup error = %v", err)
	}
}

func TestRequestDraftRejectsOutOfOrderEditFromSameWriter(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ordering.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	identity, _ := s.Identity()
	newer, err := s.SaveRequestDraft(identity, "sqlite:1", []byte("newest"), "base", "writer", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRequestDraft(identity, "sqlite:1", []byte("late old edit"), "base", "writer", 0, 1); !errors.Is(err, ErrRequestDraftConflict) {
		t.Fatalf("out-of-order old edit error = %v", err)
	}
	current, err := s.RequestDraft(identity, "sqlite:1")
	if err != nil {
		t.Fatal(err)
	}
	if string(current.Payload) != "newest" || current.Revision != newer.Revision {
		t.Fatalf("current draft after out-of-order write = %#v", current)
	}
}

func TestRequestContentHashCASRejectsStaleSave(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &Collection{Name: "CAS"}
	if err = s.CreateCollection(c); err != nil {
		t.Fatal(err)
	}
	r := &Request{CollectionID: c.ID, Spec: &dsl.Request{Name: "request", Method: "GET", URL: "https://old.example.test"}}
	if err = s.CreateRequest(r); err != nil {
		t.Fatal(err)
	}
	hash, err := s.RequestContentHash(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := *r
	updated.Spec = &dsl.Request{Name: "request", Method: "GET", URL: "https://new.example.test"}
	if err = s.UpdateRequestIfHash(&updated, hash); err != nil {
		t.Fatal(err)
	}
	stale := *r
	stale.Spec = &dsl.Request{Name: "request", Method: "GET", URL: "https://draft.example.test"}
	if err = s.UpdateRequestIfHash(&stale, hash); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("stale save error=%v", err)
	}
	current, err := s.Request(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Spec.URL != "https://new.example.test" {
		t.Fatalf("stale save replaced current request: %q", current.Spec.URL)
	}
}
