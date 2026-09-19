// Package migrations composes module-owned SQL migrations into one forward-only sequence.
package migrations

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	gooselock "github.com/pressly/goose/v3/lock"

	platformdb "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

var (
	modulePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	filePattern   = regexp.MustCompile(`^(\d{1,18})_[a-z][a-z0-9_]*\.sql$`)
)

// Provider publishes one module's immutable migration files for the selected dialect.
type Provider interface {
	Module() string
	Migrations(platformdb.Dialect) (fs.FS, error)
}

// Runner validates and composes every module before invoking Goose's forward operation.
type Runner struct {
	providers []Provider
}

type Result struct {
	Applied        int
	CurrentVersion int64
}

func NewRunner(providers ...Provider) (*Runner, error) {
	owned := append([]Provider(nil), providers...)
	seen := make(map[string]struct{}, len(owned))
	for _, provider := range owned {
		if provider == nil {
			return nil, errors.New("migration provider is required")
		}
		module := provider.Module()
		if !modulePattern.MatchString(module) {
			return nil, errors.New("migration module name is invalid")
		}
		if _, exists := seen[module]; exists {
			return nil, errors.New("migration module is duplicated")
		}
		seen[module] = struct{}{}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Module() < owned[j].Module() })
	return &Runner{providers: owned}, nil
}

// Compose returns a root-only filesystem accepted by Goose. Global versions must be unique even
// when files originate in different modules.
func (r *Runner) Compose(dialect platformdb.Dialect) (fs.FS, error) {
	if dialect != platformdb.DialectPostgres && dialect != platformdb.DialectSQLite {
		return nil, errors.New("migration dialect is unsupported")
	}
	files := make(map[string][]byte)
	versions := make(map[int64]struct{})
	for _, provider := range r.providers {
		provided, err := provider.Migrations(dialect)
		if err != nil || provided == nil {
			return nil, errors.New("migration provider failed")
		}
		err = fs.WalkDir(provided, ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return errors.New("migration filesystem is unreadable")
			}
			if entry.IsDir() {
				return nil
			}
			base := path.Base(name)
			matches := filePattern.FindStringSubmatch(base)
			if matches == nil {
				return errors.New("migration filename is invalid")
			}
			version, parseErr := strconv.ParseInt(matches[1], 10, 64)
			if parseErr != nil || version <= 0 {
				return errors.New("migration version is invalid")
			}
			if _, exists := versions[version]; exists {
				return errors.New("migration version is duplicated")
			}
			content, readErr := fs.ReadFile(provided, name)
			if readErr != nil {
				return errors.New("migration file is unreadable")
			}
			if err := validateForwardAnnotations(content); err != nil {
				return err
			}
			versions[version] = struct{}{}
			files[base] = bytes.Clone(content)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return immutableFS{files: files}, nil
}

// Up applies all pending files. It intentionally exposes no rollback operation.
func (r *Runner) Up(ctx context.Context, db *platformdb.Database) (Result, error) {
	if db == nil {
		return Result{}, errors.New("migration database is required")
	}
	composed, err := r.Compose(db.Dialect())
	if err != nil {
		return Result{}, err
	}
	var dialect goose.Dialect
	switch db.Dialect() {
	case platformdb.DialectPostgres:
		dialect = goose.DialectPostgres
	case platformdb.DialectSQLite:
		dialect = goose.DialectSQLite3
	default:
		return Result{}, errors.New("migration dialect is unsupported")
	}
	options := []goose.ProviderOption{
		goose.WithLogger(goose.NopLogger()),
		goose.WithDisableGlobalRegistry(true),
		goose.WithAllowOutofOrder(false),
	}
	if db.Dialect() == platformdb.DialectPostgres {
		locker, lockErr := gooselock.NewPostgresSessionLocker()
		if lockErr != nil {
			return Result{}, errors.New("migration lock initialization failed")
		}
		options = append(options, goose.WithSessionLocker(locker))
	}
	provider, err := goose.NewProvider(dialect, db.SQL(), composed, options...)
	if err != nil {
		return Result{}, errors.New("migration provider initialization failed")
	}
	applied, err := provider.Up(ctx)
	if err != nil {
		return Result{}, sanitizedMigrationError(ctx, "migration execution failed", err)
	}
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return Result{}, sanitizedMigrationError(ctx, "migration version check failed", err)
	}
	return Result{Applied: len(applied), CurrentVersion: current}, nil
}

func sanitizedMigrationError(ctx context.Context, stage string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", stage, contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", stage, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", stage, context.DeadlineExceeded)
	}
	return errors.New(stage)
}

