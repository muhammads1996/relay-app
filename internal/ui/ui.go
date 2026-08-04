// Package ui serves the Relay workbench (embedded single-page app) from the
// relay binary: collections, request builder, runner, history, environments
// and header presets, all backed by the SQLite store. It binds to localhost
// only. The same handler powers `relay ui` (browser) and relay-app (Wails
// desktop window).
package ui

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/adapters/tm"
	"github.com/muhaymien96/relay/internal/adapters/xray"
	"github.com/muhaymien96/relay/internal/assert"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/porter"
	"github.com/muhaymien96/relay/internal/runner"
	"github.com/muhaymien96/relay/internal/script"
	"github.com/muhaymien96/relay/internal/store"
	"github.com/muhaymien96/relay/internal/vars"
)

//go:embed index.html
var indexHTML []byte

// Server hosts the workbench for one store.
type Server struct {
	DB     *store.Store
	Engine engine.Options
	Getenv func(string) string
}

// Handler returns the HTTP handler (exported for tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/state", s.handleState)

	mux.HandleFunc("POST /api/collections", s.handleCollectionCreate)
	mux.HandleFunc("PATCH /api/collections/{id}", s.handleCollectionUpdate)
	mux.HandleFunc("DELETE /api/collections/{id}", s.handleCollectionDelete)

	mux.HandleFunc("POST /api/folders", s.handleFolderCreate)
	mux.HandleFunc("PATCH /api/folders/{id}", s.handleFolderUpdate)
	mux.HandleFunc("DELETE /api/folders/{id}", s.handleFolderDelete)

	mux.HandleFunc("POST /api/requests", s.handleRequestCreate)
	mux.HandleFunc("GET /api/requests/{id}", s.handleRequestGet)
	mux.HandleFunc("PUT /api/requests/{id}", s.handleRequestUpdate)
	mux.HandleFunc("DELETE /api/requests/{id}", s.handleRequestDelete)
	mux.HandleFunc("GET /api/requests/{id}/stats", s.handleRequestStats)
	mux.HandleFunc("GET /api/requests/{id}/curl", s.handleRequestCurl)

	mux.HandleFunc("GET /api/environments", s.handleEnvList)
	mux.HandleFunc("PUT /api/environments/{name}", s.handleEnvPut)
	mux.HandleFunc("DELETE /api/environments/{name}", s.handleEnvDelete)

	mux.HandleFunc("GET /api/presets", s.handlePresetList)
	mux.HandleFunc("POST /api/presets", s.handlePresetCreate)
	mux.HandleFunc("PUT /api/presets/{id}", s.handlePresetUpdate)
	mux.HandleFunc("DELETE /api/presets/{id}", s.handlePresetDelete)

	mux.HandleFunc("POST /api/send", s.handleSend)
	mux.HandleFunc("POST /api/run", s.handleRun)

	mux.HandleFunc("GET /api/history", s.handleHistoryList)
	mux.HandleFunc("GET /api/history/{id}", s.handleHistoryGet)

	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("PUT /api/settings", s.handleSettingsPut)

	mux.HandleFunc("POST /api/import/postman", s.handleImportPostman)
	mux.HandleFunc("POST /api/import/curl", s.handleImportCurl)
	mux.HandleFunc("POST /api/import/openapi", s.handleImportOpenAPI)
	mux.HandleFunc("GET /api/export", s.handleExport)

	mux.HandleFunc("GET /api/tests/state", s.handleTestsState)
	mux.HandleFunc("POST /api/tests", s.handleTestCreate)
	mux.HandleFunc("POST /api/tests/export", s.handleTestsExport)
	mux.HandleFunc("POST /api/tests/run", s.handleTestsRun)
	mux.HandleFunc("GET /api/tests/{id}", s.handleTestGet)
	mux.HandleFunc("PUT /api/tests/{id}", s.handleTestUpdate)
	mux.HandleFunc("DELETE /api/tests/{id}", s.handleTestDelete)
	mux.HandleFunc("POST /api/tests/{id}/run", s.handleTestRun)
	mux.HandleFunc("POST /api/test-folders", s.handleTestFolderCreate)
	mux.HandleFunc("PATCH /api/test-folders/{id}", s.handleTestFolderUpdate)
	mux.HandleFunc("DELETE /api/test-folders/{id}", s.handleTestFolderDelete)
	mux.HandleFunc("POST /api/test-sets", s.handleTestSetCreate)
	mux.HandleFunc("PUT /api/test-sets/{id}", s.handleTestSetUpdate)
	mux.HandleFunc("DELETE /api/test-sets/{id}", s.handleTestSetDelete)
	mux.HandleFunc("POST /api/test-executions", s.handleTestExecutionCreate)
	mux.HandleFunc("GET /api/test-executions/{id}", s.handleTestExecutionGet)
	mux.HandleFunc("PUT /api/test-executions/{id}", s.handleTestExecutionUpdate)
	mux.HandleFunc("DELETE /api/test-executions/{id}", s.handleTestExecutionDelete)
	mux.HandleFunc("POST /api/test-executions/{id}/run", s.handleTestExecutionRun)
	mux.HandleFunc("POST /api/test-executions/{id}/xray-push", s.handleTestExecutionXrayPush)

	mux.HandleFunc("GET /api/xray/settings", s.handleXraySettingsGet)
	mux.HandleFunc("PUT /api/xray/settings", s.handleXraySettingsPut)
	mux.HandleFunc("PUT /api/xray/credentials", s.handleXrayCredentialsPut)
	mux.HandleFunc("POST /api/xray/test-connection", s.handleXrayTestConnection)
	mux.HandleFunc("POST /api/xray/tests/{id}/validate", s.handleXrayTestValidate)
	mux.HandleFunc("POST /api/xray/tests/{id}/create", s.handleXrayTestCreate)
	mux.HandleFunc("POST /api/xray/tests/{id}/link-requirements", s.handleXrayLinkRequirements)
	mux.HandleFunc("POST /api/xray/test-sets/{id}/create", s.handleXrayTestSetCreate)
	mux.HandleFunc("POST /api/xray/push", s.handleXrayPush)
	mux.HandleFunc("GET /api/xray/test", s.handleXrayTestGet)
	mux.HandleFunc("POST /api/xray/test", s.handleXrayRequestTestCreate)
	mux.HandleFunc("POST /api/xray/requirements/link", s.handleXrayRequirementsLink)
	return mux
}

