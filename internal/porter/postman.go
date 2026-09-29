package porter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
)

// Postman Collection v2.x — only the fields we map.
type pmCollection struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Item     []pmItem     `json:"item"`
	Variable []pmVariable `json:"variable"`
	Auth     *pmAuth      `json:"auth"`
	Event    []pmEvent    `json:"event"`
}

// ImportWarning describes executable behavior or an auth mode that could not
// be represented faithfully. Location is a human-readable collection path.
type ImportWarning struct {
	Location string `json:"location"`
	Message  string `json:"message"`
}

// ImportReport summarizes a Postman import. It is available through
// ImportPostmanWithReport; ImportPostman remains as the count-only API.
type ImportReport struct {
	Requests int             `json:"requests"`
	Warnings []ImportWarning `json:"warnings,omitempty"`
}

type pmVariable struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	Src      string `json:"src"`
	Disabled bool   `json:"disabled"`
}

type pmItem struct {
	Name     string          `json:"name"`
	Item     []pmItem        `json:"item"` // folder when non-nil
	Request  *pmRequest      `json:"request"`
	Auth     *pmAuth         `json:"auth"`
	Event    []pmEvent       `json:"event"`
	Response json.RawMessage `json:"response"`
}

type pmEvent struct {
	Listen string `json:"listen"`
	Script struct {
		Type string   `json:"type"`
		Exec []string `json:"exec"`
	} `json:"script"`
}

type pmRequest struct {
	Method string     `json:"method"`
	Header []pmHeader `json:"header"`
	URL    pmURL      `json:"url"`
	Body   *pmBody    `json:"body"`
	Auth   *pmAuth    `json:"auth"`
	Event  []pmEvent  `json:"event"`
}

type pmHeader struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

// pmURL is either a string or an object in v2.x.
type pmURL struct {
	Raw   string
	Query []pmVariable
}

