package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
	schedulermigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/migrations"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

type runtimeParameters struct {
	Value string `json:"value"`
}

func TestSchedulerSQLiteRuntime(t *testing.T) {
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "scheduler-runtime.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testRuntime(t, db)
}

func TestSchedulerPostgresRuntime(t *testing.T) {
	baseDSN := os.Getenv("GO_ADMIN_SCHEDULER_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("GO_ADMIN_SCHEDULER_POSTGRES_DSN is required for the PostgreSQL non-E2E profile")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatal("postgres test dependency unavailable")
	}
	schema := "scheduler_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal("postgres test schema unavailable")
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close(context.Background())
	})
	isolatedDSN, err := isolatedSchedulerRuntimeDSN(baseDSN, schema)
	if err != nil {
		t.Fatal("postgres test configuration invalid")
	}
	db, err := database.NewProcess().Open(ctx, database.Config{Profile: config.ProfileServerPostgres, PostgresDSN: isolatedDSN, MaxOpenConnections: 5, MaxIdleConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testRuntime(t, db)
}

func isolatedSchedulerRuntimeDSN(dsn, schema string) (string, error) {
	suffix := strings.TrimPrefix(schema, "scheduler_test_")
	if len(suffix) != 32 || strings.Trim(suffix, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid Scheduler runtime schema")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("invalid PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func TestIsolatedSchedulerRuntimeDSNPreservesParameters(t *testing.T) {
	const schema = "scheduler_test_0123456789abcdef0123456789abcdef"
	value, err := isolatedSchedulerRuntimeDSN("postgres://localhost/database?application_name=scheduler+runtime&sslmode=disable", schema)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Query().Get("search_path") != schema || parsed.Query().Get("application_name") != "scheduler runtime" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("isolated Scheduler runtime DSN lost its search path or existing parameters")
	}
	if _, err := isolatedSchedulerRuntimeDSN("postgres://localhost/database", "public"); err == nil {
		t.Fatal("invalid Scheduler runtime schema was accepted")
	}
}

func testRuntime(t *testing.T, db *database.Database) {
	t.Helper()
	runner, err := migrations.NewRunner(schedulermigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Up(context.Background(), db)
	if err != nil || result.Applied != 1 {
		t.Fatalf("scheduler migration = %#v, %v", result, err)
	}
	if _, err := db.SQL().Exec(`CREATE TABLE scheduler_runtime_effects(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registration, err := scheduler.NewTaskRegistration("runtime.effect", "Runtime effect", []scheduler.ParameterField{{Name: "value", Label: "Value", Kind: scheduler.ParameterString, Required: true}}, func(ctx context.Context, tx database.Tx, value runtimeParameters) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO scheduler_runtime_effects(value) VALUES (?)`, value.Value)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := scheduler.NewRegistry(registration)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 5, 0, 0, time.UTC)
	firstID := insertDue(t, db, "first", now)
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "scheduler-runtime-owner"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "scheduler-runtime-owner"})
	if !errors.Is(err, coordination.ErrNotLeader) || second != nil {
		t.Fatalf("concurrent lease = %#v, %v", second, err)
	}
	executor, err := scheduler.NewExecutor(db, registry, scheduler.ExecutorConfig{Owner: "scheduler-runtime-owner", BatchSize: 10, TaskTimeout: time.Second, Clock: scheduler.ClockFunc(func() time.Time { return now })})
	if err != nil {
		t.Fatal(err)
	}
	executed, err := executor.RunOnce(context.Background(), lease)
	if err != nil || executed.Triggered != 1 || executed.Succeeded != 1 {
		t.Fatalf("first execution = %#v, %v", executed, err)
	}
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(context.Background(), `INSERT INTO scheduler_definitions(id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at) SELECT ?, name, name_key, task_type, schedule_json, parameters_json, ?, revision, NULL, created_at, updated_at FROM scheduler_definitions WHERE id = ?`, uuid.NewString(), false, firstID); err == nil {
		t.Fatal("active definition name was not unique")
	}
	if _, err := db.Bun().ExecContext(context.Background(), `UPDATE scheduler_definitions SET enabled = ?, next_run_at = NULL, deleted_at = ? WHERE id = ?`, false, now, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(context.Background(), `INSERT INTO scheduler_definitions(id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at) SELECT ?, name, name_key, task_type, schedule_json, parameters_json, ?, revision, NULL, created_at, updated_at FROM scheduler_definitions WHERE id = ?`, uuid.NewString(), false, firstID); err != nil {
		t.Fatalf("recreate deleted definition: %v", err)
	}
	insertDue(t, db, "takeover", now)
	takeover, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "scheduler-runtime-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = takeover.Close(context.Background()) })
	executed, err = executor.RunOnce(context.Background(), takeover)
	if err != nil || executed.Triggered != 1 || executed.Succeeded != 1 {
		t.Fatalf("takeover execution = %#v, %v", executed, err)
	}
	var effects, history int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_runtime_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_executions`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if effects != 2 || history != 2 {
		t.Fatalf("runtime effects=%d history=%d", effects, history)
	}
}

func insertDue(t *testing.T, db *database.Database, value string, now time.Time) string {
	t.Helper()
	schedule, err := json.Marshal(scheduler.Schedule{Cron: "* * * * *", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := json.Marshal(runtimeParameters{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := db.Bun().ExecContext(context.Background(), `INSERT INTO scheduler_definitions(id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, value, value, "runtime.effect", schedule, parameters, true, 1, now, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func integers(minimum, maximum int) []int {
	result := make([]int, maximum-minimum+1)
	for index := range result {
		result[index] = minimum + index
	}
	return result
}
