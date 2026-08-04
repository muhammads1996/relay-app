package porter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
)

// exportWorkspace builds a small collection with a folder, inherited
// headers, a secret, and computed vars.
func exportWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("collection.toml", "name = \"demo\"\n[headers]\nX-Correlation-Id = \"{{$uuid}}\"\n")
	write("01-health.req.toml", `name = "Health"
url = "{{baseUrl}}/health"
[[assertions]]
type = "status"
equals = 200
`)
	write("verify/02-verify.req.toml", `name = "Verify Individual"
method = "POST"
url = "{{baseUrl}}/aml/v2/verify"
[auth]
type = "bearer"
token = "{{apiToken}}"
[body]
type = "json"
content = '''
{ "idNumber": "{{testIdNumber}}" }
'''
[[assertions]]
type = "jsonpath"
path = "$.result.items[0].status"
equals = "VERIFIED"
[[assertions]]
type = "max_ms"
max_ms = 2000
`)
	return root
}

func exportEnv() *dsl.Environment {
	return &dsl.Environment{
		Vars:    map[string]string{"baseUrl": "https://sit.example.com", "testIdNumber": "8001015009087"},
		Secrets: []string{"apiToken"},
	}
}

func TestK6Export(t *testing.T) {
	script, err := K6(exportWorkspace(t), exportEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"import http from 'k6/http'",
		"group(\"verify\", () => {",
		"http.request(\"POST\", `https://sit.example.com/aml/v2/verify`",
		"${__ENV.RELAY_SECRET_APITOKEN}", // secret as env ref, not value
		"${uuid()}",                      // computed var stays dynamic
		"8001015009087",                  // plain var resolved inline
		`r.json("result.items.0.status") === "VERIFIED"`,
		"r.timings.duration <= 2000",
		"r.status === 200",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("k6 script missing %q\n%s", want, script)
		}
	}
	if strings.Contains(script, "\x00") {
		t.Error("marker leaked into script")
	}
}

func TestPlaywrightExport(t *testing.T) {
	spec, err := Playwright(exportWorkspace(t), exportEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"import { test, expect } from '@playwright/test';",
		`test.describe("verify", () => {`,
		`test("Verify Individual", async ({ request }) => {`,
		"${process.env.RELAY_SECRET_APITOKEN}",
		"${crypto.randomUUID()}",
		"const body = await r.json();",
		`expect(body.result.items[0].status).toBe("VERIFIED");`,
		"expect(r.status()).toBe(200);",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("playwright spec missing %q\n%s", want, spec)
		}
	}
	if strings.Contains(spec, "\x00") {
		t.Error("marker leaked into spec")
	}
}

func TestTmplEscaping(t *testing.T) {
	d := jsDialect{envRef: func(n string) string { return "__ENV." + n }}
	got := d.tmpl("back`tick ${not-a-var} \\slash " + mEnv + "X\x00")
	want := "`back\\`tick \\${not-a-var} \\\\slash ${__ENV.X}`"
	if got != want {
		t.Errorf("tmpl:\n got %s\nwant %s", got, want)
	}
}

func TestGjsonSelector(t *testing.T) {
	got, err := gjsonSelector(`$.a.b[0]["k k"]`)
	if err != nil || got != "a.b.0.k k" {
		t.Errorf("got %q err=%v", got, err)
	}
}

func TestJSAccessor(t *testing.T) {
	got, err := jsAccessor(`$.a.b[0]["k k"]`)
	if err != nil || got != `.a.b[0]["k k"]` {
		t.Errorf("got %q err=%v", got, err)
	}
}
