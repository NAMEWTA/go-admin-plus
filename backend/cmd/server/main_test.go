package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	platformdesktop "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/desktop"
)

func TestRuntimeOptionsKeepRolesAndProfilesExplicit(t *testing.T) {
	options, err := parseRuntimeOptions("serve", []string{
		"--profile", "server-postgres", "--listen", "127.0.0.1:9090", "--with-worker",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if options.profile != "server-postgres" || options.listen != "127.0.0.1:9090" || !options.withWorker {
		t.Fatalf("options = %#v", options)
	}
	if _, err := parseRuntimeOptions("worker", []string{"unexpected"}, false); err == nil {
		t.Fatal("runtime parser accepted positional input")
	}
}

func TestSQLiteMigrationRefusesAnActiveRuntimeLock(t *testing.T) {
	root := t.TempDir()
	lock, err := platformdesktop.AcquireInstanceLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = run(context.Background(), []string{
		"migrate", "--profile", "server-sqlite", "--sqlite-path", filepath.Join(root, "product.sqlite3"), "--data-root", root,
	}, nil, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("migration lock error = %v", err)
	}
}

func TestUnifiedCLICompletesOfflineSQLiteOperations(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "product.sqlite3")
	firstSecret := filepath.Join(root, "first.secret")
	secondSecret := filepath.Join(root, "second.secret")
	for path, value := range map[string]string{
		firstSecret: "correct horse battery staple", secondSecret: "another correct horse battery staple",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	common := []string{"--profile", "server-sqlite", "--sqlite-path", databasePath, "--data-root", root}
	var output bytes.Buffer
	if err := run(context.Background(), append([]string{"migrate"}, common...), nil, &output); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	output.Reset()
	bootstrapArgs := append([]string{"bootstrap"}, common...)
	bootstrapArgs = append(bootstrapArgs,
		"--username", "first.admin", "--display-name", "First Administrator",
		"--email", "first@example.test", "--secret-file", firstSecret,
	)
	if err := run(context.Background(), bootstrapArgs, nil, &output); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	accountID := regexp.MustCompile(`account_id=([a-zA-Z0-9-]+)`).FindStringSubmatch(output.String())
	if len(accountID) != 2 {
		t.Fatalf("bootstrap output = %q", output.String())
	}
	output.Reset()
	recoveryArgs := append([]string{"recover-admin"}, common...)
	recoveryArgs = append(recoveryArgs,
		"--account-id", accountID[1], "--reason", "lost-access", "--secret-file", secondSecret,
	)
	if err := run(context.Background(), recoveryArgs, nil, &output); err != nil {
		t.Fatalf("recover-admin: %v", err)
	}
	if strings.Contains(output.String(), "correct horse") {
		t.Fatalf("operation output leaked secret: %q", output.String())
	}
	output.Reset()
	doctorArgs := append([]string{"doctor"}, common...)
	if err := run(context.Background(), doctorArgs, nil, &output); err != nil {
		t.Fatalf("doctor: %v; output=%s", err, output.String())
	}
	if !strings.Contains(output.String(), `"profile":"server-sqlite"`) {
		t.Fatalf("doctor output = %q", output.String())
	}
}

func TestVersionHasNoRuntimeSideEffects(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"version"}, map[string]string{"GO_ADMIN_DATABASE_DSN": "private"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != version {
		t.Fatalf("version output = %q", output.String())
	}
	if err := run(context.Background(), []string{"version", "--profile", "server-sqlite"}, nil, &output); err == nil {
		t.Fatal("version accepted runtime arguments")
	}
}

func TestSecretFileMustBeRestrictedAndNeverUsesArgv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("correct horse battery staple"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, closeSecret, err := openSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if reader == nil || closeSecret == nil {
		t.Fatal("secret file did not return a bounded reader")
	}
	if err := closeSecret(); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCommonOptions("bootstrap", []string{"--password", "leaked"}); err == nil {
		t.Fatal("command accepted a password argv flag")
	}
}

func TestCommandPlaneRejectsAliases(t *testing.T) {
	for _, command := range []string{"config-check", "server", "api"} {
		if err := run(context.Background(), []string{command}, nil, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted compatibility command %q", command)
		}
	}
}

func TestDoctorReportsInvalidConfigurationAsJSONWithoutSecret(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), []string{"doctor", "--profile", "unknown"}, map[string]string{
		"GO_ADMIN_DATABASE_DSN": "postgres://operator:private@example.invalid/product",
	}, &output)
	if err == nil {
		t.Fatal("doctor accepted an invalid profile")
	}
	if !strings.Contains(output.String(), `"exit":"invalid-configuration"`) ||
		!strings.Contains(output.String(), `"name":"configuration"`) {
		t.Fatalf("doctor output = %q", output.String())
	}
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "postgres://") {
		t.Fatalf("doctor leaked secret input: %q", output.String())
	}
}
