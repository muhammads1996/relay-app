// Package runner executes a collection directory sequentially: every
// *.req.toml under the root, in lexical order, with headers and vars
// inherited from collection.toml/folder.toml files along the path.
package runner

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/assert"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/script"
	"github.com/muhaymien96/relay/internal/vars"
)

// Options configure a run.
type Options struct {
	Env        *dsl.Environment
	Getenv     func(string) string
	Delay      time.Duration // pause between requests
	Bail       bool          // stop at first failure
	Tags       []string      // run only requests containing all tags
	Priorities []string      // run only requests matching one of these priorities
	// Data makes the run data-driven: the whole collection executes once
	// per row, with the row's values as the highest-precedence variables.
	Data           []map[string]string
	Engine         engine.Options
	DisableCookies bool              // disable cookie persistence within this run
	OnStart        func(name string) // optional progress hooks
	OnDone         func(rr RequestResult)
	OnProgress     func(ProgressEvent)
}

// ProgressEvent is a snapshot of collection execution. Completed counts files
// processed (including filtered files); Results contains only executed files.
// Terminal is set for the final event, and Cancelled distinguishes a context
// cancellation from a normal finish.
type ProgressEvent struct {
	Current   string
	Completed int
	Total     int
	Results   []RequestResult
	Elapsed   time.Duration
	Terminal  bool
	Cancelled bool
	Error     string
}

// ScriptTest is one pm.test result from a test script.
type ScriptTest struct {
	Name   string
	Passed bool
	Error  string
}

// RequestResult is the outcome of one request in the run.
type RequestResult struct {
	Name         string
	File         string
	Method       string
	URL          string
	XrayKey      string
	Requirements []string
	Tags         []string
	Priority     string
	Status       int
	Iteration    int // 1-based when the run is data-driven, else 0
	Duration     time.Duration
	Err          error // transport/resolution error; assertions not evaluated
	Assertions   []assert.Outcome
	ScriptTests  []ScriptTest // results from pm.test() calls in test scripts
	skip         bool
}

// Failed reports whether the request errored or any assertion/script test failed.
func (r RequestResult) Failed() bool {
	if r.Err != nil {
		return true
	}
	for _, a := range r.Assertions {
		if !a.Passed {
			return true
		}
	}
	for _, t := range r.ScriptTests {
		if !t.Passed {
			return true
		}
	}
	return false
}

func (r RequestResult) GetName() string        { return r.Name }
func (r RequestResult) GetTestKey() string     { return r.XrayKey }
func (r RequestResult) IsFailed() bool         { return r.Failed() }
func (r RequestResult) GetDurationMs() float64 { return float64(r.Duration.Microseconds()) / 1000 }
func (r RequestResult) GetComment() string {
	if r.Err != nil {
		return r.Err.Error()
	}
	var parts []string
	for _, a := range r.Assertions {
		if !a.Passed {
			parts = append(parts, "assert "+a.Assertion.Type+": "+a.Message)
		}
	}
	for _, t := range r.ScriptTests {
		if !t.Passed {
			parts = append(parts, "pm.test "+t.Name+": "+t.Error)
		}
	}
	return strings.Join(parts, "\n")
}

// Report is a completed run.
type Report struct {
	Root     string
	Started  time.Time
	Duration time.Duration
	Results  []RequestResult
}

// Failures counts failed requests.
func (r *Report) Failures() int {
	n := 0
	for _, res := range r.Results {
		if res.Failed() {
			n++
		}
	}
	return n
}

func (r *Report) GetResults() []RequestResult { return r.Results }
func (r *Report) GetStarted() time.Time       { return r.Started }
func (r *Report) GetFinished() time.Time      { return r.Started.Add(r.Duration) }

