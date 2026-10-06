package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/store"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
)

// Match the supplied older Postman exports without including account data or
// credentials from those files in the test suite.
func policyEnvironmentFixture(t *testing.T, stage string) string {
	t.Helper()
	keys := []string{"pvb_url", "b2p_url", "bancs_annuity_url", "bancs_wrapper_url", "cmos_url", "cmos_legacy_url", "psi_url", "iam_url", "c360_url", "cmos_ibm_client_id", "cmos_ibm_client_secret", "iam_ibm_client_id", "bearer_token", "bpKey", "policyNo", "idNo", "intermediaryId", "orgId", "businessView"}
	values := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		value := "fixture-" + stage
		if strings.HasSuffix(key, "_url") {
			value = "https://example.invalid/" + stage + "/{{tenant}}"
		}
		if key == "bearer_token" {
			value = ""
		}
		values = append(values, map[string]any{"key": key, "value": value, "enabled": true})
	}
	data, err := json.Marshal(map[string]any{"id": "postman-id", "name": "Enterprise Policy Services - " + stage + " Environment", "values": values, "_postman_variable_scope": "environment"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPostmanEnvironmentPreviewAndCommit(t *testing.T) {
	s, _ := newServer(t)
	for _, tc := range []struct{ stage, name string }{
		{"SIT/ST", "enterprise-policy-services-sit-st-environment"},
		{"UAT3", "enterprise-policy-services-uat3-environment"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			source := policyEnvironmentFixture(t, tc.stage)
			before, err := s.DB.Environments()
			if err != nil {
				t.Fatal(err)
			}
			preview, report := call(t, s, "POST", "/api/import/postman?preview=1", source)
			if preview.Code != http.StatusOK || report["kind"] != "environment" || report["variables"] != float64(19) || report["secrets"] != float64(2) || report["environmentName"] != tc.name {
				t.Fatalf("preview status=%d report=%v", preview.Code, report)
			}
			after, err := s.DB.Environments()
			if err != nil || len(before) != len(after) {
				t.Fatalf("preview mutated environments: before=%d after=%d err=%v", len(before), len(after), err)
			}
			commit, report := call(t, s, "POST", "/api/import/postman?contentHash=", source)
			if commit.Code != http.StatusOK || report["environmentId"] == nil || report["sourceId"] != nil || report["collectionId"] != nil {
				t.Fatalf("commit status=%d report=%v", commit.Code, report)
			}
			env, err := s.DB.Environment(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if len(env.Vars) != 17 || len(env.Secrets) != 2 || env.Vars["cmos_ibm_client_id"] != "fixture-"+tc.stage {
				t.Fatal("enabled variables or secret names were not imported")
			}
			if _, ok := env.Vars["cmos_ibm_client_secret"]; ok {
				t.Fatal("secret value persisted")
			}
			if _, ok := env.Vars["bearer_token"]; ok {
				t.Fatal("empty token was not converted to a secret reference")
			}
			cols, err := s.DB.Collections()
			if err != nil || len(cols) != 1 {
				t.Fatal("environment import created a collection")
			}
			stale, _ := call(t, s, "POST", "/api/import/postman?contentHash=", source)
			if stale.Code != http.StatusConflict {
				t.Fatalf("stale creation status=%d", stale.Code)
			}
		})
	}
}

func newPostmanEnvironmentWorkspace(t *testing.T, count int) (*Server, []string) {
	t.Helper()
	root := t.TempDir()
	ids, dirs := []string{}, []string{}
	for i := 0; i < count; i++ {
		id := workspacepkg.NewID()
		dir := filepath.Join(root, "collections", fmt.Sprintf("collection-%d--%s", i, id))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "collection.toml"), []byte(fmt.Sprintf("id = %q\nname = %q\n", id, fmt.Sprintf("Collection %d", i))), 0600); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, fmt.Sprintf("%q", id))
		dirs = append(dirs, dir)
	}
	manifest := fmt.Sprintf("schema_version = 2\nworkspace_id = %q\ncollections = [%s]\n", workspacepkg.NewID(), strings.Join(ids, ", "))
	if err := os.WriteFile(filepath.Join(root, "workspace.toml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{DB: db, WorkspaceRoot: root, Engine: engine.NewOptions()}
	if err := s.Prepare(); err != nil {
		t.Fatal(err)
	}
	return s, dirs
}

func environmentImportURL(preview map[string]any) string {
	query := url.Values{"workspaceHash": {preview["workspaceHash"].(string)}, "contentHash": {preview["contentHash"].(string)}}
	query.Set("collectionId", fmt.Sprintf("%.0f", preview["collectionId"].(float64)))
	return "/api/import/postman?" + query.Encode()
}

func TestCanonicalPostmanEnvironmentImportAndConflicts(t *testing.T) {
	s, dirs := newPostmanEnvironmentWorkspace(t, 2)
	source := policyEnvironmentFixture(t, "SIT/ST")
	missing, _ := call(t, s, "POST", "/api/import/postman?preview=1", source)
	if missing.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing collection status=%d", missing.Code)
	}
	cols, err := s.DB.Collections()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("/api/import/postman?preview=1&collectionId=%d", cols[1].ID)
	preview, report := call(t, s, "POST", endpoint, source)
	if preview.Code != http.StatusOK || report["contentHash"] != "" {
		t.Fatalf("preview status=%d report=%v", preview.Code, report)
	}
	if len(s.currentWorkspace().Environments) != 0 {
		t.Fatal("preview published an environment")
	}
	commitURL := environmentImportURL(report)
	stale, _ := call(t, s, "POST", strings.Replace(commitURL, "workspaceHash=", "workspaceHash=stale", 1), source)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale workspace status=%d", stale.Code)
	}
	commit, committed := call(t, s, "POST", commitURL, source)
	if commit.Code != http.StatusOK || committed["fileId"] == nil || committed["sourceId"] != nil {
		t.Fatalf("commit status=%d report=%v", commit.Code, committed)
	}
	opened, err := workspacepkg.Open(s.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Collections) != 2 || len(opened.Environments) != 1 {
		t.Fatal("environment import changed collection count or did not persist")
	}
	env := opened.Environments[0]
	if filepath.Dir(env.Path) != filepath.Join(dirs[1], "environments") || len(env.Vars) != 17 || len(env.Secrets) != 2 {
		t.Fatal("environment imported into wrong collection or with wrong variables")
	}
	contents, err := os.ReadFile(env.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), `"cmos_ibm_client_secret" =`) || strings.Contains(string(contents), `"bearer_token" =`) {
		t.Fatal("secret values were written to canonical file")
	}
	stale, _ = call(t, s, "POST", commitURL, source)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale creation status=%d", stale.Code)
	}
	_, reimport := call(t, s, "POST", endpoint, source)
	if err := os.WriteFile(env.Path, append(contents, []byte("\n# changed externally\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	stale, _ = call(t, s, "POST", environmentImportURL(reimport), source)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale overwrite status=%d", stale.Code)
	}
	_, reimport = call(t, s, "POST", endpoint, source)
	commit, committed = call(t, s, "POST", environmentImportURL(reimport), strings.ReplaceAll(source, "fixture-SIT/ST", "updated"))
	if commit.Code != http.StatusOK || committed["fileId"] != env.ID {
		t.Fatalf("reimport status=%d report=%v", commit.Code, committed)
	}
	restarted := &Server{DB: s.DB, WorkspaceRoot: s.WorkspaceRoot}
	if err := restarted.Prepare(); err != nil {
		t.Fatal(err)
	}
	stored, err := restarted.DB.Environment(env.Name)
	if err != nil || stored.Vars["businessView"] != "updated" {
		t.Fatalf("reimport did not survive reload: err=%v", err)
	}
}

