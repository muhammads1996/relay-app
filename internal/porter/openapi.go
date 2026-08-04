package porter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/runner"
)

// ---- OpenAPI 3.x import ----

type oaDoc struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title string `json:"title"`
	} `json:"info"`
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths map[string]oaPath `json:"paths"`
}

type oaPath map[string]*oaOp // key = http method

type oaOp struct {
	OperationID string         `json:"operationId"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Parameters  []oaParam      `json:"parameters"`
	RequestBody *oaRequestBody `json:"requestBody"`
	Tags        []string       `json:"tags"`
}

type oaParam struct {
	Name     string `json:"name"`
	In       string `json:"in"` // query | header | path | cookie
	Required bool   `json:"required"`
	Schema   *struct {
		Type    string `json:"type"`
		Default any    `json:"default"`
	} `json:"schema"`
}

type oaRequestBody struct {
	Content map[string]*oaMediaType `json:"content"`
}

type oaMediaType struct {
	Example any             `json:"example"`
	Schema  json.RawMessage `json:"schema"`
}

// ImportOpenAPI converts an OpenAPI 3.x or Swagger 2.x JSON document into a
// directory of .req.toml files. Paths become folders when they have a common
// prefix or tag. Returns the number of requests written.
func ImportOpenAPI(data []byte, outDir string) (int, error) {
	// Try OpenAPI 3.x first.
	var doc oaDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("not a valid OpenAPI document: %w", err)
	}
	if doc.Paths == nil {
		return 0, fmt.Errorf("OpenAPI document has no paths")
	}

	baseURL := "{{baseUrl}}"
	if len(doc.Servers) > 0 && doc.Servers[0].URL != "" {
		baseURL = doc.Servers[0].URL
	}
	colName := doc.Info.Title
	if colName == "" {
		colName = "OpenAPI Import"
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, err
	}
	cfg := fmt.Sprintf("name = %q\n\n[vars]\nbaseUrl = %q\n", colName, baseURL)
	if err := os.WriteFile(filepath.Join(outDir, "collection.toml"), []byte(cfg), 0o644); err != nil {
		return 0, err
	}

	// Sort paths for deterministic output.
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Group by first tag or first path segment.
	type entry struct {
		path, method string
		op           *oaOp
	}
	groups := map[string][]entry{}
	groupOrder := []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		ops := doc.Paths[path]
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head", "options"} {
			op := ops[method]
			if op == nil {
				continue
			}
			group := ""
			if len(op.Tags) > 0 {
				group = slug(op.Tags[0])
			} else {
				// first non-empty path segment
				parts := strings.Split(strings.Trim(path, "/"), "/")
				if len(parts) > 0 && parts[0] != "" {
					group = slug(parts[0])
				}
			}
			if group == "" {
				group = "requests"
			}
			if !seen[group] {
				seen[group] = true
				groupOrder = append(groupOrder, group)
			}
			groups[group] = append(groups[group], entry{path, method, op})
		}
	}

	count := 0
	for _, g := range groupOrder {
		dir := filepath.Join(outDir, g)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return count, err
		}
		entries := groups[g]
		for i, e := range entries {
			req := oaToRequest(baseURL, e.path, e.method, e.op)
			name := fmt.Sprintf("%02d-%s.req.toml", i+1, requestSlug(e.method, e.path, e.op))
			if err := os.WriteFile(filepath.Join(dir, name), dsl.Marshal(req), 0o644); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func oaToRequest(baseURL, path, method string, op *oaOp) *dsl.Request {
	// Convert {param} → {{param}} for Relay variable syntax.
	relayPath := strings.NewReplacer("{", "{{", "}", "}}").Replace(path)
	url := strings.TrimRight(baseURL, "/") + relayPath

	name := op.Summary
	if name == "" && op.OperationID != "" {
		name = op.OperationID
	}
	if name == "" {
		name = strings.ToUpper(method) + " " + path
	}

	req := &dsl.Request{
		Name:   name,
		Method: strings.ToUpper(method),
		URL:    url,
	}

	// Query parameters.
	for _, p := range op.Parameters {
		if p.In == "query" {
			if req.Query == nil {
				req.Query = map[string]string{}
			}
			defaultVal := ""
			if p.Schema != nil && p.Schema.Default != nil {
				defaultVal = fmt.Sprintf("%v", p.Schema.Default)
			}
			req.Query[p.Name] = defaultVal
		}
		if p.In == "header" {
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			req.Headers[p.Name] = ""
		}
	}

	// Request body.
	if op.RequestBody != nil {
		if mt, ok := op.RequestBody.Content["application/json"]; ok {
			content := ""
			if mt.Example != nil {
				if b, err := json.MarshalIndent(mt.Example, "", "  "); err == nil {
					content = string(b)
				}
			}
			req.Body = &dsl.Body{Type: "json", Content: content}
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			req.Headers["Content-Type"] = "application/json"
		} else if mt, ok := op.RequestBody.Content["application/xml"]; ok {
			content := ""
			if mt.Example != nil {
				if b, err := json.MarshalIndent(mt.Example, "", "  "); err == nil {
					content = string(b)
				}
			}
			req.Body = &dsl.Body{Type: "xml", Content: content}
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			req.Headers["Content-Type"] = "application/xml"
		}
	}

	return req
}

func requestSlug(method, path string, op *oaOp) string {
	if op.OperationID != "" {
		return slug(op.OperationID)
	}
	return slug(method + "-" + path)
}

// ---- OpenAPI 3.x export ----

// ExportOpenAPI generates an OpenAPI 3.x JSON document from a collection
// directory. It produces a skeleton spec: paths from URLs, methods, and
// request body content types from body type fields.
func ExportOpenAPI(root string) ([]byte, error) {
	files, err := runner.Collect(root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.req.toml files under %s", root)
	}

	colName := filepath.Base(root)
	if cfg, err := dsl.LoadConfig(filepath.Join(root, "collection.toml")); err == nil && cfg != nil && cfg.Name != "" {
		colName = cfg.Name
	}

	baseURL := "{{baseUrl}}"
	doc := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":   colName,
			"version": "1.0.0",
		},
		"servers": []map[string]any{{"url": baseURL}},
	}

	paths := map[string]any{}
	for _, f := range files {
		req, err := dsl.LoadRequest(f)
		if err != nil {
			return nil, err
		}
		// Convert {{param}} → {param} for OpenAPI path syntax.
		oaPath := strings.NewReplacer("{{", "{", "}}", "}").Replace(req.URL)
		// Strip the base URL prefix (if it starts with it).
		if strings.HasPrefix(oaPath, baseURL) {
			oaPath = oaPath[len(baseURL):]
		} else {
			// Try to strip scheme+host up to first path segment.
			if idx := indexAfterHost(oaPath); idx >= 0 {
				oaPath = oaPath[idx:]
			}
		}
		if oaPath == "" {
			oaPath = "/"
		}

		method := strings.ToLower(req.Method)
		if _, ok := paths[oaPath]; !ok {
			paths[oaPath] = map[string]any{}
		}
		pathItem := paths[oaPath].(map[string]any)

		op := map[string]any{
			"summary":     req.Name,
			"operationId": slug(req.Method + "-" + req.Name),
			"parameters":  []any{},
			"responses": map[string]any{
				"200": map[string]any{"description": "Success"},
			},
		}

		// Query params.
		var params []any
		for k, v := range req.Query {
			params = append(params, map[string]any{
				"name":    k,
				"in":      "query",
				"schema":  map[string]any{"type": "string", "example": v},
				"example": v,
			})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}

		// Request body.
		if b := req.Body; b != nil && b.Content != "" {
			mt := "application/json"
			if b.Type == "xml" {
				mt = "application/xml"
			} else if b.Type == "urlencoded" {
				mt = "application/x-www-form-urlencoded"
			}
			op["requestBody"] = map[string]any{
				"content": map[string]any{
					mt: map[string]any{
						"example": b.Content,
					},
				},
			}
		}

		pathItem[method] = op
	}

	// Sort paths for deterministic output.
	sortedPaths := map[string]any{}
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sortedPaths[k] = paths[k]
	}
	doc["paths"] = sortedPaths

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// indexAfterHost returns the index of the first '/' after the scheme://host.
func indexAfterHost(rawURL string) int {
	// Skip scheme.
	rest := rawURL
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	// Skip host (up to next '/').
	if idx := strings.Index(rest, "/"); idx >= 0 {
		return len(rawURL) - len(rest) + idx
	}
	return -1
}