// Run executes every request under root.
func Run(ctx context.Context, root string, opts Options) (*Report, error) {
	files, err := Collect(root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.req.toml files under %s", root)
	}
	// Each run gets a fresh session by default. Callers can explicitly provide
	// a session in Engine.Cookies to share it, or opt out with DisableCookies.
	if opts.DisableCookies {
		opts.Engine.Cookies = nil
	} else if opts.Engine.Cookies == nil {
		opts.Engine.Cookies = engine.NewCookieSession()
	}

	rows := opts.Data
	if len(rows) == 0 {
		rows = []map[string]string{nil}
	}

	total := len(files) * len(rows)
	report := &Report{Root: root, Started: time.Now()}
	completed := 0
	emitProgress := func(current string, terminal, cancelled bool, err error) {
		if opts.OnProgress == nil {
			return
		}
		event := ProgressEvent{
			Current: current, Completed: completed, Total: total,
			Results: append([]RequestResult(nil), report.Results...),
			Elapsed: time.Since(report.Started), Terminal: terminal, Cancelled: cancelled,
		}
		if err != nil {
			event.Error = err.Error()
		}
		opts.OnProgress(event)
	}
	first := true
	sessionVars := map[string]string{}
loop:
	for iter, row := range rows {
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				report.Duration = time.Since(report.Started)
				emitProgress("", true, true, err)
				return report, err
			}
			if !first && opts.Delay > 0 {
				select {
				case <-time.After(opts.Delay):
				case <-ctx.Done():
					report.Duration = time.Since(report.Started)
					emitProgress("", true, true, ctx.Err())
					return report, ctx.Err()
				}
			}
			first = false
			emitProgress(filepath.Base(f), false, false, nil)
			rr := runOne(ctx, root, f, row, sessionVars, opts)
			completed++
			if rr.skip {
				emitProgress(filepath.Base(f), false, false, nil)
				continue
			}
			if len(opts.Data) > 0 {
				rr.Iteration = iter + 1
				rr.Name = fmt.Sprintf("%s [%d]", rr.Name, rr.Iteration)
			}
			if opts.OnDone != nil {
				opts.OnDone(rr)
			}
			report.Results = append(report.Results, rr)
			emitProgress(filepath.Base(f), false, false, nil)
			if opts.Bail && rr.Failed() {
				break loop
			}
		}
	}
	report.Duration = time.Since(report.Started)
	if err := ctx.Err(); err != nil {
		emitProgress("", true, true, err)
		return report, err
	}
	emitProgress("", true, false, nil)
	return report, nil
}

// RunWithProgress executes a collection and reports progress snapshots through
// callback. Cancellation uses ctx; the returned report retains completed work.
func RunWithProgress(ctx context.Context, root string, opts Options, callback func(ProgressEvent)) (*Report, error) {
	previous := opts.OnProgress
	if previous != nil && callback != nil {
		opts.OnProgress = func(event ProgressEvent) {
			previous(event)
			callback(event)
		}
	} else if callback != nil {
		opts.OnProgress = callback
	}
	return Run(ctx, root, opts)
}

func runOne(ctx context.Context, root, file string, row map[string]string, sessionVars map[string]string, opts Options) RequestResult {
	rr := RequestResult{File: file, Name: filepath.Base(file)}
	req, err := dsl.LoadRequest(file)
	if err != nil {
		rr.Err = err
		return rr
	}
	if req.Name != "" {
		rr.Name = req.Name
	}
	rr.Method = req.Method
	rr.XrayKey = req.XrayKey
	rr.Requirements = append([]string(nil), req.Requirements...)
	rr.Tags = append([]string(nil), req.Tags...)
	rr.Priority = req.Priority
	if !matchesRunFilters(req, opts) {
		rr.skip = true
		return rr
	}
	if opts.OnStart != nil {
		opts.OnStart(rr.Name)
	}

	headers, scopeVars, err := InheritedConfig(root, file)
	if err != nil {
		rr.Err = err
		return rr
	}
	getenv := opts.Getenv
	envVars := map[string]string{}
	if opts.Env != nil {
		for k, v := range opts.Env.Vars {
			envVars[k] = v
		}
	}

	collectionVars := map[string]string{}
	for k, v := range scopeVars {
		collectionVars[k] = v
	}
	for k, v := range sessionVars {
		collectionVars[k] = v
	}

	scope := vars.NewScope(row, req.Vars, collectionVars)
	if err := scope.AddEnvironment(opts.Env, getenv); err != nil {
		rr.Err = err
		return rr
	}

	// Pre-request script.
	if req.Scripts != nil && req.Scripts.PreRequest != "" {
		sc := &script.Scope{Env: envVars, Collection: collectionVars, Request: row}
		sr := script.RunPreRequest(req.Scripts.PreRequest, sc)
		// Propagate variable mutations back into the scope.
		for k, v := range sr.UpdatedVars {
			envVars[k] = v
			collectionVars[k] = v
			sessionVars[k] = v
		}
		if len(sr.Errors) > 0 {
			rr.Err = fmt.Errorf("pre-request script: %s", strings.Join(sr.Errors, "; "))
			return rr
		}
		// Rebuild scope with mutated vars.
		scope = vars.NewScope(row, req.Vars, collectionVars, envVars)
		if err := scope.AddEnvironment(opts.Env, getenv); err != nil {
			rr.Err = err
			return rr
		}
	}

	resolved, err := vars.Resolve(req, headers, scope)
	if err != nil {
		rr.Err = err
		return rr
	}
	rr.URL = resolved.URL

	start := time.Now()
	result, err := engine.Send(ctx, resolved, opts.Engine)
	rr.Duration = time.Since(start)
	if err != nil {
		rr.Err = err
		return rr
	}
	rr.Status = result.Status
	resolvedAsserts, err := ResolveAssertions(req.Assertions, scope)
	if err != nil {
		rr.Err = err
		return rr
	}
	rr.Assertions = assert.Evaluate(resolvedAsserts, result)

	// Post-response test script.
	if req.Scripts != nil && req.Scripts.Tests != "" {
		respHeaders := map[string]string{}
		for k := range result.Headers {
			respHeaders[k] = result.Headers.Get(k)
		}
		sc := &script.Scope{Env: envVars, Collection: collectionVars, Request: row}
		sr := script.RunTests(req.Scripts.Tests, sc, &script.Response{
			Code:       result.Status,
			Status:     result.StatusText,
			Headers:    respHeaders,
			Body:       result.Body,
			DurationMs: float64(rr.Duration.Microseconds()) / 1000,
		})
		for _, t := range sr.Tests {
			rr.ScriptTests = append(rr.ScriptTests, ScriptTest{Name: t.Name, Passed: t.Passed, Error: t.Error})
		}
		for k, v := range sr.UpdatedVars {
			sessionVars[k] = v
		}
		if len(sr.Errors) > 0 {
			// Script runtime errors (syntax, timeout) become a result error.
			rr.Err = fmt.Errorf("test script: %s", strings.Join(sr.Errors, "; "))
		}
	}

	return rr
}

