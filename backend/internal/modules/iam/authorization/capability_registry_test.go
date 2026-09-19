package authorization_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	administrationmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0020-administration-schema"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

const registryPostgresDSN = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"

func TestCapabilityRegistrySQLiteContract(t *testing.T) {
	db := openRegistrySQLite(t)
	runCapabilityRegistryContract(t, db)
}

func TestCapabilityRegistryPostgresContract(t *testing.T) {
	dsn := os.Getenv(registryPostgresDSN)
	if dsn == "" {
		t.Skip(registryPostgresDSN + " is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal("PostgreSQL registry administrator open failed")
	}
	schema := fmt.Sprintf("t14_registry_%d", time.Now().UnixNano())
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal("PostgreSQL registry schema creation failed")
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.SQL().ExecContext(cleanupContext, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Error("PostgreSQL registry cleanup failed")
		}
		_ = admin.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("PostgreSQL registry material invalid")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: parsed.String()})
	if err != nil {
		t.Fatal("isolated PostgreSQL registry open failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	var currentSchema string
	if err := db.Bun().QueryRowContext(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatalf("isolated schema mismatch: current=%q err=%v", currentSchema, err)
	}
	migrateRegistry(t, db)
	runCapabilityRegistryContract(t, db)
}

func runCapabilityRegistryContract(t *testing.T, db *database.Database) {
	t.Helper()
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	definitions := capabilities.ModuleCapabilities{Permissions: []capabilities.PermissionDefinition{{Code: "demo.products.read", Name: "Read demo products"}, {Code: "demo.products.write", Name: "Manage demo products"}}, Menus: []capabilities.MenuDefinition{{ID: "menu-demo-products", Key: "demo-products", Label: "Demo products", Path: "/demo/products", PermissionCode: "demo.products.read", SortOrder: 800}}}
	if err := registry.Register(context.Background(), definitions); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(context.Background(), definitions); err != nil {
		t.Fatalf("idempotent registration failed: %v", err)
	}
	var permissionCount, grantCount, menuCount, menuGrantCount int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code LIKE 'demo.products.%'`).Scan(&permissionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_role_permissions WHERE role_id = ? AND permission_code LIKE 'demo.products.%'`, "role-system-admin").Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_menus WHERE id = ? AND protected = ?`, "menu-demo-products", true).Scan(&menuCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_role_menus WHERE role_id = ? AND menu_id = ?`, "role-system-admin", "menu-demo-products").Scan(&menuGrantCount); err != nil {
		t.Fatal(err)
	}
	if permissionCount != 2 || grantCount != 2 || menuCount != 1 || menuGrantCount != 1 {
		t.Fatalf("registry projection mismatch permissions=%d grants=%d menus=%d menuGrants=%d", permissionCount, grantCount, menuCount, menuGrantCount)
	}
	if _, err := db.Bun().ExecContext(context.Background(), `INSERT INTO iam_permissions(code, name, protected) VALUES (?, ?, ?)`, "demo.products.delete", "Conflicting name", true); err != nil {
		t.Fatal(err)
	}
	err = registry.Register(context.Background(), capabilities.ModuleCapabilities{Permissions: []capabilities.PermissionDefinition{{Code: "demo.products.export", Name: "Export demo products"}, {Code: "demo.products.delete", Name: "Delete demo products"}}})
	if !errors.Is(err, authorization.ErrCapabilityRegistryConflict) {
		t.Fatalf("definition conflict = %v", err)
	}
	var rolledBack int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code = ?`, "demo.products.export").Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("registry conflict was not atomic count=%d err=%v", rolledBack, err)
	}
	menuConflict := capabilities.ModuleCapabilities{
		Permissions: []capabilities.PermissionDefinition{{Code: "demo.products.export", Name: "Export demo products"}},
		Menus:       []capabilities.MenuDefinition{{ID: "menu-demo-export01", Key: "demo-products", Label: "Export demo products", Path: "/demo/export", PermissionCode: "demo.products.export", SortOrder: 801}},
	}
	if err := registry.Register(context.Background(), menuConflict); !errors.Is(err, authorization.ErrCapabilityRegistryConflict) {
		t.Fatalf("menu metadata conflict = %v", err)
	}
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code = ?`, "demo.products.export").Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("menu conflict was not atomic count=%d err=%v", rolledBack, err)
	}
	concurrent := capabilities.ModuleCapabilities{
		Permissions: []capabilities.PermissionDefinition{{Code: "demo.products.concurrent", Name: "Concurrent demo capability"}},
		Menus:       []capabilities.MenuDefinition{{ID: "menu-demo-concurrent", Key: "demo-concurrent", Label: "Concurrent demo", Path: "/demo/concurrent", PermissionCode: "demo.products.concurrent", SortOrder: 802}},
	}
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsFound <- registry.Register(context.Background(), concurrent)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent identical registration = %v", err)
		}
	}
	conflicts := []capabilities.ModuleCapabilities{
		{Permissions: []capabilities.PermissionDefinition{{Code: "demo.race.one", Name: "Demo race one"}}, Menus: []capabilities.MenuDefinition{{ID: "menu-demo-race-one", Key: "demo-race", Label: "Demo race one", Path: "/demo/race", PermissionCode: "demo.race.one", SortOrder: 803}}},
		{Permissions: []capabilities.PermissionDefinition{{Code: "demo.race.two", Name: "Demo race two"}}, Menus: []capabilities.MenuDefinition{{ID: "menu-demo-race-two", Key: "demo-race", Label: "Demo race two", Path: "/demo/race", PermissionCode: "demo.race.two", SortOrder: 803}}},
	}
	start = make(chan struct{})
	errorsFound = make(chan error, 2)
	for _, definition := range conflicts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsFound <- registry.Register(context.Background(), definition)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsFound)
	succeeded, conflicted := 0, 0
	for err := range errorsFound {
		if err == nil {
			succeeded++
		} else if errors.Is(err, authorization.ErrCapabilityRegistryConflict) {
			conflicted++
		} else {
			t.Fatalf("concurrent conflicting registration = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent conflict outcomes succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	var racePermissions int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code IN (?, ?)`, "demo.race.one", "demo.race.two").Scan(&racePermissions); err != nil || racePermissions != 1 {
		t.Fatalf("losing capability transaction was not rolled back count=%d err=%v", racePermissions, err)
	}
}

func TestCapabilityRegistryRejectsUnstableDefinitions(t *testing.T) {
	db := openRegistrySQLite(t)
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	values := [][]capabilities.PermissionDefinition{
		nil,
		{{Code: "Demo.Products.Read", Name: "Read demo products"}},
		{{Code: "demo products read", Name: "Read demo products"}},
		{{Code: "demo.products.read", Name: " Read demo products"}},
		{{Code: "demo.products.read", Name: "Read\nproducts"}},
		{{Code: "demo.products.read", Name: "Read demo products"}, {Code: "demo.products.read", Name: "Duplicate"}},
	}
	for _, value := range values {
		if err := registry.Register(context.Background(), capabilities.ModuleCapabilities{Permissions: value}); !errors.Is(err, authorization.ErrCapabilityRegistryInvalid) {
			t.Fatalf("invalid definition %#v = %v", value, err)
		}
	}
	var count int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code LIKE 'demo.%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid definitions mutated state count=%d err=%v", count, err)
	}
}

