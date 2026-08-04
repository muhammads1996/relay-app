package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExecutionMaterializesRequestsWithInheritance(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "relay.json", `{"version":1,"name":"payments","defaultPlan":"smoke"}`)
	writeFile(t, root, "collections/api/collection.toml", `[headers]
X-Shared = "{{token}}"
`)
	writeFile(t, root, "collections/api/base.req.toml", `name = "Base"
method = "GET"
url = "{{baseUrl}}/health"

[[assert]]
type = "status"
op = "is2xx"
`)
	writeFile(t, root, "folders/core.folder.json", `{"id":"core","name":"Core","tests":["health"]}`)
	writeFile(t, root, "sets/smoke.set.json", `{"id":"smoke","name":"Smoke","tests":["health"]}`)
	writeFile(t, root, "tests/health.test.json", `{
	"id":"health",
	"name":"Health check",
	"request":"collections/api/base.req.toml",
	"folder":"core",
	"priority":"high",
	"tags":["smoke"],
	"xray":{"testKey":"PAY-1","testPlanKey":"PAY-PLAN","testSetKey":"PAY-SET","requirements":["PAY-REQ"]},
	"assertions":[{"type":"status","op":"is","exp":200}]
}`)
	writeFile(t, root, "plans/smoke.plan.json", `{"id":"smoke","name":"Smoke","include":{"sets":["smoke"]},"outputs":{"junit":"reports/junit.xml"},"xray":{"push":true,"projectKey":"PAY"}}`)
	writeFile(t, root, "executions/nightly.execution.json", `{"id":"nightly","name":"Nightly","plan":"smoke","environment":"ci","outputs":{"json":"reports/results.json"}}`)

	target, err := TargetFromPath(filepath.Join(root, "executions", "nightly.execution.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if errs := p.Validate(); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	spec, err := p.Resolve(target, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env != "ci" || len(spec.Tests) != 1 || spec.Outputs.JSON != "reports/results.json" || !spec.Xray.Push {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	dir, err := p.Materialize(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if _, err := os.Stat(filepath.Join(dir, "collections/api/collection.toml")); err != nil {
		t.Fatalf("collection inheritance not copied: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "collections/api/001-health-check.req.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{`xray_key = "PAY-1"`, `xray_plan = "PAY-PLAN"`, `xray_set = "PAY-SET"`, `priority = "high"`, `op = "is"`, `exp = 200`} {
		if !strings.Contains(text, want) {
			t.Fatalf("materialized request missing %q:\n%s", want, text)
		}
	}
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
