package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/store"
)

func runJobTestServer(t *testing.T, handler http.HandlerFunc, count int) (*Server, []store.Request, *httptest.Server) {
	t.Helper()
	api := httptest.NewServer(handler)
	db, err := store.Open(filepath.Join(t.TempDir(), "run-jobs.db"))
	if err != nil {
		api.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(); api.Close() })
	col := &store.Collection{Name: "run jobs"}
	if err := db.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	requests := make([]store.Request, 0, count)
	for i := 0; i < count; i++ {
		req := &store.Request{CollectionID: col.ID, Spec: &dsl.Request{
			Name: "request-" + string(rune('A'+i)), Method: "GET", URL: api.URL + "/" + string(rune('a'+i)),
		}}
		if err := db.CreateRequest(req); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, *req)
	}
	return &Server{DB: db, Engine: engine.NewOptions()}, requests, api
}

func waitRunJob(t *testing.T, manager *runJobManager, id string, want string) runJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := manager.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.State == want {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return runJobSnapshot{}
}

func TestRunJobManagerReportsProgressAndTerminalCounts(t *testing.T) {
	var calls atomic.Int32
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}, 3)
	manager := newRunJobManager()
	started, err := manager.Start(context.Background(), server, requests, "")
	if err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Total != 3 || started.State != "running" {
		t.Fatalf("initial snapshot = %#v", started)
	}
	finished := waitRunJob(t, manager, started.ID, "completed")
	if finished.Completed != 3 || finished.Passed != 3 || finished.Failed != 0 || len(finished.Results) != 3 {
		t.Fatalf("finished counts/results = %#v", finished)
	}
	if calls.Load() != 3 || finished.FinishedAt == nil || finished.ElapsedMs < 0 {
		t.Fatalf("calls=%d snapshot=%#v", calls.Load(), finished)
	}
	// Snapshot slices are detached from the manager's retained state.
	finished.Results[0].Name = "changed"
	again, err := manager.Status(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Results[0].Name == "changed" {
		t.Fatal("status exposed mutable manager state")
	}
}

func TestRunJobManagerCancellationStopsBeforeNextRequestAndCapacityIsBounded(t *testing.T) {
	startedRequest := make(chan struct{}, 1)
	var secondCalls atomic.Int32
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/slow") {
			startedRequest <- struct{}{}
			<-r.Context().Done()
			return
		}
		secondCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}, 2)
	requests[0].Spec.URL += "/slow"
	requests[1].Spec.URL += "/fast"
	manager := newRunJobManager()
	manager.max = 1
	started, err := manager.Start(context.Background(), server, requests, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start")
	}
	if _, err := manager.Start(context.Background(), server, requests, ""); err == nil {
		t.Fatal("manager accepted a second job beyond capacity")
	}
	if _, err := manager.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}
	finished := waitRunJob(t, manager, started.ID, "cancelled")
	if !finished.Cancelled || finished.Completed != 0 || finished.Total != 2 || finished.Failed != 0 || finished.CancelledCount != 1 || len(finished.Results) != 1 || !finished.Results[0].Cancelled {
		t.Fatalf("cancelled snapshot = %#v", finished)
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("second request ran %d times after cancellation", secondCalls.Load())
	}
}

func TestRunJobCancellationDuringFinalRequestRemainsCancelled(t *testing.T) {
	startedRequest := make(chan struct{}, 1)
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		startedRequest <- struct{}{}
		<-r.Context().Done()
	}, 1)
	requests[0].Spec.URL += "/slow"
	manager := newRunJobManager()
	started, err := manager.Start(context.Background(), server, requests, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	if _, err := manager.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}
	finished := waitRunJob(t, manager, started.ID, "cancelled")
	if !finished.Cancelled || finished.Completed != 0 || finished.Total != 1 || finished.Failed != 0 || finished.CancelledCount != 1 || len(finished.Results) != 1 || !finished.Results[0].Cancelled {
		t.Fatalf("cancelled final request snapshot = %#v", finished)
	}
}

func TestRunJobCancellationCountsFinishedAndCancelledRequestsSeparately(t *testing.T) {
	startedFourth := make(chan struct{}, 1)
	var calls atomic.Int32
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/slow") {
			startedFourth <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	}, 4)
	requests[3].Spec.URL += "/slow"
	manager := newRunJobManager()
	started, err := manager.Start(context.Background(), server, requests, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedFourth:
	case <-time.After(2 * time.Second):
		t.Fatal("fourth request did not start")
	}
	if _, err := manager.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}
	finished := waitRunJob(t, manager, started.ID, "cancelled")
	if finished.Completed != 3 || finished.Passed != 3 || finished.Failed != 0 || finished.CancelledCount != 1 || finished.Total != 4 || len(finished.Results) != 4 {
		t.Fatalf("cancelled run counts/results = %#v", finished)
	}
	cancelled := finished.Results[3]
	if !cancelled.Cancelled || cancelled.Passed || cancelled.Error != "" || cancelled.CancellationReason == "" {
		t.Fatalf("cancelled request result = %#v", cancelled)
	}
	if calls.Load() != 4 {
		t.Fatalf("request calls = %d, want 4 and no requests after cancellation", calls.Load())
	}
}

