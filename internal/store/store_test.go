package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muhaymien96/relay/internal/dsl"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCollectionRequestCRUD(t *testing.T) {
	s := open(t)
	if empty, _ := s.Empty(); !empty {
		t.Fatal("new store should be empty")
	}

	col := &Collection{Name: "AML", Headers: map[string]string{"X-Team": "qe"}, Vars: map[string]string{"basePath": "/api"}}
	if err := s.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	f := &Folder{CollectionID: col.ID, Name: "verify", Headers: map[string]string{"X-Sub": "1"}}
	if err := s.CreateFolder(f); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		CollectionID: col.ID,
		FolderID:     &f.ID,
		Spec: &dsl.Request{
			Name: "Verify", Method: "POST", URL: "{{baseUrl}}/verify",
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       &dsl.Body{Type: "json", Content: `{"a":1}`},
			Assertions: []dsl.Assertion{{Type: "status", Equals: int64(200)}},
		},
	}
	if err := s.CreateRequest(req); err != nil {
		t.Fatal(err)
	}

	got, err := s.Request(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Name != "Verify" || got.Spec.Body.Content != `{"a":1}` || len(got.Spec.Assertions) != 1 {
		t.Errorf("round trip lost data: %+v", got.Spec)
	}
	if got.FolderID == nil || *got.FolderID != f.ID {
		t.Errorf("folder id = %v", got.FolderID)
	}

	got.Spec.Name = "Verify v2"
	if err := s.UpdateRequest(got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Request(req.ID)
	if again.Spec.Name != "Verify v2" {
		t.Errorf("update lost: %q", again.Spec.Name)
	}

	// Cascade: deleting the collection removes folders and requests.
	if err := s.DeleteCollection(col.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(req.ID); err == nil {
		t.Error("request should be gone after collection delete")
	}
}

func TestEnvironments(t *testing.T) {
	s := open(t)
	e := &Environment{Name: "sit", Vars: map[string]string{"baseUrl": "https://sit"}, Secrets: []string{"apiKey"}}
	if err := s.UpsertEnvironment(e); err != nil {
		t.Fatal(err)
	}
	e.Vars["extra"] = "1"
	if err := s.UpsertEnvironment(e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Environment("sit")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vars["extra"] != "1" || len(got.Secrets) != 1 {
		t.Errorf("env = %+v", got)
	}
	envs, _ := s.Environments()
	if len(envs) != 1 {
		t.Errorf("envs = %d", len(envs))
	}
}

func TestPresetsResolution(t *testing.T) {
	s := open(t)
	col := &Collection{Name: "c"}
	if err := s.CreateCollection(col); err != nil {
		t.Fatal(err)
	}
	f := &Folder{CollectionID: col.ID, Name: "sub"}
	if err := s.CreateFolder(f); err != nil {
		t.Fatal(err)
	}

	gw := &Preset{
		Name: "om-gateway-auth",
		Headers: []PresetHeader{
			{Key: "Authorization", Value: "Bearer tok", Secret: true},
			{Key: "X-Channel", Value: "MOBILE_IOS"},
		},
		Attachments: []Attachment{{CollectionID: &col.ID}},
	}
	if err := s.CreatePreset(gw); err != nil {
		t.Fatal(err)
	}
	sub := &Preset{
		Name:        "folder-extra",
		Headers:     []PresetHeader{{Key: "X-Channel", Value: "WEB"}},
		Attachments: []Attachment{{FolderID: &f.ID}},
	}
	if err := s.CreatePreset(sub); err != nil {
		t.Fatal(err)
	}

	h, secrets, err := s.PresetHeadersFor(col.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h["X-Channel"] != "MOBILE_IOS" || h["Authorization"] != "Bearer tok" {
		t.Errorf("collection presets = %v", h)
	}
	if len(secrets) != 1 || secrets[0] != "Bearer tok" {
		t.Errorf("secrets = %v", secrets)
	}

	h, _, err = s.PresetHeadersFor(col.ID, &f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h["X-Channel"] != "WEB" { // folder-level preset wins
		t.Errorf("folder presets = %v", h)
	}
}

func TestSeedAndExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("collection.toml", "name = \"AML Demo\"\n[headers]\nX-Correlation-Id = \"{{$uuid}}\"\n")
	write("01-health.req.toml", "name = \"Health\"\nurl = \"{{baseUrl}}/health\"\n")
	write("verify/folder.toml", "[headers]\nX-Sub = \"1\"\n")
	write("verify/02-verify.req.toml", "name = \"Verify\"\nmethod = \"POST\"\nurl = \"{{baseUrl}}/verify\"\n[[assertions]]\ntype = \"status\"\nequals = 200\n")
	write("environments/sit.toml", "secrets = [\"apiKey\"]\n[vars]\nbaseUrl = \"https://sit\"\n")

	s := open(t)
	colID, err := s.SeedFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	col, err := s.Collection(colID)
	if err != nil {
		t.Fatal(err)
	}
	if col.Name != "AML Demo" || col.Headers["X-Correlation-Id"] != "{{$uuid}}" {
		t.Errorf("collection = %+v", col)
	}
	reqs, _ := s.Requests(colID)
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	envs, _ := s.Environments()
	if len(envs) != 1 || envs[0].Secrets[0] != "apiKey" {
		t.Fatalf("envs = %+v", envs)
	}

	// Export back out and re-seed into a fresh store: nothing lost.
	out := t.TempDir()
	if err := s.ExportCollectionDir(colID, out); err != nil {
		t.Fatal(err)
	}
	s2 := open(t)
	colID2, err := s2.SeedFromDir(out)
	if err != nil {
		t.Fatal(err)
	}
	reqs2, _ := s2.Requests(colID2)
	if len(reqs2) != 2 {
		t.Fatalf("re-seeded requests = %d", len(reqs2))
	}
	var verify *Request
	for i := range reqs2 {
		if reqs2[i].Spec.Name == "Verify" {
			verify = &reqs2[i]
		}
	}
	if verify == nil || verify.FolderID == nil || len(verify.Spec.Assertions) != 1 {
		t.Errorf("verify after round trip = %+v", verify)
	}
}

func TestHistory(t *testing.T) {
	s := open(t)
	h := &HistoryEntry{
		RequestName: "Verify", Method: "POST", URL: "https://x/verify",
		Status: 200, DurationMs: 12.5,
		RespHeaders: map[string]string{"Content-Type": "application/json"},
		RespBody:    []byte(`{"ok":true}`),
		Timing:      map[string]float64{"total": 12.5},
		SentAt:      time.Now(),
	}
	if err := s.AddHistory(h); err != nil {
		t.Fatal(err)
	}
	list, err := s.History(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("history = %v err=%v", list, err)
	}
	if list[0].RespBody != nil {
		t.Error("list should not carry bodies")
	}
	full, err := s.HistoryEntry(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(full.RespBody) != `{"ok":true}` || full.RespHeaders["Content-Type"] == "" {
		t.Errorf("entry = %+v", full)
	}
}
