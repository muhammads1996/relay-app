package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

const workspaceRefreshInterval = 5 * time.Second

type workspaceRefreshStatus struct {
	Generation uint64 `json:"generation"`
	Scanning   bool   `json:"scanning"`
	LastScan   string `json:"lastScan"`
	Error      string `json:"error"`
}

type workspaceRefreshFlight struct {
	done chan struct{}
	err  error
}

// refreshWorkspace joins an in-flight refresh so concurrent state reads and
// explicit refresh requests never start overlapping workspace reconciliations.
func (s *Server) refreshWorkspace() error {
	s.workspaceScanMu.Lock()
	flight := s.workspaceFlight
	if flight == nil {
		flight = s.beginWorkspaceRefreshLocked()
		s.workspaceScanMu.Unlock()
		return s.performWorkspaceRefresh(flight, s.refreshWorkspaceOnce)
	}
	return s.waitForWorkspaceRefreshLocked(flight)
}

func (s *Server) runWorkspaceRefresh(scan func() error) error {
	s.workspaceScanMu.Lock()
	flight := s.workspaceFlight
	if flight == nil {
		flight = s.beginWorkspaceRefreshLocked()
		s.workspaceScanMu.Unlock()
		return s.performWorkspaceRefresh(flight, scan)
	}
	return s.waitForWorkspaceRefreshLocked(flight)
}

func (s *Server) waitForWorkspaceRefreshLocked(flight *workspaceRefreshFlight) error {
	s.workspaceRefreshWaiters++
	s.workspaceScanMu.Unlock()
	<-flight.done
	s.workspaceScanMu.Lock()
	s.workspaceRefreshWaiters--
	returnErr := flight.err
	s.workspaceScanMu.Unlock()
	return returnErr
}

func (s *Server) beginWorkspaceRefreshLocked() *workspaceRefreshFlight {
	flight := &workspaceRefreshFlight{done: make(chan struct{})}
	s.workspaceFlight = flight
	s.workspaceScan.Scanning = true
	s.workspaceScan.Error = ""
	s.lastScanAttempt = time.Now()
	return flight
}

func (s *Server) performWorkspaceRefresh(flight *workspaceRefreshFlight, scan func() error) error {
	err := scan()
	generation := uint64(0)
	if err == nil {
		s.workspaceMu.RLock()
		generation = s.workspaceGeneration
		s.workspaceMu.RUnlock()
	}

	s.workspaceScanMu.Lock()
	if err != nil {
		s.workspaceScan.Error = "Workspace scan failed. Current files could not be verified."
	} else {
		s.workspaceScan.Generation = generation
		s.workspaceScan.LastScan = time.Now().UTC().Format(time.RFC3339Nano)
		s.workspaceScan.Error = ""
	}
	s.workspaceScan.Scanning = false
	flight.err = err
	close(flight.done)
	s.workspaceFlight = nil
	s.workspaceScanMu.Unlock()
	return err
}

func (s *Server) scheduleWorkspaceRefresh() {
	if s.WorkspaceRoot == "" {
		return
	}
	s.workspaceScanMu.Lock()
	if s.workspaceFlight != nil || time.Since(s.lastScanAttempt) < workspaceRefreshInterval {
		s.workspaceScanMu.Unlock()
		return
	}
	flight := s.beginWorkspaceRefreshLocked()
	s.workspaceScanMu.Unlock()
	go func() { _ = s.performWorkspaceRefresh(flight, s.refreshWorkspaceOnce) }()
}

func workspaceFingerprint(ws *workspacepkg.Workspace) string {
	if ws == nil || ws.Legacy {
		return ""
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "manifest\x00%s\x00", ws.Manifest.Hash)
	for _, c := range ws.Collections {
		_, _ = fmt.Fprintf(h, "collection\x00%s\x00%s\x00%s\x00", c.ID, c.Path, c.Hash)
	}
	for _, f := range ws.Folders {
		_, _ = fmt.Fprintf(h, "folder\x00%s\x00%s\x00%s\x00", f.ID, f.Path, f.Hash)
	}
	for _, r := range ws.Requests {
		_, _ = fmt.Fprintf(h, "request\x00%s\x00%s\x00%s\x00", r.ID, r.Path, r.Hash)
	}
	for _, e := range ws.Environments {
		_, _ = fmt.Fprintf(h, "environment\x00%s\x00%s\x00%s\x00", e.ID, e.Path, e.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) handleWorkspaceRefreshStatus(w http.ResponseWriter, r *http.Request) {
	s.scheduleWorkspaceRefresh()
	writeJSON(w, s.workspaceRefreshStatus())
}

func (s *Server) handleWorkspaceRefresh(w http.ResponseWriter, r *http.Request) {
	if s.WorkspaceRoot == "" {
		httpError(w, http.StatusNotFound, fmt.Errorf("workspace files are not enabled"))
		return
	}
	if err := s.refreshWorkspace(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, s.workspaceRefreshStatus())
		return
	}
	writeJSON(w, s.workspaceRefreshStatus())
}

func (s *Server) workspaceRefreshStatus() workspaceRefreshStatus {
	s.workspaceScanMu.Lock()
	defer s.workspaceScanMu.Unlock()
	return s.workspaceScan
}
