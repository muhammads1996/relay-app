package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/muhaymien96/relay/internal/store"
)

const (
	maxRunJobs        = 32
	maxRunJobRequests = 1000
	defaultRunJobTTL  = 30 * time.Minute
)

var ErrRunJobNotFound = errors.New("collection run job not found")
var ErrRunJobCapacity = errors.New("collection run job capacity reached")
var ErrRunJobTooManyRequests = errors.New("collection run request limit exceeded")

// runJobManager is intended to be owned by one Server instance. It keeps no
// package-global state, and completed jobs are retained only for a short TTL.
type runJobManager struct {
	mu   sync.Mutex
	jobs map[string]*runJob
	ttl  time.Duration
	max  int
}

type runJob struct {
	id             string
	server         *Server
	requests       []store.Request
	env            string
	cancel         context.CancelFunc
	started        time.Time
	finished       time.Time
	current        string
	completed      int
	total          int
	passed         int
	failed         int
	cancelledCount int
	results        []runResult
	state          string
	cancelled      bool
	terminalErr    string
}

type runJobSnapshot struct {
	ID             string      `json:"id"`
	State          string      `json:"state"`
	CurrentRequest string      `json:"currentRequest,omitempty"`
	Completed      int         `json:"completed"`
	Total          int         `json:"total"`
	Passed         int         `json:"passed"`
	Failed         int         `json:"failed"`
	CancelledCount int         `json:"cancelledCount"`
	Results        []runResult `json:"results"`
	ElapsedMs      float64     `json:"elapsedMs"`
	StartedAt      time.Time   `json:"startedAt"`
	FinishedAt     *time.Time  `json:"finishedAt,omitempty"`
	Cancelled      bool        `json:"cancelled"`
	Error          string      `json:"error,omitempty"`
}

func newRunJobManager() *runJobManager {
	return &runJobManager{jobs: make(map[string]*runJob), ttl: defaultRunJobTTL, max: maxRunJobs}
}