// ListenAndServe binds to 127.0.0.1:port (0 picks a free one) and serves
// until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	fmt.Printf("relay ui: http://%s\n", ln.Addr())
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	set, err := s.DB.Settings()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, set)
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var set store.Settings
	if err := json.NewDecoder(r.Body).Decode(&set); err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.SaveSettings(set); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, set)
}

// engineOptions applies the stored workspace settings on top of the
// server's defaults for each send.
func (s *Server) engineOptions() engine.Options {
	opts := s.Engine
	set, err := s.DB.Settings()
	if err != nil {
		return opts
	}
	opts.Timeout = time.Duration(set.TimeoutSeconds) * time.Second
	opts.FollowRedirects = set.FollowRedirects
	opts.Insecure = set.Insecure
	return opts
}

func (s *Server) getenv() func(string) string {
	if s.Getenv != nil {
		return s.Getenv
	}
	return os.Getenv
}

// resolveStored resolves a stored request: preset headers (collection then
// folder level), collection/folder headers and vars, environment, auth.
// Returns the resolved request, the scope, and any preset secret values
// that must be masked in display surfaces.
func (s *Server) resolveStored(req *store.Request, envName string) (*vars.Resolved, *vars.Scope, []string, error) {
	return s.resolveStoredWithSession(req, envName, nil)
}

func (s *Server) resolveStoredWithSession(req *store.Request, envName string, sessionVars map[string]string) (*vars.Resolved, *vars.Scope, []string, error) {
	col, err := s.DB.Collection(req.CollectionID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("collection: %w", err)
	}
	var folder *store.Folder
	if req.FolderID != nil {
		if folder, err = s.DB.Folder(*req.FolderID); err != nil {
			return nil, nil, nil, fmt.Errorf("folder: %w", err)
		}
	}
	presetHeaders, presetSecrets, err := s.DB.PresetHeadersFor(req.CollectionID, req.FolderID)
	if err != nil {
		return nil, nil, nil, err
	}

	inherited := map[string]string{}
	for k, v := range presetHeaders {
		inherited[k] = v
	}
	for k, v := range col.Headers {
		inherited[k] = v
	}
	if folder != nil {
		for k, v := range folder.Headers {
			inherited[k] = v
		}
	}

	var folderVars map[string]string
	if folder != nil {
		folderVars = folder.Vars
	}
	scope := vars.NewScope(req.Spec.Vars, sessionVars, folderVars, col.Vars)
	if envName != "" {
		env, err := s.DB.Environment(envName)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("environment %q not found", envName)
		}
		if err := scope.AddEnvironment(&dsl.Environment{Vars: env.Vars, Secrets: env.Secrets}, s.getenv()); err != nil {
			return nil, nil, nil, err
		}
	}

	resolved, err := vars.Resolve(req.Spec, inherited, scope)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolved, scope, presetSecrets, nil
}

func mask(s string, scope *vars.Scope, extraSecrets []string) string {
	s = scope.MaskSecrets(s)
	for _, v := range extraSecrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "••••••")
		}
	}
	return s
}

