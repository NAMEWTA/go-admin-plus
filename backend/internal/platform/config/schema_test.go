package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeFileSchemasAreIndependentAndSecretFree(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	schemaRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "config", "schema"))
	tests := []struct {
		file           string
		profile        string
		wantDatabase   bool
		forbiddenProps []string
	}{
		{file: "server-postgres.schema.json", profile: "server-postgres", forbiddenProps: []string{"dsn", "secret", "token", "dataDirectory"}},
		{file: "server-sqlite.schema.json", profile: "server-sqlite", wantDatabase: true, forbiddenProps: []string{"dsn", "secret", "token", "dataDirectory"}},
		{file: "desktop-sqlite.schema.json", profile: "desktop-sqlite", forbiddenProps: []string{"database", "http", "secret", "token", "dataDirectory", "logDirectory", "port"}},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			contents, err := os.ReadFile(filepath.Join(schemaRoot, test.file))
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(contents, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			if schema["additionalProperties"] != false {
				t.Fatal("root schema must reject unknown properties")
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatal("schema properties missing")
			}
			profile := properties["profile"].(map[string]any)
			if profile["const"] != test.profile {
				t.Fatalf("profile const = %#v", profile["const"])
			}
			_, hasDatabase := properties["database"]
			if hasDatabase != test.wantDatabase {
				t.Fatalf("database property present = %t", hasDatabase)
			}
			session, ok := properties["session"].(map[string]any)
			if !ok || session["additionalProperties"] != false {
				t.Fatal("session schema must exist and reject unknown properties")
			}
			sessionProperties, ok := session["properties"].(map[string]any)
			if !ok || len(sessionProperties) != 3 {
				t.Fatal("session schema must declare exactly three policy fields")
			}
			for _, property := range test.forbiddenProps {
				if _, exists := properties[property]; exists {
					t.Fatalf("forbidden property %q exists", property)
				}
			}
		})
	}
}
