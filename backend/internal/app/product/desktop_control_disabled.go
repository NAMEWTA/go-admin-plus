//go:build !desktop_native_e2e

package product

import (
	desktophost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/desktop"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func desktopPrivateRoute(db *database.Database, sessions *session.Service) *desktophost.PrivateRoute {
	return newDesktopSetupPrivateRoute(db, sessions, desktopSetupPath)
}
