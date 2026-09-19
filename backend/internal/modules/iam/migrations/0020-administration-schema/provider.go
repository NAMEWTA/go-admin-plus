package administrationmigration

import (
	"embed"
	"errors"
	"io/fs"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

//go:embed postgres/*.sql sqlite/*.sql
var files embed.FS

type Provider struct{}

func (Provider) Module() string { return "iam-administration" }

func (Provider) Migrations(dialect database.Dialect) (fs.FS, error) {
	switch dialect {
	case database.DialectPostgres:
		return fs.Sub(files, "postgres")
	case database.DialectSQLite:
		return fs.Sub(files, "sqlite")
	default:
		return nil, errors.New("iam administration migration dialect is unsupported")
	}
}