func (u *pmURL) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		u.Raw = s
		return nil
	}
	var obj struct {
		Raw   string       `json:"raw"`
		Query []pmVariable `json:"query"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	u.Raw = obj.Raw
	u.Query = obj.Query
	return nil
}

type pmBody struct {
	Mode       string       `json:"mode"` // raw | urlencoded | formdata
	Raw        string       `json:"raw"`
	URLEncoded []pmVariable `json:"urlencoded"`
	FormData   []pmVariable `json:"formdata"`
	Options    *struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

type pmAuth struct {
	Type   string       `json:"type"`
	Bearer []pmVariable `json:"bearer"`
	Basic  []pmVariable `json:"basic"`
	APIKey []pmVariable `json:"apikey"`
}

// ImportPostman converts a Postman Collection v2.x JSON export into a
// directory of .req.toml files under outDir. Collection-level variables go
// into collection.toml. Returns the number of requests written.
func ImportPostman(data []byte, outDir string) (int, error) {
	report, err := ImportPostmanWithReport(data, outDir)
	return report.Requests, err
}

// ImportPostmanWithReport imports a Postman v2 collection and reports
// unsupported executable APIs and auth modes. Auth inheritance is flattened
// onto requests, and inherited scripts are concatenated in scope order.
func ImportPostmanWithReport(data []byte, outDir string) (ImportReport, error) {
	var report ImportReport
	var col pmCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return report, fmt.Errorf("not a Postman collection: %w", err)
	}
	if col.Info.Name == "" && len(col.Item) == 0 {
		return report, fmt.Errorf("not a Postman collection: missing info.name and item")
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return report, err
	}
	cfg := &strings.Builder{}
	fmt.Fprintf(cfg, "name = %q\n", col.Info.Name)
	if len(col.Variable) > 0 {
		fmt.Fprintf(cfg, "\n[vars]\n")
		for _, v := range col.Variable {
			if v.Disabled {
				report.Warnings = append(report.Warnings, ImportWarning{Location: "collection: " + col.Info.Name, Message: fmt.Sprintf("disabled collection variable %q was not imported", v.Key)})
				continue
			}
			fmt.Fprintf(cfg, "%s = %q\n", v.Key, v.Value)
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "collection.toml"), []byte(cfg.String()), 0o644); err != nil {
		return report, err
	}
	collectionEvents := scriptsForEvents(col.Event, "collection: "+col.Info.Name, &report)
	count, err := writeItems(col.Item, outDir, col.Auth, collectionEvents, "", &report)
	report.Requests = count
	return report, err
}

type inheritedScripts struct {
	pre   string
	tests string
}

func appendScript(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

func scriptsForEvents(events []pmEvent, location string, report *ImportReport) inheritedScripts {
	var scripts inheritedScripts
	for _, ev := range events {
		src := strings.Join(ev.Script.Exec, "\n")
		if strings.TrimSpace(src) == "" {
			continue
		}
		phase := strings.ToLower(ev.Listen)
		switch phase {
		case "prerequest":
			scripts.pre = appendScript(scripts.pre, src)
		case "test":
			scripts.tests = appendScript(scripts.tests, src)
		default:
			report.Warnings = append(report.Warnings, ImportWarning{Location: location, Message: fmt.Sprintf("Postman %q event script was not imported", ev.Listen)})
			continue
		}
		if ev.Script.Type != "" && ev.Script.Type != "text/javascript" {
			report.Warnings = append(report.Warnings, ImportWarning{Location: location, Message: fmt.Sprintf("script type %q may not execute as JavaScript in Relay", ev.Script.Type)})
		}
		for _, api := range unsupportedPMAPIs(src) {
			report.Warnings = append(report.Warnings, ImportWarning{Location: location, Message: fmt.Sprintf("unsupported Postman API pm.%s is preserved in the script but may not execute in Relay", api)})
		}
	}
	return scripts
}

func unsupportedPMAPIs(src string) []string {
	// Relay's script shim exposes these top-level Postman namespaces/methods.
	// Detection is intentionally conservative and used only for migration warnings.
	known := map[string]bool{"test": true, "expect": true, "environment": true, "collectionVariables": true, "variables": true, "response": true, "request": true}
	seen := map[string]bool{}
	for _, match := range pmAPI.FindAllStringSubmatch(src, -1) {
		if !known[match[1]] {
			seen[match[1]] = true
		}
	}
	var out []string
	for api := range seen {
		out = append(out, api)
	}
	sort.Strings(out)
	return out
}

var pmAPI = regexp.MustCompile(`\bpm\.([A-Za-z_$][\w$]*)`)

func writeItems(items []pmItem, dir string, inheritedAuth *pmAuth, inherited inheritedScripts, parent string, report *ImportReport) (int, error) {
	count := 0
	for i, it := range items {
		location := it.Name
		if parent != "" {
			location = parent + " / " + it.Name
		}
		if it.Item != nil { // folder
			sub := filepath.Join(dir, slug(it.Name))
			if err := os.MkdirAll(sub, 0o755); err != nil {
				return count, err
			}
			auth := inheritedAuth
			if it.Auth != nil {
				auth = it.Auth
			}
			scripts := inherited
			folderScripts := scriptsForEvents(it.Event, "folder: "+location, report)
			scripts.pre = appendScript(scripts.pre, folderScripts.pre)
			scripts.tests = appendScript(scripts.tests, folderScripts.tests)
			n, err := writeItems(it.Item, sub, auth, scripts, location, report)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		if it.Request == nil {
			if len(it.Response) > 0 {
				report.Warnings = append(report.Warnings, ImportWarning{Location: "item: " + location, Message: "saved Postman response example was not imported"})
			}
			if len(it.Event) > 0 {
				_ = scriptsForEvents(it.Event, "item: "+location, report)
				report.Warnings = append(report.Warnings, ImportWarning{Location: "item: " + location, Message: "event scripts on an item without a request were not imported"})
			}
			continue
		}
		auth := inheritedAuth
		if it.Auth != nil {
			auth = it.Auth
		}
		if it.Request.Auth != nil {
			auth = it.Request.Auth
		}
		scripts := inherited
		itemScripts := scriptsForEvents(it.Event, "item: "+location, report)
		scripts.pre = appendScript(scripts.pre, itemScripts.pre)
		scripts.tests = appendScript(scripts.tests, itemScripts.tests)
		requestScripts := scriptsForEvents(it.Request.Event, "request: "+location, report)
		scripts.pre = appendScript(scripts.pre, requestScripts.pre)
		scripts.tests = appendScript(scripts.tests, requestScripts.tests)
		req := convert(it.Name, it.Request)
		warnDroppedRequestContent(it.Request, "request: "+location, report)
		if auth != nil {
			req.Auth = convertAuth(auth)
			if auth.Type != "noauth" && req.Auth == nil {
				report.Warnings = append(report.Warnings, ImportWarning{Location: location, Message: fmt.Sprintf("Postman auth type %q is not supported; effective authentication was not imported", auth.Type)})
			}
		}
		if scripts.pre != "" || scripts.tests != "" {
			req.Scripts = &dsl.Scripts{PreRequest: scripts.pre, Tests: scripts.tests}
		}
		name := fmt.Sprintf("%02d-%s.req.toml", i+1, slug(it.Name))
		if err := os.WriteFile(filepath.Join(dir, name), dsl.Marshal(req), 0o644); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func warnDroppedRequestContent(request *pmRequest, location string, report *ImportReport) {
	if request.Body == nil {
		return
	}
	switch request.Body.Mode {
	case "raw", "formdata":
		// These modes are represented by the Relay request DSL.
	case "urlencoded":
		for _, field := range request.Body.URLEncoded {
			if field.Disabled {
				report.Warnings = append(report.Warnings, ImportWarning{Location: location, Message: fmt.Sprintf("disabled URL encoded field %q was dropped", field.Key)})
			}
		}
	case "":
		// An absent mode has no body data to drop.
	default:
		report.Warnings = append(report.Warnings, ImportWarning{Location: location, Message: fmt.Sprintf("Postman body mode %q was not imported", request.Body.Mode)})
	}
}

func convertAuth(a *pmAuth) *dsl.Auth {
	get := func(kvs []pmVariable, key string) string {
		for _, kv := range kvs {
			if kv.Key == key {
				return kv.Value
			}
		}
		return ""
	}
	switch a.Type {
	case "bearer":
		return &dsl.Auth{Type: "bearer", Token: get(a.Bearer, "token")}
	case "basic":
		return &dsl.Auth{Type: "basic", Username: get(a.Basic, "username"), Password: get(a.Basic, "password")}
	case "apikey":
		in := get(a.APIKey, "in")
		if in != "query" {
			in = "header"
		}
		return &dsl.Auth{Type: "apikey", Key: get(a.APIKey, "key"), Value: get(a.APIKey, "value"), In: in}
	default:
		return nil
	}
}

func convert(name string, pr *pmRequest) *dsl.Request {
	r := &dsl.Request{
		Name:   name,
		Method: strings.ToUpper(pr.Method),
		URL:    pr.URL.Raw,
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	for _, h := range pr.Header {
		r.HeaderEntries = append(r.HeaderEntries, dsl.Entry{Key: h.Key, Value: h.Value, Disabled: h.Disabled})
	}
	for _, q := range pr.URL.Query {
		r.QueryEntries = append(r.QueryEntries, dsl.Entry{Key: q.Key, Value: q.Value, Disabled: q.Disabled})
	}
	if b := pr.Body; b != nil {
		switch b.Mode {
		case "raw":
			bodyType := "raw"
			if b.Options != nil && b.Options.Raw.Language == "json" {
				bodyType = "json"
			} else if strings.HasPrefix(strings.TrimSpace(b.Raw), "{") ||
				strings.HasPrefix(strings.TrimSpace(b.Raw), "[") {
				bodyType = "json"
			}
			r.Body = &dsl.Body{Type: bodyType, Content: b.Raw}
		case "urlencoded":
			pairs := make([]string, 0, len(b.URLEncoded))
			for _, kv := range b.URLEncoded {
				if kv.Disabled {
					continue
				}
				pairs = append(pairs, kv.Key+"="+kv.Value)
			}
			r.Body = &dsl.Body{Type: "urlencoded", Content: strings.Join(pairs, "&")}
		case "formdata":
			fields := make([]dsl.FormField, 0, len(b.FormData))
			for _, kv := range b.FormData {
				fieldType := kv.Type
				if fieldType == "" {
					fieldType = "text"
				}
				fields = append(fields, dsl.FormField{
					Key:      kv.Key,
					Value:    kv.Value,
					Type:     fieldType,
					File:     kv.Src,
					Disabled: kv.Disabled,
				})
			}
			r.Body = &dsl.Body{Type: "formdata", FormData: fields}
		}
	}
	if a := pr.Auth; a != nil {
		r.Auth = convertAuth(a)
	}
	return r
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "item"
	}
	return s
}
