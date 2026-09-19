package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeconfig "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
)

func TestSessionPolicyDefaultsAndPrecedenceAcrossProfiles(t *testing.T) {
	serverFile := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(serverFile, []byte(`{
  "profile":"server-sqlite",
  "session":{"idleTimeoutSeconds":1200,"absoluteTimeoutSeconds":3600,"rotationIntervalSeconds":600}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile:     runtimeconfig.ProfileServerSQLite,
		File:        serverFile,
		Environment: map[string]string{"GO_ADMIN_SESSION_IDLE_SECONDS": "1500"},
		CLI:         map[string]string{"session.rotationIntervalSeconds": "300"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, _ := snapshot.ServerSQLite()
	policy := server.SessionPolicy()
	if policy.IdleTimeout() != 1500*time.Second || policy.AbsoluteTimeout() != time.Hour || policy.RotationInterval() != 5*time.Minute {
		t.Fatalf("session precedence mismatch: idle=%s absolute=%s rotation=%s", policy.IdleTimeout(), policy.AbsoluteTimeout(), policy.RotationInterval())
	}

	postgres, err := runtimeconfig.Load(runtimeconfig.Input{Profile: runtimeconfig.ProfileServerPostgres, Environment: map[string]string{"GO_ADMIN_DATABASE_DSN": "postgres://private"}})
	if err != nil {
		t.Fatal(err)
	}
	postgresValue, _ := postgres.ServerPostgres()
	desktop, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile: runtimeconfig.ProfileDesktopSQLite,
		Desktop: runtimeconfig.DesktopMaterial{DataDirectory: filepath.Join(t.TempDir(), "data"), LogDirectory: filepath.Join(t.TempDir(), "logs"), LoopbackPort: 43127, StartupToken: "0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	desktopValue, _ := desktop.DesktopSQLite()
	for name, value := range map[string]runtimeconfig.SessionPolicy{"postgres": postgresValue.SessionPolicy(), "desktop": desktopValue.SessionPolicy()} {
		if value.IdleTimeout() != 30*time.Minute || value.AbsoluteTimeout() != 12*time.Hour || value.RotationInterval() != 15*time.Minute {
			t.Fatalf("%s default session policy mismatch", name)
		}
	}
}

func TestSessionPolicyRejectsInvalidRelationsAndUnknownFields(t *testing.T) {
	for name, document := range map[string]string{
		"rotation exceeds idle": `{"profile":"server-sqlite","session":{"idleTimeoutSeconds":600,"absoluteTimeoutSeconds":3600,"rotationIntervalSeconds":601}}`,
		"idle exceeds absolute": `{"profile":"server-sqlite","session":{"idleTimeoutSeconds":3600,"absoluteTimeoutSeconds":1800,"rotationIntervalSeconds":300}}`,
		"unknown session field": `{"profile":"server-sqlite","session":{"idleTimeoutSeconds":600,"legacyRefreshSeconds":30}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.json")
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := runtimeconfig.Load(runtimeconfig.Input{Profile: runtimeconfig.ProfileServerSQLite, File: path})
			if err == nil {
				t.Fatal("invalid session policy accepted")
			}
			if strings.Contains(err.Error(), "601") {
				t.Fatal("configuration value leaked")
			}
			if name == "unknown session field" && !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	if _, err := runtimeconfig.NewSessionPolicy(0, time.Hour, time.Minute); err == nil {
		t.Fatal("zero policy accepted")
	}
}

func TestSessionPolicyRejectsOverflowBeforeDurationConversionForEveryProfileAndSource(t *testing.T) {
	const overflow = "36028797018965768"
	profiles := []runtimeconfig.Profile{
		runtimeconfig.ProfileServerSQLite,
		runtimeconfig.ProfileServerPostgres,
		runtimeconfig.ProfileDesktopSQLite,
	}
	for _, profile := range profiles {
		for _, source := range []string{"file", "environment", "cli"} {
			t.Run(string(profile)+"/"+source, func(t *testing.T) {
				input := sessionPolicyInput(t, profile)
				switch source {
				case "file":
					path := filepath.Join(t.TempDir(), "runtime.json")
					document := fmt.Sprintf(`{"profile":%q,"session":{"idleTimeoutSeconds":%s}}`, profile, overflow)
					if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
						t.Fatal(err)
					}
					input.File = path
				case "environment":
					input.Environment["GO_ADMIN_SESSION_ABSOLUTE_SECONDS"] = overflow
				case "cli":
					input.CLI["session.rotationIntervalSeconds"] = overflow
				}
				_, err := runtimeconfig.Load(input)
				if err == nil {
					t.Fatal("overflowing session seconds were accepted")
				}
				if strings.Contains(err.Error(), overflow) {
					t.Fatal("overflowing configuration value leaked")
				}
			})
		}
	}
}

func sessionPolicyInput(t *testing.T, profile runtimeconfig.Profile) runtimeconfig.Input {
	t.Helper()
	input := runtimeconfig.Input{Profile: profile, Environment: map[string]string{}, CLI: map[string]string{}}
	switch profile {
	case runtimeconfig.ProfileServerPostgres:
		input.Environment["GO_ADMIN_DATABASE_DSN"] = "postgres://private"
	case runtimeconfig.ProfileDesktopSQLite:
		input.Desktop = runtimeconfig.DesktopMaterial{
			DataDirectory: filepath.Join(t.TempDir(), "data"),
			LogDirectory:  filepath.Join(t.TempDir(), "logs"),
			LoopbackPort:  43127,
			StartupToken:  "0123456789abcdef0123456789abcdef",
		}
	}
	return input
}

func TestLoadAppliesDocumentedPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(configPath, []byte(`{
  "profile": "server-sqlite",
  "http": {"listen": "127.0.0.1:8100"},
  "log": {"level": "debug"},
  "database": {"path": "from-file.sqlite3"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile: runtimeconfig.ProfileServerSQLite,
		File:    configPath,
		Environment: map[string]string{
			"GO_ADMIN_HTTP_LISTEN": "127.0.0.1:8200",
			"GO_ADMIN_LOG_LEVEL":   "warn",
			"GO_ADMIN_SQLITE_PATH": "from-env.sqlite3",
		},
		CLI: map[string]string{
			"http.listen":   "127.0.0.1:8300",
			"database.path": "from-cli.sqlite3",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if snapshot.Profile() != runtimeconfig.ProfileServerSQLite {
		t.Fatalf("Profile() = %q", snapshot.Profile())
	}
	server, ok := snapshot.ServerSQLite()
	if !ok {
		t.Fatal("ServerSQLite() did not return the selected profile")
	}
	if server.HTTPListen() != "127.0.0.1:8300" {
		t.Fatalf("HTTPListen() = %q", server.HTTPListen())
	}
	if server.LogLevel() != "warn" {
		t.Fatalf("LogLevel() = %q", server.LogLevel())
	}
	if server.DatabasePath() != "from-cli.sqlite3" {
		t.Fatalf("DatabasePath() = %q", server.DatabasePath())
	}
}

func TestLoadAppliesDocumentedPrecedenceToPostgres(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(configPath, []byte(`{
  "profile": "server-postgres",
  "http": {"listen": "127.0.0.1:8100"},
  "log": {"level": "debug"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile: runtimeconfig.ProfileServerPostgres,
		File:    configPath,
		Environment: map[string]string{
			"GO_ADMIN_DATABASE_DSN": "postgres://runtime-secret",
			"GO_ADMIN_HTTP_LISTEN":  "127.0.0.1:8200",
			"GO_ADMIN_LOG_LEVEL":    "warn",
		},
		CLI: map[string]string{
			"http.listen": "127.0.0.1:8300",
			"log.level":   "error",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	postgres, ok := snapshot.ServerPostgres()
	if !ok || postgres.HTTPListen() != "127.0.0.1:8300" || postgres.LogLevel() != "error" {
		t.Fatalf("ServerPostgres() = %#v", postgres)
	}
}

func TestLoadResolvesPostgresSecretWithoutLeakingDiagnostics(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "database-secret")
	secret := "postgres://operator:correct-horse@example.invalid/product"
	if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile: runtimeconfig.ProfileServerPostgres,
		Environment: map[string]string{
			"GO_ADMIN_DATABASE_DSN_FILE": secretPath,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	postgres, ok := snapshot.ServerPostgres()
	if !ok || postgres.DatabaseDSN() != secret {
		t.Fatal("ServerPostgres() did not resolve the secret file")
	}

	_, err = runtimeconfig.Load(runtimeconfig.Input{
		Profile: runtimeconfig.ProfileServerPostgres,
		Environment: map[string]string{
			"GO_ADMIN_DATABASE_DSN":      secret,
			"GO_ADMIN_DATABASE_DSN_FILE": secretPath,
		},
	})
	if err == nil {
		t.Fatal("Load() accepted conflicting secret sources")
	}
	for _, forbidden := range []string{secret, secretPath, "correct-horse"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Load() error leaked %q: %v", forbidden, err)
		}
	}
	if !strings.Contains(err.Error(), "database.dsn") || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("Load() error = %q, want field and rule", err)
	}
}

func TestLoadRejectsUnknownAndCrossProfileFields(t *testing.T) {
	for name, document := range map[string]string{
		"unknown root":        `{"profile":"server-sqlite","extraField":"do-not-print"}`,
		"unknown nested":      `{"profile":"server-sqlite","database":{"path":"data.sqlite3","extraScope":"do-not-print"}}`,
		"postgres-only field": `{"profile":"server-postgres","database":{"path":"/private/database.sqlite3"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.json")
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			input := runtimeconfig.Input{Profile: runtimeconfig.ProfileServerSQLite, File: path}
			if name == "postgres-only field" {
				input.Profile = runtimeconfig.ProfileServerPostgres
				input.Environment = map[string]string{"GO_ADMIN_DATABASE_DSN": "postgres://private"}
			}
			_, err := runtimeconfig.Load(input)
			if err == nil {
				t.Fatal("Load() accepted a field outside the profile schema")
			}
			if !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("Load() error = %q, want unknown-field rule", err)
			}
			for _, forbidden := range []string{"do-not-print", "/private/database.sqlite3", path} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("Load() error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestLoadDesktopUsesOnlyNativeHostMaterial(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "desktop.json")
	if err := os.WriteFile(configPath, []byte(`{
  "profile": "desktop-sqlite",
  "log": {"level": "debug"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDirectory := filepath.Join(t.TempDir(), "data")
	logDirectory := filepath.Join(t.TempDir(), "logs")
	const startupToken = "0123456789abcdef0123456789abcdef"

	snapshot, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile: runtimeconfig.ProfileDesktopSQLite,
		File:    configPath,
		Environment: map[string]string{
			"GO_ADMIN_LOG_LEVEL": "warn",
		},
		CLI: map[string]string{"log.level": "error"},
		Desktop: runtimeconfig.DesktopMaterial{
			DataDirectory: dataDirectory,
			LogDirectory:  logDirectory,
			LoopbackPort:  43127,
			StartupToken:  startupToken,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	desktop, ok := snapshot.DesktopSQLite()
	if !ok {
		t.Fatal("DesktopSQLite() did not return the selected profile")
	}
	if desktop.LogLevel() != "error" || desktop.DataDirectory() != dataDirectory || desktop.LogDirectory() != logDirectory {
		t.Fatalf("DesktopSQLite() = %#v", desktop)
	}
	if desktop.LoopbackAddress() != "127.0.0.1:43127" || desktop.StartupToken() != startupToken {
		t.Fatal("DesktopSQLite() did not preserve native launch material")
	}
}

func TestLoadRejectsInvalidSourcesBeforeStartup(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "missing-secret")
	profilePath := filepath.Join(t.TempDir(), "wrong-profile.json")
	if err := os.WriteFile(profilePath, []byte(`{"profile":"server-postgres"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		input     runtimeconfig.Input
		wantField string
		wantRule  string
		forbidden string
	}{
		{
			name:      "unknown environment field",
			input:     runtimeconfig.Input{Profile: runtimeconfig.ProfileServerSQLite, Environment: map[string]string{"GO_ADMIN_UNEXPECTED_SCOPE": "private-scope"}},
			wantField: "GO_ADMIN_UNEXPECTED_SCOPE",
			wantRule:  "unknown field",
			forbidden: "private-scope",
		},
		{
			name:      "secret CLI field",
			input:     runtimeconfig.Input{Profile: runtimeconfig.ProfileServerPostgres, Environment: map[string]string{"GO_ADMIN_DATABASE_DSN": "postgres://safe"}, CLI: map[string]string{"database.dsn": "postgres://leaked"}},
			wantField: "database.dsn",
			wantRule:  "not permitted",
			forbidden: "postgres://leaked",
		},
		{
			name:      "missing secret",
			input:     runtimeconfig.Input{Profile: runtimeconfig.ProfileServerPostgres},
			wantField: "database.dsn",
			wantRule:  "required",
		},
		{
			name:      "unreadable secret",
			input:     runtimeconfig.Input{Profile: runtimeconfig.ProfileServerPostgres, Environment: map[string]string{"GO_ADMIN_DATABASE_DSN_FILE": secretPath}},
			wantField: "database.dsn",
			wantRule:  "unreadable",
			forbidden: secretPath,
		},
		{
			name:      "profile conflict",
			input:     runtimeconfig.Input{Profile: runtimeconfig.ProfileServerSQLite, File: profilePath},
			wantField: "profile",
			wantRule:  "conflicts",
			forbidden: profilePath,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtimeconfig.Load(test.input)
			if err == nil {
				t.Fatal("Load() accepted invalid input")
			}
			if !strings.Contains(err.Error(), test.wantField) || !strings.Contains(err.Error(), test.wantRule) {
				t.Fatalf("Load() error = %q, want field %q and rule %q", err, test.wantField, test.wantRule)
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("Load() error leaked %q: %v", test.forbidden, err)
			}
		})
	}
}

func TestSnapshotIsOwnedAndSafeToFormat(t *testing.T) {
	const secret = "postgres://admin:never-log-this@example.invalid/product"
	environment := map[string]string{"GO_ADMIN_DATABASE_DSN": secret}
	snapshot, err := runtimeconfig.Load(runtimeconfig.Input{
		Profile:     runtimeconfig.ProfileServerPostgres,
		Environment: environment,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	environment["GO_ADMIN_DATABASE_DSN"] = "mutated"
	postgres, ok := snapshot.ServerPostgres()
	if !ok || postgres.DatabaseDSN() != secret {
		t.Fatal("Snapshot changed after mutating its input")
	}
	for _, formatted := range []string{
		fmt.Sprint(runtimeconfig.Input{Profile: runtimeconfig.ProfileServerPostgres, Environment: environment}),
		fmt.Sprintf("%#v", runtimeconfig.Input{Profile: runtimeconfig.ProfileServerPostgres, Environment: environment}),
		fmt.Sprintf("%#v", runtimeconfig.DesktopMaterial{DataDirectory: "/private/data", StartupToken: secret}),
		fmt.Sprint(snapshot),
		fmt.Sprintf("%#v", snapshot),
		fmt.Sprint(postgres),
		fmt.Sprintf("%#v", postgres),
	} {
		if strings.Contains(formatted, secret) || strings.Contains(formatted, "never-log-this") || strings.Contains(formatted, "mutated") || strings.Contains(formatted, "/private/data") {
			t.Fatalf("formatted snapshot leaked secret: %s", formatted)
		}
		if !strings.Contains(formatted, "redacted") {
			t.Fatalf("formatted snapshot = %q, want redaction marker", formatted)
		}
	}
}

func TestSensitiveInputsAreSafeForJSONAndStructuredLogging(t *testing.T) {
	const (
		dsn        = "postgres://admin:structured-secret@example.invalid/product"
		startup    = "0123456789abcdef0123456789abcdef"
		configPath = "/private/runtime/config.json"
		secretPath = "/private/runtime/database-secret"
		dataPath   = "/private/runtime/data"
		logPath    = "/private/runtime/logs"
		sqlitePath = "/private/runtime/data.sqlite3"
	)
	material := runtimeconfig.DesktopMaterial{
		DataDirectory: dataPath,
		LogDirectory:  logPath,
		LoopbackPort:  41234,
		StartupToken:  startup,
	}
	input := runtimeconfig.Input{
		Profile: runtimeconfig.ProfileServerPostgres,
		File:    configPath,
		Environment: map[string]string{
			"GO_ADMIN_DATABASE_DSN":      dsn,
			"GO_ADMIN_DATABASE_DSN_FILE": secretPath,
		},
		CLI:     map[string]string{"database.path": sqlitePath},
		Desktop: material,
	}

	encodedInput, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal(Input) error = %v", err)
	}
	encodedMaterial, err := json.Marshal(material)
	if err != nil {
		t.Fatalf("json.Marshal(DesktopMaterial) error = %v", err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("runtime configuration", slog.Any("input", input), slog.Any("desktop", material))

	for name, output := range map[string]string{
		"input JSON":     string(encodedInput),
		"desktop JSON":   string(encodedMaterial),
		"structured log": logs.String(),
	} {
		for _, forbidden := range []string{dsn, startup, configPath, secretPath, dataPath, logPath, sqlitePath, "structured-secret"} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("%s leaked %q: %s", name, forbidden, output)
			}
		}
		if !strings.Contains(output, "redacted") {
			t.Fatalf("%s = %q, want redaction marker", name, output)
		}
	}
}

func TestLoadValidatesTypedValuesWithoutEchoingThem(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "same-directory")
	tests := []struct {
		name      string
		input     runtimeconfig.Input
		wantField string
		forbidden string
	}{
		{
			name: "invalid listen address",
			input: runtimeconfig.Input{
				Profile: runtimeconfig.ProfileServerSQLite,
				CLI:     map[string]string{"http.listen": "not-an-address-secret"},
			},
			wantField: "http.listen",
			forbidden: "not-an-address-secret",
		},
		{
			name: "invalid log level",
			input: runtimeconfig.Input{
				Profile:     runtimeconfig.ProfileServerPostgres,
				Environment: map[string]string{"GO_ADMIN_DATABASE_DSN": "postgres://private"},
				CLI:         map[string]string{"log.level": "verbose-secret"},
			},
			wantField: "log.level",
			forbidden: "verbose-secret",
		},
		{
			name: "desktop directory conflict",
			input: runtimeconfig.Input{
				Profile: runtimeconfig.ProfileDesktopSQLite,
				Desktop: runtimeconfig.DesktopMaterial{
					DataDirectory: dataDirectory,
					LogDirectory:  dataDirectory,
					LoopbackPort:  41001,
					StartupToken:  "0123456789abcdef0123456789abcdef",
				},
			},
			wantField: "desktop.logDirectory",
			forbidden: dataDirectory,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtimeconfig.Load(test.input)
			if err == nil {
				t.Fatal("Load() accepted an invalid typed value")
			}
			if !strings.Contains(err.Error(), test.wantField) {
				t.Fatalf("Load() error = %q, want field %q", err, test.wantField)
			}
			if strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("Load() error leaked %q: %v", test.forbidden, err)
			}
		})
	}
}
