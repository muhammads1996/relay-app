package ui

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/pack"
	"github.com/muhaymien96/relay/internal/playwrightpack"
	"github.com/muhaymien96/relay/internal/store"
)

type testsExportRequest struct {
	Scope          string  `json:"scope"`
	Format         string  `json:"format"`
	Name           string  `json:"name"`
	CollectionID   int64   `json:"collectionId"`
	FolderID       *int64  `json:"folderId"`
	SourceFolderID *int64  `json:"sourceFolderId"`
	TestIDs        []int64 `json:"testIds"`
	TestSetID      int64   `json:"testSetId"`
	ExecutionID    int64   `json:"executionId"`
	Env            string  `json:"env"`
}

func (s *Server) handleTestsExport(w http.ResponseWriter, r *http.Request) {
	var in testsExportRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, err)
		return
	}
	tmp, err := os.MkdirTemp("", "relay-test-export-*")
	if err != nil {
		httpError(w, 500, err)
		return
	}
	defer os.RemoveAll(tmp)

	name := ""
	switch strings.ToLower(strings.TrimSpace(in.Format)) {
	case "", "relay-pack", "pack":
		name, err = s.writeTestPack(tmp, in)
	case "playwright":
		name, err = s.writePlaywrightProject(tmp, in)
	default:
		err = fmt.Errorf("unsupported test export format %q", in.Format)
	}
	if err != nil {
		httpError(w, 422, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	suffix := ".relay.zip"
	if strings.EqualFold(in.Format, "playwright") {
		suffix = ".playwright.zip"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s%s"`, slugName(name), suffix))
	zw := zip.NewWriter(w)
	if err := zipDir(zw, tmp); err != nil {
		_ = zw.Close()
		return
	}
	_ = zw.Close()
}

func (s *Server) writeTestPack(root string, in testsExportRequest) (string, error) {
	tests, name, err := s.exportTestsForScope(in)
	if err != nil {
		return "", err
	}
	if len(tests) == 0 {
		return "", fmt.Errorf("no tests selected for export")
	}
	manifest := pack.Manifest{Version: 1, Name: name, DefaultPlan: "all"}
	if err := writeJSONFile(filepath.Join(root, "relay.json"), manifest); err != nil {
		return "", err
	}

	collections := map[int64]*store.Collection{}
	sourceFolders := map[int64]*store.Folder{}
	testFolders := map[int64]store.TestFolder{}
	testFolderMembers := map[int64][]string{}
	selectedIDs := map[int64]string{}
	var packTests []pack.Test

	for _, tc := range tests {
		req, err := s.DB.Request(tc.RequestID)
		if err != nil {
			return "", err
		}
		if _, ok := collections[req.CollectionID]; !ok {
			col, err := s.DB.Collection(req.CollectionID)
			if err != nil {
				return "", err
			}
			collections[req.CollectionID] = col
		}
		var sf *store.Folder
		if req.FolderID != nil {
			if cached, ok := sourceFolders[*req.FolderID]; ok {
				sf = cached
			} else {
				loaded, err := s.DB.Folder(*req.FolderID)
				if err != nil {
					return "", err
				}
				sourceFolders[*req.FolderID] = loaded
				sf = loaded
			}
		}
		testReq := cloneRequestForTest(req, tc)
		colSlug := slugName(collections[req.CollectionID].Name)
		parts := []string{"collections", colSlug}
		if sf != nil {
			parts = append(parts, slugPath(sf.Name))
		}
		testID := stableTestID(tc)
		selectedIDs[tc.ID] = testID
		parts = append(parts, testID+".req.toml")
		rel := filepath.Join(parts...)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(root, rel), dsl.Marshal(testReq.Spec), 0o644); err != nil {
			return "", err
		}
		pt := pack.Test{
			ID:          testID,
			Name:        tc.Name,
			Request:     filepath.ToSlash(rel),
			Enabled:     &tc.Enabled,
			Tags:        append([]string(nil), tc.Tags...),
			Owner:       tc.Owner,
			Priority:    tc.Priority,
			Assertions:  append([]dsl.Assertion(nil), tc.Assertions...),
			ScriptTests: tc.ScriptTests,
			Xray: pack.XrayRef{
				TestKey:     tc.XrayKey,
				TestPlanKey: tc.TestPlanKey,
				Requirements: append([]string(nil),
					tc.Requirements...),
			},
		}
		if tc.FolderID != nil {
			tf, ok := testFolders[*tc.FolderID]
			if !ok {
				folders, err := s.DB.TestFolders()
				if err != nil {
					return "", err
				}
				for _, f := range folders {
					if f.ID == *tc.FolderID {
						tf = f
						testFolders[f.ID] = f
						break
					}
				}
			}
			if tf.ID != 0 {
				folderID := stableFolderID(tf)
				pt.Folder = folderID
				testFolderMembers[tf.ID] = append(testFolderMembers[tf.ID], testID)
			}
		}
		packTests = append(packTests, pt)
	}

	for _, col := range collections {
		if err := writeCollectionConfig(root, col); err != nil {
			return "", err
		}
	}
	for _, f := range sourceFolders {
		col := collections[f.CollectionID]
		if col == nil {
			continue
		}
		if err := writeSourceFolderConfig(root, col, f); err != nil {
			return "", err
		}
	}
	sort.Slice(packTests, func(i, j int) bool { return packTests[i].ID < packTests[j].ID })
	for _, t := range packTests {
		if err := writeJSONFile(filepath.Join(root, "tests", t.ID+".test.json"), t); err != nil {
			return "", err
		}
	}
	for id, members := range testFolderMembers {
		tf := testFolders[id]
		sort.Strings(members)
		f := pack.Folder{ID: stableFolderID(tf), Name: tf.Name, Tests: members, Xray: pack.XrayRef{TestSetKey: tf.XrayKey, TestPlanKey: tf.TestPlanKey}}
		if err := writeJSONFile(filepath.Join(root, "folders", f.ID+".folder.json"), f); err != nil {
			return "", err
		}
	}
	if err := writeSelectedTestSets(root, s, selectedIDs); err != nil {
		return "", err
	}
	allIDs := make([]string, 0, len(packTests))
	for _, t := range packTests {
		allIDs = append(allIDs, t.ID)
	}
	pl := pack.Plan{
		ID:      "all",
		Name:    name,
		Include: pack.Selector{Tests: allIDs},
		Gates:   pack.Gates{FailOnAnyFailure: true, FailOnXrayError: true},
		Outputs: pack.Outputs{JUnit: "relay-results.xml", JSON: "relay-results.json"},
	}
	if err := writeJSONFile(filepath.Join(root, "plans", "all.plan.json"), pl); err != nil {
		return "", err
	}
	if err := writeExecutionConfig(root, s, in, name, allIDs); err != nil {
		return "", err
	}
	if err := writeEnvironmentConfigs(root, s); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Server) writePlaywrightProject(root string, in testsExportRequest) (string, error) {
	tests, name, err := s.exportTestsForScope(in)
	if err != nil {
		return "", err
	}
	if len(tests) == 0 {
		return "", fmt.Errorf("no tests selected for Playwright export")
	}
	envs, err := s.DB.Environments()
	if err != nil {
		return "", err
	}
	envName := strings.TrimSpace(in.Env)
	if envName == "" && in.Scope == "execution" && in.ExecutionID != 0 {
		if ex, err := s.DB.TestExecution(in.ExecutionID); err == nil && ex != nil {
			envName = ex.Env
		}
	}
	var cases []playwrightpack.Case
	for _, tc := range tests {
		req, err := s.DB.Request(tc.RequestID)
		if err != nil {
			return "", err
		}
		col, err := s.DB.Collection(req.CollectionID)
		if err != nil {
			return "", err
		}
		var folder *store.Folder
		folderName := ""
		folderVars := map[string]string{}
		folderHeaders := map[string]string{}
		if req.FolderID != nil {
			folder, err = s.DB.Folder(*req.FolderID)
			if err != nil {
				return "", err
			}
			folderName = folder.Name
			folderVars = folder.Vars
			folderHeaders = folder.Headers
		}
		testReq := cloneRequestForTest(req, tc)
		cases = append(cases, playwrightpack.Case{
			ID:                stableTestID(tc),
			Test:              tc,
			Request:           *testReq.Spec,
			CollectionName:    col.Name,
			FolderName:        folderName,
			CollectionVars:    col.Vars,
			FolderVars:        folderVars,
			CollectionHeaders: col.Headers,
			FolderHeaders:     folderHeaders,
		})
	}
	if err := playwrightpack.Export(root, playwrightpack.Project{
		Name:               name,
		DefaultEnvironment: envName,
		Cases:              cases,
		Envs:               envs,
	}); err != nil {
		return "", err
	}
	return name, nil
}

func writeSelectedTestSets(root string, s *Server, selectedIDs map[int64]string) error {
	sets, err := s.DB.TestSets()
	if err != nil {
		return err
	}
	for _, ts := range sets {
		var members []string
		for _, id := range ts.TestIDs {
			if stable, ok := selectedIDs[id]; ok {
				members = append(members, stable)
			}
		}
		if len(members) == 0 {
			continue
		}
		sort.Strings(members)
		set := pack.Set{ID: stableSetID(ts), Name: ts.Name, Tests: members}
		if err := writeJSONFile(filepath.Join(root, "sets", set.ID+".set.json"), set); err != nil {
			return err
		}
	}
	return nil
}

func writeExecutionConfig(root string, s *Server, in testsExportRequest, name string, allIDs []string) error {
	if in.Scope != "execution" || in.ExecutionID == 0 {
		return nil
	}
	ex, err := s.DB.TestExecution(in.ExecutionID)
	if err != nil {
		return err
	}
	ids := append([]string(nil), allIDs...)
	sort.Strings(ids)
	exec := pack.Execution{
		ID:          stableExecutionID(*ex),
		Name:        firstNonEmpty(ex.Name, name),
		Plan:        "all",
		Environment: ex.Env,
		Include:     pack.Selector{Tests: ids, Tags: append([]string(nil), ex.Tags...), Priorities: append([]string(nil), ex.Priorities...)},
		Gates:       pack.Gates{FailOnAnyFailure: true, FailOnXrayError: true},
		Xray:        pack.XrayRun{TestPlanKey: ex.TestPlanKey, Summary: ex.Name, ExecutionKey: ex.XrayKey},
		Outputs:     pack.Outputs{JUnit: "relay-results.xml", JSON: "relay-results.json"},
	}
	if len(ex.TestSetIDs) > 0 {
		sets, err := s.DB.TestSets()
		if err != nil {
			return err
		}
		byID := map[int64]string{}
		for _, set := range sets {
			byID[set.ID] = stableSetID(set)
		}
		for _, id := range ex.TestSetIDs {
			if stable := byID[id]; stable != "" {
				exec.Include.Sets = append(exec.Include.Sets, stable)
			}
		}
	}
	return writeJSONFile(filepath.Join(root, "executions", exec.ID+".execution.json"), exec)
}

func writeEnvironmentConfigs(root string, s *Server) error {
	envs, err := s.DB.Environments()
	if err != nil {
		return err
	}
	for _, env := range envs {
		if err := writeEnvironmentConfig(filepath.Join(root, "environments", env.Name+".toml"), env); err != nil {
			return err
		}
	}
	return nil
}

func writeEnvironmentConfig(path string, env store.Environment) error {
	var b strings.Builder
	if len(env.Secrets) > 0 {
		names := append([]string(nil), env.Secrets...)
		sort.Strings(names)
		b.WriteString("secrets = [")
		for i, n := range names {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", n)
		}
		b.WriteString("]\n")
	}
	writeSimpleTable(&b, "vars", env.Vars)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func (s *Server) exportTestsForScope(in testsExportRequest) ([]store.TestCase, string, error) {
	switch in.Scope {
	case "collection":
		if in.CollectionID == 0 {
			return nil, "", fmt.Errorf("collectionId is required")
		}
		col, err := s.DB.Collection(in.CollectionID)
		if err != nil {
			return nil, "", err
		}
		all, err := s.DB.TestCases()
		if err != nil {
			return nil, "", err
		}
		var out []store.TestCase
		for _, tc := range all {
			req, err := s.DB.Request(tc.RequestID)
			if err != nil {
				return nil, "", err
			}
			if req.CollectionID == in.CollectionID {
				out = append(out, tc)
			}
		}
		return out, col.Name, nil
	case "folder":
		name := "test-folder"
		if in.FolderID == nil || *in.FolderID == 0 {
			all, err := s.DB.TestCases()
			if err != nil {
				return nil, "", err
			}
			var out []store.TestCase
			for _, tc := range all {
				if tc.FolderID == nil {
					out = append(out, tc)
				}
			}
			return out, "unfiled-tests", nil
		}
		tests, err := s.selectedTests(testSelection{FolderIDs: []int64{*in.FolderID}})
		if in.FolderID != nil {
			for _, f := range mustTestFolders(s) {
				if f.ID == *in.FolderID {
					name = f.Name
				}
			}
		}
		return tests, name, err
	case "source-folder":
		if in.SourceFolderID == nil || *in.SourceFolderID == 0 {
			return nil, "", fmt.Errorf("sourceFolderId is required")
		}
		folder, err := s.DB.Folder(*in.SourceFolderID)
		if err != nil {
			return nil, "", err
		}
		all, err := s.DB.TestCases()
		if err != nil {
			return nil, "", err
		}
		var out []store.TestCase
		for _, tc := range all {
			req, err := s.DB.Request(tc.RequestID)
			if err != nil {
				return nil, "", err
			}
			if req.FolderID != nil && *req.FolderID == folder.ID {
				out = append(out, tc)
			}
		}
		return out, folder.Name, nil
	case "set":
		set, err := s.DB.TestSet(in.TestSetID)
		if err != nil {
			return nil, "", err
		}
		tests, err := s.selectedTests(testSelection{TestSetIDs: []int64{in.TestSetID}})
		return tests, set.Name, err
	case "execution":
		ex, err := s.DB.TestExecution(in.ExecutionID)
		if err != nil {
			return nil, "", err
		}
		tests, err := s.selectedTests(testSelection{TestIDs: ex.TestIDs, FolderIDs: ex.FolderIDs, TestSetIDs: ex.TestSetIDs, Tags: ex.Tags, Priorities: ex.Priorities})
		return tests, ex.Name, err
	default:
		if len(in.TestIDs) == 0 {
			tests, err := s.DB.TestCases()
			return tests, "all-tests", err
		}
		tests, err := s.selectedTests(testSelection{TestIDs: in.TestIDs})
		name := in.Name
		if name == "" {
			name = "selected-tests"
		}
		return tests, name, err
	}
}

func mustTestFolders(s *Server) []store.TestFolder {
	folders, _ := s.DB.TestFolders()
	return folders
}

func writeCollectionConfig(root string, col *store.Collection) error {
	return writeSimpleConfig(filepath.Join(root, "collections", slugName(col.Name), "collection.toml"), col.Name, col.Headers, col.Vars)
}

func writeSourceFolderConfig(root string, col *store.Collection, f *store.Folder) error {
	return writeSimpleConfig(filepath.Join(root, "collections", slugName(col.Name), slugPath(f.Name), "folder.toml"), "", f.Headers, f.Vars)
}

func writeSimpleConfig(path, name string, headers, vars map[string]string) error {
	var b strings.Builder
	if name != "" {
		fmt.Fprintf(&b, "name = %q\n", name)
	}
	writeSimpleTable(&b, "headers", headers)
	writeSimpleTable(&b, "vars", vars)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeSimpleTable(b *strings.Builder, name string, vals map[string]string) {
	if len(vals) == 0 {
		return
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "\n[%s]\n", name)
	for _, k := range keys {
		fmt.Fprintf(b, "%q = %q\n", k, vals[k])
	}
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func zipDir(zw *zip.Writer, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
}

func stableTestID(t store.TestCase) string {
	return fmt.Sprintf("test-%d-%s", t.ID, slugName(t.Name))
}

func stableFolderID(f store.TestFolder) string {
	return fmt.Sprintf("folder-%d-%s", f.ID, slugName(f.Name))
}

func stableSetID(s store.TestSet) string {
	return fmt.Sprintf("set-%d-%s", s.ID, slugName(s.Name))
}

func stableExecutionID(e store.TestExecution) string {
	return fmt.Sprintf("execution-%d-%s", e.ID, slugName(e.Name))
}

var uiExportSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

func slugName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = uiExportSlugRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "relay"
	}
	return s
}

func slugPath(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = slugName(p)
	}
	return filepath.Join(parts...)
}
