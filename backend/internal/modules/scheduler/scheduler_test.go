package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
	schedulermigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/migrations"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
	reliablemigration "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations/reliable-runtime"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

type testParameters struct {
	Message string `json:"message"`
	Count   int64  `json:"count"`
	Enabled bool   `json:"enabled"`
}

type authorizerStub struct {
	mu          sync.Mutex
	scope       Scope
	err         error
	permissions []string
}

type loseBeforeCommit struct{ db *database.Database }

func (value loseBeforeCommit) WithinTx(ctx context.Context, callback func(context.Context, database.Tx) error) error {
	return value.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := callback(ctx, tx); err != nil {
			return err
		}
		return coordination.ErrLeaseLost
	})
}

type capabilityRegistrar struct {
	value capabilities.ModuleCapabilities
}

func (registrar *capabilityRegistrar) Register(_ context.Context, value capabilities.ModuleCapabilities) error {
	registrar.value = value
	return nil
}

func TestSchedulerDeclaresCapabilitiesAndRequiresExplicitAuthorization(t *testing.T) {
	registrar := &capabilityRegistrar{}
	if err := RegisterCapabilities(context.Background(), registrar); err != nil {
		t.Fatal(err)
	}
	want := []string{PermissionDefinitionsRead, PermissionDefinitionsWrite, PermissionDefinitionsDelete, PermissionExecutionsRead}
	if len(registrar.value.Permissions) != len(want) || len(registrar.value.Menus) != 2 {
		t.Fatalf("capabilities = %#v", registrar.value)
	}
	for index, code := range want {
		if registrar.value.Permissions[index].Code != code || registrar.value.Permissions[index].Name == "" {
			t.Fatalf("permission[%d] = %#v", index, registrar.value.Permissions[index])
		}
	}
	db := schedulerDatabase(t)
	authorizer := &authorizerStub{scope: ScopeAll}
	registry := schedulerRegistry(t, func(context.Context, database.Tx, testParameters) error { return nil })
	clock := ClockFunc(func() time.Time { return time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC) })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	if service.authorizer != authorizer {
		t.Fatalf("service lost injected authorizer: %T", service.authorizer)
	}
	if _, err := NewService(db, nil, registry, clock); err == nil {
		t.Fatal("nil authorizer accepted")
	}
}

func (stub *authorizerStub) RequireInTx(_ context.Context, _ database.Tx, _, permission string) (AuthorizationDecision, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.permissions = append(stub.permissions, permission)
	return AuthorizationDecision{Scope: stub.scope}, stub.err
}