// Start begins an asynchronous sequential run over an immutable copy of the
// supplied requests. The manager belongs to a single Server, so jobs cannot be
// observed or cancelled through another workspace's manager.
func (m *runJobManager) Start(parent context.Context, server *Server, requests []store.Request, env string) (runJobSnapshot, error) {
	if server == nil {
		return runJobSnapshot{}, fmt.Errorf("server is required")
	}
	if len(requests) == 0 {
		return runJobSnapshot{}, fmt.Errorf("collection has no requests")
	}
	if len(requests) > maxRunJobRequests {
		return runJobSnapshot{}, fmt.Errorf("%w: collection has %d requests; async runs are limited to %d", ErrRunJobTooManyRequests, len(requests), maxRunJobRequests)
	}
	if parent == nil {
		parent = context.Background()
	}
	cloned, err := cloneRunRequests(requests)
	if err != nil {
		return runJobSnapshot{}, fmt.Errorf("copying collection requests: %w", err)
	}
	id, err := newRunJobID()
	if err != nil {
		return runJobSnapshot{}, fmt.Errorf("creating run job id: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	job := &runJob{
		id: id, server: server, requests: cloned, env: env, cancel: cancel,
		started: time.Now(), total: len(cloned), state: "running",
		results: make([]runResult, 0, len(cloned)),
	}

	m.mu.Lock()
	m.cleanupLocked(time.Now())
	if m.max <= 0 {
		m.max = maxRunJobs
	}
	if len(m.jobs) >= m.max {
		m.evictOldestCompletedLocked()
	}
	if len(m.jobs) >= m.max {
		m.mu.Unlock()
		cancel()
		return runJobSnapshot{}, fmt.Errorf("%w (%d jobs)", ErrRunJobCapacity, m.max)
	}
	m.jobs[id] = job
	snapshot := snapshotRunJob(job, time.Now())
	m.mu.Unlock()

	go m.run(ctx, job)
	return snapshot, nil
}

// Status returns a detached snapshot. Completed jobs expire after the manager
// TTL; expiration is swept on Start, Status, and Cancel calls.
func (m *runJobManager) Status(id string) (runJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(time.Now())
	job := m.jobs[id]
	if job == nil {
		return runJobSnapshot{}, ErrRunJobNotFound
	}
	return snapshotRunJob(job, time.Now()), nil
}

// Cancel requests context cancellation. A running request is allowed to
// return its result; no subsequent request starts after cancellation.
func (m *runJobManager) Cancel(id string) (runJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(time.Now())
	job := m.jobs[id]
	if job == nil {
		return runJobSnapshot{}, ErrRunJobNotFound
	}
	if !isRunJobTerminal(job.state) {
		job.cancel()
	}
	return snapshotRunJob(job, time.Now()), nil
}

func (m *runJobManager) run(ctx context.Context, job *runJob) {
	for i := range job.requests {
		if ctx.Err() != nil {
			break
		}
		req := &job.requests[i]
		name := ""
		if req.Spec != nil {
			name = req.Spec.Name
		}
		m.mu.Lock()
		if isRunJobTerminal(job.state) {
			m.mu.Unlock()
			return
		}
		job.current = name
		m.mu.Unlock()

		rr := job.executeRequest(ctx, req)
		m.mu.Lock()
		job.results = append(job.results, rr)
		if rr.Cancelled {
			job.cancelledCount++
		} else {
			job.completed++
			if rr.Passed {
				job.passed++
			} else {
				job.failed++
			}
		}
		job.current = ""
		m.mu.Unlock()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	job.finished = time.Now()
	job.current = ""
	if ctx.Err() != nil {
		job.state = "cancelled"
		job.cancelled = true
		job.terminalErr = ctx.Err().Error()
	} else {
		job.state = "completed"
	}
	job.cancel()
	m.cleanupLocked(job.finished)
}

func (job *runJob) executeRequest(ctx context.Context, req *store.Request) runResult {
	rr := runResult{}
	if req.Spec == nil {
		rr.Error = "request has no specification"
		return rr
	}
	rr.RequestID = req.ID
	rr.Name = req.Spec.Name
	rr.Method = req.Spec.Method
	rr.URL = req.Spec.URL
	started := time.Now()
	out, _, err := job.server.execute(ctx, req, job.env, false)
	rr.DurationMs = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		if ctx.Err() != nil {
			rr.Cancelled = true
			rr.CancellationReason = "Request cancelled before it finished"
			return rr
		}
		rr.Error = err.Error()
		return rr
	}
	if out == nil {
		rr.Error = "request execution returned no result"
		return rr
	}
	rr.URL = out.URL
	rr.Status = out.Status
	rr.DurationMs = out.Timing["total"]
	rr.Assertions = append([]assertionResult(nil), out.Assertions...)
	rr.ScriptTests = append([]scriptTestResult(nil), out.ScriptTests...)
	rr.Passed = true
	for _, assertion := range rr.Assertions {
		if !assertion.Passed {
			rr.Passed = false
		}
	}
	for _, test := range rr.ScriptTests {
		if !test.Passed {
			rr.Passed = false
		}
	}
	return rr
}

func (m *runJobManager) cleanupLocked(now time.Time) {
	ttl := m.ttl
	if ttl <= 0 {
		ttl = defaultRunJobTTL
	}
	for id, job := range m.jobs {
		if isRunJobTerminal(job.state) && !job.finished.IsZero() && now.Sub(job.finished) >= ttl {
			delete(m.jobs, id)
		}
	}
}

func (m *runJobManager) evictOldestCompletedLocked() {
	var oldestID string
	var oldest time.Time
	for id, job := range m.jobs {
		if !isRunJobTerminal(job.state) {
			continue
		}
		if oldestID == "" || job.finished.Before(oldest) {
			oldestID, oldest = id, job.finished
		}
	}
	if oldestID != "" {
		delete(m.jobs, oldestID)
	}
}

func snapshotRunJob(job *runJob, now time.Time) runJobSnapshot {
	end := job.finished
	elapsed := now.Sub(job.started)
	var finishedAt *time.Time
	if !end.IsZero() {
		elapsed = end.Sub(job.started)
		finishedAt = &end
	}
	results := append([]runResult(nil), job.results...)
	for i := range results {
		results[i].Assertions = append([]assertionResult(nil), results[i].Assertions...)
		results[i].ScriptTests = append([]scriptTestResult(nil), results[i].ScriptTests...)
	}
	return runJobSnapshot{
		ID: job.id, State: job.state, CurrentRequest: job.current,
		Completed: job.completed, Total: job.total, Passed: job.passed, Failed: job.failed, CancelledCount: job.cancelledCount,
		Results: results, ElapsedMs: float64(elapsed.Microseconds()) / 1000,
		StartedAt: job.started, FinishedAt: finishedAt, Cancelled: job.cancelled,
		Error: job.terminalErr,
	}
}

func isRunJobTerminal(state string) bool { return state == "completed" || state == "cancelled" }

func cloneRunRequests(requests []store.Request) ([]store.Request, error) {
	data, err := json.Marshal(requests)
	if err != nil {
		return nil, err
	}
	var cloned []store.Request
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func newRunJobID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