// handleRequestCurl returns a copy-pasteable curl command for a request.
// Environment secret values are replaced with $RELAY_SECRET_* shell
// references and preset secret values are masked, so the command never
// carries secret material.
func (s *Server) handleRequestCurl(w http.ResponseWriter, r *http.Request) {
	id, err := atoi64(r.PathValue("id"))
	if err != nil {
		httpError(w, 400, fmt.Errorf("bad id %q", r.PathValue("id")))
		return
	}
	req, err := s.DB.Request(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", id))
		return
	}
	resolved, scope, presetSecrets, err := s.resolveStored(req, r.URL.Query().Get("env"))
	if err != nil {
		httpError(w, 422, err)
		return
	}
	cmd := porter.Curl(resolved)
	for name, value := range scope.SecretValues() {
		if value != "" {
			// `'"$VAR"'` closes any surrounding single-quoted segment, lets
			// the shell expand the variable, and reopens the quote — correct
			// both inside and outside quoted arguments.
			cmd = strings.ReplaceAll(cmd, value, `'"$`+vars.SecretEnvVar(name)+`"'`)
		}
	}
	for _, value := range presetSecrets {
		if value != "" {
			cmd = strings.ReplaceAll(cmd, value, "••••••")
		}
	}
	writeJSON(w, map[string]string{"curl": cmd})
}

type sendResult struct {
	Method         string             `json:"method"`
	URL            string             `json:"url"`
	RequestHeaders map[string]string  `json:"requestHeaders"`
	HeaderOrigin   map[string]string  `json:"headerOrigin"`
	Status         int                `json:"status"`
	StatusText     string             `json:"statusText"`
	Proto          string             `json:"proto"`
	Headers        map[string]string  `json:"headers"`
	Body           string             `json:"body"`
	Truncated      bool               `json:"truncated"`
	Size           int64              `json:"size"`
	Timing         map[string]float64 `json:"timing"`
	Assertions     []assertionResult  `json:"assertions,omitempty"`
	ScriptTests    []scriptTestResult `json:"scriptTests,omitempty"`
}

type scriptTestResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

