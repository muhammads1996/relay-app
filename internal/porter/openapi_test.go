package porter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muhaymien96/relay/internal/porter"
)

const petStoreSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "Pet Store", "version": "1.0.0"},
  "servers": [{"url": "https://petstore.example.com/v2"}],
  "paths": {
    "/pets": {
      "get": {
        "operationId": "listPets",
        "summary": "List all pets",
        "tags": ["pets"],
        "parameters": [
          {"name": "limit", "in": "query", "schema": {"type": "integer", "default": 10}}
        ],
        "responses": {"200": {"description": "A list of pets"}}
      },
      "post": {
        "operationId": "createPet",
        "summary": "Create a pet",
        "tags": ["pets"],
        "requestBody": {
          "content": {
            "application/json": {
              "example": {"name": "Fido", "species": "dog"}
            }
          }
        },
        "responses": {"201": {"description": "Created"}}
      }
    },
    "/pets/{petId}": {
      "get": {
        "operationId": "getPet",
        "summary": "Get a pet",
        "tags": ["pets"],
        "parameters": [
          {"name": "petId", "in": "path", "required": true}
        ],
        "responses": {"200": {"description": "A pet"}}
      }
    }
  }
}`

func TestImportOpenAPI(t *testing.T) {
	dir := t.TempDir()
	n, err := porter.ImportOpenAPI([]byte(petStoreSpec), dir)
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 requests, got %d", n)
	}
	// All should be under a "pets" folder.
	entries, _ := os.ReadDir(filepath.Join(dir, "pets"))
	if len(entries) != 3 {
		t.Errorf("expected 3 files in pets/, got %d", len(entries))
	}
	// collection.toml should have the server URL as baseUrl.
	col, err := os.ReadFile(filepath.Join(dir, "collection.toml"))
	if err != nil {
		t.Fatal(err)
	}
	colStr := string(col)
	if !containsString(colStr, "petstore.example.com") {
		t.Errorf("collection.toml missing server URL: %s", colStr)
	}
	if !containsString(colStr, "Pet Store") {
		t.Errorf("collection.toml missing title: %s", colStr)
	}
}

func TestImportOpenAPIPathParams(t *testing.T) {
	dir := t.TempDir()
	_, err := porter.ImportOpenAPI([]byte(petStoreSpec), dir)
	if err != nil {
		t.Fatal(err)
	}
	// The getPet request should use Relay variable syntax {{petId}}.
	entries, _ := os.ReadDir(filepath.Join(dir, "pets"))
	found := false
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, "pets", e.Name()))
		if containsString(string(data), "{{petId}}") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected {{petId}} path param in at least one request file")
	}
}

func TestExportOpenAPI(t *testing.T) {
	dir := t.TempDir()
	// First import so we have something to export.
	_, err := porter.ImportOpenAPI([]byte(petStoreSpec), dir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	out, err := porter.ExportOpenAPI(dir)
	if err != nil {
		t.Fatalf("ExportOpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("export not valid JSON: %v\n%s", err, out)
	}
	if doc["openapi"] != "3.0.3" {
		t.Errorf("expected openapi 3.0.3, got %v", doc["openapi"])
	}
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Errorf("expected paths in export, got %v", doc)
	}
}

func TestImportOpenAPIInvalid(t *testing.T) {
	_, err := porter.ImportOpenAPI([]byte(`{"not":"openapi"}`), t.TempDir())
	if err == nil {
		t.Fatal("expected error for document without paths")
	}
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsRuntime(s, sub))
}

func containsRuntime(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
