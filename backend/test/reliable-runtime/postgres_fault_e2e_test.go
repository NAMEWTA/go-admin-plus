package reliableruntime_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

const (
	postgresE2EDSNEnv   = "GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN"
	postgresWorkerRole  = "GO_ADMIN_TEST_POSTGRES_WORKER_ROLE"
	postgresWorkerTable = "GO_ADMIN_TEST_POSTGRES_WORKER_TABLE"

	workerHolder   = "holder"
	workerTakeover = "takeover"
	workerRetry    = "retry"
	workerReplay   = "replay"

	statusLeaseLost = "T08_LEASE_LOST"
	statusDelivered = "T08_DELIVERED"
	statusRetried   = "T08_RETRIED"
	statusReplayed  = "T08_REPLAY_SUPPRESSED"
)

var postgresE2ETablePattern = regexp.MustCompile(`^e2e_(?:effects|gate|block|receipt)_[a-z0-9]+$`)

// SQLite cross-process exclusion is deliberately delegated to the T-04 Database instance-lock
// contract. This harness covers the T-08-owned PostgreSQL advisory executor and outbox recovery.
func TestPostgresDispatcherRecoversAcrossBackendTerminationAndProcesses(t *testing.T) {
	dsn := os.Getenv(postgresE2EDSNEnv)
	if dsn == "" {
		t.Skip("set GO_ADMIN_TEST_POSTGRES_DISPOSABLE_DSN to run the PostgreSQL fault E2E")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := openReliablePostgres(t, ctx, dsn)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	table := "e2e_effects_" + suffix
	gateTable := "e2e_gate_" + suffix
	blockFunction := "e2e_block_" + suffix
	receiptTrigger := "e2e_receipt_" + suffix
	if !postgresE2ETablePattern.MatchString(table) || !postgresE2ETablePattern.MatchString(gateTable) ||
		!postgresE2ETablePattern.MatchString(blockFunction) || !postgresE2ETablePattern.MatchString(receiptTrigger) {
		t.Fatal("generated PostgreSQL E2E table is invalid")
	}
	initialID := "e2e-initial-" + suffix
	replayID := "e2e-replay-" + suffix
	businessKey := "order:e2e-" + suffix

	resetPostgresOutboxFixture(t, ctx, db)
	t.Cleanup(func() {
		cleanupPostgresFaultFixture(
			db, table, gateTable, blockFunction, receiptTrigger, initialID, replayID, businessKey,
		)
	})
	if _, err := db.SQL().ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s (business_key text PRIMARY KEY, payload bytea NOT NULL)`, table,
	)); err != nil {
		t.Fatal("create PostgreSQL E2E effect table failed")
	}
	if _, err := db.SQL().ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s (id integer PRIMARY KEY)`, gateTable)); err != nil {
		t.Fatal("create PostgreSQL E2E gate table failed")
	}
	if _, err := db.SQL().ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			PERFORM 1 FROM %s;
			RETURN NEW;
		END
		$body$`, blockFunction, gateTable)); err != nil {
		t.Fatal("create PostgreSQL E2E blocker function failed")
	}
	if _, err := db.SQL().ExecContext(ctx, fmt.Sprintf(`
		CREATE TRIGGER %s AFTER INSERT ON reliable_consumer_receipt
		FOR EACH ROW WHEN (NEW.business_key = '%s') EXECUTE FUNCTION %s()`,
		receiptTrigger, businessKey, blockFunction,
	)); err != nil {
		t.Fatal("create PostgreSQL E2E receipt trigger failed")
	}
	store := newPostgresFaultStore(t, db)
	initial := reliableEvent(initialID, businessKey, time.Now().UTC().Truncate(time.Microsecond))
	enqueueEvent(t, db, store, initial)

	lockTx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("begin PostgreSQL E2E blocker failed")
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = lockTx.Rollback()
		}
	}()
	if _, err := lockTx.ExecContext(ctx, fmt.Sprintf(`LOCK TABLE %s IN ACCESS EXCLUSIVE MODE`, gateTable)); err != nil {
		t.Fatal("lock PostgreSQL E2E gate table failed")
	}

	holder := startPostgresFaultWorker(t, ctx, workerHolder, table)
	backendPID := waitForPostgresHolderBackend(t, ctx, db, holder)
	var terminated bool
	if err := db.SQL().QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, backendPID).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate PostgreSQL holder backend failed")
	}
	if err := lockTx.Rollback(); err != nil {
		t.Fatal("release PostgreSQL E2E blocker failed")
	}
	lockHeld = false
	if !holder.waitForStatus(15*time.Second, statusLeaseLost) {
		t.Fatal("holder did not stop with lease-lost status")
	}

	assertPostgresOutboxState(t, ctx, db, initialID, outbox.StateClaimed, 1)
	assertPostgresEffectAndReceipt(t, ctx, db, table, businessKey, initialID, 0, 0)
	if !waitForPostgresCondition(ctx, db, `
		SELECT claim_until <= clock_timestamp()
		FROM reliable_outbox WHERE id = $1`, initialID) {
		t.Fatal("holder claim did not reach database expiry")
	}

	if worker := startPostgresFaultWorker(t, ctx, workerTakeover, table); !worker.waitForStatus(15*time.Second, statusDelivered) {
		t.Fatal("takeover worker did not deliver the recovered event")
	}
	assertPostgresOutboxState(t, ctx, db, initialID, outbox.StateDelivered, 2)
	assertPostgresEffectAndReceipt(t, ctx, db, table, businessKey, initialID, 1, 1)

	replay := reliableEvent(replayID, businessKey, time.Now().UTC().Truncate(time.Microsecond))
	replay.Topic = "orders.replayed"
	enqueueEvent(t, db, store, replay)
	if worker := startPostgresFaultWorker(t, ctx, workerRetry, table); !worker.waitForStatus(15*time.Second, statusRetried) {
		t.Fatal("retry worker did not persist a retry")
	}
	assertPostgresOutboxState(t, ctx, db, replayID, outbox.StateRetry, 1)
	if !waitForPostgresCondition(ctx, db, `
		SELECT available_at <= clock_timestamp()
		FROM reliable_outbox WHERE id = $1`, replayID) {
		t.Fatal("replay retry did not become available")
	}
	if worker := startPostgresFaultWorker(t, ctx, workerReplay, table); !worker.waitForStatus(15*time.Second, statusReplayed) {
		t.Fatal("replay worker did not suppress the duplicate effect")
	}
	assertPostgresOutboxState(t, ctx, db, replayID, outbox.StateDelivered, 2)
	assertPostgresEffectAndReceipt(t, ctx, db, table, businessKey, initialID, 1, 1)
}

func TestPostgresFaultWorker(t *testing.T) {
	role := os.Getenv(postgresWorkerRole)
	if role == "" {
		t.Skip("PostgreSQL fault worker is started only by its parent E2E")
	}
	dsn := os.Getenv(postgresE2EDSNEnv)
	table := os.Getenv(postgresWorkerTable)
	if dsn == "" || !postgresE2ETablePattern.MatchString(table) {
		t.Fatal("PostgreSQL fault worker configuration is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := openReliablePostgres(t, ctx, dsn)
	store := newPostgresFaultStore(t, db)
	lease, err := coordination.Acquire(ctx, db, coordination.Config{Owner: "pg-e2e-worker"})
	if err != nil {
		t.Fatal("PostgreSQL fault worker lease acquisition failed")
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		_ = lease.Close(closeCtx)
	})

	consumers := map[string]outbox.TransactionalConsumer(nil)
	if role != workerRetry {
		consumer, err := postgresFaultConsumer(table)
		if err != nil {
			t.Fatal("PostgreSQL fault worker consumer setup failed")
		}
		topic := "orders.changed"
		if role == workerReplay {
			topic = "orders.replayed"
		}
		consumers = map[string]outbox.TransactionalConsumer{topic: consumer}
	}
	dispatcher, err := outbox.NewDispatcher(store, outbox.DispatcherConfig{
		Owner: "pg-e2e-worker", LeaseDuration: 3 * time.Second, RetryDelay: 25 * time.Millisecond,
		BatchSize: 1, Now: func() time.Time { return time.Now().UTC() },
	}, consumers)
	if err != nil {
		t.Fatal("PostgreSQL fault worker dispatcher setup failed")
	}
	result, runErr := dispatcher.RunOnce(ctx, lease, time.Now().UTC())
	switch role {
	case workerHolder:
		if !errors.Is(runErr, coordination.ErrLeaseLost) || result.Claimed != 1 {
			t.Fatal("PostgreSQL holder did not fail closed")
		}
		fmt.Fprintln(os.Stdout, statusLeaseLost)
	case workerTakeover:
		if runErr != nil || result.Claimed != 1 || result.Delivered != 1 {
			t.Fatal("PostgreSQL takeover result is invalid")
		}
		fmt.Fprintln(os.Stdout, statusDelivered)
	case workerRetry:
		if runErr != nil || result.Claimed != 1 || result.Retried != 1 {
			t.Fatal("PostgreSQL retry result is invalid")
		}
		fmt.Fprintln(os.Stdout, statusRetried)
	case workerReplay:
		if runErr != nil || result.Claimed != 1 || result.Replayed != 1 {
			t.Fatal("PostgreSQL replay result is invalid")
		}
		fmt.Fprintln(os.Stdout, statusReplayed)
	default:
		t.Fatal("PostgreSQL fault worker role is invalid")
	}
}

type postgresFaultWorker struct {
	command  *exec.Cmd
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	done     chan error
	finished bool
	result   error
}

func startPostgresFaultWorker(t *testing.T, ctx context.Context, role, table string) *postgresFaultWorker {
	t.Helper()
	worker := &postgresFaultWorker{done: make(chan error, 1)}
	worker.command = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPostgresFaultWorker$", "-test.count=1")
	worker.command.Env = append(os.Environ(), postgresWorkerRole+"="+role, postgresWorkerTable+"="+table)
	worker.command.Stdout = &worker.stdout
	worker.command.Stderr = &worker.stderr
	if err := worker.command.Start(); err != nil {
		t.Fatal("start PostgreSQL fault worker failed")
	}
	go func() { worker.done <- worker.command.Wait() }()
	return worker
}

func (worker *postgresFaultWorker) pollExited() bool {
	if worker.finished {
		return true
	}
	select {
	case worker.result = <-worker.done:
		worker.finished = true
		return true
	default:
		return false
	}
}

func (worker *postgresFaultWorker) waitForStatus(timeout time.Duration, status string) bool {
	if !worker.finished {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case worker.result = <-worker.done:
			worker.finished = true
		case <-timer.C:
			_ = worker.command.Process.Kill()
			worker.result = <-worker.done
			worker.finished = true
			return false
		}
	}
	return worker.result == nil && strings.Contains(worker.stdout.String(), status+"\n")
}

func waitForPostgresHolderBackend(
	t *testing.T,
	ctx context.Context,
	db *database.Database,
	worker *postgresFaultWorker,
) int {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := db.SQL().QueryRowContext(ctx, `
			SELECT activity.pid
			FROM pg_stat_activity AS activity
			WHERE activity.wait_event_type = 'Lock'
			  AND activity.query LIKE $1
			  AND EXISTS (
			      SELECT 1 FROM pg_locks AS held
			      WHERE held.pid = activity.pid AND held.locktype = 'advisory' AND held.granted
			  )
			ORDER BY activity.query_start DESC
			LIMIT 1`, "%INSERT INTO reliable_consumer_receipt%").Scan(&pid)
		if err == nil {
			return pid
		}
		if worker.pollExited() {
			t.Fatal("holder exited before reaching the blocked consumer transaction")
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out locating PostgreSQL holder backend")
		case <-ticker.C:
		}
	}
}

func newPostgresFaultStore(t *testing.T, db *database.Database) *outbox.Store {
	t.Helper()
	store, err := outbox.NewStore(db,
		outbox.TopicSchema{
			Topic:       "orders.changed",
			Payload:     []outbox.PayloadFieldSchema{{Name: "revision", Kind: outbox.PayloadNumber, Required: true}},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 2},
		},
		outbox.TopicSchema{
			Topic:       "orders.replayed",
			Payload:     []outbox.PayloadFieldSchema{{Name: "revision", Kind: outbox.PayloadNumber, Required: true}},
			BusinessKey: outbox.BusinessKeySchema{Prefix: "order", MinParts: 1, MaxParts: 2},
		},
	)
	if err != nil {
		t.Fatal("create PostgreSQL fault store failed")
	}
	return store
}

func postgresFaultConsumer(table string) (outbox.TransactionalConsumer, error) {
	return outbox.NewTransactionalConsumer("e2e", "projector", []string{table}, outbox.Mutation{
		Operation: outbox.OperationInsert, Table: table,
		Values: []outbox.ColumnBinding{
			{Column: "business_key", Field: outbox.FieldBusinessKey},
			{Column: "payload", Field: outbox.FieldPayload},
		},
		ExpectExactly: 1,
	})
}

func resetPostgresOutboxFixture(t *testing.T, ctx context.Context, db *database.Database) {
	t.Helper()
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM reliable_consumer_receipt`); err != nil {
		t.Fatal("reset PostgreSQL receipts failed")
	}
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM reliable_outbox`); err != nil {
		t.Fatal("reset PostgreSQL outbox failed")
	}
}

func cleanupPostgresFaultFixture(
	db *database.Database,
	table, gateTable, blockFunction, receiptTrigger, initialID, replayID, businessKey string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = db.SQL().ExecContext(ctx, `DELETE FROM reliable_consumer_receipt WHERE business_key = $1`, businessKey)
	_, _ = db.SQL().ExecContext(ctx, `DELETE FROM reliable_outbox WHERE id IN ($1, $2)`, initialID, replayID)
	_, _ = db.SQL().ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON reliable_consumer_receipt`, receiptTrigger))
	_, _ = db.SQL().ExecContext(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, blockFunction))
	_, _ = db.SQL().ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, table))
	_, _ = db.SQL().ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, gateTable))
}