type assertionResult struct {
	Type    string `json:"type"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID int64  `json:"requestId"`
		Env       string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	req, err := s.DB.Request(in.RequestID)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", in.RequestID))
		return
	}
	out, status, err := s.execute(r.Context(), req, in.Env, true)
	if err != nil {
		httpError(w, status, err)
		return
	}
	writeJSON(w, out)
}

// execute resolves, sends, asserts, and (optionally) records history.
func (s *Server) execute(ctx context.Context, req *store.Request, envName string, record bool) (*sendResult, int, error) {
	return s.executeWithSession(ctx, req, envName, record, nil)
}

func (s *Server) executeWithSession(ctx context.Context, req *store.Request, envName string, record bool, sessionVars map[string]string) (*sendResult, int, error) {
	resolved, scope, presetSecrets, err := s.resolveStoredWithSession(req, envName, sessionVars)
	if err != nil {
		return nil, 422, err
	}
	result, err := engine.Send(ctx, resolved, s.engineOptions())
	if err != nil {
		return nil, 502, err
	}

	asserts, err := runner.ResolveAssertions(req.Spec.Assertions, scope)
	if err != nil {
		return nil, 422, err
	}
	outcomes := assert.Evaluate(asserts, result)

	out := &sendResult{
		Method:         resolved.Method,
		URL:            mask(resolved.URL, scope, presetSecrets),
		RequestHeaders: map[string]string{},
		HeaderOrigin:   resolved.HeaderOrigin,
		Status:         result.Status,
		StatusText:     result.StatusText,
		Proto:          result.Proto,
		Headers:        map[string]string{},
		Size:           result.Size,
		Timing: map[string]float64{
			"dns":      ms(result.Timing.DNS),
			"connect":  ms(result.Timing.Connect),
			"tls":      ms(result.Timing.TLS),
			"ttfb":     ms(result.Timing.TTFB),
			"download": ms(result.Timing.Download),
			"total":    ms(result.Timing.Total),
		},
	}
	for k := range resolved.Headers {
		out.RequestHeaders[k] = mask(resolved.Headers.Get(k), scope, presetSecrets)
	}
	for k := range result.Headers {
		out.Headers[k] = result.Headers.Get(k)
	}
	const uiBodyCap = 2 << 20
	body := result.Body
	if len(body) > uiBodyCap {
		body, out.Truncated = body[:uiBodyCap], true
	}
	out.Body = string(body)
	for _, o := range outcomes {
		out.Assertions = append(out.Assertions, assertionResult{Type: o.Assertion.Type, Passed: o.Passed, Message: o.Message})
	}

	if req.Spec.Scripts != nil && strings.TrimSpace(req.Spec.Scripts.Tests) != "" {
		respHeaders := map[string]string{}
		for k := range result.Headers {
			respHeaders[k] = result.Headers.Get(k)
		}

		envVars := map[string]string{}
		if envName != "" {
			if env, err := s.DB.Environment(envName); err == nil {
				for k, v := range env.Vars {
					envVars[k] = v
				}
			}
		}
		colVars := map[string]string{}
		if col, err := s.DB.Collection(req.CollectionID); err == nil {
			for k, v := range col.Vars {
				colVars[k] = v
			}
			if req.FolderID != nil {
				if folder, err := s.DB.Folder(*req.FolderID); err == nil {
					for k, v := range folder.Vars {
						colVars[k] = v
					}
				}
			}
		}
		for k, v := range sessionVars {
			colVars[k] = v
		}
		reqVars := map[string]string{}
		for k, v := range req.Spec.Vars {
			reqVars[k] = v
		}

		sr := script.RunTests(req.Spec.Scripts.Tests, &script.Scope{
			Env:        envVars,
			Collection: colVars,
			Request:    reqVars,
		}, &script.Response{
			Code:       result.Status,
			Status:     result.StatusText,
			Headers:    respHeaders,
			Body:       result.Body,
			DurationMs: out.Timing["total"],
		})
		for _, t := range sr.Tests {
			out.ScriptTests = append(out.ScriptTests, scriptTestResult{Name: t.Name, Passed: t.Passed, Error: t.Error})
		}
		for _, errMsg := range sr.Errors {
			out.ScriptTests = append(out.ScriptTests, scriptTestResult{Name: "Script runtime", Passed: false, Error: errMsg})
		}
		for k, v := range sr.UpdatedVars {
			if sessionVars != nil {
				sessionVars[k] = v
			}
		}
	}

	if record {
		_ = s.DB.AddHistory(&store.HistoryEntry{
			RequestID:   &req.ID,
			RequestName: req.Spec.Name,
			Method:      resolved.Method,
			URL:         out.URL,
			Status:      result.Status,
			DurationMs:  out.Timing["total"],
			RespHeaders: out.Headers,
			RespBody:    body,
			Timing:      out.Timing,
			SentAt:      time.Now(),
		})
	}
	return out, 200, nil
}

type runResult struct {
	RequestID   int64              `json:"requestId"`
	Name        string             `json:"name"`
	Method      string             `json:"method"`
	URL         string             `json:"url"`
	Status      int                `json:"status"`
	DurationMs  float64            `json:"durationMs"`
	Passed      bool               `json:"passed"`
	Error       string             `json:"error,omitempty"`
	Assertions  []assertionResult  `json:"assertions,omitempty"`
	ScriptTests []scriptTestResult `json:"scriptTests,omitempty"`
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CollectionID int64  `json:"collectionId"`
		Env          string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	requests, err := s.DB.Requests(in.CollectionID)
	if err != nil || len(requests) == 0 {
		httpError(w, 404, fmt.Errorf("collection %d has no requests", in.CollectionID))
		return
	}

	var results []runResult
	var durations []float64
	passed := 0
	started := time.Now()
	for i := range requests {
		req := &requests[i]
		rr := runResult{RequestID: req.ID, Name: req.Spec.Name, Method: req.Spec.Method, URL: req.Spec.URL}
		out, _, err := s.execute(r.Context(), req, in.Env, false)
		if err != nil {
			rr.Error = err.Error()
		} else {
			rr.URL = out.URL
			rr.Status = out.Status
			rr.DurationMs = out.Timing["total"]
			rr.Assertions = out.Assertions
			rr.ScriptTests = out.ScriptTests
			rr.Passed = true
			for _, a := range out.Assertions {
				if !a.Passed {
					rr.Passed = false
				}
			}
			for _, t := range out.ScriptTests {
				if !t.Passed {
					rr.Passed = false
				}
			}
			durations = append(durations, rr.DurationMs)
		}
		if rr.Passed {
			passed++
		}
		results = append(results, rr)
	}

	writeJSON(w, map[string]any{
		"results":    results,
		"executed":   len(results),
		"passed":     passed,
		"failed":     len(results) - passed,
		"p95Ms":      p95(durations),
		"durationMs": float64(time.Since(started).Microseconds()) / 1000,
		"finishedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

func p95(durations []float64) float64 {
	if len(durations) == 0 {
		return 0
	}
	sorted := append([]float64(nil), durations...)
	for i := 1; i < len(sorted); i++ { // insertion sort; n is tiny
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	idx := (len(sorted)*95 + 99) / 100
	if idx > 0 {
		idx--
	}
	return sorted[idx]
}

func (s *Server) handleImportPostman(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r, 20<<20)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tmp, err := os.MkdirTemp("", "relay-import-*")
	if err != nil {
		httpError(w, 500, err)
		return
	}
	defer os.RemoveAll(tmp)
	n, err := porter.ImportPostman(data, tmp)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	colID, err := s.DB.SeedFromDir(tmp)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"collectionId": colID, "requests": n})
}

// handleImportCurl parses a pasted curl command into a new request in the
// given collection.
func (s *Server) handleImportCurl(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CollectionID int64  `json:"collectionId"`
		FolderID     *int64 `json:"folderId"`
		Curl         string `json:"curl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	spec, err := porter.ParseCurl(in.Curl)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	if in.CollectionID == 0 {
		cols, err := s.DB.Collections()
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if len(cols) == 0 {
			col := &store.Collection{Name: "Imported", Headers: map[string]string{}, Vars: map[string]string{}}
			if err := s.DB.CreateCollection(col); err != nil {
				httpError(w, 500, err)
				return
			}
			in.CollectionID = col.ID
		} else {
			in.CollectionID = cols[0].ID
		}
	}
	req := &store.Request{CollectionID: in.CollectionID, FolderID: in.FolderID, Spec: spec}
	if err := s.DB.CreateRequest(req); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, req)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "environment" {
		name := r.URL.Query().Get("env")
		if name == "" {
			httpError(w, 400, fmt.Errorf("env query param required for environment export"))
			return
		}
		env, err := s.DB.Environment(name)
		if err != nil {
			httpError(w, 404, fmt.Errorf("environment %q not found", name))
			return
		}
		w.Header().Set("Content-Type", "application/toml; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.toml"`, downloadSlug(env.Name)))
		_, _ = w.Write(store.MarshalEnvironment(*env))
		return
	}
	if format == "curl" {
		reqID, err := atoi64(r.URL.Query().Get("request"))
		if err != nil {
			httpError(w, 400, fmt.Errorf("request query param required for curl export"))
			return
		}
		req, err := s.DB.Request(reqID)
		if err != nil {
			httpError(w, 404, fmt.Errorf("request %d not found", reqID))
			return
		}
		resolved, scope, presetSecrets, err := s.resolveStored(req, r.URL.Query().Get("env"))
		if err != nil {
			httpError(w, 422, err)
			return
		}
		cmd := porter.Curl(resolved)
		for name, value := range scope.SecretValues() {
			if value != "" {
				cmd = strings.ReplaceAll(cmd, value, `'$`+vars.SecretEnvVar(name)+`'`)
			}
		}
		for _, value := range presetSecrets {
			if value != "" {
				cmd = strings.ReplaceAll(cmd, value, "••••••")
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(cmd))
		return
	}

	colID, err := atoi64(r.URL.Query().Get("collection"))
	if err != nil {
		httpError(w, 400, fmt.Errorf("collection query param required"))
		return
	}
	tmp, err := os.MkdirTemp("", "relay-export-*")
	if err != nil {
		httpError(w, 500, err)
		return
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "collection")
	opts := store.CollectionExportOptions{}
	if r.URL.Query().Has("requests") {
		opts.FilterRequests = true
		opts.RequestIDs, err = parseExportIDs(r.URL.Query().Get("requests"))
		if err != nil {
			httpError(w, 400, err)
			return
		}
		if len(opts.RequestIDs) == 0 {
			httpError(w, 400, fmt.Errorf("requests query param must contain at least one request id"))
			return
		}
	}
	if r.URL.Query().Has("environments") {
		opts.FilterEnvironments = true
		raw := r.URL.Query().Get("environments")
		if raw == "*" {
			opts.FilterEnvironments = false
		} else {
			opts.EnvironmentNames = parseExportNames(raw)
		}
	}
	if err := s.DB.ExportCollectionDirWithOptions(colID, dir, opts); err != nil {
		httpError(w, 500, err)
		return
	}
	if format == "relay" {
		payload, err := zipDirectory(dir)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		col, err := s.DB.Collection(colID)
		if err != nil {
			httpError(w, 404, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.relay.zip"`, downloadSlug(col.Name)))
		_, _ = w.Write(payload)
		return
	}

	var env *dsl.Environment
	if name := r.URL.Query().Get("env"); name != "" {
		e, err := s.DB.Environment(name)
		if err != nil {
			httpError(w, 404, fmt.Errorf("environment %q not found", name))
			return
		}
		env = &dsl.Environment{Vars: e.Vars, Secrets: e.Secrets}
	}

	var script string
	contentType := "text/plain; charset=utf-8"
	switch format {
	case "k6":
		script, err = porter.K6(dir, env)
	case "playwright":
		script, err = porter.Playwright(dir, env)
	case "postman":
		var out []byte
		out, err = porter.ExportPostman(dir)
		script, contentType = string(out), "application/json"
	case "openapi":
		var out []byte
		out, err = porter.ExportOpenAPI(dir)
		script, contentType = string(out), "application/json"
	default:
		httpError(w, 400, fmt.Errorf("format must be curl, relay, environment, k6, playwright, postman or openapi"))
		return
	}
	if err != nil {
		httpError(w, 422, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write([]byte(script))
}

func parseExportIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	seen := map[int64]bool{}
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid request id %q", strings.TrimSpace(part))
		}
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out, nil
}

func parseExportNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name != "" && !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
	}
	return out
}

func zipDirectory(root string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Deflate
		dst, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func downloadSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "export"
	}
	return out
}

// handleImportOpenAPI imports an OpenAPI 3.x JSON document as a collection.
func (s *Server) handleImportOpenAPI(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r, 20<<20)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tmp, err := os.MkdirTemp("", "relay-import-oa-*")
	if err != nil {
		httpError(w, 500, err)
		return
	}
	defer os.RemoveAll(tmp)
	n, err := porter.ImportOpenAPI(data, tmp)
	if err != nil {
		httpError(w, 422, err)
		return
	}
	colID, err := s.DB.SeedFromDir(tmp)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"collectionId": colID, "requests": n})
}

// handleExport adds openapi to the existing export handler.
// (The original handler is preserved; this only extends the switch.)

// --- Xray Cloud settings ---

func (s *Server) handleXraySettingsPut(w http.ResponseWriter, r *http.Request) {
	var xs store.XraySettings
	if err := json.NewDecoder(r.Body).Decode(&xs); err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.SaveXraySettings(xs); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, xs)
}

// xrayConfig builds an xray.Config from stored settings/credentials plus
// environment overrides, or returns an error describing what's missing.
func (s *Server) xrayConfig() (xray.Config, store.XraySettings, error) {
	xs, err := s.DB.XraySettings()
	if err != nil || xs.ProjectKey == "" {
		return xray.Config{}, xs, fmt.Errorf("xray not configured: set project key in Settings → Xray")
	}
	creds, _ := s.DB.XrayCredentials()
	cfg := xray.ConfigFromEnv()
	cfg.ClientID = firstNonEmpty(cfg.ClientID, creds.ClientID)
	cfg.ClientSecret = firstNonEmpty(cfg.ClientSecret, creds.ClientSecret)
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return xray.Config{}, xs, fmt.Errorf("Xray client id and client secret are missing")
	}
	if xs.CloudURL != "" {
		cfg.GQLURL = xs.CloudURL
	}
	if xs.AuthURL != "" {
		cfg.AuthURL = xs.AuthURL
	}
	if xs.JiraBaseURL != "" {
		cfg.JiraBaseURL = xs.JiraBaseURL
	}
	cfg.JiraEmail = firstNonEmpty(cfg.JiraEmail, creds.JiraEmail, xs.JiraEmail)
	cfg.JiraAPIToken = firstNonEmpty(cfg.JiraAPIToken, os.Getenv("JIRA_API_KEY"), creds.JiraAPIKey)
	return cfg, xs, nil
}

