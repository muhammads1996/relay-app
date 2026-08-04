// Package pack loads and materializes portable Relay test packs.
package pack

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
)

type Manifest struct {
	Version           int    `json:"version"`
	Name              string `json:"name"`
	DefaultCollection string `json:"defaultCollection,omitempty"`
	DefaultPlan       string `json:"defaultPlan,omitempty"`
}

type XrayRef struct {
	TestKey      string   `json:"testKey,omitempty"`
	TestSetKey   string   `json:"testSetKey,omitempty"`
	TestPlanKey  string   `json:"testPlanKey,omitempty"`
	Requirements []string `json:"requirements,omitempty"`
}

type XrayRun struct {
	Push         bool   `json:"push,omitempty"`
	ProjectKey   string `json:"projectKey,omitempty"`
	TestPlanKey  string `json:"testPlanKey,omitempty"`
	Summary      string `json:"summary,omitempty"`
	ExecutionKey string `json:"executionKey,omitempty"`
}

type Outputs struct {
	JUnit string `json:"junit,omitempty"`
	JSON  string `json:"json,omitempty"`
	Xray  string `json:"xray,omitempty"`
}

type Gates struct {
	FailOnAnyFailure bool    `json:"failOnAnyFailure"`
	FailOnXrayError  bool    `json:"failOnXrayError"`
	MinPassRate      float64 `json:"minPassRate,omitempty"`
}

type Selector struct {
	Folders    []string `json:"folders,omitempty"`
	Sets       []string `json:"sets,omitempty"`
	Tests      []string `json:"tests,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Priorities []string `json:"priorities,omitempty"`
}

type Test struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Request     string          `json:"request"`
	Enabled     *bool           `json:"enabled,omitempty"`
	Folder      string          `json:"folder,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Owner       string          `json:"owner,omitempty"`
	Priority    string          `json:"priority,omitempty"`
	Xray        XrayRef         `json:"xray,omitempty"`
	Assertions  []dsl.Assertion `json:"assertions,omitempty"`
	ScriptTests string          `json:"scriptTests,omitempty"`
}

type Folder struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Tests []string `json:"tests,omitempty"`
	Xray  XrayRef  `json:"xray,omitempty"`
}

type Set struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tests   []string `json:"tests,omitempty"`
	Include Selector `json:"include,omitempty"`
}

type Plan struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Include Selector `json:"include,omitempty"`
	Exclude Selector `json:"exclude,omitempty"`
	Gates   Gates    `json:"gates,omitempty"`
	Xray    XrayRun  `json:"xray,omitempty"`
	Outputs Outputs  `json:"outputs,omitempty"`
}