func TestPostmanEnvironmentRejectsMalformedInput(t *testing.T) {
	s, _ := newServer(t)
	for _, source := range []string{`{"name":"bad","values":null}`, `{"name":"bad","values":[{"key":""}]}`, `{"name":"bad","_postman_variable_scope":"environment"}`} {
		rec, _ := call(t, s, "POST", "/api/import/postman", source)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("malformed input status=%d", rec.Code)
		}
	}
}

func TestPostmanEnvironmentConcurrentCreationAndReimportID(t *testing.T) {
	s, _ := newServer(t)
	source := policyEnvironmentFixture(t, "SIT/ST")
	start := make(chan struct{})
	statuses := make(chan int, 12)
	handler := s.Handler()
	for i := 0; i < cap(statuses); i++ {
		go func() {
			<-start
			r := prepareLocalRequest(httptest.NewRequest("POST", "/api/import/postman?contentHash=", strings.NewReader(source)))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < cap(statuses); i++ {
		status := <-statuses
		if status == http.StatusOK {
			successes++
		} else if status != http.StatusConflict {
			t.Errorf("concurrent creation status=%d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent creation winners=%d, want 1", successes)
	}
	first, err := s.DB.Environment("enterprise-policy-services-sit-st-environment")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := call(t, s, "POST", "/api/import/postman", policyEnvironmentFixture(t, "UAT3"))
	if second.Code != http.StatusOK {
		t.Fatalf("second environment status=%d", second.Code)
	}
	_, preview := call(t, s, "POST", "/api/import/postman?preview=1", source)
	for _, endpoint := range []string{"/api/import/postman?contentHash=" + preview["contentHash"].(string), "/api/import/postman"} {
		rec, report := call(t, s, "POST", endpoint, source)
		if rec.Code != http.StatusOK || report["environmentId"] != float64(first.ID) {
			t.Fatalf("reimport status=%d ID=%v, want %d", rec.Code, report["environmentId"], first.ID)
		}
	}
}
