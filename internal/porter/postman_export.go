package porter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/runner"
)

// ExportPostman converts a collection directory (the .req.toml interchange
// format) into a Postman Collection v2.1 JSON document. Unlike the script
// exporters, variables stay as raw {{placeholders}} — Postman shares the
// same syntax, so the export is lossless for migration. Inherited
// collection/folder headers are flattened into each request (Postman has no
// header inheritance), and assertions become pm.test scripts.
func ExportPostman(root string) ([]byte, error) {
	files, err := runner.Collect(root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.req.toml files under %s", root)
	}

	colName := filepath.Base(root)
	var colVars map[string]string
	if cfg, err := dsl.LoadConfig(filepath.Join(root, "collection.toml")); err != nil {
		return nil, err
	} else if cfg != nil {
		if cfg.Name != "" {
			colName = cfg.Name
		}
		colVars = cfg.Vars
	}

	doc := map[string]any{
		"info": map[string]any{
			"name":   colName,
			"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
	}
	if len(colVars) > 0 {
		var pmVars []map[string]string
		for _, k := range sortedKeys(colVars) {
			pmVars = append(pmVars, map[string]string{"key": k, "value": colVars[k]})
		}
		doc["variable"] = pmVars
	}

	folders := map[string][]any{} // group -> items
	var groupOrder []string
	seen := map[string]bool{}

	for _, f := range files {
		req, err := dsl.LoadRequest(f)
		if err != nil {
			return nil, err
		}
		inherited, _, err := runner.InheritedConfig(root, f)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, filepath.Dir(f))
		if err != nil {
			return nil, err
		}
		group := ""
		if rel != "." {
			group = filepath.ToSlash(rel)
		}
		if !seen[group] {
			seen[group] = true
			groupOrder = append(groupOrder, group)
		}
		folders[group] = append(folders[group], postmanItem(req, inherited))
	}

	var items []any
	for _, g := range groupOrder {
		if g == "" {
			items = append(items, folders[""]...)
			continue
		}
		items = append(items, map[string]any{"name": g, "item": folders[g]})
	}
	doc["item"] = items

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func postmanItem(req *dsl.Request, inherited map[string]string) map[string]any {
	merged := map[string]string{}
	for k, v := range inherited {
		merged[k] = v
	}
	for k, v := range req.Headers {
		if v == "" { // disables an inherited header
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	var headers []map[string]string
	for _, k := range sortedKeys(merged) {
		headers = append(headers, map[string]string{"key": k, "value": merged[k]})
	}

	rawURL := req.URL
	if len(req.Query) > 0 {
		var pairs []string
		for _, k := range sortedKeys(req.Query) {
			pairs = append(pairs, k+"="+req.Query[k])
		}
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + strings.Join(pairs, "&")
	}

	pmReq := map[string]any{
		"method": req.Method,
		"header": headers,
		"url":    map[string]any{"raw": rawURL},
	}

	if b := req.Body; b != nil && b.Content != "" {
		switch b.Type {
		case "urlencoded":
			var kvs []map[string]string
			for _, pair := range strings.Split(b.Content, "&") {
				k, v, _ := strings.Cut(pair, "=")
				kvs = append(kvs, map[string]string{"key": k, "value": v})
			}
			pmReq["body"] = map[string]any{"mode": "urlencoded", "urlencoded": kvs}
		default:
			body := map[string]any{"mode": "raw", "raw": b.Content}
			if b.Type == "json" || b.Type == "xml" {
				body["options"] = map[string]any{"raw": map[string]string{"language": b.Type}}
			}
			pmReq["body"] = body
		}
	}
	if b := req.Body; b != nil && b.Type == "formdata" && len(b.FormData) > 0 {
		var fields []map[string]any
		for _, f := range b.FormData {
			fieldType := f.Type
			if fieldType == "" {
				fieldType = "text"
			}
			field := map[string]any{"key": f.Key, "type": fieldType}
			if fieldType == "file" {
				field["src"] = f.File
			} else {
				field["value"] = f.Value
			}
			if f.Disabled {
				field["disabled"] = true
			}
			fields = append(fields, field)
		}
		pmReq["body"] = map[string]any{"mode": "formdata", "formdata": fields}
	}

	if a := req.Auth; a != nil {
		kv := func(key, value string) map[string]string {
			return map[string]string{"key": key, "value": value, "type": "string"}
		}
		switch a.Type {
		case "bearer":
			pmReq["auth"] = map[string]any{"type": "bearer", "bearer": []map[string]string{kv("token", a.Token)}}
		case "basic":
			pmReq["auth"] = map[string]any{"type": "basic",
				"basic": []map[string]string{kv("username", a.Username), kv("password", a.Password)}}
		case "apikey":
			in := a.In
			if in == "" {
				in = "header"
			}
			pmReq["auth"] = map[string]any{"type": "apikey",
				"apikey": []map[string]string{kv("key", a.Key), kv("value", a.Value), kv("in", in)}}
		}
	}

	item := map[string]any{"name": req.Name, "request": pmReq}

	var events []map[string]any

	// Emit TOML assertions as pm.test lines.
	if script := pmTests(req.Assertions); len(script) > 0 {
		events = append(events, map[string]any{
			"listen": "test",
			"script": map[string]any{"type": "text/javascript", "exec": script},
		})
	}

	// Preserve pre-request and test scripts verbatim (already pm.* syntax).
	if s := req.Scripts; s != nil {
		if s.PreRequest != "" {
			events = append(events, map[string]any{
				"listen": "prerequest",
				"script": map[string]any{
					"type": "text/javascript",
					"exec": splitLines(s.PreRequest),
				},
			})
		}
		if s.Tests != "" {
			// Merge with assertion lines if any.
			testLines := splitLines(s.Tests)
			if len(events) > 0 && events[0]["listen"] == "test" {
				existing := events[0]["script"].(map[string]any)["exec"].([]string)
				testLines = append(testLines, existing...)
				events[0]["script"].(map[string]any)["exec"] = testLines
			} else {
				events = append(events, map[string]any{
					"listen": "test",
					"script": map[string]any{"type": "text/javascript", "exec": testLines},
				})
			}
		}
	}

	if len(events) > 0 {
		item["event"] = events
	}
	return item
}

func splitLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines
}

// pmTests renders assertions as Postman test-script lines.
func pmTests(assertions []dsl.Assertion) []string {
	var lines []string
	add := func(name, expr string) {
		lines = append(lines,
			fmt.Sprintf("pm.test(%s, function () { %s; });", jsString(name), expr))
	}
	for _, a := range assertions {
		switch a.Type {
		case "status":
			add(fmt.Sprintf("status is %v", a.Equals),
				fmt.Sprintf("pm.response.to.have.status(%s)", jsValue(a.Equals)))
		case "jsonpath":
			acc, err := jsAccessor(a.Path)
			if err != nil {
				continue
			}
			add(fmt.Sprintf("%s == %v", a.Path, a.Equals),
				fmt.Sprintf("pm.expect(pm.response.json()%s).to.eql(%s)", acc, jsValue(a.Equals)))
		case "header":
			if a.Contains != "" {
				add(fmt.Sprintf("header %s contains %s", a.Name, a.Contains),
					fmt.Sprintf("pm.expect(pm.response.headers.get(%s)).to.include(%s)", jsString(a.Name), jsString(a.Contains)))
			} else {
				add(fmt.Sprintf("header %s equals %v", a.Name, a.Equals),
					fmt.Sprintf("pm.expect(pm.response.headers.get(%s)).to.eql(%s)", jsString(a.Name), jsValue(a.Equals)))
			}
		case "contains":
			add(fmt.Sprintf("body contains %s", a.Contains),
				fmt.Sprintf("pm.expect(pm.response.text()).to.include(%s)", jsString(a.Contains)))
		case "max_ms":
			add(fmt.Sprintf("response under %dms", a.MaxMs),
				fmt.Sprintf("pm.expect(pm.response.responseTime).to.be.below(%d)", a.MaxMs))
		}
	}
	return lines
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