type Execution struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Plan        string   `json:"plan,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Include     Selector `json:"include,omitempty"`
	Gates       Gates    `json:"gates,omitempty"`
	Xray        XrayRun  `json:"xray,omitempty"`
	Outputs     Outputs  `json:"outputs,omitempty"`
}

type Pack struct {
	Root       string
	Manifest   Manifest
	Tests      map[string]Test
	Folders    map[string]Folder
	Sets       map[string]Set
	Plans      map[string]Plan
	Executions map[string]Execution
}

type RunSpec struct {
	Root         string
	Env          string
	Tests        []Test
	Gates        Gates
	Xray         XrayRun
	Outputs      Outputs
	Materialized string
}

type Target struct {
	Root string
	Kind string
	ID   string
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

func IsPackDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "relay.json"))
	return err == nil && !st.IsDir()
}

func TargetFromPath(path string) (Target, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Target{}, err
	}
	if st.IsDir() {
		if IsPackDir(path) {
			return Target{Root: path, Kind: "pack"}, nil
		}
		return Target{}, fmt.Errorf("%s is not a Relay pack directory", path)
	}
	dir := filepath.Dir(path)
	kindDir := filepath.Base(dir)
	root := filepath.Dir(dir)
	if !IsPackDir(root) {
		return Target{}, fmt.Errorf("%s is not inside a Relay pack directory", path)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	switch kindDir {
	case "plans":
		return Target{Root: root, Kind: "plan", ID: trimTypedSuffix(base, ".plan")}, nil
	case "executions":
		return Target{Root: root, Kind: "execution", ID: trimTypedSuffix(base, ".execution")}, nil
	case "folders":
		return Target{Root: root, Kind: "folder", ID: trimTypedSuffix(base, ".folder")}, nil
	case "sets":
		return Target{Root: root, Kind: "set", ID: trimTypedSuffix(base, ".set")}, nil
	case "tests":
		return Target{Root: root, Kind: "test", ID: trimTypedSuffix(base, ".test")}, nil
	default:
		return Target{}, fmt.Errorf("unsupported Relay pack target %s", path)
	}
}

func trimTypedSuffix(s, suffix string) string {
	return strings.TrimSuffix(s, suffix)
}

func Load(root string) (*Pack, error) {
	var manifest Manifest
	if err := readJSON(filepath.Join(root, "relay.json"), &manifest); err != nil {
		return nil, err
	}
	if manifest.Version == 0 {
		manifest.Version = 1
	}
	p := &Pack{
		Root: root, Manifest: manifest,
		Tests: map[string]Test{}, Folders: map[string]Folder{},
		Sets: map[string]Set{}, Plans: map[string]Plan{}, Executions: map[string]Execution{},
	}
	if err := loadDir(filepath.Join(root, "tests"), func(path string) error {
		var v Test
		if err := readJSON(path, &v); err != nil {
			return err
		}
		p.Tests[v.ID] = v
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadDir(filepath.Join(root, "folders"), func(path string) error {
		var v Folder
		if err := readJSON(path, &v); err != nil {
			return err
		}
		p.Folders[v.ID] = v
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadDir(filepath.Join(root, "sets"), func(path string) error {
		var v Set
		if err := readJSON(path, &v); err != nil {
			return err
		}
		p.Sets[v.ID] = v
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadDir(filepath.Join(root, "plans"), func(path string) error {
		var v Plan
		if err := readJSON(path, &v); err != nil {
			return err
		}
		p.Plans[v.ID] = v
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadDir(filepath.Join(root, "executions"), func(path string) error {
		var v Execution
		if err := readJSON(path, &v); err != nil {
			return err
		}
		p.Executions[v.ID] = v
		return nil
	}); err != nil {
		return nil, err
	}
	return p, nil
}

func loadDir(dir string, fn func(string) error) error {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".json") {
			return fn(path)
		}
		return nil
	})
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (p *Pack) Validate() []error {
	var errs []error
	if p.Manifest.Version != 1 {
		errs = append(errs, fmt.Errorf("unsupported pack version %d", p.Manifest.Version))
	}
	for id, t := range p.Tests {
		if id == "" || t.ID != id {
			errs = append(errs, fmt.Errorf("test %q has invalid id %q", id, t.ID))
		}
		if strings.TrimSpace(t.Request) == "" {
			errs = append(errs, fmt.Errorf("test %s has no request", id))
		} else if !insideRoot(p.Root, t.Request) {
			errs = append(errs, fmt.Errorf("test %s request escapes pack root", id))
		} else if _, err := os.Stat(filepath.Join(p.Root, filepath.FromSlash(t.Request))); err != nil {
			errs = append(errs, fmt.Errorf("test %s request missing: %s", id, t.Request))
		}
		if t.Folder != "" {
			if _, ok := p.Folders[t.Folder]; !ok {
				errs = append(errs, fmt.Errorf("test %s references missing folder %s", id, t.Folder))
			}
		}
		if t.Priority != "" && !validPriority(t.Priority) {
			errs = append(errs, fmt.Errorf("test %s has unsupported priority %q", id, t.Priority))
		}
	}
	for id, f := range p.Folders {
		for _, testID := range f.Tests {
			if _, ok := p.Tests[testID]; !ok {
				errs = append(errs, fmt.Errorf("folder %s references missing test %s", id, testID))
			}
		}
	}
	for id, s := range p.Sets {
		for _, testID := range s.Tests {
			if _, ok := p.Tests[testID]; !ok {
				errs = append(errs, fmt.Errorf("set %s references missing test %s", id, testID))
			}
		}
	}
	for id, pl := range p.Plans {
		errs = append(errs, p.validateSelector("plan "+id, pl.Include)...)
		errs = append(errs, p.validateSelector("plan "+id+" exclude", pl.Exclude)...)
		if pl.Xray.Push && strings.TrimSpace(pl.Xray.ProjectKey) == "" {
			errs = append(errs, fmt.Errorf("plan %s enables Xray push without projectKey", id))
		}
	}
	for id, ex := range p.Executions {
		if ex.Plan != "" {
			if _, ok := p.Plans[ex.Plan]; !ok {
				errs = append(errs, fmt.Errorf("execution %s references missing plan %s", id, ex.Plan))
			}
		}
		errs = append(errs, p.validateSelector("execution "+id, ex.Include)...)
		if ex.Xray.Push && strings.TrimSpace(ex.Xray.ProjectKey) == "" {
			errs = append(errs, fmt.Errorf("execution %s enables Xray push without projectKey", id))
		}
	}
	return errs
}

func (p *Pack) validateSelector(label string, s Selector) []error {
	var errs []error
	for _, id := range s.Tests {
		if _, ok := p.Tests[id]; !ok {
			errs = append(errs, fmt.Errorf("%s references missing test %s", label, id))
		}
	}
	for _, id := range s.Folders {
		if _, ok := p.Folders[id]; !ok {
			errs = append(errs, fmt.Errorf("%s references missing folder %s", label, id))
		}
	}
	for _, id := range s.Sets {
		if _, ok := p.Sets[id]; !ok {
			errs = append(errs, fmt.Errorf("%s references missing set %s", label, id))
		}
	}
	return errs
}

func (p *Pack) Resolve(target Target, envOverride, planOverride, executionOverride string) (RunSpec, error) {
	spec := RunSpec{Root: p.Root, Gates: Gates{FailOnAnyFailure: true, FailOnXrayError: true}}
	kind, id := target.Kind, target.ID
	if executionOverride != "" {
		kind, id = "execution", executionOverride
	} else if planOverride != "" {
		kind, id = "plan", planOverride
	} else if kind == "pack" {
		if p.Manifest.DefaultPlan == "" {
			kind = "all"
		} else {
			kind, id = "plan", p.Manifest.DefaultPlan
		}
	}
	var include Selector
	var exclude Selector
	switch kind {
	case "all":
		for id := range p.Tests {
			include.Tests = append(include.Tests, id)
		}
	case "test":
		include.Tests = []string{id}
	case "folder":
		include.Folders = []string{id}
	case "set":
		include.Sets = []string{id}
	case "plan":
		pl, ok := p.Plans[id]
		if !ok {
			return spec, fmt.Errorf("plan %s not found", id)
		}
		include, exclude = pl.Include, pl.Exclude
		spec.Gates, spec.Xray, spec.Outputs = mergeGates(spec.Gates, pl.Gates), pl.Xray, pl.Outputs
	case "execution":
		ex, ok := p.Executions[id]
		if !ok {
			return spec, fmt.Errorf("execution %s not found", id)
		}
		if ex.Plan != "" {
			pl, ok := p.Plans[ex.Plan]
			if !ok {
				return spec, fmt.Errorf("plan %s not found", ex.Plan)
			}
			include, exclude = pl.Include, pl.Exclude
			spec.Gates, spec.Xray, spec.Outputs = mergeGates(spec.Gates, pl.Gates), pl.Xray, pl.Outputs
		}
		include = mergeSelector(include, ex.Include)
		spec.Gates, spec.Xray, spec.Outputs = mergeGates(spec.Gates, ex.Gates), mergeXray(spec.Xray, ex.Xray), mergeOutputs(spec.Outputs, ex.Outputs)
		spec.Env = ex.Environment
	default:
		return spec, fmt.Errorf("unsupported pack target kind %s", kind)
	}
	if envOverride != "" {
		spec.Env = envOverride
	}
	tests := p.selectTests(include)
	tests = excludeTests(tests, exclude)
	sort.SliceStable(tests, func(i, j int) bool { return tests[i].ID < tests[j].ID })
	if len(tests) == 0 {
		return spec, fmt.Errorf("pack target resolved to zero tests")
	}
	spec.Tests = tests
	return spec, nil
}

func (p *Pack) selectTests(sel Selector) []Test {
	selected := map[string]Test{}
	add := func(id string) {
		if t, ok := p.Tests[id]; ok && testEnabled(t) {
			selected[id] = t
		}
	}
	for _, id := range sel.Tests {
		add(id)
	}
	for _, folderID := range sel.Folders {
		for _, t := range p.Tests {
			if t.Folder == folderID {
				add(t.ID)
			}
		}
		if f, ok := p.Folders[folderID]; ok {
			for _, id := range f.Tests {
				add(id)
			}
		}
	}
	for _, setID := range sel.Sets {
		if s, ok := p.Sets[setID]; ok {
			for _, id := range s.Tests {
				add(id)
			}
			for _, t := range p.selectTests(s.Include) {
				add(t.ID)
			}
		}
	}
	if len(sel.Tags) > 0 || len(sel.Priorities) > 0 {
		for _, t := range p.Tests {
			if testEnabled(t) && matchesSelector(t, sel) {
				add(t.ID)
			}
		}
	}
	if len(sel.Tests) == 0 && len(sel.Folders) == 0 && len(sel.Sets) == 0 && len(sel.Tags) == 0 && len(sel.Priorities) == 0 {
		for id := range p.Tests {
			add(id)
		}
	}
	out := make([]Test, 0, len(selected))
	for _, t := range selected {
		out = append(out, t)
	}
	return out
}

func excludeTests(in []Test, sel Selector) []Test {
	if len(sel.Tests) == 0 && len(sel.Folders) == 0 && len(sel.Tags) == 0 && len(sel.Priorities) == 0 {
		return in
	}
	var out []Test
	testIDs := stringSet(sel.Tests)
	folderIDs := stringSet(sel.Folders)
	for _, t := range in {
		if testIDs[t.ID] || folderIDs[t.Folder] || matchesSelector(t, sel) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func matchesSelector(t Test, sel Selector) bool {
	if len(sel.Tags) > 0 {
		have := stringSetLower(t.Tags)
		for _, tag := range sel.Tags {
			if !have[strings.ToLower(strings.TrimSpace(tag))] {
				return false
			}
		}
	}
	if len(sel.Priorities) > 0 {
		prios := stringSetLower(sel.Priorities)
		if !prios[strings.ToLower(strings.TrimSpace(t.Priority))] {
			return false
		}
	}
	return true
}

func testEnabled(t Test) bool {
	return t.Enabled == nil || *t.Enabled
}

func (p *Pack) Materialize(spec RunSpec) (string, error) {
	dir, err := os.MkdirTemp("", "relay-pack-*")
	if err != nil {
		return "", err
	}
	if err := copyInheritanceAndAssets(p.Root, dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	for i, t := range spec.Tests {
		rel := filepath.FromSlash(t.Request)
		src := filepath.Join(p.Root, rel)
		req, err := dsl.LoadRequest(src)
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		applyTest(req, t)
		outRel := filepath.Join(filepath.Dir(rel), fmt.Sprintf("%03d-%s.req.toml", i+1, slug(t.Name)))
		out := filepath.Join(dir, outRel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(out, dsl.Marshal(req), 0o644); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func applyTest(req *dsl.Request, t Test) {
	if t.Name != "" {
		req.Name = t.Name
	}
	req.Tags = append([]string(nil), t.Tags...)
	req.Owner = t.Owner
	req.Priority = t.Priority
	req.XrayKey = t.Xray.TestKey
	req.Requirements = append([]string(nil), t.Xray.Requirements...)
	req.XrayPlan = t.Xray.TestPlanKey
	req.XraySet = t.Xray.TestSetKey
	if t.Assertions != nil {
		req.Assertions = append([]dsl.Assertion(nil), t.Assertions...)
	}
	if t.ScriptTests != "" {
		if req.Scripts == nil {
			req.Scripts = &dsl.Scripts{}
		}
		req.Scripts.Tests = t.ScriptTests
	}
}

func copyInheritanceAndAssets(root, dst string) error {
	collections := filepath.Join(root, "collections")
	return filepath.WalkDir(collections, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".req.toml") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func mergeSelector(a, b Selector) Selector {
	a.Tests = append(a.Tests, b.Tests...)
	a.Folders = append(a.Folders, b.Folders...)
	a.Sets = append(a.Sets, b.Sets...)
	a.Tags = append(a.Tags, b.Tags...)
	a.Priorities = append(a.Priorities, b.Priorities...)
	return a
}

func mergeGates(a, b Gates) Gates {
	if b.FailOnAnyFailure {
		a.FailOnAnyFailure = true
	}
	if b.FailOnXrayError {
		a.FailOnXrayError = true
	}
	if b.MinPassRate != 0 {
		a.MinPassRate = b.MinPassRate
	}
	return a
}

func mergeXray(a, b XrayRun) XrayRun {
	if b.Push {
		a.Push = true
	}
	if b.ProjectKey != "" {
		a.ProjectKey = b.ProjectKey
	}
	if b.TestPlanKey != "" {
		a.TestPlanKey = b.TestPlanKey
	}
	if b.Summary != "" {
		a.Summary = b.Summary
	}
	if b.ExecutionKey != "" {
		a.ExecutionKey = b.ExecutionKey
	}
	return a
}

func mergeOutputs(a, b Outputs) Outputs {
	if b.JUnit != "" {
		a.JUnit = b.JUnit
	}
	if b.JSON != "" {
		a.JSON = b.JSON
	}
	if b.Xray != "" {
		a.Xray = b.Xray
	}
	return a
}

func insideRoot(root, rel string) bool {
	if filepath.IsAbs(rel) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func stringSet(in []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range in {
		out[s] = true
	}
	return out
}

func stringSetLower(in []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range in {
		out[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return out
}

func validPriority(p string) bool {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "low", "med", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func slug(s string) string {
	x := strings.Trim(slugRun.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if x == "" {
		return "test"
	}
	return x
}
