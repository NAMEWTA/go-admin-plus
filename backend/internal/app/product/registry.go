// Package product is the only composition point for the complete Go Admin Plus product.
package product

import (
	"context"
	"errors"
	"fmt"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit"
	auditmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit/migrations/0011-audit"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	filesaccountlifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/account_lifecycle_migration"
	filesmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0010-files"
	capacitymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files/migrations/0020-capacity"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	bootstraprecoverymigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0030-bootstrap-recovery"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	accountlifecyclemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0060-account-lifecycle"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
	schedulermigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/migrations"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliableruntime "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
)

var ErrCapabilityRegistration = errors.New("product capability registration failed")

type ModuleID string

const (
	ModuleIAM       ModuleID = "iam"
	ModuleAudit     ModuleID = "audit"
	ModuleScheduler ModuleID = "scheduler"
	ModuleFiles     ModuleID = "files"
)

type ModuleDefinition struct {
	ID               ModuleID
	MigrationModules []string
	RegistersMenu    bool
}

type ProfileDefinition struct {
	Profile config.Profile
	Dialect database.Dialect
}

type CapabilityRegistrar interface {
	Register(context.Context, capabilities.ModuleCapabilities) error
}

type capabilityRegistration struct {
	module   ModuleID
	register func(context.Context, CapabilityRegistrar) error
}

var moduleDefinitions = []ModuleDefinition{
	{ID: ModuleIAM, MigrationModules: []string{
		"iam-session",
		"iam-administration",
		"iam-bootstrap-recovery",
		"iam-session-protection",
		"iam-account-lifecycle",
	}},
	{ID: ModuleAudit, MigrationModules: []string{"audit"}, RegistersMenu: true},
	{ID: ModuleScheduler, MigrationModules: []string{"scheduler"}, RegistersMenu: true},
	{ID: ModuleFiles, MigrationModules: []string{"files", "files-capacity", "files-account-lifecycle"}, RegistersMenu: true},
}

var capabilityRegistrations = []capabilityRegistration{
	{module: ModuleAudit, register: func(ctx context.Context, registrar CapabilityRegistrar) error {
		return audit.RegisterCapabilities(ctx, registrar)
	}},
	{module: ModuleScheduler, register: func(ctx context.Context, registrar CapabilityRegistrar) error {
		return scheduler.RegisterCapabilities(ctx, registrar)
	}},
	{module: ModuleFiles, register: func(ctx context.Context, registrar CapabilityRegistrar) error {
		return files.RegisterCapabilities(ctx, registrar)
	}},
}

func Modules() []ModuleDefinition {
	modules := make([]ModuleDefinition, len(moduleDefinitions))
	for index, module := range moduleDefinitions {
		modules[index] = module
		modules[index].MigrationModules = append([]string(nil), module.MigrationModules...)
	}
	return modules
}

func Profiles() []ProfileDefinition {
	return []ProfileDefinition{
		{Profile: config.ProfileServerPostgres, Dialect: database.DialectPostgres},
		{Profile: config.ProfileServerSQLite, Dialect: database.DialectSQLite},
		{Profile: config.ProfileDesktopSQLite, Dialect: database.DialectSQLite},
	}
}

func NewMigrationRunner() (*migrations.Runner, error) {
	return migrations.NewRunner(
		sessionmigration.Provider{},
		administrationmigration.Provider{},
		bootstraprecoverymigration.Provider{},
		sessionprotectionmigration.Provider{},
		accountlifecyclemigration.Provider{},
		auditmigration.Provider{},
		schedulermigration.Provider{},
		filesmigration.Provider{},
		capacitymigration.Provider{},
		filesaccountlifecyclemigration.Provider{},
		reliableruntime.Provider{},
	)
}

func RegisterCapabilities(ctx context.Context, registrar CapabilityRegistrar) error {
	if registrar == nil {
		return ErrCapabilityRegistration
	}
	for _, registration := range capabilityRegistrations {
		if err := registration.register(ctx, registrar); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return errors.Join(ErrCapabilityRegistration, contextErr)
			}
			return fmt.Errorf("%w: %s", ErrCapabilityRegistration, registration.module)
		}
	}
	return nil
}
