package ui

import (
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/store"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

func TestRequestIDsForStateReturnsStableIDIndexSnapshot(t *testing.T) {
	s := &Server{requestIndexes: map[int64]store.WorkspaceFile{
		7: {FileID: "request-a"},
		9: {FileID: "request-b"},
	}}
	ws, got := s.workspaceStateSnapshot()
	if ws != nil || len(got.requests) != 2 || got.requests["request-a"] != 7 || got.requests["request-b"] != 9 {
		t.Fatalf("unexpected reverse request index: %#v", got)
	}
	s.requestIndexes[7] = store.WorkspaceFile{FileID: "request-c"}
	if got.requests["request-a"] != 7 || got.requests["request-c"] != 0 {
		t.Fatalf("returned index is not a snapshot: %#v", got)
	}
}

func TestStateUsesPublishedSnapshotDuringRefresh(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{DB: db, workspace: &workspacepkg.Workspace{Manifest: workspacepkg.Manifest{ID: "00000000-0000-4000-8000-000000000010"}}}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.runWorkspaceRefresh(func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	response := httptest.NewRecorder()
	stateDone := make(chan struct{})
	go func() {
		s.handleState(response, httptest.NewRequest("GET", "/api/state", nil))
		close(stateDone)
	}()
	select {
	case <-stateDone:
		if response.Code != 200 {
			close(release)
			<-done
			t.Fatalf("state status %d: %s", response.Code, response.Body)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("GET /api/state waited for the background refresh")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRefreshCoalescesConcurrentScans(t *testing.T) {
	s := &Server{}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	scan := func() error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}
	const concurrent = 8
	var wg sync.WaitGroup
	errs := make(chan error, concurrent)
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			errs <- s.runWorkspaceRefresh(scan)
		}()
	}
	<-started
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.workspaceScanMu.Lock()
		waiters := s.workspaceRefreshWaiters
		s.workspaceScanMu.Unlock()
		if waiters == concurrent-1 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("%d callers joined the active scan; want %d", waiters, concurrent-1)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("started %d concurrent scans; want one", got)
	}
}
