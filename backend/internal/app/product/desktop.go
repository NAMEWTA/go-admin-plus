package product

import (
	"context"
	"time"

	desktophost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/desktop"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

// BuildDesktop adapts the product composition root to the Desktop Host's
// application-facing Builder contract.
func BuildDesktop(ctx context.Context, db *database.Database, options desktophost.ProductOptions) (desktophost.Product, error) {
	built, err := Build(ctx, db, Options{
		SessionPolicy:     config.DefaultSessionPolicy(),
		FilesRoot:         options.FilesRoot,
		WorkerOwner:       options.WorkerOwner,
		WorkerInterval:    time.Second,
		AuditRetentionAge: 30 * 24 * time.Hour,
	})
	if err != nil {
		return desktophost.Product{}, err
	}
	return desktophost.Product{
		Application:  built.Application,
		Readiness:    built.Readiness,
		PrivateRoute: desktopPrivateRoute(db, built.Sessions),
	}, nil
}
