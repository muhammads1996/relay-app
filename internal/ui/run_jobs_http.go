package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func (s *Server) handleRunJobStart(w http.ResponseWriter, r *http.Request) {
	if s.runJobs == nil {
		httpError(w, http.StatusServiceUnavailable, fmt.Errorf("collection run jobs are unavailable"))
		return
	}
	if s.isVersioned() {
		if err := s.refreshWorkspace(); err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
	}
	var in struct {
		CollectionID int64  `json:"collectionId"`
		Env          string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if in.CollectionID <= 0 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("collectionId is required"))
		return
	}
	if in.Env != "" {
		if s.isVersioned() {
			if _, _, ok := s.canonicalEnv(in.Env); !ok {
				httpError(w, http.StatusNotFound, fmt.Errorf("environment %q not found in workspace files", in.Env))
				return
			}
		} else if _, err := s.DB.Environment(in.Env); err != nil {
			httpError(w, http.StatusNotFound, fmt.Errorf("environment %q not found", in.Env))
			return
		}
	}
	requests, err := s.DB.Requests(in.CollectionID)
	if err != nil || len(requests) == 0 {
		httpError(w, http.StatusNotFound, fmt.Errorf("collection %d has no requests", in.CollectionID))
		return
	}
	if s.isVersioned() {
		current := requests[:0]
		for _, req := range requests {
			if _, _, ok := s.canonicalRequest(req.ID); ok {
				current = append(current, req)
			}
		}
		requests = current
		if len(requests) == 0 {
			httpError(w, http.StatusNotFound, fmt.Errorf("collection %d has no requests in workspace files", in.CollectionID))
			return
		}
	}
	// The request context ends when this endpoint responds. Runs are instead
	// tied to the server lifetime (Background until Server gains a shutdown ctx).
	job, err := s.runJobs.Start(context.Background(), s, requests, in.Env)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrRunJobCapacity):
			status = http.StatusTooManyRequests
		case errors.Is(err, ErrRunJobTooManyRequests):
			status = http.StatusRequestEntityTooLarge
		}
		httpError(w, status, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, job)
}

func (s *Server) handleRunJobStatus(w http.ResponseWriter, r *http.Request) {
	if s.runJobs == nil {
		httpError(w, http.StatusServiceUnavailable, fmt.Errorf("collection run jobs are unavailable"))
		return
	}
	job, err := s.runJobs.Status(r.PathValue("id"))
	if errors.Is(err, ErrRunJobNotFound) {
		httpError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, job)
}

func (s *Server) handleRunJobCancel(w http.ResponseWriter, r *http.Request) {
	if s.runJobs == nil {
		httpError(w, http.StatusServiceUnavailable, fmt.Errorf("collection run jobs are unavailable"))
		return
	}
	job, err := s.runJobs.Cancel(r.PathValue("id"))
	if errors.Is(err, ErrRunJobNotFound) {
		httpError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, job)
}
