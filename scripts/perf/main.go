// Command perf runs the local Relay release-gate microbenchmarks. Generated
// workspaces and any temporary download output stay under ignored test-results.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/store"
	"github.com/muhaymien96/relay/internal/ui"
	"github.com/muhaymien96/relay/internal/vars"
	"github.com/muhaymien96/relay/internal/workspace"
)

const downloadBytes = int64(100 << 20)

func main() {
	count := flag.Int("sends", 1000, "number of sequential repeat sends")
	sizesArg := flag.String("sizes", "1000,10000", "comma-separated generated workspace request counts")
	profile := flag.Bool("cpuprofile", false, "write phase-specific CPU profiles under the output root")
	flag.Parse()
	if *count < 1 {
		fatalf("-sends must be positive")
	}
	if err := os.MkdirAll("test-results", 0700); err != nil {
		fatalf("create test-results: %v", err)
	}
	base, err := os.MkdirTemp("test-results", "perf-")
	if err != nil {
		fatalf("create unique output root: %v", err)
	}
	root, err := filepath.Abs(base)
	if err != nil {
		fatalf("resolve output root: %v", err)
	}
	printHost()
	fmt.Printf("output_root=%s\n", root)
	fmt.Printf("download_fixture_bytes=%d (100 MiB)\n", downloadBytes)
	fmt.Printf("repeat_sends=%d\n", *count)

	sizes, err := parseSizes(*sizesArg)
	if err != nil {
		fatalf("invalid -sizes: %v", err)
	}
	for _, n := range sizes {
		if err = benchmarkWorkspace(root, n, *profile); err != nil {
			fatalf("workspace %d: %v", n, err)
		}
	}
	if err = benchmarkDownload(root); err != nil {
		fatalf("streaming download: %v", err)
	}
	if err = benchmarkRepeatSends(*count); err != nil {
		fatalf("repeat sends: %v", err)
	}
}

func parseSizes(input string) ([]int, error) {
	parts := strings.Split(input, ",")
	sizes := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 100000 {
			return nil, fmt.Errorf("request counts must be integers from 1 through 100000")
		}
		sizes = append(sizes, n)
	}
	return sizes, nil
}

func printHost() {
	fmt.Printf("host_os=%s host_arch=%s go=%s logical_cpus=%d\n", runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU())
	fmt.Printf("processor_identifier=%q\n", os.Getenv("PROCESSOR_IDENTIFIER"))
	fmt.Printf("os_release=not reported by the Go runtime\n")
}

func benchmarkWorkspace(root string, n int, profile bool) error {
	workspaceRoot := filepath.Join(root, fmt.Sprintf("workspace-%d", n))
	if err := makeWorkspace(workspaceRoot, n); err != nil {
		return err
	}
	var openTimes []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		var loaded *workspace.Workspace
		var err error
		if profile && i == 0 {
			loaded, err = profiled(root, fmt.Sprintf("open-%d", n), func() (*workspace.Workspace, error) { return workspace.Open(workspaceRoot) })
		} else {
			loaded, err = workspace.Open(workspaceRoot)
		}
		openTimes = append(openTimes, time.Since(start))
		if err != nil {
			return err
		}
		if len(loaded.Requests) != n {
			return fmt.Errorf("Open loaded %d requests, expected %d", len(loaded.Requests), n)
		}
	}
	db, err := store.Open(filepath.Join(workspaceRoot, "relay.db"))
	if err != nil {
		return err
	}
	server := &ui.Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: workspaceRoot}
	start := time.Now()
	prepare := func() error { return server.Prepare() }
	if profile {
		err = profiledErr(root, fmt.Sprintf("prepare-cold-%d", n), prepare)
	} else {
		err = prepare()
	}
	if err != nil {
		db.Close()
		return err
	}
	coldIndex := time.Since(start)
	warmTimes := make([]time.Duration, 0, 3)
	for i := 0; i < cap(warmTimes); i++ {
		start = time.Now()
		if profile && i == 0 {
			err = profiledErr(root, fmt.Sprintf("prepare-warm-%d", n), prepare)
		} else {
			err = prepare()
		}
		if err != nil {
			db.Close()
			return err
		}
		warmTimes = append(warmTimes, time.Since(start))
	}
	stateRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7717/api/state", nil)
	stateRequest.Host = "127.0.0.1:7717"
	stateRequest.RemoteAddr = "127.0.0.1:54321"
	stateRequest.Header.Set("Origin", "http://127.0.0.1:7717")
	stateResponse := httptest.NewRecorder()
	start = time.Now()
	serveState := func() error { server.Handler().ServeHTTP(stateResponse, stateRequest); return nil }
	if profile {
		err = profiledErr(root, fmt.Sprintf("state-get-%d", n), serveState)
	} else {
		err = serveState()
	}
	if err != nil {
		db.Close()
		return err
	}
	stateTime := time.Since(start)
	if stateResponse.Code != http.StatusOK {
		db.Close()
		return fmt.Errorf("GET /api/state returned %d", stateResponse.Code)
	}
	if err = db.Close(); err != nil {
		return err
	}
	fmt.Printf("workspace requests=%d open_ms=%s index_cold_ms=%.2f index_warm_ms=%s state_get_ms=%.2f\n", n, durationsMS(openTimes), float64(coldIndex.Microseconds())/1000, durationsMS(warmTimes), float64(stateTime.Microseconds())/1000)
	return nil
}

