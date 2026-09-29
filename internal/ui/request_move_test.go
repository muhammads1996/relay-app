package ui

import (
	"testing"

	"github.com/muhaymien96/relay/internal/store"
)

func TestRequestUpdateMovesAndValidatesDestination(t *testing.T) {
	s, reqID := newServer(t)
	destination := &store.Collection{Name: "destination", Vars: map[string]string{"baseUrl": "https://destination"}}
	if err := s.DB.CreateCollection(destination); err != nil {
		t.Fatal(err)
	}
	folder := &store.Folder{CollectionID: destination.ID, Name: "folder"}
	if err := s.DB.CreateFolder(folder); err != nil {
		t.Fatal(err)
	}

	body := `{"collectionId":` + itoa(destination.ID) + `,"folderId":` + itoa(folder.ID) + `,"spec":{"name":"Moved","method":"GET","url":"/moved"}}`
	rec, _ := call(t, s, "PUT", "/api/requests/"+itoa(reqID), body)
	if rec.Code != 200 {
		t.Fatalf("move status %d: %s", rec.Code, rec.Body.String())
	}
	moved, err := s.DB.Request(reqID)
	if err != nil || moved.CollectionID != destination.ID || moved.FolderID == nil || *moved.FolderID != folder.ID {
		t.Fatalf("stored request after move = %+v, err %v", moved, err)
	}

	wrong := &store.Folder{CollectionID: 1, Name: "wrong"}
	if err := s.DB.CreateFolder(wrong); err != nil {
		t.Fatal(err)
	}
	invalid := `{"collectionId":` + itoa(destination.ID) + `,"folderId":` + itoa(wrong.ID) + `,"spec":{"name":"Should not save","url":"/bad"}}`
	rec, _ = call(t, s, "PUT", "/api/requests/"+itoa(reqID), invalid)
	if rec.Code != 422 {
		t.Fatalf("invalid destination status %d: %s", rec.Code, rec.Body.String())
	}
	unchanged, err := s.DB.Request(reqID)
	if err != nil || unchanged.CollectionID != destination.ID || unchanged.FolderID == nil || *unchanged.FolderID != folder.ID || unchanged.Spec.Name != "Moved" {
		t.Fatalf("invalid update changed request: %+v, err %v", unchanged, err)
	}

	if err := s.DB.DeleteRequest(reqID); err != nil {
		t.Fatal(err)
	}
	rec, _ = call(t, s, "PUT", "/api/requests/"+itoa(reqID), `{"collectionId":`+itoa(destination.ID)+`,"spec":{"url":"/stale"}}`)
	if rec.Code != 404 {
		t.Fatalf("stale request status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequestUpdateCanClearFolderWhileMoving(t *testing.T) {
	s, reqID := newServer(t)
	request, err := s.DB.Request(reqID)
	if err != nil {
		t.Fatal(err)
	}
	folder := &store.Folder{CollectionID: request.CollectionID, Name: "source"}
	if err := s.DB.CreateFolder(folder); err != nil {
		t.Fatal(err)
	}
	request.FolderID = &folder.ID
	if err := s.DB.UpdateRequest(request); err != nil {
		t.Fatal(err)
	}
	destination := &store.Collection{Name: "another"}
	if err := s.DB.CreateCollection(destination); err != nil {
		t.Fatal(err)
	}
	body := `{"collectionId":` + itoa(destination.ID) + `,"folderId":null,"spec":{"name":"Moved","url":"/"}}`
	rec, _ := call(t, s, "PUT", "/api/requests/"+itoa(reqID), body)
	if rec.Code != 200 {
		t.Fatalf("move status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := s.DB.Request(reqID)
	if err != nil || got.CollectionID != destination.ID || got.FolderID != nil || got.Spec.Method != "GET" {
		t.Fatalf("moved request = %+v, err %v", got, err)
	}
}
