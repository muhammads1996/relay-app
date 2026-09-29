package ui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/store"
)

func TestExecuteReturnsMaskedConsoleWithoutFailingScript(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer httpServer.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	collection := &store.Collection{Name: "console test"}
	if err := db.CreateCollection(collection); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEnvironment(&store.Environment{Name: "local", Secrets: []string{"apiToken"}}); err != nil {
		t.Fatal(err)
	}
	request := &store.Request{CollectionID: collection.ID, Spec: &dsl.Request{
		Name: "console output", Method: "GET", URL: httpServer.URL,
		Scripts: &dsl.Scripts{Tests: `
			console.log("token", pm.environment.get("apiToken"));
			console.warn("still running");
			console.error("diagnostic only");
			pm.test("response is okay", function() { pm.expect(pm.response.code).to.equal(200); });
		`},
	}}
	if err := db.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, Engine: engine.NewOptions(), Getenv: func(name string) string {
		if name == "RELAY_SECRET_APITOKEN" {
			return "shhh"
		}
		return ""
	}}
	out, status, err := server.execute(t.Context(), request, "local", false)
	if err != nil {
		t.Fatalf("execute returned status %d: %v", status, err)
	}
	if len(out.ScriptTests) != 1 || !out.ScriptTests[0].Passed {
		t.Fatalf("script tests = %+v", out.ScriptTests)
	}
	if len(out.Console) != 3 {
		t.Fatalf("console = %+v", out.Console)
	}
	if out.Console[0].Level != "log" || !strings.Contains(out.Console[0].Message, "••••••") || strings.Contains(out.Console[0].Message, "shhh") {
		t.Errorf("secret was not masked in console output: %+v", out.Console[0])
	}
	if out.Console[1].Level != "warn" || out.Console[1].Message != "still running" {
		t.Errorf("warn output = %+v", out.Console[1])
	}
	if out.Console[2].Level != "error" || out.Console[2].Message != "diagnostic only" {
		t.Errorf("error output = %+v", out.Console[2])
	}
}
