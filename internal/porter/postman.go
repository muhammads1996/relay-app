package porter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
}

type pmVariable struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	Src      string `json:"src"`
	Disabled bool   `json:"disabled"`
}

type pmItem struct {
	Name    string     `json:"name"`
	Item    []pmItem   `json:"item"` // folder when non-nil
	Request *pmRequest `json:"request"`
}

type pmRequest struct {
	Method string     `json:"method"`
	Header []pmHeader `json:"header"`
	URL    pmURL      `json:"url"`
	Body   *pmBody    `json:"body"`
	Auth   *pmAuth    `json:"auth"`
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
	var col pmCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return 0, fmt.Errorf("not a Postman collection: %w", err)
	}
	if col.Info.Name == "" && len(col.Item) == 0 {
		return 0, fmt.Errorf("not a Postman collection: missing info.name and item")
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, err
	}
	cfg := &strings.Builder{}
	fmt.Fprintf(cfg, "name = %q\n", col.Info.Name)
	if len(col.Variable) > 0 {
		fmt.Fprintf(cfg, "\n[vars]\n")
		for _, v := range col.Variable {
			fmt.Fprintf(cfg, "%s = %q\n", v.Key, v.Value)
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "collection.toml"), []byte(cfg.String()), 0o644); err != nil {
		return 0, err
	}

	return writeItems(col.Item, outDir)
}

func writeItems(items []pmItem, dir string) (int, error) {
	count := 0
	for i, it := range items {
		if it.Item != nil { // folder
			sub := filepath.Join(dir, slug(it.Name))
			if err := os.MkdirAll(sub, 0o755); err != nil {
				return count, err
			}
			n, err := writeItems(it.Item, sub)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		if it.Request == nil {
			continue
		}
		req := convert(it.Name, it.Request)
		name := fmt.Sprintf("%02d-%s.req.toml", i+1, slug(it.Name))
		if err := os.WriteFile(filepath.Join(dir, name), dsl.Marshal(req), 0o644); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
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
		if h.Disabled {
			continue
		}
		if r.Headers == nil {
			r.Headers = map[string]string{}
		}
		r.Headers[h.Key] = h.Value
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
			r.Auth = &dsl.Auth{Type: "bearer", Token: get(a.Bearer, "token")}
		case "basic":
			r.Auth = &dsl.Auth{
				Type:     "basic",
				Username: get(a.Basic, "username"),
				Password: get(a.Basic, "password"),
			}
		case "apikey":
			in := get(a.APIKey, "in")
			if in != "query" {
				in = "header"
			}
			r.Auth = &dsl.Auth{
				Type:  "apikey",
				Key:   get(a.APIKey, "key"),
				Value: get(a.APIKey, "value"),
				In:    in,
			}
		}
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
