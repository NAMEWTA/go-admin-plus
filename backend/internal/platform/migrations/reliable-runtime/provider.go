// Package reliableruntime publishes the database schema owned by the reliable runtime.
package reliableruntime

import (
	"embed"
	"errors"
	"io/fs"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

//go:embed postgres/*.sql sqlite/*.sql
var files embed.FS

type Provider struct{}

func (Provider) Module() string { return "reliable-runtime" }

func (Provider) Migrations(dialect database.Dialect) (fs.FS, error) {
	switch dialect {
	case database.DialectPostgres:
		return fs.Sub(files, "postgres")
	case database.DialectSQLite:
		return fs.Sub(files, "sqlite")
	default:
		return nil, errors.New("reliable runtime migration dialect is unsupported")
	}
}