func TestRegistryIsCompileTimeTypedStrictAndImmutable(t *testing.T) {
	minimum, maximum := int64(1), int64(5)
	registration, err := NewTaskRegistration("test.cleanup", "Cleanup", []ParameterField{
		{Name: "message", Label: "Message", Kind: ParameterString, Required: true, AllowedValues: []string{"keep", "remove"}},
		{Name: "count", Label: "Count", Kind: ParameterInteger, Required: true, Minimum: &minimum, Maximum: &maximum},
		{Name: "enabled", Label: "Enabled", Kind: ParameterBoolean, Required: true},
	}, func(context.Context, database.Tx, testParameters) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(registration)
	if err != nil {
		t.Fatal(err)
	}
	valid := []byte(`{"message":"keep","count":2,"enabled":true}`)
	if _, err := registry.normalize("test.cleanup", valid); err != nil {
		t.Fatalf("valid parameters: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`null`),
		[]byte(`{"message":"keep","count":2}`),
		[]byte(`{"message":"keep","count":2,"enabled":true,"extra":false}`),
		[]byte(`{"message":"keep","message":"remove","count":2,"enabled":true}`),
		[]byte(`{"message":"other","count":2,"enabled":true}`),
		[]byte(`{"message":"keep","count":9,"enabled":true}`),
		[]byte(`{"message":"keep","count":9007199254740992,"enabled":true}`),
		[]byte(`{"message":"keep","count":2,"enabled":true} {}`),
	} {
		if _, err := registry.normalize("test.cleanup", invalid); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid parameters %s: %v", invalid, err)
		}
	}
	descriptors := registry.TaskTypes()
	descriptors[0].Fields[0].Name = "mutated"
	if got := registry.TaskTypes()[0].Fields[0].Name; got != "message" {
		t.Fatalf("registry descriptor mutated: %s", got)
	}
	if _, err := NewTaskRegistration("test.secret", "Secret", []ParameterField{{Name: "token", Label: "Token", Kind: ParameterString, Required: true}}, func(context.Context, database.Tx, struct {
		Token string `json:"token"`
	}) error {
		return nil
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("sensitive parameter registration: %v", err)
	}
}

func TestScheduleNormalizesTimezoneAndCoalescesMissedOccurrences(t *testing.T) {
	schedule, ok := normalizeSchedule(Schedule{Cron: " 0,30  1 * 1,12 * ", Timezone: "Asia/Shanghai"})
	if !ok || schedule.Cron != "0,30 1 * 1,12 *" {
		t.Fatalf("normalized schedule: %#v %v", schedule, ok)
	}
	after := time.Date(2026, 1, 1, 17, 30, 0, 0, time.UTC)
	next, ok := nextOccurrence(schedule, after)
	if !ok || !next.Equal(time.Date(2026, 1, 2, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("next: %s %v", next, ok)
	}
	for _, value := range []Schedule{{Cron: "61 * * * *", Timezone: "UTC"}, {Cron: "* * * * *", Timezone: "invalid/zone"}, {Cron: "@every 1s", Timezone: "UTC"}} {
		if _, ok := normalizeSchedule(value); ok {
			t.Fatalf("invalid schedule accepted: %#v", value)
		}
	}
	if _, ok := nextOccurrence(schedule, after.In(time.FixedZone("unsafe", 3600))); ok {
		t.Fatal("non-UTC clock accepted")
	}
	// 夏令时跳过不存在的 02:30，本地日期继续前进。
	next, ok = nextOccurrence(Schedule{Cron: "30 2 * * *", Timezone: "America/New_York"}, time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC))
	if !ok || !next.Equal(time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("DST next: %s %v", next, ok)
	}
}

func TestServiceLifecycleAuthorizationRevisionAndLiteralSearch(t *testing.T) {
	db := schedulerDatabase(t)
	registry := schedulerRegistry(t, func(context.Context, database.Tx, testParameters) error { return nil })
	authorizer := &authorizerStub{scope: ScopeAll}
	now := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	service, err := NewService(db, authorizer, registry, ClockFunc(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	input := definitionInput("Percent % underscore _ Unicode 世界")
	created, err := service.CreateDefinition(context.Background(), "actor", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Enabled || created.Revision != 1 || created.NextRunAt != nil {
		t.Fatalf("created = %#v", created)
	}
	if _, err := service.CreateDefinition(context.Background(), "actor", input); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	for _, search := range []string{"%", "_", "世界"} {
		page, err := service.ListDefinitions(context.Background(), "actor", DefinitionQuery{Search: search, Page: 1, PageSize: 20})
		if err != nil || page.Total != 1 {
			t.Fatalf("literal search %q = %#v, %v", search, page, err)
		}
	}
	if _, err := service.UpdateDefinition(context.Background(), "actor", created.ID, 99, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	enabled, err := service.EnableDefinition(context.Background(), "actor", created.ID, created.Revision)
	if err != nil || !enabled.Enabled || enabled.Revision != 2 || enabled.NextRunAt == nil {
		t.Fatalf("enabled = %#v, %v", enabled, err)
	}
	if _, err := service.UpdateDefinition(context.Background(), "actor", created.ID, enabled.Revision, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("enabled update = %v", err)
	}
	stopped, err := service.StopDefinition(context.Background(), "actor", created.ID, enabled.Revision)
	if err != nil || stopped.Enabled || stopped.Revision != 3 || stopped.NextRunAt != nil {
		t.Fatalf("stopped = %#v, %v", stopped, err)
	}
	if err := service.DeleteDefinition(context.Background(), "actor", created.ID, stopped.Revision); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteDefinition(context.Background(), "actor", created.ID, stopped.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted again = %v", err)
	}
	recreated, err := service.CreateDefinition(context.Background(), "actor", input)
	if err != nil || recreated.ID == created.ID || recreated.Name != created.Name {
		t.Fatalf("recreated = %#v, %v", recreated, err)
	}
	authorizer.scope = ScopeSelf
	if _, err := service.ListDefinitions(context.Background(), "actor", DefinitionQuery{Page: 1, PageSize: 20}); !errors.Is(err, ErrDenied) {
		t.Fatalf("self scope = %v", err)
	}
}

func TestExecutorRollsBackAndRecordsTimeoutWhenHandlerIgnoresContext(t *testing.T) {
	db := schedulerDatabase(t)
	if _, err := db.SQL().Exec(`CREATE TABLE scheduler_test_effects(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := schedulerRegistry(t, func(ctx context.Context, tx database.Tx, parameters testParameters) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduler_test_effects(value) VALUES (?)`, parameters.Message); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	})
	authorizer := &authorizerStub{scope: ScopeAll}
	current := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	clock := ClockFunc(func() time.Time { return current })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	definition := createEnabled(t, service, "timeout", "must-rollback")
	current = time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "timeout-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "timeout-owner", BatchSize: 1, TaskTimeout: 10 * time.Millisecond, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunOnce(context.Background(), lease)
	if err != nil || result != (ExecuteResult{Triggered: 1, Failed: 1}) {
		t.Fatalf("timeout result = %#v, %v", result, err)
	}
	var effects int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_test_effects`).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("timeout effects = %d, %v", effects, err)
	}
	page, err := service.ListExecutions(context.Background(), "actor", ExecutionQuery{DefinitionID: definition.ID, Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || page.Rows[0].Status != ExecutionFailed || page.Rows[0].ErrorCode != "task_timeout" {
		t.Fatalf("timeout execution = %#v, %v", page, err)
	}
}

func TestExecutorCoalescesOccurrencesMissedWhileTaskRuns(t *testing.T) {
	db := schedulerDatabase(t)
	current := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	invocations := 0
	registry := schedulerRegistry(t, func(context.Context, database.Tx, testParameters) error {
		invocations++
		current = current.Add(5 * time.Minute)
		return nil
	})
	authorizer := &authorizerStub{scope: ScopeAll}
	clock := ClockFunc(func() time.Time { return current })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	definition := createEnabled(t, service, "long-running", "advance-clock")
	current = time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "long-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "long-owner", BatchSize: 10, TaskTimeout: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunOnce(context.Background(), lease)
	if err != nil || result != (ExecuteResult{Triggered: 1, Succeeded: 1}) || invocations != 1 {
		t.Fatalf("long task result = %#v invocations=%d, %v", result, invocations, err)
	}
	page, err := service.ListDefinitions(context.Background(), "actor", DefinitionQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || page.Rows[0].ID != definition.ID || page.Rows[0].NextRunAt == nil || !page.Rows[0].NextRunAt.Equal(time.Date(2026, 8, 27, 10, 7, 0, 0, time.UTC)) {
		t.Fatalf("long task definition = %#v, %v", page, err)
	}
}

func TestExecutorUsesInjectedLeaseCoalescesAndRollsBackFailedTaskEffects(t *testing.T) {
	db := schedulerDatabase(t)
	if _, err := db.SQL().Exec(`CREATE TABLE scheduler_test_effects(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := schedulerRegistry(t, func(ctx context.Context, tx database.Tx, parameters testParameters) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduler_test_effects(value) VALUES (?)`, parameters.Message); err != nil {
			return err
		}
		if parameters.Message == "fail" {
			return NewTaskFailure("expected_failure")
		}
		return nil
	})
	authorizer := &authorizerStub{scope: ScopeAll}
	current := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	clock := ClockFunc(func() time.Time { return current })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	success := createEnabled(t, service, "success", "ok")
	failure := createEnabled(t, service, "failure", "fail")
	current = time.Date(2026, 8, 27, 12, 5, 0, 0, time.UTC)
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "test-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "test-owner", BatchSize: 10, TaskTimeout: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunOnce(context.Background(), lease)
	if err != nil || result != (ExecuteResult{Triggered: 2, Succeeded: 1, Failed: 1}) {
		t.Fatalf("result = %#v, %v", result, err)
	}
	var effects []string
	rows, err := db.SQL().Query(`SELECT value FROM scheduler_test_effects ORDER BY value`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		effects = append(effects, value)
	}
	_ = rows.Close()
	if !reflect.DeepEqual(effects, []string{"ok"}) {
		t.Fatalf("effects = %v", effects)
	}
	page, err := service.ListExecutions(context.Background(), "actor", ExecutionQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 2 {
		t.Fatalf("executions = %#v, %v", page, err)
	}
	codes := map[string]string{}
	for _, value := range page.Rows {
		codes[value.DefinitionID] = value.ErrorCode
	}
	if codes[success.ID] != "" || codes[failure.ID] != "expected_failure" {
		t.Fatalf("execution codes = %#v", codes)
	}
	definitions, err := service.ListDefinitions(context.Background(), "actor", DefinitionQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions.Rows {
		want := time.Date(2026, 8, 27, 12, 6, 0, 0, time.UTC)
		if definition.NextRunAt == nil || !definition.NextRunAt.Equal(want) {
			t.Fatalf("coalesced next = %#v", definition.NextRunAt)
		}
	}
	wrongOwner, err := NewExecutor(db, registry, ExecutorConfig{Owner: "other-owner", BatchSize: 1, TaskTimeout: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongOwner.RunOnce(context.Background(), lease); !errors.Is(err, coordination.ErrLeaseMismatch) {
		t.Fatalf("owner mismatch = %v", err)
	}
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.RunOnce(context.Background(), lease); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatalf("closed lease = %v", err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_executions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lease-loss executions = %d, %v", count, err)
	}
}

func TestLeaseLossBeforeCommitRollsBackTaskEffectAndExecutionRecord(t *testing.T) {
	db := schedulerDatabase(t)
	if _, err := db.SQL().Exec(`CREATE TABLE scheduler_test_effects(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := schedulerRegistry(t, func(ctx context.Context, tx database.Tx, parameters testParameters) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO scheduler_test_effects(value) VALUES (?)`, parameters.Message)
		return err
	})
	authorizer := &authorizerStub{scope: ScopeAll}
	current := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	clock := ClockFunc(func() time.Time { return current })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	definition := createEnabled(t, service, "lease-loss", "must-rollback")
	originalNext := *definition.NextRunAt
	current = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "loss-owner", BatchSize: 1, TaskTimeout: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.runOnce(context.Background(), loseBeforeCommit{db: db}); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatalf("lease loss = %v", err)
	}
	var effects, executions int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_test_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_executions`).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	if err := db.SQL().QueryRow(`SELECT next_run_at FROM scheduler_definitions WHERE id = ?`, definition.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || executions != 0 || !next.Equal(originalNext) {
		t.Fatalf("lease-loss state effects=%d executions=%d next=%s", effects, executions, next)
	}
}

func TestTaskExternalEffectUsesSameTransactionOutbox(t *testing.T) {
	db := schedulerOutboxDatabase(t)
	store, err := outbox.NewStore(db, outbox.TopicSchema{Topic: "scheduler.test", Payload: []outbox.PayloadFieldSchema{{Name: "outcome", Kind: outbox.PayloadString, Required: true, AllowedStrings: []string{"ok", "fail"}}}, BusinessKey: outbox.BusinessKeySchema{Prefix: "scheduler", MinParts: 1, MaxParts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	registry := schedulerRegistry(t, func(ctx context.Context, tx database.Tx, parameters testParameters) error {
		_, err := store.Enqueue(ctx, tx, outbox.Event{ID: uuid.NewString(), Topic: "scheduler.test", BusinessKey: "scheduler:" + parameters.Message, Payload: []byte(`{"outcome":"` + parameters.Message + `"}`), OccurredAt: now})
		if err != nil {
			return err
		}
		if parameters.Message == "fail" {
			return NewTaskFailure("expected_failure")
		}
		return nil
	})
	authorizer := &authorizerStub{scope: ScopeAll}
	current := now
	clock := ClockFunc(func() time.Time { return current })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	createEnabled(t, service, "outbox-ok", "ok")
	createEnabled(t, service, "outbox-fail", "fail")
	current = time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "outbox-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "outbox-owner", BatchSize: 2, TaskTimeout: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunOnce(context.Background(), lease)
	if err != nil || result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("execution = %#v, %v", result, err)
	}
	var events, executions int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM reliable_outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scheduler_executions`).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if events != 1 || executions != 2 {
		t.Fatalf("outbox events=%d executions=%d", events, executions)
	}
}

func TestStopWaitsForCurrentTaskTransactionAndPreventsAnotherExecution(t *testing.T) {
	db := schedulerDatabase(t)
	started := make(chan struct{})
	release := make(chan struct{})
	registry := schedulerRegistry(t, func(ctx context.Context, _ database.Tx, _ testParameters) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	authorizer := &authorizerStub{scope: ScopeAll}
	current := time.Date(2026, 8, 27, 10, 0, 30, 0, time.UTC)
	clock := ClockFunc(func() time.Time { return current })
	service, err := NewService(db, authorizer, registry, clock)
	if err != nil {
		t.Fatal(err)
	}
	definition := createEnabled(t, service, "blocking", "block")
	current = time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)
	lease, err := coordination.Acquire(context.Background(), db, coordination.Config{Owner: "linear-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "linear-owner", BatchSize: 1, TaskTimeout: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { _, err := executor.RunOnce(context.Background(), lease); runDone <- err }()
	<-started
	stopDone := make(chan error, 1)
	go func() {
		_, err := service.StopDefinition(context.Background(), "actor", definition.ID, definition.Revision)
		stopDone <- err
	}()
	select {
	case err := <-stopDone:
		t.Fatalf("stop returned before task transaction: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunOnce(context.Background(), lease)
	if err != nil || result.Triggered != 0 {
		t.Fatalf("post-stop run = %#v, %v", result, err)
	}
}

func schedulerRegistry(t *testing.T, handler TaskHandler[testParameters]) *Registry {
	t.Helper()
	registration, err := NewTaskRegistration("test.effect", "Test effect", []ParameterField{{Name: "message", Label: "Message", Kind: ParameterString, Required: true}, {Name: "count", Label: "Count", Kind: ParameterInteger, Required: true}, {Name: "enabled", Label: "Enabled", Kind: ParameterBoolean, Required: true}}, handler)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(registration)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func definitionInput(name string) DefinitionInput {
	return DefinitionInput{Name: name, TaskType: "test.effect", Schedule: Schedule{Cron: "* * * * *", Timezone: "UTC"}, Parameters: map[string]any{"message": name, "count": 1, "enabled": true}}
}
func createEnabled(t *testing.T, service *Service, name, message string) Definition {
	t.Helper()
	input := definitionInput(name)
	input.Parameters["message"] = message
	created, err := service.CreateDefinition(context.Background(), "actor", input)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := service.EnableDefinition(context.Background(), "actor", created.ID, created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return enabled
}
func allMinutes() []int {
	result := make([]int, 60)
	for i := range result {
		result[i] = i
	}
	return result
}
func allHours() []int {
	result := make([]int, 24)
	for i := range result {
		result[i] = i
	}
	return result
}
func allMonths() []int {
	result := make([]int, 12)
	for i := range result {
		result[i] = i + 1
	}
	return result
}

func schedulerDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "scheduler.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(schedulermigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Up(context.Background(), db)
	if err != nil || first.Applied != 1 {
		t.Fatalf("migration = %#v, %v", first, err)
	}
	second, err := runner.Up(context.Background(), db)
	if err != nil || second.Applied != 0 {
		t.Fatalf("idempotent migration = %#v, %v", second, err)
	}
	return db
}

func schedulerOutboxDatabase(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.NewProcess().Open(context.Background(), database.Config{Profile: config.ProfileServerSQLite, SQLitePath: filepath.Join(t.TempDir(), "scheduler-outbox.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(reliablemigration.Provider{}, schedulermigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := runner.Up(context.Background(), db); err != nil || result.Applied != 2 {
		t.Fatalf("combined migration = %#v, %v", result, err)
	}
	return db
}

func TestManualRunKeepsExecutionIdentityAcrossExpiredClaim(t *testing.T) {
	db := schedulerDatabase(t)
	current := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	var observed string
	registry := schedulerRegistry(t, func(ctx context.Context, _ database.Tx, _ testParameters) error {
		observed = ExecutionID(ctx)
		return nil
	})
	service, err := NewService(db, &authorizerStub{scope: ScopeAll}, registry, ClockFunc(func() time.Time { return current }))
	if err != nil {
		t.Fatal(err)
	}
	definition := createEnabled(t, service, "manual-recovery", "idempotency")
	id, err := service.RunDefinition(context.Background(), "actor", definition.ID, definition.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunDefinition(context.Background(), "actor", definition.ID, definition.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping execution accepted: %v", err)
	}
	if _, err := db.SQL().Exec(`UPDATE scheduler_runs SET state='running',claim_token='dead-process',lease_until=?,attempts=1 WHERE id=?`, current.Add(-time.Second), id); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(db, registry, ExecutorConfig{Owner: "replacement-worker", BatchSize: 1, TaskTimeout: time.Second, Clock: ClockFunc(func() time.Time { return current })})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunAvailable(context.Background())
	if err != nil || result.Succeeded != 1 || observed != id {
		t.Fatalf("recovery=%#v id=%q want=%q %v", result, observed, id, err)
	}
	var historyID string
	var attempts int
	if err := db.SQL().QueryRow(`SELECT id FROM scheduler_executions WHERE definition_id=?`, definition.ID).Scan(&historyID); err != nil || historyID != id {
		t.Fatalf("history identity %q %v", historyID, err)
	}
	if err := db.SQL().QueryRow(`SELECT attempts FROM scheduler_runs WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts %d %v", attempts, err)
	}
}