// scopedRequests filters a collection's requests down to an explicit set of
// request IDs (if any) and/or a tag expression like "regression,!flaky"
// (comma-separated terms, "!" prefix negates).
func scopedRequests(all []store.Request, requestIDs []int64, tagExpr string) []store.Request {
	var byID map[int64]bool
	if len(requestIDs) > 0 {
		byID = make(map[int64]bool, len(requestIDs))
		for _, id := range requestIDs {
			byID[id] = true
		}
	}
	out := make([]store.Request, 0, len(all))
	for _, req := range all {
		if byID != nil && !byID[req.ID] {
			continue
		}
		if !matchesTagExpr(req.Spec.Tags, tagExpr) {
			continue
		}
		out = append(out, req)
	}
	return out
}

// matchesTagExpr evaluates a comma-separated tag expression against a
// test's tags. Each term must be present (or, with a "!" prefix, absent).
// An empty expression matches everything.
func matchesTagExpr(tags []string, expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	have := make(map[string]bool, len(tags))
	for _, t := range tags {
		have[strings.ToLower(strings.TrimSpace(t))] = true
	}
	for _, term := range strings.Split(expr, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if strings.HasPrefix(term, "!") {
			if have[strings.ToLower(strings.TrimSpace(term[1:]))] {
				return false
			}
			continue
		}
		if !have[strings.ToLower(term)] {
			return false
		}
	}
	return true
}

// pushMappingRow is one row of the dry-run preview: what will happen to a
// given test on push, before any network call mutates Xray state.
type pushMappingRow struct {
	RequestID int64  `json:"requestId"`
	Name      string `json:"name"`
	XrayKey   string `json:"xrayKey,omitempty"`
	Status    string `json:"status"` // linked | missing | create
	Detail    string `json:"detail,omitempty"`
}