func TestRunJobManagerExpiresCompletedJobs(t *testing.T) {
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }, 1)
	manager := newRunJobManager()
	started, err := manager.Start(context.Background(), server, requests, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = waitRunJob(t, manager, started.ID, "completed")
	manager.mu.Lock()
	manager.jobs[started.ID].finished = time.Now().Add(-2 * manager.ttl)
	manager.mu.Unlock()
	if _, err := manager.Status(started.ID); err != ErrRunJobNotFound {
		t.Fatalf("expired status error = %v", err)
	}
}

func TestRunJobHTTPStartAndStatusRoutes(t *testing.T) {
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }, 1)
	handler := server.Handler()
	body, _ := json.Marshal(map[string]any{"collectionId": requests[0].CollectionID})
	startReq := httptest.NewRequest(http.MethodPost, "/api/run-jobs", bytes.NewReader(body))
	startReq.Host = "127.0.0.1:7717"
	startReq.RemoteAddr = "127.0.0.1:54321"
	startReq.Header.Set("Origin", "http://127.0.0.1:7717")
	startRes := httptest.NewRecorder()
	handler.ServeHTTP(startRes, startReq)
	if startRes.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", startRes.Code, startRes.Body.String())
	}
	var started runJobSnapshot
	if err := json.Unmarshal(startRes.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Total != 1 {
		t.Fatalf("start response = %#v", started)
	}

	finished := waitRunJob(t, server.runJobs, started.ID, "completed")
	statusReq := httptest.NewRequest(http.MethodGet, "/api/run-jobs/"+started.ID, nil)
	statusReq.Host = "127.0.0.1:7717"
	statusReq.RemoteAddr = "127.0.0.1:54321"
	statusReq.Header.Set("Origin", "http://127.0.0.1:7717")
	statusRes := httptest.NewRecorder()
	handler.ServeHTTP(statusRes, statusReq)
	if statusRes.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", statusRes.Code, statusRes.Body.String())
	}
	var status runJobSnapshot
	if err := json.Unmarshal(statusRes.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Completed != 1 || status.Passed != 1 || len(status.Results) != 1 || finished.ID != status.ID {
		t.Fatalf("status response = %#v", status)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/run-jobs/missing", nil)
	missingReq.Host = "127.0.0.1:7717"
	missingReq.RemoteAddr = "127.0.0.1:54321"
	missingReq.Header.Set("Origin", "http://127.0.0.1:7717")
	missingRes := httptest.NewRecorder()
	handler.ServeHTTP(missingRes, missingReq)
	if missingRes.Code != http.StatusNotFound {
		t.Fatalf("missing status code = %d, want 404", missingRes.Code)
	}
}

func TestRunJobHTTPCancelRoute(t *testing.T) {
	startedRequest := make(chan struct{}, 1)
	server, requests, _ := runJobTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		startedRequest <- struct{}{}
		<-r.Context().Done()
	}, 1)
	requests[0].Spec.URL += "/slow"
	if err := server.DB.UpdateRequest(&requests[0]); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	body, _ := json.Marshal(map[string]any{"collectionId": requests[0].CollectionID})
	startReq := httptest.NewRequest(http.MethodPost, "/api/run-jobs", bytes.NewReader(body))
	setRunJobLocalOrigin(startReq)
	startRes := httptest.NewRecorder()
	handler.ServeHTTP(startRes, startReq)
	if startRes.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", startRes.Code, startRes.Body.String())
	}
	var started runJobSnapshot
	if err := json.Unmarshal(startRes.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/run-jobs/"+started.ID+"/cancel", nil)
	setRunJobLocalOrigin(cancelReq)
	cancelRes := httptest.NewRecorder()
	handler.ServeHTTP(cancelRes, cancelReq)
	if cancelRes.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", cancelRes.Code, cancelRes.Body.String())
	}
	finished := waitRunJob(t, server.runJobs, started.ID, "cancelled")
	if !finished.Cancelled || finished.Completed != 0 || finished.CancelledCount != 1 || finished.Total != 1 {
		t.Fatalf("cancelled job = %#v", finished)
	}
}

func setRunJobLocalOrigin(r *http.Request) {
	r.Host = "127.0.0.1:7717"
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set("Origin", "http://127.0.0.1:7717")
}
