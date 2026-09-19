package database

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildSQLiteURIIsPlatformIndependent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		wantHost    string
		wantEscaped string
	}{
		{name: "posix", path: "/var/lib/app data/db#one?.sqlite", wantEscaped: "/var/lib/app%20data/db%23one%3F.sqlite"},
		{name: "posix backslash filename", path: `/var/lib/a\b.sqlite`, wantEscaped: "/var/lib/a%5Cb.sqlite"},
		{name: "windows drive", path: `C:\Program Files\App\db#one?.sqlite`, wantEscaped: "/C:/Program%20Files/App/db%23one%3F.sqlite"},
		{name: "unc backslash", path: `\\fileserver\shared data\App\db#one?.sqlite`, wantEscaped: "//fileserver/shared%20data/App/db%23one%3F.sqlite"},
		{name: "unc slash", path: `//fileserver/shared data/App/db#one?.sqlite`, wantEscaped: "//fileserver/shared%20data/App/db%23one%3F.sqlite"},
	}
	wantPragmas := []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn, err := buildSQLiteURI(test.path)
			if err != nil {
				t.Fatalf("buildSQLiteURI() error = %v", err)
			}
			parsed, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if parsed.Scheme != "file" || parsed.Host != test.wantHost || parsed.EscapedPath() != test.wantEscaped {
				t.Fatalf("URI = %q; scheme=%q host=%q path=%q", dsn, parsed.Scheme, parsed.Host, parsed.EscapedPath())
			}
			if got := parsed.Query()["_pragma"]; !reflect.DeepEqual(got, wantPragmas) {
				t.Fatalf("_pragma = %#v, want %#v", got, wantPragmas)
			}
		})
	}
}

func TestSQLiteUNCURIHasDriverAcceptedEmptyAuthority(t *testing.T) {
	t.Parallel()

	localPath := filepath.Join(t.TempDir(), "unc-driver.db")
	doubleSlashPath := "//" + strings.TrimPrefix(filepath.ToSlash(localPath), "/")
	dsn, err := buildSQLiteURI(doubleSlashPath)
	if err != nil {
		t.Fatalf("buildSQLiteURI() error = %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "//") {
		t.Fatalf("UNC URI = %q, want empty authority and double-slash path", dsn)
	}
	query := parsed.Query()
	query.Set("mode", "memory")
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", parsed.String())
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("modernc rejected empty-authority UNC URI: %v", err)
	}
}

func TestBuildSQLiteURIRejectsNonAbsolutePaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"", "relative/app.db", `C:relative\app.db`, `\\server`} {
		if _, err := buildSQLiteURI(path); err == nil {
			t.Fatalf("buildSQLiteURI(%q) unexpectedly succeeded", path)
		}
	}
}
