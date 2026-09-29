// Command relay is a lightweight, local-first API client: send single
// requests, run collections with assertions (JUnit/JSON reports for CI),
// import Postman collections, and export curl commands.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/muhaymien96/relay/internal/adapters/tm"
	"github.com/muhaymien96/relay/internal/adapters/xray"
	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/pack"
	"github.com/muhaymien96/relay/internal/playwrightpack"
	"github.com/muhaymien96/relay/internal/porter"
	"github.com/muhaymien96/relay/internal/runner"
	"github.com/muhaymien96/relay/internal/secretstore"
	"github.com/muhaymien96/relay/internal/store"
	"github.com/muhaymien96/relay/internal/ui"
	"github.com/muhaymien96/relay/internal/vars"
	"github.com/muhaymien96/relay/internal/workspace"
)

var version = "0.3.0"

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

type runCIConfig struct {
	Report     string        `json:"report"`
	Out        string        `json:"out"`
	JSONOut    string        `json:"jsonOut"`
	Tags       []string      `json:"tags"`
	Priorities []string      `json:"priorities"`
	Gates      gateConfig    `json:"gates"`
	Xray       xrayRunConfig `json:"xray"`
}

type gateConfig struct {
	FailOnAnyFailure bool `json:"failOnAnyFailure"`
	FailOnXrayError  bool `json:"failOnXrayError"`
}

type xrayRunConfig struct {
	Push         bool   `json:"push"`
	ProjectKey   string `json:"projectKey"`
	TestPlanKey  string `json:"testPlanKey"`
	Summary      string `json:"summary"`
	ExecutionKey string `json:"executionKey"`
}

