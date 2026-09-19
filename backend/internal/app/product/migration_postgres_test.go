package product

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/recovery"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

func TestPostgresExplicitMigrationAndExactRuntimeSchema(t *testing.T) {
	adminDSN := os.Getenv("GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN")
	if adminDSN == "" {
		t.Skip("GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: adminDSN})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("t09_%d_%d", os.Getpid(), time.Now().UnixNano())
	if !regexp.MustCompile(`^t09_[0-9]+_[0-9]+$`).MatchString(name) {
		t.Fatal("unsafe disposable database name")
	}
	if _, err := admin.SQL().ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanup, openErr := database.NewProcess().Open(cleanupCtx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: adminDSN})
		if openErr != nil {
			t.Errorf("open PostgreSQL cleanup connection: %v", openErr)
			return
		}
		defer cleanup.Close()
		if _, dropErr := cleanup.SQL().ExecContext(cleanupCtx, "DROP DATABASE "+name+" WITH (FORCE)"); dropErr != nil {
			t.Errorf("drop disposable PostgreSQL database: %v", dropErr)
		}
	})
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	dsn := parsed.String()
	snapshot, err := config.Load(config.Input{
		Profile: config.ProfileServerPostgres, Environment: map[string]string{"GO_ADMIN_DATABASE_DSN": dsn},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := PrepareRuntimeSchema(ctx, db, false); err != nil {
		t.Fatalf("explicitly migrated schema rejected: %v", err)
	}
	releasePresence, err := recovery.AcquireRuntimePresence(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, snapshot); err == nil {
		_ = releasePresence()
		t.Fatal("migration entered while a PostgreSQL runtime held the presence lock")
	}
	if err := releasePresence(); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeSchema(ctx, db, true); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("PostgreSQL runtime auto-migration error = %v", err)
	}
}