func assertPostgresOutboxState(
	t *testing.T,
	ctx context.Context,
	db *database.Database,
	eventID string,
	wantState outbox.State,
	wantAttempts int,
) {
	t.Helper()
	var state outbox.State
	var attempts int
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT state, attempts FROM reliable_outbox WHERE id = $1`, eventID,
	).Scan(&state, &attempts); err != nil || state != wantState || attempts != wantAttempts {
		t.Fatal("PostgreSQL outbox state assertion failed")
	}
}

func assertPostgresEffectAndReceipt(
	t *testing.T,
	ctx context.Context,
	db *database.Database,
	table, businessKey, receiptEventID string,
	wantEffects, wantReceipts int,
) {
	t.Helper()
	var effects int
	if err := db.SQL().QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE business_key = $1`, table,
	), businessKey).Scan(&effects); err != nil || effects != wantEffects {
		t.Fatal("PostgreSQL effect assertion failed")
	}
	var receipts int
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM reliable_consumer_receipt
		WHERE consumer_name = 'projector' AND business_key = $1 AND event_id = $2`,
		businessKey, receiptEventID,
	).Scan(&receipts); err != nil || receipts != wantReceipts {
		t.Fatal("PostgreSQL receipt assertion failed")
	}
}

func waitForPostgresCondition(ctx context.Context, db *database.Database, query string, argument string) bool {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var ready bool
		if err := db.SQL().QueryRowContext(ctx, query, argument).Scan(&ready); err == nil && ready {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