func validateForwardAnnotations(content []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	upSeen := false
	statementBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		isAnnotation := strings.HasPrefix(trimmed, "--") && strings.Contains(line, "+goose")
		if !isAnnotation {
			if !upSeen && trimmed != "" && !strings.HasPrefix(trimmed, "--") {
				return errors.New("migration contains content before forward directive")
			}
			continue
		}
		annotation, err := extractAnnotation(line)
		if err != nil {
			return errors.New("migration annotation is invalid")
		}
		switch annotation {
		case "up":
			if upSeen || statementBlock {
				return errors.New("migration annotation is invalid")
			}
			upSeen = true
		case "down", "no transaction":
			return errors.New("migration violates forward atomic policy")
		case "statementbegin":
			if !upSeen || statementBlock {
				return errors.New("migration annotation is invalid")
			}
			statementBlock = true
		case "statementend":
			if !statementBlock {
				return errors.New("migration annotation is invalid")
			}
			statementBlock = false
		case "envsub on", "envsub off":
			return errors.New("migration violates deterministic input policy")
		default:
			return errors.New("migration annotation is invalid")
		}
	}
	if scanner.Err() != nil {
		return errors.New("migration annotation is unreadable")
	}
	if !upSeen {
		return errors.New("migration is missing forward directive")
	}
	if statementBlock {
		return errors.New("migration annotation is invalid")
	}
	return nil
}

func extractAnnotation(line string) (string, error) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", errors.New("leading whitespace")
	}
	command := strings.ReplaceAll(line, "--", "")
	command = strings.Replace(command, "+goose", "", 1)
	if strings.Contains(command, "+goose") {
		return "", errors.New("multiple annotations")
	}
	command = strings.TrimSpace(command)
	for _, allowed := range []string{"up", "down", "statementbegin", "statementend", "no transaction", "envsub on", "envsub off"} {
		if strings.EqualFold(command, allowed) {
			return allowed, nil
		}
	}
	return "", errors.New("unsupported annotation")
}

type immutableFS struct {
	files map[string][]byte
}

func (m immutableFS) Open(name string) (fs.File, error) {
	if name == "." {
		names := make([]string, 0, len(m.files))
		for filename := range m.files {
			names = append(names, filename)
		}
		sort.Strings(names)
		entries := make([]fs.DirEntry, 0, len(names))
		for _, filename := range names {
			entries = append(entries, fileInfo{name: filename, size: int64(len(m.files[filename]))})
		}
		return &directory{entries: entries}, nil
	}
	if !fs.ValidPath(name) || path.Base(name) != name {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	content, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	info := fileInfo{name: name, size: int64(len(content))}
	return &file{Reader: bytes.NewReader(content), info: info}, nil
}

func (m immutableFS) ReadFile(name string) ([]byte, error) {
	content, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return bytes.Clone(content), nil
}

type file struct {
	*bytes.Reader
	info fileInfo
}

func (f *file) Close() error               { return nil }
func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }

type directory struct {
	entries []fs.DirEntry
	offset  int
}

func (d *directory) Close() error { return nil }
func (d *directory) Stat() (fs.FileInfo, error) {
	return fileInfo{name: ".", directory: true}, nil
}
func (d *directory) Read([]byte) (int, error) { return 0, io.EOF }
func (d *directory) ReadDir(count int) ([]fs.DirEntry, error) {
	if d.offset >= len(d.entries) && count > 0 {
		return nil, io.EOF
	}
	end := len(d.entries)
	if count > 0 && d.offset+count < end {
		end = d.offset + count
	}
	entries := append([]fs.DirEntry(nil), d.entries[d.offset:end]...)
	d.offset = end
	return entries, nil
}

type fileInfo struct {
	name      string
	size      int64
	directory bool
}

func (i fileInfo) Name() string { return i.name }
func (i fileInfo) Size() int64  { return i.size }
func (i fileInfo) Mode() fs.FileMode {
	if i.directory {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i fileInfo) ModTime() time.Time         { return time.Time{} }
func (i fileInfo) IsDir() bool                { return i.directory }
func (i fileInfo) Sys() any                   { return nil }
func (i fileInfo) Type() fs.FileMode          { return i.Mode().Type() }
func (i fileInfo) Info() (fs.FileInfo, error) { return i, nil }

var _ fs.ReadFileFS = immutableFS{}
var _ fs.ReadDirFile = (*directory)(nil)

func (r Result) String() string {
	return fmt.Sprintf("migration.Result{Applied:%d, CurrentVersion:%d}", r.Applied, r.CurrentVersion)
}