// handleXrayPush runs the (scoped) collection and pushes results to Xray
// Cloud, or — with dryRun — previews the test/key mapping without pushing.
func (s *Server) handleXrayPush(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CollectionID int64   `json:"collectionId"`
		Env          string  `json:"env"`
		Summary      string  `json:"summary"`
		RequestIDs   []int64 `json:"requestIds,omitempty"`
		TestIDs      []int64 `json:"testIds,omitempty"`
		RequestID    int64   `json:"requestId,omitempty"`
		TestSetID    int64   `json:"testSetId,omitempty"`
		Tags         string  `json:"tags,omitempty"`
		DryRun       bool    `json:"dryRun,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	cfg, xs, err := s.xrayConfig()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	client := xray.New(cfg)

	if len(in.TestIDs) > 0 || in.RequestID != 0 || in.TestSetID != 0 {
		var testSetIDs []int64
		if in.TestSetID != 0 {
			testSetIDs = []int64{in.TestSetID}
		}
		tests, err := s.selectedTests(testSelection{TestIDs: in.TestIDs, RequestID: in.RequestID, TestSetIDs: testSetIDs})
		if err != nil {
			httpError(w, 422, err)
			return
		}
		started := time.Now()
		results := make([]tm.TestResult, 0, len(tests))
		for _, tc := range tests {
			if !tc.Enabled {
				continue
			}
			res, err := s.runTestCaseValue(r.Context(), tc, in.Env, true)
			status := tm.StatusPASS
			comment := ""
			if err != nil || !res.Passed {
				status = tm.StatusFAIL
				comment = res.Error
			}
			steps := tmStepsFromStore(res.Steps)
			if tc.XrayKey == "" {
				if xs.ProjectKey == "" {
					httpError(w, 422, fmt.Errorf("set Xray project key before auto-creating tests"))
					return
				}
				key, err := client.CreateTest(tm.NewTest{ProjectKey: xs.ProjectKey, Summary: tc.Name, TestType: "Manual", Steps: testStepsText(tc.Assertions)})
				if err != nil {
					httpError(w, 502, err)
					return
				}
				tc.XrayKey = key
				_ = s.DB.UpdateTestCase(&tc)
			}
			results = append(results, tm.TestResult{
				TestKey:    tc.XrayKey,
				Name:       tc.Name,
				Status:     status,
				Comment:    comment,
				DurationMs: res.DurationMs,
				Steps:      steps,
			})
		}
		summary := in.Summary
		if summary == "" {
			summary = "Relay Test Management execution"
		}
		key, err := client.PushExecution(tm.Execution{
			ProjectKey: xs.ProjectKey, TestPlanKey: xs.TestPlanKey, Summary: summary,
			StartedAt: started, FinishedAt: time.Now(), Results: results,
		})
		if err != nil {
			httpError(w, 502, fmt.Errorf("xray push failed: %w", err))
			return
		}
		writeJSON(w, map[string]string{"executionKey": key})
		return
	}

	all, err := s.DB.Requests(in.CollectionID)
	if err != nil || len(all) == 0 {
		httpError(w, 404, fmt.Errorf("collection %d has no requests", in.CollectionID))
		return
	}
	requests := scopedRequests(all, in.RequestIDs, in.Tags)
	if len(requests) == 0 {
		httpError(w, 422, fmt.Errorf("no tests match the selected scope"))
		return
	}

	if in.DryRun {
		rows := make([]pushMappingRow, 0, len(requests))
		for i := range requests {
			req := &requests[i]
			row := pushMappingRow{RequestID: req.ID, Name: req.Spec.Name, XrayKey: req.Spec.XrayKey}
			if req.Spec.XrayKey == "" {
				row.Status = "create"
				row.Detail = "no Xray key linked — Xray will auto-provision a test issue"
			} else if ref, gerr := client.GetTest(req.Spec.XrayKey); gerr != nil {
				row.Status = "missing"
				row.Detail = gerr.Error()
			} else if ref == nil {
				row.Status = "missing"
				row.Detail = fmt.Sprintf("%s not found in Xray", req.Spec.XrayKey)
			} else {
				row.Status = "linked"
				row.Detail = ref.Summary
			}
			rows = append(rows, row)
		}
		writeJSON(w, map[string]any{"rows": rows})
		return
	}

	started := time.Now()
	var results []tm.TestResult
	for i := range requests {
		req := &requests[i]
		out, _, execErr := s.execute(r.Context(), req, in.Env, true)
		status := tm.StatusPASS
		comment := ""
		var duration float64
		var steps []tm.TestStep
		if execErr != nil {
			status = tm.StatusFAIL
			comment = execErr.Error()
		} else {
			duration = out.Timing["total"]
			steps = tmStepsFromSend(out)
			for _, st := range steps {
				if st.Status == tm.StatusFAIL {
					status = tm.StatusFAIL
					if comment != "" {
						comment += "; "
					}
					comment += st.Comment
				}
			}
		}
		results = append(results, tm.TestResult{
			TestKey:      req.Spec.XrayKey,
			Name:         req.Spec.Name,
			Status:       status,
			Comment:      comment,
			DurationMs:   duration,
			Steps:        steps,
			Requirements: append([]string(nil), req.Spec.Requirements...),
		})
	}
	if len(results) == 0 {
		httpError(w, 422, fmt.Errorf("no enabled tests selected"))
		return
	}
	finished := time.Now()

	summary := in.Summary
	if summary == "" {
		col, _ := s.DB.Collection(in.CollectionID)
		if col != nil {
			summary = "Relay run: " + col.Name
		} else {
			summary = "Relay automated test execution"
		}
	}

	exec := tm.Execution{
		ProjectKey:  xs.ProjectKey,
		TestPlanKey: xs.TestPlanKey,
		Summary:     summary,
		StartedAt:   started,
		FinishedAt:  finished,
		Results:     results,
	}

	key, err := client.PushExecution(exec)
	if err != nil {
		httpError(w, 502, fmt.Errorf("xray push failed: %w", err))
		return
	}
	writeJSON(w, map[string]string{"executionKey": key})
}

func tmStepsFromSend(out *sendResult) []tm.TestStep {
	steps := make([]tm.TestStep, 0, len(out.Assertions)+len(out.ScriptTests))
	for i, a := range out.Assertions {
		status := tm.StatusPASS
		if !a.Passed {
			status = tm.StatusFAIL
		}
		steps = append(steps, tm.TestStep{
			Name:    fmt.Sprintf("Assertion %d: %s", i+1, a.Type),
			Type:    a.Type,
			Status:  status,
			Comment: a.Message,
		})
	}
	for i, st := range out.ScriptTests {
		status := tm.StatusPASS
		if !st.Passed {
			status = tm.StatusFAIL
		}
		steps = append(steps, tm.TestStep{
			Name:    fmt.Sprintf("Script test %d: %s", i+1, st.Name),
			Type:    "script",
			Status:  status,
			Comment: st.Error,
		})
	}
	return steps
}

func tmStepsFromStore(in []store.TestStepResult) []tm.TestStep {
	steps := make([]tm.TestStep, 0, len(in))
	for _, st := range in {
		status := tm.StatusPASS
		if !st.Passed {
			status = tm.StatusFAIL
		}
		steps = append(steps, tm.TestStep{
			Name: st.Name, Type: st.Type, Expected: st.Expected, Actual: st.Actual,
			Status: status, Comment: st.Message,
		})
	}
	return steps
}

// handleXrayTestGet looks up an existing Xray test issue by key, used by
// the "link existing test" traceability action to validate a key before
// it's saved onto the request.
func (s *Server) handleXrayTestGet(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		httpError(w, 400, fmt.Errorf("missing key"))
		return
	}
	cfg, _, err := s.xrayConfig()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	ref, err := xray.New(cfg).GetTest(key)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, ref) // null when not found
}

// handleXrayTestCreate creates a new Xray test issue for a request and
// saves the resulting key onto it.
func (s *Server) handleXrayRequestTestCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID int64  `json:"requestId"`
		Summary   string `json:"summary"`
		TestType  string `json:"testType"`
		Steps     string `json:"steps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	req, err := s.DB.Request(in.RequestID)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", in.RequestID))
		return
	}
	cfg, xs, err := s.xrayConfig()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	summary := in.Summary
	if summary == "" {
		summary = req.Spec.Name
	}
	key, err := xray.New(cfg).CreateTest(tm.NewTest{
		ProjectKey: xs.ProjectKey,
		Summary:    summary,
		TestType:   in.TestType,
		Steps:      in.Steps,
	})
	if err != nil {
		httpError(w, 502, err)
		return
	}
	req.Spec.XrayKey = key
	if err := s.DB.UpdateRequest(req); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"xrayKey": key})
}

