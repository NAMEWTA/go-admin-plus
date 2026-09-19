package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedConfigSelectsDatabaseAndOptionalServices(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := "database:\n  driver: " + driver + "\n  postgres:\n    dsn: postgres://user:private@localhost/test\nredis:\n  enabled: true\n  address: localhost:6379\nruntime:\n  role: all\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := LoadRuntime(Input{File: path, Environment: map[string]string{"GO_ADMIN_REDIS_ENABLED": "false"}, CLI: map[string]string{"http.listen": "127.0.0.1:9001"}})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Settings().Database.Driver != driver || snapshot.Settings().Redis.Enabled || snapshot.Settings().HTTP.Listen != "127.0.0.1:9001" {
				t.Fatal("configuration precedence is incorrect")
			}
			if strings.Contains(snapshot.String(), "private") {
				t.Fatal("secret leaked")
			}
			copy := snapshot.Settings()
			copy.Storage.AllowedTypes[0] = "changed"
			if snapshot.Settings().Storage.AllowedTypes[0] == "changed" {
				t.Fatal("mutable configuration escaped")
			}
		})
	}
}

func TestUnifiedConfigRejectsInvalidShapeBeforeOpeningDependencies(t *testing.T) {
	for _, source := range []string{"redis:\n  enable: true\n", "database:\n  driver: unknown\n", "runtime:\n  role: worker\n", "http:\n  trustedProxies: [not-a-cidr]\n", "redis:\n  password: hidden-secret\n  passwordFile: missing\n", "---\nredis: {}\n---\nredis: {}\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadRuntime(Input{File: path})
		if err == nil {
			t.Fatalf("accepted invalid configuration: %s", source)
		}
		if strings.Contains(err.Error(), "hidden-secret") {
			t.Fatal("validation exposed secret")
		}
	}
}
