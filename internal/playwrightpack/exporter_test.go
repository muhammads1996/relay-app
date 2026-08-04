package playwrightpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
)

func TestExportPlaywrightProject(t *testing.T) {
	root := t.TempDir()
	err := Export(root, Project{
		Name:               "AML Suite",
		DefaultEnvironment: "dev",
		Envs: []store.Environment{
			{Name: "dev", Vars: map[string]string{"baseUrl": "https://dev-api.example.test"}, Secrets: []string{"apiToken"}},
			{Name: "sit", Vars: map[string]string{"baseUrl": "https://sit-api.example.test"}, Secrets: []string{"apiToken"}},
		},
		Cases: []Case{{
			ID: "test-1-verify",
			Test: store.TestCase{
				Name: "Verify Individual", Enabled: true, Owner: "risk", Priority: "high",
				Tags: []string{"regression"}, XrayKey: "AML-T1", TestPlanKey: "AML-P1", Requirements: []string{"AML-R1"},
			},
			Request: dsl.Request{
				Name: "Verify Individual", Method: "POST", URL: "{{baseUrl}}/verify",
				Headers: map[string]string{"Content-Type": "application/json"},
				Auth:    &dsl.Auth{Type: "bearer", Token: "{{apiToken}}"},
				Body:    &dsl.Body{Type: "json", Content: `{"idNumber":"{{idNumber}}","active":true}`},
				Assertions: []dsl.Assertion{
					{Type: "status", Equals: int64(200)},
					{Type: "jsonpath", Path: "$.result.status", Equals: "VERIFIED"},
				},
				Scripts: &dsl.Scripts{
					PreRequest: `pm.collectionVariables.set("requestToken", "abc");`,
					Tests:      `pm.test("captures id", function() { pm.collectionVariables.set("id", "42"); });`,
				},
			},
			CollectionName: "AML",
			CollectionVars: map[string]string{"tenant": "risk"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"package.json",
		"playwright.config.ts",
		"tests/models/relay.model.ts",
		"tests/models/request-models.model.ts",
		"tests/fixtures/index.ts",
		"tests/fixtures/test-data.ts",
		"tests/services/api.service.ts",
		"tests/services/aml.service.ts",
		"tests/specs/relay-generated-suite.spec.ts",
		"reporters/relay-xray-reporter.ts",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	spec := read(t, root, "tests/specs/relay-generated-suite.spec.ts")
	for _, want := range []string{"relayStep", "Assertion 1: status", "Assertion 2: jsonpath", "runPostmanTests", "scriptResult.passed"} {
		if !strings.Contains(spec, want) {
			t.Fatalf("spec missing %q:\n%s", want, spec)
		}
	}
	models := read(t, root, "tests/models/request-models.model.ts")
	if !strings.Contains(models, "interface VerifyIndividualRequest") || !strings.Contains(models, "idNumber: string") || !strings.Contains(models, "active: boolean") {
		t.Fatalf("request model inference missing:\n%s", models)
	}
	meta := read(t, root, "tests/fixtures/test-metadata.generated.ts")
	for _, want := range []string{"AML-T1", "AML-P1", "AML-R1", "requestToken", "pm.collectionVariables.set"} {
		if !strings.Contains(meta+read(t, root, ".env.example"), want) {
			t.Fatalf("metadata/env missing %q", want)
		}
	}
	env := read(t, root, ".env")
	for _, want := range []string{"Environment=dev", "RELAY_VAR_BASEURL=https://dev-api.example.test", "RELAY_SECRET_APITOKEN="} {
		if !strings.Contains(env, want) {
			t.Fatalf(".env missing %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "dev-key") {
		t.Fatalf("secret leaked into .env:\n%s", env)
	}
	data := read(t, root, "tests/fixtures/test-data.ts")
	for _, want := range []string{"Environment = process.env.Environment", "https://dev-api.example.test", "https://sit-api.example.test"} {
		if !strings.Contains(data, want) {
			t.Fatalf("test data missing %q:\n%s", want, data)
		}
	}
	service := read(t, root, "tests/services/api.service.ts")
	for _, want := range []string{"runPreRequestScript", "runPostmanTests", "new Function('pm', source)(pm)", "auth.in === 'query'", "randomUUID"} {
		if !strings.Contains(service, want) {
			t.Fatalf("service missing %q:\n%s", want, service)
		}
	}
}

func TestResultsToExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-playwright-results.json")
	if err := os.WriteFile(path, []byte(`{
  "schema": "relay.playwright.results.v1",
  "startedAt": "2026-07-03T08:00:00Z",
  "finishedAt": "2026-07-03T08:00:01Z",
  "summary": {"tests": 1, "passed": 0, "failed": 1, "skipped": 0},
  "results": [{
    "id": "test-1",
    "name": "Verify",
    "testKey": "AML-T1",
    "testPlanKey": "AML-P1",
    "requirements": ["AML-R1"],
    "status": "FAILED",
    "durationMs": 42,
    "comment": "status mismatch",
    "steps": [{"name":"Assertion 1: status","type":"status","expected":"200","status":"FAILED","comment":"got 500"}]
  }]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := LoadResults(path)
	if err != nil {
		t.Fatal(err)
	}
	exec := results.ToExecution("AML", "AML-P1", "Generated")
	if exec.ProjectKey != "AML" || exec.TestPlanKey != "AML-P1" || exec.Summary != "Generated" {
		t.Fatalf("execution metadata = %+v", exec)
	}
	if len(exec.Results) != 1 || exec.Results[0].TestKey != "AML-T1" || len(exec.Results[0].Steps) != 1 || len(exec.Results[0].Requirements) != 1 {
		t.Fatalf("execution results = %+v", exec.Results)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
