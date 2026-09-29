package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

func TestUpdateRequestMovesAtomicallyAndKeepsLinkedTests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	from := &Collection{Name: "from"}
	to := &Collection{Name: "to", Vars: map[string]string{"baseUrl": "https://destination"}}
	for _, c := range []*Collection{from, to} {
		if err := s.CreateCollection(c); err != nil {
			t.Fatal(err)
		}
	}
	folder := &Folder{CollectionID: to.ID, Name: "destination"}
	if err := s.CreateFolder(folder); err != nil {
		t.Fatal(err)
	}
	req := &Request{CollectionID: from.ID, Spec: &dsl.Request{Name: "get", URL: "{{baseUrl}}/"}}
	if err := s.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	tc := &TestCase{RequestID: req.ID, Name: "linked test"}
	if err := s.CreateTestCase(tc); err != nil {
		t.Fatal(err)
	}

	bad := &Request{ID: req.ID, CollectionID: to.ID, FolderID: &folder.ID, Spec: &dsl.Request{Name: "changed", URL: "/changed"}}
	otherFolder := &Folder{CollectionID: from.ID, Name: "wrong collection"}
	if err := s.CreateFolder(otherFolder); err != nil {
		t.Fatal(err)
	}
	bad.FolderID = &otherFolder.ID
	if err := s.UpdateRequest(bad); !errors.Is(err, ErrInvalidRequestDestination) {
		t.Fatalf("UpdateRequest with mismatched folder error = %v", err)
	}
	got, err := s.Request(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CollectionID != from.ID || got.FolderID != nil || got.Spec.Name != "get" {
		t.Fatalf("invalid move partially changed request: %+v", got)
	}

	bad.FolderID = &folder.ID
	if err := s.UpdateRequest(bad); err != nil {
		t.Fatal(err)
	}
	linked, err := s.TestCasesForRequest(req.ID)
	if err != nil || len(linked) != 1 || linked[0].ID != tc.ID {
		t.Fatalf("linked tests after move = %+v, err %v", linked, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Request(req.ID)
	if err != nil || got.CollectionID != to.ID || got.FolderID == nil || *got.FolderID != folder.ID {
		t.Fatalf("moved request after reopen = %+v, err %v", got, err)
	}
	linked, err = s.TestCasesForRequest(req.ID)
	if err != nil || len(linked) != 1 || linked[0].ID != tc.ID {
		t.Fatalf("linked tests after reopen = %+v, err %v", linked, err)
	}
}

func TestUpdateRequestRejectsMissingDestinationAndStaleRequest(t *testing.T) {
	s := open(t)
	c := &Collection{Name: "c"}
	if err := s.CreateCollection(c); err != nil {
		t.Fatal(err)
	}
	req := &Request{CollectionID: c.ID, Spec: &dsl.Request{Name: "get", URL: "/"}}
	if err := s.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []int64{0, 999} {
		update := &Request{ID: req.ID, CollectionID: dest, Spec: req.Spec}
		if err := s.UpdateRequest(update); !errors.Is(err, ErrInvalidRequestDestination) {
			t.Errorf("destination %d error = %v", dest, err)
		}
	}
	if err := s.DeleteRequest(req.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRequest(&Request{ID: req.ID, CollectionID: c.ID, Spec: req.Spec}); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("stale update error = %v", err)
	}
}
