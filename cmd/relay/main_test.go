package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/adapters/tm"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/workspace"
)

func TestImportPostmanEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		slug string
	}{
		{"Policy Services / SIT", "policy-services-sit"},
		{"Policy_Services_UAT3", "policy-services-uat3"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "postman_environment.json")
			var values []map[string]any
			for i := 0; i < 18; i++ {
				values = append(values, map[string]any{"key": fmt.Sprintf("service_%d", i), "value": "https://example.invalid/{{tenant}}", "enabled": true})
			}
			values = append(values, map[string]any{"key": "client_secret", "value": "synthetic-sensitive-value", "enabled": true})
			data, err := json.Marshal(map[string]any{"name": tc.name, "values": values, "_postman_variable_scope": "environment"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, data, 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "target")
			if err := cmdImport([]string{"postman", input, "--out", out}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(out, "environments", tc.slug+".toml")
			env, err := dsl.LoadEnvironment(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(env.Vars) != 18 || len(env.Secrets) != 1 || env.Secrets[0] != "client_secret" {
				t.Fatalf("vars count = %d, secrets = %v", len(env.Vars), env.Secrets)
			}
			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(written), "synthetic-sensitive-value") {
				t.Fatal("import persisted the secret value")
			}
			if _, err := os.Stat(filepath.Join(out, "collection.toml")); !os.IsNotExist(err) {
				t.Fatal("environment import created a collection")
			}
			if err := cmdImport([]string{"postman", input, "--out", out}); err == nil {
				t.Fatal("expected an error when the environment already exists")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(written) {
				t.Fatal("existing environment was changed by rejected import")
			}
		})
	}
}

func TestImportPostmanEnvironmentDefaultDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	input := filepath.Join(root, "environment.json")
	if err := os.WriteFile(input, []byte(`{"name":"local","values":[{"key":"base-url","value":"https://example.invalid"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdImport([]string{"postman", input}); err != nil {
		t.Fatal(err)
	}
	env, err := dsl.LoadEnvironment(filepath.Join(root, "environments", "local.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if env.Vars["base-url"] != "https://example.invalid" {
		t.Fatalf("vars = %#v", env.Vars)
	}
}

func TestImportPostmanEnvironmentCanonicalWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		slug string
	}{
		{"Policy_Services_SIT", "policy-services-sit"},
		{"CON", "environment-con"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			root := t.TempDir()
			collectionID := workspace.NewID()
			collectionDir := filepath.Join(root, "collections", "policy-services--"+collectionID)
			if err := os.MkdirAll(collectionDir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := fmt.Sprintf("schema_version = 2\nworkspace_id = %q\ncollections = [%q]\n", workspace.NewID(), collectionID)
			if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			collection := fmt.Sprintf("id = %q\nschema_version = 2\nname = %q\n", collectionID, "Policy Services")
			if err := os.WriteFile(filepath.Join(collectionDir, "collection.toml"), []byte(collection), 0o600); err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(root, "environment.json")
			data := fmt.Sprintf(`{"name":%q,"values":[{"key":"base","value":"https://example.invalid"}]}`, tc.name)
			if err := os.WriteFile(input, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := cmdImport([]string{"postman", input, "--out", collectionDir}); err != nil {
				t.Fatal(err)
			}
			loaded, err := workspace.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Environments) != 1 {
				t.Fatalf("loaded %d environments", len(loaded.Environments))
			}
			env := loaded.Environments[0]
			if env.ID == "" || env.SchemaVersion != workspace.SchemaVersion || env.Name != tc.slug || env.CollectionID != collectionID || env.Vars["base"] != "https://example.invalid" {
				t.Fatalf("loaded environment = %+v", env)
			}
			reloaded, err := workspace.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Environments[0].ID != env.ID {
				t.Fatal("environment identity changed after reopening workspace")
			}
		})
	}
}

func TestImportPostmanCollectionStillWorks(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "collection.json")
	if err := os.WriteFile(input, []byte(`{
		"info":{"name":"Example"},
		"item":[{"name":"Health","request":{"method":"GET","url":"https://example.invalid/health"}}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "target")
	if err := cmdImport([]string{"postman", input, "--out", out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "collection.toml")); err != nil {
		t.Fatal(err)
	}
	requests, err := filepath.Glob(filepath.Join(out, "*.req.toml"))
	if err != nil || len(requests) != 1 {
		t.Fatalf("request files = %v, error = %v", requests, err)
	}
}

func TestImportPostmanEnvironmentInvalidInputCreatesNoOutput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "environment.json")
	if err := os.WriteFile(input, []byte(`{"name":"local","values":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "target")
	if err := cmdImport([]string{"postman", input, "--out", out}); err == nil {
		t.Fatal("expected invalid environment to fail")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("invalid import created output")
	}
}

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
