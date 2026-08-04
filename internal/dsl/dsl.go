// Package dsl defines the on-disk request format (.req.toml) and the
// workspace configuration files (collection.toml, folder.toml, environment
// files). Files are plain TOML so collections stay git-diffable.
package dsl

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Scripts holds optional JavaScript source for pre-request and test phases.
type Scripts struct {
	PreRequest string `toml:"pre_request,omitempty" json:"preRequest,omitempty"`
	Tests      string `toml:"tests,omitempty" json:"tests,omitempty"`
}

// Request is one .req.toml file.
type Request struct {
	Name       string            `toml:"name" json:"name"`
	Method     string            `toml:"method" json:"method"`
	URL        string            `toml:"url" json:"url"`
	Query      map[string]string `toml:"query" json:"query,omitempty"`
	Headers    map[string]string `toml:"headers" json:"headers,omitempty"`
	Vars       map[string]string `toml:"vars" json:"vars,omitempty"`
	Auth       *Auth             `toml:"auth" json:"auth,omitempty"`
	Body       *Body             `toml:"body" json:"body,omitempty"`
	Assertions []Assertion       `toml:"assertions" json:"assertions,omitempty"`
	Scripts    *Scripts          `toml:"scripts" json:"scripts,omitempty"`

	// Test-management metadata. Optional; absent from older .req.toml files.
	Tags         []string `toml:"tags" json:"tags,omitempty"`
	Owner        string   `toml:"owner" json:"owner,omitempty"`
	Priority     string   `toml:"priority" json:"priority,omitempty"` // low | med | high
	XrayKey      string   `toml:"xray_key" json:"xrayKey,omitempty"`
	Requirements []string `toml:"requirements" json:"requirements,omitempty"`
	// XrayPlan/XraySet override the collection-level Xray test plan/test set
	// for this request. Empty means "inherit from collection.toml".
	XrayPlan string `toml:"xray_plan" json:"xrayPlan,omitempty"`
	XraySet  string `toml:"xray_set" json:"xraySet,omitempty"`

	// Path is where the request was loaded from (not serialized).
	Path string `toml:"-" json:"-"`
}

// Auth holds the request auth helper config.
type Auth struct {
	Type     string `toml:"type" json:"type"` // bearer | basic | apikey
	Token    string `toml:"token" json:"token,omitempty"`
	Username string `toml:"username" json:"username,omitempty"`
	Password string `toml:"password" json:"password,omitempty"`
	Key      string `toml:"key" json:"key,omitempty"`     // apikey header/query name
	Value    string `toml:"value" json:"value,omitempty"` // apikey value
	In       string `toml:"in" json:"in,omitempty"`       // header (default) | query
}

// Body holds the request body. Content and File are mutually exclusive.
type Body struct {
	Type     string      `toml:"type" json:"type"` // json | xml | raw | urlencoded | formdata | binary
	Content  string      `toml:"content" json:"content,omitempty"`
	File     string      `toml:"file" json:"file,omitempty"`
	FormData []FormField `toml:"form_data" json:"formData,omitempty"`
}

// FormField is one multipart/form-data field. Type is text or file.
type FormField struct {
	Key      string `toml:"key" json:"key"`
	Value    string `toml:"value" json:"value,omitempty"`
	Type     string `toml:"type" json:"type,omitempty"`
	File     string `toml:"file" json:"file,omitempty"`
	Disabled bool   `toml:"disabled" json:"disabled,omitempty"`
}

// Assertion is one post-response check.
//
// Type is one of: status | jsonpath | header | contains | max_ms (legacy,
// operator-less forms) or status | header | json | text | timing | forall |
// exists | count | script (current forms, which carry an explicit Op).
//
// Op selects the comparison within a type (e.g. status: equals|is2xx|oneof;
// header: equals|contains|exists; json: equals|gt|lengthGt|exists; text:
// contains|notcontains; count: ==|>|<). Field/Sub are used by forall/exists
// to compare a field within each array item (item[Field] Sub Exp). Code
// holds a JS snippet for type "script", evaluated via the same pm.* runtime
// as request-level test scripts.
//
// Equals/Contains/MaxMs are kept for backward compatibility with .req.toml
// files written before Op/Exp existed; evalOne falls back to them when Op
// and Exp are both unset.
type Assertion struct {
	Type     string `toml:"type" json:"type"`
	Op       string `toml:"op,omitempty" json:"op,omitempty"`
	Path     string `toml:"path" json:"path,omitempty"`
	Name     string `toml:"name" json:"name,omitempty"`
	Field    string `toml:"field,omitempty" json:"field,omitempty"`
	Sub      string `toml:"sub,omitempty" json:"sub,omitempty"`
	Exp      any    `toml:"exp,omitempty" json:"exp,omitempty"`
	Code     string `toml:"code,omitempty" json:"code,omitempty"`
	Equals   any    `toml:"equals" json:"equals,omitempty"`
	Contains string `toml:"contains" json:"contains,omitempty"`
	MaxMs    int64  `toml:"max_ms" json:"max_ms,omitempty"`
}

// Config is a collection.toml or folder.toml: headers and vars that
// requests beneath it inherit.
type Config struct {
	Name    string            `toml:"name"`
	Headers map[string]string `toml:"headers"`
	Vars    map[string]string `toml:"vars"`
}

// Environment is an environments/<name>.toml file. Secret variables are
// listed by name only and resolved from the OS environment
// (RELAY_SECRET_<NAME>) so secret values never touch the repo.
type Environment struct {
	Vars    map[string]string `toml:"vars"`
	Secrets []string          `toml:"secrets"`
}

// LoadRequest parses one .req.toml file.
func LoadRequest(path string) (*Request, error) {
	var r Request
	if _, err := toml.DecodeFile(path, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	r.Method = strings.ToUpper(r.Method)
	if r.URL == "" {
		return nil, fmt.Errorf("%s: missing url", path)
	}
	r.Path = path
	return &r, nil
}

// LoadConfig parses a collection.toml or folder.toml. A missing file is not
// an error; it returns nil.
func LoadConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// LoadEnvironment parses an environment file.
func LoadEnvironment(path string) (*Environment, error) {
	var e Environment
	if _, err := toml.DecodeFile(path, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &e, nil
}
