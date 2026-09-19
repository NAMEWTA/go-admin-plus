package product_test

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/app/product"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestProductProfileMatrix(t *testing.T) {
	want := []product.ProfileDefinition{
		{Profile: config.ProfileServerPostgres, Dialect: database.DialectPostgres},
		{Profile: config.ProfileServerSQLite, Dialect: database.DialectSQLite},
		{Profile: config.ProfileDesktopSQLite, Dialect: database.DialectSQLite},
	}
	if got := product.Profiles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Profiles() = %#v, want %#v", got, want)
	}
}

func TestProductModulesAreCompleteAndOwned(t *testing.T) {
	modules := product.Modules()
	want := []product.ModuleID{
		product.ModuleIAM,
		product.ModuleAudit,
		product.ModuleScheduler,
		product.ModuleFiles,
	}
	got := make([]product.ModuleID, len(modules))
	for index, module := range modules {
		got[index] = module.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Modules() = %v, want %v", got, want)
	}
	wantMigrations := map[product.ModuleID][]string{
		product.ModuleIAM: {
			"iam-session",
			"iam-administration",
			"iam-bootstrap-recovery",
			"iam-session-protection",
			"iam-account-lifecycle",
		},
		product.ModuleFiles: {"files", "files-capacity", "files-account-lifecycle"},
	}
	for _, module := range modules {
		if want, ok := wantMigrations[module.ID]; ok && !reflect.DeepEqual(module.MigrationModules, want) {
			t.Fatalf("Modules()[%s].MigrationModules = %v, want %v", module.ID, module.MigrationModules, want)
		}
	}

	modules[0].MigrationModules[0] = "mutated"
	if product.Modules()[0].MigrationModules[0] != "iam-session" {
		t.Fatal("Modules() exposed mutable product state")
	}
}

func TestProductMigrationMatrixComposesBothDialects(t *testing.T) {
	runner, err := product.NewMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []database.Dialect{database.DialectSQLite, database.DialectPostgres} {
		dialect := dialect
		t.Run(string(dialect), func(t *testing.T) {
			composed, err := runner.Compose(dialect)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := fs.ReadDir(composed, ".")
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 12 {
				t.Fatalf("composed migration files = %d, want 12", len(entries))
			}
		})
	}
}

func TestProductCapabilityRegistrationIsOrderedAndFailFast(t *testing.T) {
	registrar := &recordingRegistrar{failAt: 3, err: errors.New("private failure")}
	err := product.RegisterCapabilities(context.Background(), registrar)
	if !errors.Is(err, product.ErrCapabilityRegistration) {
		t.Fatalf("RegisterCapabilities() error = %v", err)
	}
	if got := err.Error(); got != "product capability registration failed: files" {
		t.Fatalf("RegisterCapabilities() exposed unstable detail: %q", got)
	}
	want := []string{"audit", "scheduler", "files"}
	if !reflect.DeepEqual(registrar.modules, want) {
		t.Fatalf("registered modules = %v, want %v", registrar.modules, want)
	}
	if err := product.RegisterCapabilities(context.Background(), nil); !errors.Is(err, product.ErrCapabilityRegistration) {
		t.Fatalf("nil registrar error = %v", err)
	}
}

func TestProductSQLiteAppliesMigrationsAndCapabilities(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewProcess().Open(ctx, database.Config{
		Profile:    config.ProfileServerSQLite,
		SQLitePath: filepath.Join(t.TempDir(), "product.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runner, err := product.NewMigrationRunner()
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Up(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 12 {
		t.Fatalf("applied migrations = %d, want 12", result.Applied)
	}

	capabilities, err := authorization.NewCapabilityRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := product.RegisterCapabilities(ctx, capabilities); err != nil {
		t.Fatal(err)
	}
	var menus int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_menus`).Scan(&menus); err != nil {
		t.Fatal(err)
	}
	if menus != 7 {
		t.Fatalf("product menus = %d, want 7", menus)
	}
}

type recordingRegistrar struct {
	modules []string
	failAt  int
	err     error
}

func (r *recordingRegistrar) Register(_ context.Context, definitions capabilities.ModuleCapabilities) error {
	module := definitions.Permissions[0].Code
	for index, character := range module {
		if character == '.' {
			module = module[:index]
			break
		}
	}
	r.modules = append(r.modules, module)
	if len(r.modules) == r.failAt {
		return r.err
	}
	return nil
}