func profiled[T any](root, name string, fn func() (T, error)) (T, error) {
	var zero T
	path := filepath.Join(root, name+".cpu.pprof")
	file, err := os.Create(path)
	if err != nil {
		return zero, err
	}
	if err = pprof.StartCPUProfile(file); err != nil {
		file.Close()
		return zero, err
	}
	value, runErr := fn()
	pprof.StopCPUProfile()
	closeErr := file.Close()
	if runErr != nil {
		return zero, runErr
	}
	if closeErr != nil {
		return zero, closeErr
	}
	return value, nil
}

func profiledErr(root, name string, fn func() error) error {
	_, err := profiled(root, name, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

func makeWorkspace(root string, n int) error {
	collectionID := "00000000-0000-4000-8000-000000000001"
	collectionDir := filepath.Join(root, "collections", "perf--"+collectionID)
	if err := os.MkdirAll(collectionDir, 0700); err != nil {
		return err
	}
	marker := "schema_version = 2\nworkspace_id = \"00000000-0000-4000-8000-000000000010\"\ncollections = [\"" + collectionID + "\"]\n"
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte(marker), 0600); err != nil {
		return err
	}
	collection := "id = \"" + collectionID + "\"\nschema_version = 2\nname = \"Performance fixture\"\n"
	if err := os.WriteFile(filepath.Join(collectionDir, "collection.toml"), []byte(collection), 0600); err != nil {
		return err
	}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012x", i+10000)
		name := fmt.Sprintf("Request %05d", i)
		path := filepath.Join(collectionDir, fmt.Sprintf("%05d-request.req.toml", i))
		body := fmt.Sprintf("id = %q\nname = %q\nmethod = \"GET\"\nurl = \"https://example.test/items/%d\"\n", id, name, i)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			return err
		}
	}
	return nil
}

func benchmarkDownload(root string) error {
	chunk := bytesRepeat('R', 1<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(downloadBytes))
		flusher, _ := w.(http.Flusher)
		remaining := downloadBytes
		for remaining > 0 {
			n := int64(len(chunk))
			if remaining < n {
				n = remaining
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			remaining -= n
		}
	}))
	defer server.Close()
	req := &dsl.Request{Name: "100 MiB local download", Method: http.MethodGet, URL: server.URL}
	resolved, err := vars.Resolve(req, nil, vars.NewScope())
	if err != nil {
		return err
	}
	destination := filepath.Join(root, "download-100m.bin")
	start := time.Now()
	opts := engine.NewOptions()
	opts.MaxBodyBytes = 1 << 20
	result, err := engine.SendToFile(context.Background(), resolved, opts, destination)
	elapsed := time.Since(start)
	if err != nil {
		return err
	}
	stat, err := os.Stat(destination)
	if err != nil {
		return err
	}
	if result.ActualBytes != downloadBytes || stat.Size() != downloadBytes || !result.BodyComplete {
		return fmt.Errorf("incomplete download: result=%+v file=%d", result, stat.Size())
	}
	fmt.Printf("stream_download bytes=%d duration_s=%.3f mib_per_s=%.2f buffered=%d body_complete=%t\n", result.ActualBytes, elapsed.Seconds(), float64(downloadBytes)/(1<<20)/elapsed.Seconds(), result.BufferedBytes, result.BodyComplete)
	return os.Remove(destination)
}

func benchmarkRepeatSends(count int) error {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	resolved, err := vars.Resolve(&dsl.Request{Name: "repeat", Method: http.MethodGet, URL: server.URL}, nil, vars.NewScope())
	if err != nil {
		return err
	}
	opts := engine.NewOptions()
	opts.MaxBodyBytes = 1024
	// Warm the code paths and let per-send HTTP clients close their idle pools.
	for i := 0; i < 20; i++ {
		if _, err = engine.Send(context.Background(), resolved, opts); err != nil {
			return err
		}
	}
	runtime.GC()
	debug.FreeOSMemory()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()
	start := time.Now()
	for i := 0; i < count; i++ {
		result, sendErr := engine.Send(context.Background(), resolved, opts)
		if sendErr != nil {
			return sendErr
		}
		if result.Status != http.StatusOK || string(result.Body) != "ok" {
			return fmt.Errorf("unexpected repeat-send response: %+v", result)
		}
	}
	elapsed := time.Since(start)
	runtime.GC()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&after)
	fmt.Printf("repeat_send count=%d duration_s=%.3f sends_per_s=%.2f heap_after_gc_delta_bytes=%d total_allocated_delta_bytes=%d goroutine_delta=%d\n", count, elapsed.Seconds(), float64(count)/elapsed.Seconds(), signedDelta(after.HeapAlloc, before.HeapAlloc), after.TotalAlloc-before.TotalAlloc, runtime.NumGoroutine()-goroutinesBefore)
	return nil
}

func durationsMS(values []time.Duration) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprintf("%.2f", float64(value.Microseconds())/1000)
	}
	return strings.Join(parts, ",")
}

func signedDelta(after, before uint64) int64 {
	if after >= before {
		return int64(after - before)
	}
	return -int64(before - after)
}

func bytesRepeat(value byte, count int) []byte {
	data := make([]byte, count)
	for i := range data {
		data[i] = value
	}
	return data
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
