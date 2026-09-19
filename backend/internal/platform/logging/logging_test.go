package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/logging"
)

func TestJSONSinkFiltersLevelAddsIdentityAndRedactsSecretCorpus(t *testing.T) {
	var output bytes.Buffer
	logger, closer, err := logging.New(logging.Config{
		Service: "go-admin-plus", Version: "v-test", Profile: "server-postgres", Level: "warn",
		Sink: logging.Sink{Mode: logging.ModeJSON, Writer: &output},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	logger.Debug("hidden", slog.String("password", "debug-secret"))
	logger.Warn("request completed", append(logging.EventFields{
		TraceID: "trace-1", RequestID: "request-1", Route: "/api/v1/files", Module: "files", Status: 503,
		Latency: 7 * time.Millisecond, Database: "postgres", ErrorClass: "unavailable",
	}.Attrs(),
		slog.String("password", "password-value"), slog.String("cookie", "session=cookie-value"), slog.String("csrf", "csrf-value"),
		slog.String("dsn", "postgres://operator:dsn-secret@example.invalid/private"), slog.String("request_body", `{"secret":"body-value"}`),
		slog.Any("error", errors.New("postgres://operator:error-secret@example.invalid/private")))...)
	text := output.String()
	for _, secret := range []string{"hidden", "debug-secret", "password-value", "cookie-value", "csrf-value", "dsn-secret", "body-value", "error-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log leaked %q: %s", secret, text)
		}
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"service": "go-admin-plus", "version": "v-test", "profile": "server-postgres", "trace_id": "trace-1", "request_id": "request-1", "module": "files"} {
		if event[key] != want {
			t.Fatalf("event[%s]=%v want=%v", key, event[key], want)
		}
	}
	if event["password"] != "[redacted]" || event["error"] != "internal" {
		t.Fatalf("redacted event=%v", event)
	}
}

func TestConsoleAndRotatingFileSinks(t *testing.T) {
	var console bytes.Buffer
	logger, closer, err := logging.New(logging.Config{Service: "service", Version: "v1", Profile: "server-sqlite", Level: "info", Sink: logging.Sink{Mode: logging.ModeConsole, Writer: &console}})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("console event", slog.String("token", "private-token"))
	_ = closer.Close()
	if !strings.Contains(console.String(), "console event") || strings.Contains(console.String(), "private-token") {
		t.Fatalf("console output=%q", console.String())
	}

	directory := t.TempDir()
	logger, closer, err = logging.New(logging.Config{Service: "service", Version: "v1", Profile: "desktop-sqlite", Level: "debug", Sink: logging.Sink{
		Mode: logging.ModeRotatingFile, Directory: directory, Filename: "runtime.log", MaximumBytes: 180, Backups: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 12 {
		logger.Info("rotating event", slog.Int("index", index), slog.String("padding", strings.Repeat("x", 32)))
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime.log", "runtime.log.1"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("rotated file %s info=%v err=%v", name, info, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("rotated file %s permissions=%o", name, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "runtime.log.3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation exceeded backups: %v", err)
	}
}

func TestRotatingSinkRejectsSymlinkFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged Windows symlink creation is not portable")
	}
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "runtime.log")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := logging.New(logging.Config{Service: "service", Version: "v1", Profile: "desktop-sqlite", Level: "info", Sink: logging.Sink{
		Mode: logging.ModeRotatingFile, Directory: directory, Filename: "runtime.log", MaximumBytes: 180, Backups: 2,
	}}); err == nil {
		t.Fatal("rotating sink accepted symlink file")
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "private" {
		t.Fatalf("outside file changed: %q err=%v", content, err)
	}
}

func TestSinkFailureWritesOnlyMinimalFallback(t *testing.T) {
	var fallback bytes.Buffer
	logger, closer, err := logging.New(logging.Config{Service: "service", Version: "v1", Profile: "server-sqlite", Level: "info", Sink: logging.Sink{
		Mode: logging.ModeJSON, Writer: failingWriter{}, Fallback: &fallback,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	logger.Log(context.Background(), slog.LevelError, "postgres://operator:message-secret@example.invalid/private", slog.String("password", "attribute-secret"))
	if fallback.String() != "{\"level\":\"ERROR\",\"msg\":\"log_sink_failed\"}\n" {
		t.Fatalf("fallback=%q", fallback.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("private sink failure") }
