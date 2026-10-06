package porter

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParsePostmanEnvironmentPolicyExports(t *testing.T) {
	for _, tc := range []struct {
		name string
		slug string
	}{
		{"Policy Services / SIT", "policy-services-sit"},
		{"Policy_Services_UAT3", "policy-services-uat3"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			var values []map[string]any
			for i := 0; i < 15; i++ {
				values = append(values, map[string]any{"key": fmt.Sprintf("service_url_%d", i), "value": "https://example.invalid/{{tenant}}", "enabled": true})
			}
			for _, key := range []string{"accessToken", "client_secret", "password"} {
				values = append(values, map[string]any{"key": key, "value": "synthetic-secret-value", "type": "default", "enabled": true})
			}
			values = append(values, map[string]any{"key": "session", "value": "synthetic-secret-value", "type": "secret", "enabled": true})
			data, err := json.Marshal(map[string]any{"name": tc.name, "values": values, "_postman_variable_scope": "environment"})
			if err != nil {
				t.Fatal(err)
			}
			env, err := ParsePostmanEnvironment(data)
			if err != nil {
				t.Fatal(err)
			}
			if env.Name != tc.slug || env.Variables != 19 || len(env.Vars) != 15 {
				t.Fatalf("unexpected import: name=%q variables=%d ordinary=%d", env.Name, env.Variables, len(env.Vars))
			}
			if !reflect.DeepEqual(env.Secrets, []string{"accessToken", "client_secret", "password", "session"}) {
				t.Fatalf("secret names = %v", env.Secrets)
			}
			if env.Vars["service_url_0"] != "https://example.invalid/{{tenant}}" {
				t.Fatal("template URL was not preserved")
			}
			serialized, _ := json.Marshal(env)
			if strings.Contains(string(serialized), "synthetic-secret-value") {
				t.Fatal("secret value escaped into the parsed environment or warnings")
			}
		})
	}
}

func TestParsePostmanEnvironmentValues(t *testing.T) {
	env, err := ParsePostmanEnvironment([]byte(`{
		"name":"local", "values":[
			{"key":"ordinary","value":"quoted \"text\"\n{{reference}}"},
			{"key":"empty","value":""},
			{"key":"missing"},
			{"key":"null","value":null},
			{"key":"number","value":123.50},
			{"key":"bool","value":false},
			{"key":"object","value": {"list": [1, true]}},
			{"key":"array","value":["a", "b"]},
			{"key":"disabled","value":"ignored","enabled":false},
			{"key":"legacy_disabled","value":"ignored","disabled":true},
			{"key":"api-key.2","value":"synthetic-secret-value"},
			{"key":"typed","type":"secret","value":"synthetic-secret-value"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ordinary": "quoted \"text\"\n{{reference}}", "empty": "", "missing": "", "null": "",
		"number": "123.50", "bool": "false", "object": `{"list":[1,true]}`, "array": `["a","b"]`,
	}
	if !reflect.DeepEqual(env.Vars, want) {
		t.Fatalf("variables = %#v; want %#v", env.Vars, want)
	}
	if env.Variables != 10 || !reflect.DeepEqual(env.Secrets, []string{"api-key.2", "typed"}) {
		t.Fatalf("count = %d; secrets = %v", env.Variables, env.Secrets)
	}
	if len(env.Warnings) != 4 {
		t.Fatalf("warnings = %v", env.Warnings)
	}
	if !strings.Contains(env.Warnings[2].Message, "RELAY_SECRET_API_KEY_2") {
		t.Fatalf("secret warning = %q", env.Warnings[2].Message)
	}
}

func TestParsePostmanEnvironmentRejectsInvalidExports(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"invalid JSON", `{`},
		{"collection", `{"info":{"name":"collection"},"item":[]}`},
		{"missing name", `{"values":[]}`},
		{"blank name", `{"name":"  ","values":[]}`},
		{"missing values", `{"name":"local"}`},
		{"null values", `{"name":"local","values":null}`},
		{"object values", `{"name":"local","values":{}}`},
		{"non-environment scope", `{"name":"local","values":[],"_postman_variable_scope":"globals"}`},
		{"empty key", `{"name":"local","values":[{"key":"","value":"x"}]}`},
		{"blank key", `{"name":"local","values":[{"key":" ","value":"x"}]}`},
		{"duplicate key", `{"name":"local","values":[{"key":"base","value":"x"},{"key":"base","value":"y"}]}`},
		{"non-string key", `{"name":"local","values":[{"key":1,"value":"x"}]}`},
		{"invalid enabled", `{"name":"local","values":[{"key":"base","enabled":"yes"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePostmanEnvironment([]byte(tc.data)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestIsPostmanEnvironment(t *testing.T) {
	for _, tc := range []struct {
		data string
		want bool
	}{
		{`{"name":"local","values":[]}`, true},
		{`{"name":"local","_postman_variable_scope":"environment"}`, true},
		{`{"name":"local","values":null}`, true},
		{`{"info":{"name":"collection"},"item":[]}`, false},
		{`[]`, false},
		{`null`, false},
		{`{`, false},
	} {
		if got := IsPostmanEnvironment([]byte(tc.data)); got != tc.want {
			t.Errorf("IsPostmanEnvironment(%s) = %t; want %t", tc.data, got, tc.want)
		}
	}
}

func TestParsePostmanEnvironmentWindowsReservedNames(t *testing.T) {
	names := []string{"CON", "PRN", "AUX", "NUL"}
	for i := 1; i <= 9; i++ {
		names = append(names, fmt.Sprintf("COM%d", i), fmt.Sprintf("LPT%d", i))
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			data := []byte(fmt.Sprintf(`{"name":%q,"values":[]}`, name))
			env, err := ParsePostmanEnvironment(data)
			if err != nil {
				t.Fatal(err)
			}
			want := "environment-" + strings.ToLower(name)
			if env.Name != want {
				t.Fatalf("name = %q; want %q", env.Name, want)
			}
			if len(env.Warnings) != 1 || !strings.Contains(env.Warnings[0].Message, want) {
				t.Fatalf("rename warning = %v", env.Warnings)
			}
		})
	}
}
