// Package files owns authorized file metadata and local content lifecycle use cases.
package files

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	PermissionFilesRead   = "files.objects.read"
	PermissionFilesWrite  = "files.objects.write"
	PermissionFilesDelete = "files.objects.delete"
	MaximumPage           = 1_000_000
)

var (
	ErrDenied         = errors.New("files authorization denied")
	ErrValidation     = errors.New("files request invalid")
	ErrNotFound       = errors.New("files object not found")
	ErrConflict       = errors.New("files object conflict")
	ErrInternal       = errors.New("files operation failed")
	ErrAuthentication = errors.New("files authentication required")
	ErrCSRF           = errors.New("files csrf rejected")
)

type Scope string

const (
	ScopeSelf Scope = "self"
	ScopeAll  Scope = "all"
)

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type Authorizer interface {
	RequireInTx(context.Context, database.Tx, string, string) (Scope, error)
}

type Storage interface {
	Stage(context.Context, string, io.Reader) (StagedContent, error)
	Publish(context.Context, string, string) error
	Abort(context.Context, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	ObjectExists(context.Context, string) (bool, error)
	TemporaryExists(context.Context, string) (bool, error)
}

type Metadata struct {
	ID, OriginalName, MediaType, SHA256 string
	SizeBytes                           int64
	Revision                            int64
	CreatedAt, UpdatedAt                time.Time
}

type UploadInput struct {
	OriginalName, DeclaredMediaType string
	DeclaredSizeBytes               int64
	Content                         io.Reader
}

type ListQuery struct {
	Search, Sort, Direction string
	Page, PageSize          int
}

type Page struct {
	Rows  []Metadata
	Total int
}

type Download struct {
	Metadata Metadata
	Content  io.ReadCloser
}

type DeleteTarget struct {
	ID       string
	Revision int64
}

type Observation struct {
	Operation string
	Outcome   string
}

type Observer interface{ Observe(Observation) }

func normalizeFilename(value string) (string, bool) {
	value = strings.TrimSpace(value)
	length := runeLength(value)
	if length < 1 || length > 255 || value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return "", false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return value, true
}

func normalizeListQuery(query ListQuery) (ListQuery, bool) {
	query.Search = strings.ToLower(strings.TrimSpace(query.Search))
	if query.Sort == "" {
		query.Sort = "createdAt"
	}
	if query.Direction == "" {
		query.Direction = "descending"
	}
	validSort := query.Sort == "name" || query.Sort == "sizeBytes" || query.Sort == "createdAt"
	return query, runeLength(query.Search) <= 100 && query.Page >= 1 && query.Page <= MaximumPage && query.PageSize >= 1 && query.PageSize <= 100 &&
		validSort && (query.Direction == "ascending" || query.Direction == "descending")
}

func runeLength(value string) int {
	if !utf8.ValidString(value) {
		return -1
	}
	return utf8.RuneCountInString(value)
}

func nameKey(value string) string { return strings.ToLower(value) }

func validScope(scope Scope) bool { return scope == ScopeSelf || scope == ScopeAll }