// handleXrayRequirementsLink links a request's Xray test to one or more
// requirement issues, then persists the requirement keys on the request.
func (s *Server) handleXrayRequirementsLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID       int64    `json:"requestId"`
		RequirementKeys []string `json:"requirementKeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	req, err := s.DB.Request(in.RequestID)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", in.RequestID))
		return
	}
	if req.Spec.XrayKey == "" {
		httpError(w, 422, fmt.Errorf("link or create an Xray test before linking requirements"))
		return
	}
	if len(in.RequirementKeys) == 0 {
		httpError(w, 422, fmt.Errorf("add at least one requirement key before linking"))
		return
	}
	cfg, err := s.jiraConfig()
	if err != nil {
		httpError(w, 422, err)
		return
	}
	if err := xray.New(cfg).LinkRequirements(req.Spec.XrayKey, in.RequirementKeys); err != nil {
		httpError(w, 502, err)
		return
	}
	existing := map[string]bool{}
	for _, k := range req.Spec.Requirements {
		existing[k] = true
	}
	for _, k := range in.RequirementKeys {
		if k != "" && !existing[k] {
			req.Spec.Requirements = append(req.Spec.Requirements, k)
			existing[k] = true
		}
	}
	if err := s.DB.UpdateRequest(req); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"requirements": req.Spec.Requirements})
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
