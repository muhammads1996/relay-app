package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/muhaymien96/relay/internal/adapters/tm"
)

func TestImportPlaywrightResultsToXray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-playwright-results.json")
	if err := os.WriteFile(path, []byte(`{
  "schema": "relay.playwright.results.v1",
  "startedAt": "2026-07-03T08:00:00Z",
  "finishedAt": "2026-07-03T08:00:01Z",
  "summary": {"tests": 1, "passed": 1, "failed": 0, "skipped": 0},
  "results": [{
    "id": "test-1",
    "name": "Health",
    "testKey": "AML-T1",
    "status": "PASSED",
    "durationMs": 12,
    "steps": [{"name":"Assertion 1: status","type":"status","status":"PASSED"}]
  }]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var got tm.Execution
	key, err := importPlaywrightResultsToXray(path, "AML", "AML-P1", "Playwright import", func(exec tm.Execution) (string, error) {
		got = exec
		return "AML-E1", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if key != "AML-E1" {
		t.Fatalf("key = %q", key)
	}
	if got.ProjectKey != "AML" || got.TestPlanKey != "AML-P1" || got.Summary != "Playwright import" {
		t.Fatalf("execution metadata = %+v", got)
	}
	if len(got.Results) != 1 || got.Results[0].TestKey != "AML-T1" || len(got.Results[0].Steps) != 1 {
		t.Fatalf("execution results = %+v", got.Results)
	}
}
