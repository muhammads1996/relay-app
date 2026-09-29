package script_test

import (
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/script"
)

func TestPMTestPass(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	resp := &script.Response{Code: 200, Status: "200 OK", Body: []byte(`{"ok":true}`), DurationMs: 42}
	res := script.RunTests(`
		pm.test("status is 200", function() {
			pm.expect(pm.response.code).to.equal(200);
		});
		pm.test("body has ok", function() {
			var j = pm.response.json();
			pm.expect(j.ok).to.equal(true);
		});
	`, scope, resp)
	if len(res.Tests) != 2 {
		t.Fatalf("expected 2 tests, got %d", len(res.Tests))
	}
	for _, tr := range res.Tests {
		if !tr.Passed {
			t.Errorf("test %q failed: %s", tr.Name, tr.Error)
		}
	}
}

func TestPMTestFail(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	resp := &script.Response{Code: 404, Status: "404 Not Found", Body: []byte(`{}`), DurationMs: 10}
	res := script.RunTests(`
		pm.test("status is 200", function() {
			pm.expect(pm.response.code).to.equal(200);
		});
	`, scope, resp)
	if len(res.Tests) != 1 {
		t.Fatalf("expected 1 test, got %d", len(res.Tests))
	}
	if res.Tests[0].Passed {
		t.Fatal("expected test to fail but it passed")
	}
	if got, want := res.Tests[0].Error, "expected 404 to equal 200"; got != want {
		t.Errorf("failure message = %q, want %q", got, want)
	}
	if strings.Contains(res.Tests[0].Error, "github.com/") {
		t.Errorf("failure message leaked a runtime path: %q", res.Tests[0].Error)
	}
}

func TestConsoleOutputDoesNotBecomeFailure(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	res := script.RunTests(`
		console.log("started", 3);
		console.warn("careful");
		console.error("diagnostic");
		pm.test("still passes", function() { pm.expect(1).to.equal(1); });
	`, scope, &script.Response{Code: 200})
	if len(res.Errors) != 0 {
		t.Fatalf("console output became runtime errors: %v", res.Errors)
	}
	if len(res.Tests) != 1 || !res.Tests[0].Passed {
		t.Fatalf("console output changed test result: %+v", res.Tests)
	}
	wantLevels := []string{"log", "warn", "error"}
	wantMessages := []string{"started 3", "careful", "diagnostic"}
	if len(res.Console) != len(wantLevels) {
		t.Fatalf("console entries = %+v", res.Console)
	}
	for i, message := range res.Console {
		if message.Level != wantLevels[i] || message.Message != wantMessages[i] {
			t.Errorf("console[%d] = %+v, want %s %q", i, message, wantLevels[i], wantMessages[i])
		}
	}
}

func TestUncaughtExceptionRemainsRuntimeErrorAlongsideConsole(t *testing.T) {
	res := script.RunTests(`console.warn("before"); throw new Error("broken");`,
		&script.Scope{Env: map[string]string{}, Collection: map[string]string{}}, &script.Response{Code: 200})
	if len(res.Console) != 1 || res.Console[0].Level != "warn" {
		t.Fatalf("console output = %+v", res.Console)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "broken") {
		t.Fatalf("uncaught exception was not retained as runtime error: %v", res.Errors)
	}
}

func TestPMVariableSetGet(t *testing.T) {
	scope := &script.Scope{
		Env:        map[string]string{"host": "localhost"},
		Collection: map[string]string{},
	}
	res := script.RunPreRequest(`
		pm.environment.set("host", "production.example.com");
		pm.collectionVariables.set("token", "abc123");
	`, scope)
	if res.UpdatedVars["host"] != "production.example.com" {
		t.Errorf("env update not reflected: %v", res.UpdatedVars)
	}
	if res.UpdatedVars["token"] != "abc123" {
		t.Errorf("collection var update not reflected: %v", res.UpdatedVars)
	}
}

func TestPMExpectInclude(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	resp := &script.Response{Code: 200, Status: "200 OK", Body: []byte(`"hello world"`), DurationMs: 5}
	res := script.RunTests(`
		pm.test("body includes hello", function() {
			pm.expect(pm.response.text()).to.include("hello");
		});
	`, scope, resp)
	if len(res.Tests) != 1 || !res.Tests[0].Passed {
		t.Fatalf("expected passing test, got %+v", res.Tests)
	}
}

func TestPMExpectBelow(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	resp := &script.Response{Code: 200, Status: "200 OK", Body: []byte(`{}`), DurationMs: 50}
	res := script.RunTests(`
		pm.test("response time is fast", function() {
			pm.expect(pm.response.responseTime).to.be.below(100);
		});
	`, scope, resp)
	if len(res.Tests) != 1 || !res.Tests[0].Passed {
		t.Fatalf("expected passing test: %+v", res.Tests)
	}
}

func TestPMTimeout(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	// Infinite loop should be interrupted by the timeout.
	res := script.RunPreRequest(`while(true){}`, scope)
	if len(res.Errors) == 0 {
		t.Fatal("expected timeout error")
	}
}

func TestPMResponseJSONPath(t *testing.T) {
	scope := &script.Scope{Env: map[string]string{}, Collection: map[string]string{}}
	resp := &script.Response{
		Code: 200, Status: "200 OK",
		Body:       []byte(`{"result":{"status":"VERIFIED","score":99}}`),
		DurationMs: 30,
	}
	res := script.RunTests(`
		pm.test("result.status is VERIFIED", function() {
			var j = pm.response.json();
			pm.expect(j.result.status).to.equal("VERIFIED");
		});
		pm.test("score above 50", function() {
			var j = pm.response.json();
			pm.expect(j.result.score).to.be.above(50);
		});
	`, scope, resp)
	for _, tr := range res.Tests {
		if !tr.Passed {
			t.Errorf("test %q failed: %s", tr.Name, tr.Error)
		}
	}
}