func matchesRunFilters(req *dsl.Request, opts Options) bool {
	if len(opts.Tags) > 0 {
		have := map[string]bool{}
		for _, t := range req.Tags {
			have[strings.ToLower(strings.TrimSpace(t))] = true
		}
		for _, t := range opts.Tags {
			if !have[strings.ToLower(strings.TrimSpace(t))] {
				return false
			}
		}
	}
	if len(opts.Priorities) > 0 {
		p := strings.ToLower(strings.TrimSpace(req.Priority))
		ok := false
		for _, want := range opts.Priorities {
			if p == strings.ToLower(strings.TrimSpace(want)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// ResolveAssertions interpolates {{variables}} in assertion fields so
// data-driven rows (and the UI runner) can drive expected values, not just
// inputs.
func ResolveAssertions(in []dsl.Assertion, scope *vars.Scope) ([]dsl.Assertion, error) {
	out := make([]dsl.Assertion, len(in))
	for i, a := range in {
		var err error
		if s, ok := a.Equals.(string); ok {
			if a.Equals, err = scope.Interpolate(s); err != nil {
				return nil, fmt.Errorf("assertion %s: %w", a.Type, err)
			}
		}
		if s, ok := a.Exp.(string); ok {
			if a.Exp, err = scope.Interpolate(s); err != nil {
				return nil, fmt.Errorf("assertion %s: %w", a.Type, err)
			}
		}
		for _, f := range []*string{&a.Path, &a.Name, &a.Contains, &a.Field, &a.Code} {
			if *f == "" {
				continue
			}
			if *f, err = scope.Interpolate(*f); err != nil {
				return nil, fmt.Errorf("assertion %s: %w", a.Type, err)
			}
		}
		out[i] = a
	}
	return out, nil
}

// InheritedConfig merges headers and vars from collection.toml/folder.toml
// files on the path from root down to the request's directory. Deeper files
// win. Exported for the porters, which walk collections the same way.
func InheritedConfig(root, file string) (headers, scopeVars map[string]string, err error) {
	headers = map[string]string{}
	scopeVars = map[string]string{}
	rel, err := filepath.Rel(root, filepath.Dir(file))
	if err != nil {
		return nil, nil, err
	}
	dirs := []string{root}
	if rel != "." {
		cur := root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			dirs = append(dirs, cur)
		}
	}
	for _, d := range dirs {
		for _, name := range []string{"collection.toml", "folder.toml"} {
			cfg, err := dsl.LoadConfig(filepath.Join(d, name))
			if err != nil {
				return nil, nil, err
			}
			if cfg == nil {
				continue
			}
			for k, v := range cfg.Headers {
				headers[k] = v
			}
			for k, v := range cfg.Vars {
				scopeVars[k] = v
			}
		}
	}
	return headers, scopeVars, nil
}

// Collect returns every *.req.toml under root in lexical order.
func Collect(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".req.toml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
