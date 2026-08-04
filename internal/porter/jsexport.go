package porter

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/runner"
	"github.com/muhaymien96/relay/internal/vars"
)

// Script exports resolve variables at export time, with two exceptions that
// must stay dynamic in the generated script:
//   - secrets become environment references (the value never enters the
//     script), marked here and rendered as __ENV.X / process.env.X;
//   - computed variables ({{$uuid}}…) become live expressions so every
//     iteration of a load test generates fresh values.
//
// Markers use NUL bytes, which cannot appear in TOML strings.
const (
	mUUID = "\x00c:uuid\x00"
	mTS   = "\x00c:ts\x00"
	mISO  = "\x00c:iso\x00"
	mRand = "\x00c:rand\x00"
	mEnv  = "\x00env:" // followed by env var name, closed by \x00
)

var markerComputed = map[string]string{
	"$uuid":         mUUID,
	"$timestamp":    mTS,
	"$isoTimestamp": mISO,
	"$randomInt":    mRand,
}

// exportItem is one request prepared for script generation.
type exportItem struct {
	Name       string
	Group      string // relative folder path, "" for collection root
	Method     string
	URL        string
	Headers    [][2]string // sorted by name
	Body       string
	Assertions []dsl.Assertion
}

// collectExportItems walks a collection directory exactly like the runner
// (lexical order, config inheritance) and resolves each request with the
// marker scope.
func collectExportItems(root string, env *dsl.Environment) ([]exportItem, error) {
	files, err := runner.Collect(root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.req.toml files under %s", root)
	}
	markerGetenv := func(envVar string) string { return mEnv + envVar + "\x00" }

	var items []exportItem
	for _, f := range files {
		req, err := dsl.LoadRequest(f)
		if err != nil {
			return nil, err
		}
		headers, scopeVars, err := runner.InheritedConfig(root, f)
		if err != nil {
			return nil, err
		}
		scope := vars.NewScope(markerComputed, req.Vars, scopeVars)
		if err := scope.AddEnvironment(env, markerGetenv); err != nil {
			return nil, err
		}
		resolved, err := vars.Resolve(req, headers, scope)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}

		item := exportItem{
			Name:       req.Name,
			Method:     resolved.Method,
			URL:        resolved.URL,
			Body:       string(resolved.Body),
			Assertions: req.Assertions,
		}
		if item.Name == "" {
			item.Name = filepath.Base(f)
		}
		rel, err := filepath.Rel(root, filepath.Dir(f))
		if err != nil {
			return nil, err
		}
		if rel != "." {
			item.Group = filepath.ToSlash(rel)
		}
		names := make([]string, 0, len(resolved.Headers))
		for k := range resolved.Headers {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			item.Headers = append(item.Headers, [2]string{k, resolved.Headers.Get(k)})
		}
		items = append(items, item)
	}
	return items, nil
}

// jsDialect maps the markers to expressions for a target runtime.
type jsDialect struct {
	uuid, ts, iso, rand string
	envRef              func(name string) string
}

// tmpl renders s as a JS template literal, substituting markers with the
// dialect's expressions.
func (d jsDialect) tmpl(s string) string {
	var b strings.Builder
	b.WriteByte('`')
	for len(s) > 0 {
		i := strings.IndexByte(s, '\x00')
		if i == -1 {
			b.WriteString(escapeTmpl(s))
			break
		}
		b.WriteString(escapeTmpl(s[:i]))
		rest := s[i+1:]
		j := strings.IndexByte(rest, '\x00')
		if j == -1 { // stray NUL; drop it
			b.WriteString(escapeTmpl(rest))
			break
		}
		marker := rest[:j]
		s = rest[j+1:]
		switch {
		case marker == "c:uuid":
			fmt.Fprintf(&b, "${%s}", d.uuid)
		case marker == "c:ts":
			fmt.Fprintf(&b, "${%s}", d.ts)
		case marker == "c:iso":
			fmt.Fprintf(&b, "${%s}", d.iso)
		case marker == "c:rand":
			fmt.Fprintf(&b, "${%s}", d.rand)
		case strings.HasPrefix(marker, "env:"):
			fmt.Fprintf(&b, "${%s}", d.envRef(marker[4:]))
		}
	}
	b.WriteByte('`')
	return b.String()
}

func escapeTmpl(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "${", `\${`)
	return s
}

// jsValue renders an assertion's expected value as a JS literal.
func jsValue(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // keep < > & readable in generated scripts
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%q", fmt.Sprintf("%v", v))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// jsString renders a plain string as a quoted JS literal.
func jsString(s string) string {
	return jsValue(s)
}

// groupOrder returns the distinct groups in first-seen order.
func groupOrder(items []exportItem) []string {
	var order []string
	seen := map[string]bool{}
	for _, it := range items {
		if !seen[it.Group] {
			seen[it.Group] = true
			order = append(order, it.Group)
		}
	}
	return order
}