const usage = `relay — lightweight, local-first API client

Usage:
  relay send <file.req.toml> [--env NAME] [-v] [--insecure] [--timeout 30s]
  relay run  <dir>           [--env NAME] [--report junit|json] [--out FILE]
                             [--data rows.csv|rows.json] [--delay 0ms] [--bail]
                             [--config relay-run.json] [--plan ID] [--execution ID] [--no-cookies]
                             [--xray-push] [--insecure] [--timeout 30s]
  relay import postman <collection.json> [--out DIR]
  relay import curl '<command>'          [--out FILE]   (or pipe via stdin)
  relay import openapi <spec.json>       [--out DIR]
  relay export curl <file.req.toml> [--env NAME]
  relay export postman <dir> [--out collection.json]
  relay export openapi <dir> [--out spec.json]
  relay export k6 <dir> [--env NAME] [--out script.js]
  relay export playwright <dir> [--env NAME] [--out api.spec.ts]
  relay pack validate <relay-dir>
  relay workspace migrate <dir> [--apply] [--db relay.db]
  relay xray import <relay-playwright-results.json> --project KEY [--plan KEY] [--summary TEXT]
  relay ui [dir] [--db relay.db] [--port 7717]
  relay version

Environments are TOML files at environments/<NAME>.toml, found by walking up
from the request file. Secrets listed in an environment are read from
RELAY_SECRET_<NAME> process environment variables.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "send":
		err = cmdSend(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "import":
		err = cmdImport(os.Args[2:])
	case "export":
		err = cmdExport(os.Args[2:])
	case "pack":
		err = cmdPack(os.Args[2:])
	case "workspace":
		err = cmdWorkspace(os.Args[2:])
	case "xray":
		err = cmdXray(os.Args[2:])
	case "ui":
		err = cmdUI(os.Args[2:])
	case "version", "--version":
		fmt.Println("relay", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

// parseInterleaved parses flags that may appear before or after positional
// arguments (stdlib flag stops at the first positional).
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func engineFlags(fs *flag.FlagSet) func() engine.Options {
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification")
	timeout := fs.Duration("timeout", 30*time.Second, "request timeout")
	noRedirect := fs.Bool("no-redirect", false, "do not follow redirects")
	return func() engine.Options {
		o := engine.NewOptions()
		o.Insecure = *insecure
		o.Timeout = *timeout
		o.FollowRedirects = !*noRedirect
		return o
	}
}

// loadEnv finds environments/<name>.toml walking up from start.
func loadEnv(name, start string) (*dsl.Environment, error) {
	if name == "" {
		return nil, nil
	}
	if filepath.Ext(name) == ".toml" { // direct path
		return dsl.LoadEnvironment(name)
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		p := filepath.Join(dir, "environments", name+".toml")
		if _, err := os.Stat(p); err == nil {
			return dsl.LoadEnvironment(p)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("environment %q not found (looked for environments/%s.toml up from %s)", name, name, start)
		}
		dir = parent
	}
}

func resolveFile(file, envName string) (*dsl.Request, *vars.Resolved, error) {
	req, err := dsl.LoadRequest(file)
	if err != nil {
		return nil, nil, err
	}
	env, err := loadEnv(envName, filepath.Dir(file))
	if err != nil {
		return nil, nil, err
	}
	scope := vars.NewScope(req.Vars)
	if err := scope.AddEnvironment(env, os.Getenv); err != nil {
		return nil, nil, err
	}
	resolved, err := vars.Resolve(req, nil, scope)
	if err != nil {
		return nil, nil, err
	}
	return req, resolved, nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	envName := fs.String("env", "", "environment name")
	verbose := fs.Bool("v", false, "print request and response headers")
	opts := engineFlags(fs)
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay send <file.req.toml>")
	}

	_, resolved, err := resolveFile(pos[0], *envName)
	if err != nil {
		return err
	}
	if *verbose {
		fmt.Printf("> %s %s\n", resolved.Method, resolved.URL)
		for k, v := range resolved.Headers {
			fmt.Printf("> %s: %s\n", k, v[0])
		}
		fmt.Println()
	}

	result, err := engine.Send(context.Background(), resolved, opts())
	if err != nil {
		return err
	}

	fmt.Printf("%s %s  %s  %s\n", resolved.Method, resolved.URL, result.StatusText, result.Timing.Total.Round(time.Millisecond))
	if *verbose {
		for k, v := range result.Headers {
			fmt.Printf("< %s: %s\n", k, v[0])
		}
		t := result.Timing
		fmt.Printf("< timing: dns=%s connect=%s tls=%s ttfb=%s download=%s total=%s\n",
			t.DNS.Round(time.Microsecond), t.Connect.Round(time.Microsecond),
			t.TLS.Round(time.Microsecond), t.TTFB.Round(time.Microsecond),
			t.Download.Round(time.Microsecond), t.Total.Round(time.Microsecond))
	}
	fmt.Println(string(prettyJSON(result.Body)))
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	envName := fs.String("env", "", "environment name")
	configFile := fs.String("config", "", "CI config JSON file")
	report := fs.String("report", "", "report format: junit or json")
	out := fs.String("out", "", "report output file (default stdout)")
	jsonOut := fs.String("json-out", "", "write Relay JSON report in addition to --report")
	delay := fs.Duration("delay", 0, "delay between requests")
	bail := fs.Bool("bail", false, "stop at first failure")
	dataFile := fs.String("data", "", "CSV or JSON file of variable rows for data-driven runs")
	packPlan := fs.String("plan", "", "Relay pack plan id")
	packExecution := fs.String("execution", "", "Relay pack execution id")
	tagsFlag := fs.String("tags", "", "comma-separated tags; all must be present")
	prioritiesFlag := fs.String("priorities", "", "comma-separated priorities to include")
	disableCookies := fs.Bool("no-cookies", false, "disable cookie persistence during this run")
	xrayPush := fs.Bool("xray-push", false, "push run as a new Xray Cloud Test Execution")
	xrayProject := fs.String("xray-project", "", "Xray/Jira project key for execution push")
	xrayPlan := fs.String("xray-plan", "", "optional Xray test plan key")
	xraySummary := fs.String("xray-summary", "", "Xray Test Execution summary")
	xrayExecutionKey := fs.String("xray-execution-key", "", "existing Xray Test Execution key (reserved; append/update not implemented yet)")
	opts := engineFlags(fs)
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay run <dir>")
	}
	root := pos[0]
	if _, statErr := os.Stat(filepath.Join(root, "workspace.toml")); statErr == nil {
		loaded, loadErr := workspace.Open(root)
		if loadErr != nil {
			return loadErr
		}
		if len(loaded.Collections) != 1 {
			return fmt.Errorf("workspace has %d collections; run relay run on an explicit collection directory under collections/", len(loaded.Collections))
		}
		root = filepath.Dir(loaded.Collections[0].Path)
	}
	if target, err := pack.TargetFromPath(root); err == nil {
		return runPackTarget(runPackOptions{
			Target:           target,
			Plan:             *packPlan,
			Execution:        *packExecution,
			EnvName:          *envName,
			ConfigFile:       *configFile,
			Report:           *report,
			Out:              *out,
			JSONOut:          *jsonOut,
			Delay:            *delay,
			Bail:             *bail,
			DataFile:         *dataFile,
			TagsFlag:         *tagsFlag,
			PrioritiesFlag:   *prioritiesFlag,
			XrayPush:         *xrayPush,
			XrayProject:      *xrayProject,
			XrayPlan:         *xrayPlan,
			XraySummary:      *xraySummary,
			XrayExecutionKey: *xrayExecutionKey,
			Engine:           opts(),
			DisableCookies:   *disableCookies,
		})
	}
	cfg, err := loadRunCIConfig(*configFile)
	if err != nil {
		return err
	}
	if *report == "" {
		*report = cfg.Report
	}
	if *out == "" {
		*out = cfg.Out
	}
	if *jsonOut == "" {
		*jsonOut = cfg.JSONOut
	}
	tags := cfg.Tags
	if *tagsFlag != "" {
		tags = splitCSVFlag(*tagsFlag)
	}
	priorities := cfg.Priorities
	if *prioritiesFlag != "" {
		priorities = splitCSVFlag(*prioritiesFlag)
	}
	pushXray := cfg.Xray.Push || *xrayPush
	projectKey := firstNonEmpty(*xrayProject, cfg.Xray.ProjectKey)
	testPlanKey := firstNonEmpty(*xrayPlan, cfg.Xray.TestPlanKey)
	summary := firstNonEmpty(*xraySummary, cfg.Xray.Summary)
	existingExecutionKey := firstNonEmpty(*xrayExecutionKey, cfg.Xray.ExecutionKey)
	failOnAnyFailure := true
	failOnXrayError := true
	if *configFile != "" {
		failOnAnyFailure = cfg.Gates.FailOnAnyFailure
		failOnXrayError = cfg.Gates.FailOnXrayError
	}

	env, err := loadEnv(*envName, root)
	if err != nil {
		return err
	}
	data, err := loadData(*dataFile)
	if err != nil {
		return err
	}

	rep, err := runner.Run(context.Background(), root, runner.Options{
		Env:            env,
		Getenv:         os.Getenv,
		Delay:          *delay,
		Bail:           *bail,
		Tags:           tags,
		Priorities:     priorities,
		Data:           data,
		Engine:         opts(),
		DisableCookies: *disableCookies,
		OnDone: func(rr runner.RequestResult) {
			mark := "PASS"
			if rr.Failed() {
				mark = "FAIL"
			}
			fmt.Printf("[%s] %-40s %s %s (%d, %s)\n", mark, rr.Name, rr.Method, rr.URL, rr.Status, rr.Duration.Round(time.Millisecond))
			if rr.Err != nil {
				fmt.Printf("       error: %v\n", rr.Err)
			}
			for _, a := range rr.Assertions {
				if !a.Passed {
					fmt.Printf("       assert %s: %s\n", a.Assertion.Type, a.Message)
				}
			}
		},
	})
	if err != nil {
		return err
	}
	if len(rep.Results) == 0 {
		return fmt.Errorf("no requests matched the selected run filters")
	}
	fmt.Printf("\n%d requests, %d failed, %s\n", len(rep.Results), rep.Failures(), rep.Duration.Round(time.Millisecond))

	if *report != "" {
		if err := writeRunReport(*report, *out, rep); err != nil {
			return err
		}
	}
	if *jsonOut != "" {
		if err := writeRunReport("json", *jsonOut, rep); err != nil {
			return err
		}
	}
	if pushXray {
		if existingExecutionKey != "" {
			return fmt.Errorf("--xray-execution-key is reserved until Xray Cloud append/update execution API support is verified; omit it to create a new Test Execution")
		}
		if projectKey == "" {
			return fmt.Errorf("xray project key is required for --xray-push")
		}
		if summary == "" {
			summary = "Relay automated execution - " + time.Now().UTC().Format(time.RFC3339)
		}
		exec := runnerReportToExecution(rep, projectKey, testPlanKey, summary)
		client := xray.New(xray.ConfigFromEnv())
		key, err := client.PushExecution(exec)
		if err != nil {
			if failOnXrayError {
				fmt.Fprintf(os.Stderr, "relay: xray push failed: %v\n", err)
				os.Exit(3)
			}
			fmt.Fprintf(os.Stderr, "warning: xray push failed: %v\n", err)
		} else {
			fmt.Printf("Xray Test Execution: %s\n", key)
		}
	}
	if failOnAnyFailure && rep.Failures() > 0 {
		os.Exit(1)
	}
	return nil
}

type runPackOptions struct {
	Target           pack.Target
	Plan             string
	Execution        string
	EnvName          string
	ConfigFile       string
	Report           string
	Out              string
	JSONOut          string
	Delay            time.Duration
	Bail             bool
	DataFile         string
	TagsFlag         string
	PrioritiesFlag   string
	XrayPush         bool
	XrayProject      string
	XrayPlan         string
	XraySummary      string
	XrayExecutionKey string
	Engine           engine.Options
	DisableCookies   bool
}

func runPackTarget(o runPackOptions) error {
	cfg, err := loadRunCIConfig(o.ConfigFile)
	if err != nil {
		return err
	}
	p, err := pack.Load(o.Target.Root)
	if err != nil {
		return err
	}
	if errs := p.Validate(); len(errs) > 0 {
		return packValidationError(errs)
	}
	spec, err := p.Resolve(o.Target, o.EnvName, o.Plan, o.Execution)
	if err != nil {
		return err
	}
	root, err := p.Materialize(spec)
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	report := firstNonEmpty(o.Report, cfg.Report)
	out := firstNonEmpty(o.Out, cfg.Out)
	jsonOut := firstNonEmpty(o.JSONOut, cfg.JSONOut)
	if report == "" && spec.Outputs.JUnit != "" {
		report, out = "junit", spec.Outputs.JUnit
	}
	if jsonOut == "" && spec.Outputs.JSON != "" {
		jsonOut = spec.Outputs.JSON
	}
	tags := cfg.Tags
	if o.TagsFlag != "" {
		tags = splitCSVFlag(o.TagsFlag)
	}
	priorities := cfg.Priorities
	if o.PrioritiesFlag != "" {
		priorities = splitCSVFlag(o.PrioritiesFlag)
	}

	pushXray := spec.Xray.Push || cfg.Xray.Push || o.XrayPush
	projectKey := firstNonEmpty(o.XrayProject, cfg.Xray.ProjectKey, spec.Xray.ProjectKey)
	testPlanKey := firstNonEmpty(o.XrayPlan, cfg.Xray.TestPlanKey, spec.Xray.TestPlanKey)
	summary := firstNonEmpty(o.XraySummary, cfg.Xray.Summary, spec.Xray.Summary)
	existingExecutionKey := firstNonEmpty(o.XrayExecutionKey, cfg.Xray.ExecutionKey, spec.Xray.ExecutionKey)
	failOnAnyFailure := spec.Gates.FailOnAnyFailure
	failOnXrayError := spec.Gates.FailOnXrayError
	if o.ConfigFile != "" {
		failOnAnyFailure = cfg.Gates.FailOnAnyFailure
		failOnXrayError = cfg.Gates.FailOnXrayError
	}

	env, err := loadEnv(spec.Env, o.Target.Root)
	if err != nil {
		return err
	}
	data, err := loadData(o.DataFile)
	if err != nil {
		return err
	}
	rep, err := runner.Run(context.Background(), root, runner.Options{
		Env:            env,
		Getenv:         os.Getenv,
		Delay:          o.Delay,
		Bail:           o.Bail,
		Tags:           tags,
		Priorities:     priorities,
		Data:           data,
		Engine:         o.Engine,
		DisableCookies: o.DisableCookies,
		OnDone: func(rr runner.RequestResult) {
			mark := "PASS"
			if rr.Failed() {
				mark = "FAIL"
			}
			fmt.Printf("[%s] %-40s %s %s (%d, %s)\n", mark, rr.Name, rr.Method, rr.URL, rr.Status, rr.Duration.Round(time.Millisecond))
			if rr.Err != nil {
				fmt.Printf("       error: %v\n", rr.Err)
			}
			for _, a := range rr.Assertions {
				if !a.Passed {
					fmt.Printf("       assert %s: %s\n", a.Assertion.Type, a.Message)
				}
			}
		},
	})
	if err != nil {
		return err
	}
	if len(rep.Results) == 0 {
		return fmt.Errorf("no requests matched the selected run filters")
	}
	fmt.Printf("\n%d requests, %d failed, %s\n", len(rep.Results), rep.Failures(), rep.Duration.Round(time.Millisecond))

	if report != "" {
		if err := writeRunReport(report, out, rep); err != nil {
			return err
		}
	}
	if jsonOut != "" {
		if err := writeRunReport("json", jsonOut, rep); err != nil {
			return err
		}
	}
	if pushXray {
		if existingExecutionKey != "" {
			return fmt.Errorf("--xray-execution-key is reserved until Xray Cloud append/update execution API support is verified; omit it to create a new Test Execution")
		}
		if projectKey == "" {
			return fmt.Errorf("xray project key is required for Xray push")
		}
		if summary == "" {
			summary = "Relay automated execution - " + time.Now().UTC().Format(time.RFC3339)
		}
		key, err := xray.New(xray.ConfigFromEnv()).PushExecution(runnerReportToExecution(rep, projectKey, testPlanKey, summary))
		if err != nil {
			if failOnXrayError {
				fmt.Fprintf(os.Stderr, "relay: xray push failed: %v\n", err)
				os.Exit(3)
			}
			fmt.Fprintf(os.Stderr, "warning: xray push failed: %v\n", err)
		} else {
			fmt.Printf("Xray Test Execution: %s\n", key)
		}
	}
	if failOnAnyFailure && rep.Failures() > 0 {
		os.Exit(1)
	}
	return nil
}

func packValidationError(errs []error) error {
	var b strings.Builder
	b.WriteString("invalid Relay pack")
	for _, err := range errs {
		b.WriteString("\n - ")
		b.WriteString(err.Error())
	}
	return fmt.Errorf("%s", b.String())
}

func loadRunCIConfig(path string) (runCIConfig, error) {
	cfg := runCIConfig{
		Gates: gateConfig{FailOnAnyFailure: true, FailOnXrayError: true},
	}
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func writeRunReport(format, out string, rep *runner.Report) error {
	w := os.Stdout
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	switch format {
	case "junit":
		return runner.WriteJUnit(w, rep)
	case "json":
		return runner.WriteJSON(w, rep)
	default:
		return fmt.Errorf("unknown report format %q (junit|json)", format)
	}
}

func splitCSVFlag(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func runnerReportToExecution(rep *runner.Report, projectKey, testPlanKey, summary string) tm.Execution {
	exec := tm.Execution{
		ProjectKey: projectKey, TestPlanKey: testPlanKey, Summary: summary,
		StartedAt: rep.Started, FinishedAt: rep.Started.Add(rep.Duration),
	}
	for _, r := range rep.Results {
		status := tm.StatusPASS
		if r.Failed() {
			status = tm.StatusFAIL
		}
		exec.Results = append(exec.Results, tm.TestResult{
			TestKey: r.XrayKey, Name: r.Name, Status: status, Comment: r.GetComment(),
			DurationMs: r.GetDurationMs(), Requirements: append([]string(nil), r.Requirements...),
		})
	}
	return exec
}

func cmdImport(args []string) error {
	if len(args) >= 1 && args[0] == "curl" {
		return cmdImportCurl(args[1:])
	}
	if len(args) >= 1 && args[0] == "openapi" {
		return cmdImportOpenAPI(args[1:])
	}
	if len(args) < 1 || args[0] != "postman" {
		return fmt.Errorf("usage: relay import postman|curl|openapi ...")
	}
	fs := flag.NewFlagSet("import postman", flag.ExitOnError)
	out := fs.String("out", "", "output directory (default: collection name)")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay import postman <collection.json>")
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	dir := *out
	if dir == "" {
		var probe struct {
			Info struct {
				Name string `json:"name"`
			} `json:"info"`
		}
		_ = json.Unmarshal(data, &probe)
		if probe.Info.Name == "" {
			return fmt.Errorf("collection has no name; pass --out DIR")
		}
		dir = probe.Info.Name
	}
	n, err := porter.ImportPostman(data, dir)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d requests into %s/\n", n, dir)
	return nil
}

func cmdImportOpenAPI(args []string) error {
	fs := flag.NewFlagSet("import openapi", flag.ExitOnError)
	out := fs.String("out", "", "output directory (default: spec title slug)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay import openapi <spec.json>")
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	dir := *out
	if dir == "" {
		var probe struct {
			Info struct {
				Title string `json:"title"`
			} `json:"info"`
		}
		_ = json.Unmarshal(data, &probe)
		if probe.Info.Title != "" {
			dir = nonSlugRun.ReplaceAllString(strings.ToLower(probe.Info.Title), "-")
			dir = strings.Trim(dir, "-")
		}
		if dir == "" {
			dir = "openapi-collection"
		}
	}
	n, err := porter.ImportOpenAPI(data, dir)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d requests into %s/\n", n, dir)
	return nil
}

// cmdImportCurl turns a pasted curl command (argument or stdin) into a
// .req.toml file.
func cmdImportCurl(args []string) error {
	fs := flag.NewFlagSet("import curl", flag.ExitOnError)
	out := fs.String("out", "", "output file (default derived from the URL path)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	command := strings.TrimSpace(strings.Join(pos, " "))
	if command == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		command = strings.TrimSpace(string(data))
	}
	if command == "" {
		return fmt.Errorf("usage: relay import curl '<command>' (or pipe the command via stdin)")
	}
	req, err := porter.ParseCurl(command)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		slugged := strings.Trim(nonSlugRun.ReplaceAllString(strings.ToLower(req.Name), "-"), "-")
		if slugged == "" {
			slugged = "request"
		}
		path = slugged + ".req.toml"
	}
	if err := os.WriteFile(path, dsl.Marshal(req), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%s %s)\n", path, req.Method, req.URL)
	return nil
}

func cmdExport(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: relay export curl|k6|playwright|postman <target>")
	}
	target := args[0]
	fs := flag.NewFlagSet("export "+target, flag.ExitOnError)
	envName := fs.String("env", "", "environment name")
	out := fs.String("out", "", "output file (default stdout)")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay export %s <target>", target)
	}

	var script string
	switch target {
	case "curl":
		_, resolved, err := resolveFile(pos[0], *envName)
		if err != nil {
			return err
		}
		script = porter.Curl(resolved) + "\n"
	case "k6", "playwright":
		env, err := loadEnv(*envName, pos[0])
		if err != nil {
			return err
		}
		if target == "k6" {
			script, err = porter.K6(pos[0], env)
		} else {
			script, err = porter.Playwright(pos[0], env)
		}
		if err != nil {
			return err
		}
	case "postman":
		out, err := porter.ExportPostman(pos[0])
		if err != nil {
			return err
		}
		script = string(out)
	case "openapi":
		out, err := porter.ExportOpenAPI(pos[0])
		if err != nil {
			return err
		}
		script = string(out)
	default:
		return fmt.Errorf("unknown export target %q (curl|k6|playwright|postman|openapi)", target)
	}

	if *out != "" {
		if err := os.WriteFile(*out, []byte(script), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", *out)
		return nil
	}
	fmt.Print(script)
	return nil
}

func cmdPack(args []string) error {
	if len(args) < 1 || args[0] != "validate" {
		return fmt.Errorf("usage: relay pack validate <relay-dir>")
	}
	fs := flag.NewFlagSet("pack validate", flag.ExitOnError)
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay pack validate <relay-dir>")
	}
	target, err := pack.TargetFromPath(pos[0])
	if err != nil {
		return err
	}
	p, err := pack.Load(target.Root)
	if err != nil {
		return err
	}
	if errs := p.Validate(); len(errs) > 0 {
		return packValidationError(errs)
	}
	fmt.Printf("Relay pack valid: %s (%d tests, %d folders, %d sets, %d plans, %d executions)\n",
		p.Manifest.Name, len(p.Tests), len(p.Folders), len(p.Sets), len(p.Plans), len(p.Executions))
	return nil
}

func cmdWorkspace(args []string) error {
	if len(args) == 0 || args[0] != "migrate" {
		return fmt.Errorf("usage: relay workspace migrate <dir> [--apply] [--db relay.db]")
	}
	fs := flag.NewFlagSet("workspace migrate", flag.ExitOnError)
	apply := fs.Bool("apply", false, "create backup and migrate when preview has no blockers")
	dbPathFlag := fs.String("db", "", "SQLite database path (default <dir>/relay.db")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay workspace migrate <dir> [--apply] [--db relay.db]")
	}
	root, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	dbPath := *dbPathFlag
	if dbPath == "" {
		dbPath = filepath.Join(root, "relay.db")
	} else if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(root, dbPath)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	preview, err := workspace.PreviewSQLiteMigration(db, root)
	if err != nil {
		return err
	}
	fmt.Printf("SQLite workspace migration preview: %d collections, %d folders, %d requests, %d environments, %d test cases, %d test sets, %d executions, %d presets\n", preview.Collections, preview.Folders, preview.Requests, preview.Environments, preview.TestCases, preview.TestSets, preview.TestExecutions, preview.Presets)
	for _, reason := range preview.Blockers {
		fmt.Printf("BLOCKED: %s\n", reason)
	}
	if !*apply {
		return nil
	}
	result, err := workspace.MigrateSQLite(db, root)
	if err != nil {
		return err
	}
	fmt.Printf("workspace migrated: %s\nSQLite backup: %s\nID map: %s\n", result.Workspace, result.Backup, filepath.Join(root, ".relay", "migration-v2-id-map.json"))
	return nil
}

func cmdXray(args []string) error {
	if len(args) < 1 || args[0] != "import" {
		return fmt.Errorf("usage: relay xray import <relay-playwright-results.json> --project KEY [--plan KEY] [--summary TEXT]")
	}
	fs := flag.NewFlagSet("xray import", flag.ExitOnError)
	project := fs.String("project", "", "Xray/Jira project key")
	plan := fs.String("plan", "", "optional Xray test plan key")
	summary := fs.String("summary", "", "Xray Test Execution summary")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: relay xray import <relay-playwright-results.json> --project KEY [--plan KEY] [--summary TEXT]")
	}
	if strings.TrimSpace(*project) == "" {
		return fmt.Errorf("--project is required")
	}
	key, err := importPlaywrightResultsToXray(pos[0], *project, *plan, *summary, func(exec tm.Execution) (string, error) {
		return xray.New(xray.ConfigFromEnv()).PushExecution(exec)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Xray Test Execution: %s\n", key)
	return nil
}

func importPlaywrightResultsToXray(path, project, plan, summary string, push func(tm.Execution) (string, error)) (string, error) {
	results, err := playwrightpack.LoadResults(path)
	if err != nil {
		return "", err
	}
	exec := results.ToExecution(project, plan, summary)
	key, err := push(exec)
	if err != nil {
		return "", fmt.Errorf("xray import failed: %w", err)
	}
	if key == "" {
		return "", fmt.Errorf("xray import returned no execution key")
	}
	return key, nil
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	port := fs.Int("port", 7717, "port to bind on 127.0.0.1 (0 picks a free port)")
	dbPath := fs.String("db", "", "SQLite workspace database (default <dir>/relay.db)")
	opts := engineFlags(fs)
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	root := "."
	if len(pos) == 1 {
		root = pos[0]
	} else if len(pos) > 1 {
		return fmt.Errorf("usage: relay ui [dir]")
	}
	db, err := openWorkspaceDB(root, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if runtime.GOOS == "windows" {
		db.SetSecretStore(secretstore.New())
	}
	srv := &ui.Server{DB: db, Engine: opts(), WorkspaceRoot: root}
	if err := srv.Prepare(); err != nil {
		return fmt.Errorf("open workspace files: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.ListenAndServe(ctx, *port)
}

// openWorkspaceDB opens the workspace database (default <dir>/relay.db) and,
// when the database is brand new and the directory holds .req.toml files,
// seeds it from them so existing file-based workspaces open seamlessly.
func openWorkspaceDB(dir, dbPath string) (*store.Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := workspace.RecoverMigration(abs); err != nil {
		return nil, err
	}
	_, markerErr := os.Stat(filepath.Join(abs, "workspace.toml"))
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return nil, markerErr
	}
	if dbPath == "" {
		dbPath = filepath.Join(abs, "relay.db")
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	empty, err := db.Empty()
	if err != nil {
		db.Close()
		return nil, err
	}
	if empty {
		if os.IsNotExist(markerErr) && (lenMatches(abs) > 0 || hasNestedRequests(abs)) {
			if _, err := db.SeedFromDir(abs); err != nil {
				db.Close()
				return nil, fmt.Errorf("seeding from %s: %w", abs, err)
			}
			fmt.Printf("relay: seeded workspace database from %s\n", abs)
		}
	}
	return db, nil
}

func lenMatches(abs string) int {
	matches, _ := filepath.Glob(filepath.Join(abs, "*.req.toml"))
	return len(matches)
}

func hasNestedRequests(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (strings.HasPrefix(d.Name(), ".") && path != dir) {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, ".req.toml") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// loadData reads data-driven rows from a CSV (header row = variable names)
// or a JSON array of objects.
func loadData(path string) ([]map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".json") {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var rows []map[string]any
		if err := dec.Decode(&rows); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out := make([]map[string]string, 0, len(rows))
		for _, r := range rows {
			m := make(map[string]string, len(r))
			for k, v := range r {
				m[k] = fmt.Sprintf("%v", v)
			}
			out = append(out, m)
		}
		return out, nil
	}
	recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(recs) < 2 {
		return nil, fmt.Errorf("%s: need a header row and at least one data row", path)
	}
	header := recs[0]
	out := make([]map[string]string, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		m := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(rec) {
				m[strings.TrimSpace(h)] = rec[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func prettyJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return b
	}
	return out
}
