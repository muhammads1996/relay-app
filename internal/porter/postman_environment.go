package porter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/muhaymien96/relay/internal/vars"
)

// PostmanEnvironment is a standalone Postman environment ready for Relay.
// Name is a safe filename stem. Variables counts enabled variables, including
// secrets, whose values are deliberately excluded from Vars.
type PostmanEnvironment struct {
	Name      string
	Variables int
	Vars      map[string]string
	Secrets   []string
	Warnings  []ImportWarning
}

type pmEnvironment struct {
	Name   string          `json:"name"`
	Scope  string          `json:"_postman_variable_scope"`
	Values json.RawMessage `json:"values"`
}

type pmEnvironmentVariable struct {
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
	Type     string          `json:"type"`
	Enabled  *bool           `json:"enabled"`
	Disabled bool            `json:"disabled"`
}

// IsPostmanEnvironment recognizes standalone environment exports, including
// older exports without _postman_variable_scope. Parsing validates the fields.
func IsPostmanEnvironment(data []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return false
	}
	var scope string
	_ = json.Unmarshal(fields["_postman_variable_scope"], &scope)
	_, hasValues := fields["values"]
	return scope == "environment" || hasValues
}

// ParsePostmanEnvironment converts a standalone environment export. Disabled
// variables are skipped; secret values are discarded and reported as names.
func ParsePostmanEnvironment(data []byte) (PostmanEnvironment, error) {
	var result PostmanEnvironment
	var env pmEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		return result, fmt.Errorf("not a Postman environment: %w", err)
	}
	if strings.TrimSpace(env.Name) == "" {
		return result, fmt.Errorf("not a Postman environment: missing name")
	}
	if env.Scope != "" && env.Scope != "environment" {
		return result, fmt.Errorf("not a Postman environment: unsupported variable scope %q", env.Scope)
	}
	if len(env.Values) == 0 || bytes.Equal(bytes.TrimSpace(env.Values), []byte("null")) {
		return result, fmt.Errorf("not a Postman environment: missing values array")
	}
	var entries []pmEnvironmentVariable
	if err := json.Unmarshal(env.Values, &entries); err != nil {
		return result, fmt.Errorf("not a Postman environment: invalid values array: %w", err)
	}

	result.Name = postmanEnvironmentSlug(env.Name)
	result.Vars = make(map[string]string)
	location := "environment: " + env.Name
	if result.Name != env.Name {
		result.Warnings = append(result.Warnings, ImportWarning{
			Location: location,
			Message:  fmt.Sprintf("environment name %q was normalized to %q", env.Name, result.Name),
		})
	}
	seen := make(map[string]bool)
	for i, entry := range entries {
		if entry.Disabled || (entry.Enabled != nil && !*entry.Enabled) {
			result.Warnings = append(result.Warnings, ImportWarning{
				Location: location,
				Message:  fmt.Sprintf("disabled environment variable %q was not imported", entry.Key),
			})
			continue
		}
		if strings.TrimSpace(entry.Key) == "" {
			return PostmanEnvironment{}, fmt.Errorf("Postman environment variable %d has an empty key", i+1)
		}
		if seen[entry.Key] {
			return PostmanEnvironment{}, fmt.Errorf("Postman environment has duplicate variable %q", entry.Key)
		}
		seen[entry.Key] = true
		result.Variables++
		if strings.EqualFold(entry.Type, "secret") || isPostmanEnvironmentSecret(entry.Key) {
			result.Secrets = append(result.Secrets, entry.Key)
			result.Warnings = append(result.Warnings, ImportWarning{
				Location: location,
				Message:  fmt.Sprintf("secret variable %q was imported by name only; set %s to supply its value", entry.Key, vars.SecretEnvVar(entry.Key)),
			})
			continue
		}
		value, err := postmanEnvironmentValue(entry.Value)
		if err != nil {
			return PostmanEnvironment{}, fmt.Errorf("Postman environment variable %q: %w", entry.Key, err)
		}
		result.Vars[entry.Key] = value
	}
	return result, nil
}

func postmanEnvironmentSlug(name string) string {
	name = slug(name)
	// Windows reserves these basenames even when a filename has an extension.
	reserved := name == "con" || name == "prn" || name == "aux" || name == "nul"
	if len(name) == 4 && (strings.HasPrefix(name, "com") || strings.HasPrefix(name, "lpt")) {
		reserved = name[3] >= '1' && name[3] <= '9'
	}
	if reserved {
		return "environment-" + name
	}
	return name
}

func postmanEnvironmentValue(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", fmt.Errorf("invalid JSON value")
	}
	return compact.String(), nil
}

func isPostmanEnvironmentSecret(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), "_", ""))
	for _, part := range []string{"token", "secret", "password", "passwd", "credential", "apikey", "privatekey"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}