func TestCapabilityRegistryCountsUnicodeDisplayRunes(t *testing.T) {
	db := openRegistrySQLite(t)
	registry, err := authorization.NewCapabilityRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	valid := capabilities.ModuleCapabilities{
		Permissions: []capabilities.PermissionDefinition{{Code: "demo.unicode.read", Name: strings.Repeat("界", 100)}},
		Menus:       []capabilities.MenuDefinition{{ID: "menu-demo-unicode", Key: "demo-unicode", Label: strings.Repeat("界", 80), Path: "/demo/unicode", PermissionCode: "demo.unicode.read", SortOrder: 900}},
	}
	if err := registry.Register(context.Background(), valid); err != nil {
		t.Fatalf("100/80 rune boundary = %v", err)
	}
	invalid := capabilities.ModuleCapabilities{Permissions: []capabilities.PermissionDefinition{{Code: "demo.unicode.write", Name: strings.Repeat("界", 101)}}}
	if err := registry.Register(context.Background(), invalid); !errors.Is(err, authorization.ErrCapabilityRegistryInvalid) {
		t.Fatalf("101 rune boundary = %v", err)
	}
}

func TestCapabilityRegistryRejectsMissingOrUnprotectedSystemAdministrator(t *testing.T) {
	for _, test := range []struct {
		name      string
		breakRole func(t *testing.T, db *database.Database)
	}{
		{name: "unprotected", breakRole: func(t *testing.T, db *database.Database) {
			if _, err := db.Bun().ExecContext(context.Background(), `UPDATE iam_roles SET protected = ? WHERE role_key = ?`, false, "system-admin"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "disabled", breakRole: func(t *testing.T, db *database.Database) {
			if _, err := db.Bun().ExecContext(context.Background(), `UPDATE iam_roles SET enabled = ? WHERE role_key = ?`, false, "system-admin"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing", breakRole: func(t *testing.T, db *database.Database) {
			for _, statement := range []string{`DELETE FROM iam_role_menus`, `DELETE FROM iam_role_permissions`, `DELETE FROM iam_roles WHERE role_key = 'system-admin'`} {
				if _, err := db.Bun().ExecContext(context.Background(), statement); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openRegistrySQLite(t)
			test.breakRole(t, db)
			registry, err := authorization.NewCapabilityRegistry(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(context.Background(), capabilities.ModuleCapabilities{Permissions: []capabilities.PermissionDefinition{{Code: "demo.products.read", Name: "Read demo products"}}}); !errors.Is(err, authorization.ErrCapabilityRegistryConflict) {
				t.Fatalf("system administrator invariant = %v", err)
			}
			var count int
			if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM iam_permissions WHERE code = ?`, "demo.products.read").Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid role mutated registry count=%d err=%v", count, err)
			}
		})
	}
}

func TestCapabilityRegistryRequiresDatabase(t *testing.T) {
	if _, err := authorization.NewCapabilityRegistry(nil); !errors.Is(err, authorization.ErrCapabilityRegistryInvalid) {
		t.Fatalf("nil database = %v", err)
	}
}

type registryFailureDatabase struct {
	dialect database.Dialect
	err     error
}

func (value registryFailureDatabase) Dialect() database.Dialect { return value.dialect }
func (value registryFailureDatabase) WithinTx(context.Context, func(context.Context, database.Tx) error) error {
	return value.err
}

func TestCapabilityRegistryPreservesContextSentinelsAndRejectsDialect(t *testing.T) {
	if _, err := authorization.NewCapabilityRegistry(registryFailureDatabase{dialect: "mysql"}); !errors.Is(err, authorization.ErrCapabilityRegistryInvalid) {
		t.Fatalf("invalid dialect = %v", err)
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		registry, err := authorization.NewCapabilityRegistry(registryFailureDatabase{dialect: database.DialectSQLite, err: errors.Join(errors.New("driver detail"), sentinel)})
		if err != nil {
			t.Fatal(err)
		}
		err = registry.Register(context.Background(), capabilities.ModuleCapabilities{Permissions: []capabilities.PermissionDefinition{{Code: "demo.products.read", Name: "Read demo products"}}})
		if !errors.Is(err, sentinel) {
			t.Fatalf("lost context sentinel %v: %v", sentinel, err)
		}
	}
}

func openRegistrySQLite(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "registry.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateRegistry(t, db)
	return db
}

func migrateRegistry(t *testing.T, db *database.Database) {
	t.Helper()
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, administrationmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
