package files

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

const AccountDeletionRequestedTopic = "iam.account-deletion.requested.v1"

type AccountDeletionStrategy string

const (
	AccountDeletionTransfer AccountDeletionStrategy = "transfer"
	AccountDeletionPurge    AccountDeletionStrategy = "purge"
)

type AccountDeletionClaim struct {
	AccountID, TransferTargetID string
	Strategy                    AccountDeletionStrategy
}

type AccountDeletionPort interface {
	ClaimAccountDeletion(context.Context, string) (string, string, string, error)
	FailAccountDeletion(context.Context, string, string) error
	CompleteAccountDeletion(context.Context, string) error
}

type AccountLifecycle struct {
	db      Database
	storage Storage
	iam     AccountDeletionPort
	policy  CapacityPolicy
	now     func() time.Time
}

type AccountLifecycleOption func(*AccountLifecycle)

func WithAccountLifecycleCapacityPolicy(policy CapacityPolicy) AccountLifecycleOption {
	return func(worker *AccountLifecycle) { worker.policy = policy }
}

func WithAccountLifecycleClock(clock func() time.Time) AccountLifecycleOption {
	return func(worker *AccountLifecycle) { worker.now = clock }
}

func NewAccountLifecycle(db Database, storage Storage, iam AccountDeletionPort, options ...AccountLifecycleOption) (*AccountLifecycle, error) {
	if db == nil || storage == nil || iam == nil {
		return nil, errors.New("files account lifecycle dependencies are required")
	}
	worker := &AccountLifecycle{db: db, storage: storage, iam: iam, policy: DefaultCapacityPolicy(), now: time.Now}
	for _, option := range options {
		if option != nil {
			option(worker)
		}
	}
	if worker.now == nil || !worker.policy.valid() {
		return nil, errors.New("files account lifecycle options are invalid")
	}
	return worker, nil
}

func NewAccountDeletionRequestedConsumer() (outbox.TransactionalConsumer, error) {
	return outbox.NewTransactionalConsumer("files", "files-account-lifecycle", []string{"files_account_lifecycle_events"}, outbox.Mutation{
		Operation: outbox.OperationInsert,
		Table:     "files_account_lifecycle_events",
		Values: []outbox.ColumnBinding{
			{Column: "event_id", Field: outbox.FieldEventID},
			{Column: "business_key", Field: outbox.FieldBusinessKey},
			{Column: "payload", Field: outbox.FieldPayload},
			{Column: "occurred_at", Field: outbox.FieldOccurredAt},
		},
		ExpectExactly: 1,
	})
}

type accountLifecycleEvent struct {
	EventID, BusinessKey, ClaimToken string
	Payload                          []byte
	OccurredAt                       time.Time
	Attempts                         int
}

type accountLifecycleRequest struct {
	DeletionID, AccountID, TransferTargetID string
	Strategy                                AccountDeletionStrategy
}

func (worker *AccountLifecycle) RunOnce(ctx context.Context) error {
	if worker == nil {
		return ErrInternal
	}
	event, found, err := worker.claimEvent(ctx)
	if err != nil || !found {
		return worker.normalize(ctx, err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(ctx, lifecycleClaimKey{}, event.ClaimToken))
	defer cancel()
	go worker.renewClaim(ctx, cancel, event)
	request, err := parseAccountLifecycleEvent(event)
	if err != nil {
		return worker.fail(ctx, event.EventID, "invalid_event", "", err)
	}
	if event.Attempts > 3 {
		return worker.fail(ctx, event.EventID, "attempts_exhausted", request.DeletionID, ErrConflict)
	}
	accountID, strategy, transferTargetID, err := worker.iam.ClaimAccountDeletion(ctx, request.DeletionID)
	if err != nil {
		return worker.fail(ctx, event.EventID, "claim_failed", request.DeletionID, err)
	}
	if accountID == "" {
		return worker.finishEvent(ctx, event.EventID, "canceled")
	}
	if accountID != request.AccountID || AccountDeletionStrategy(strategy) != request.Strategy || transferTargetID != request.TransferTargetID {
		return worker.fail(ctx, event.EventID, "contract_mismatch", request.DeletionID, ErrConflict)
	}
	switch request.Strategy {
	case AccountDeletionTransfer:
		err = worker.transfer(ctx, request.AccountID, request.TransferTargetID)
	case AccountDeletionPurge:
		err = worker.purge(ctx, request.AccountID)
	default:
		err = ErrValidation
	}
	if err != nil {
		return worker.fail(ctx, event.EventID, "files_operation_failed", request.DeletionID, err)
	}
	if err := worker.iam.CompleteAccountDeletion(ctx, request.DeletionID); err != nil {
		return worker.fail(ctx, event.EventID, "completion_failed", request.DeletionID, err)
	}
	return worker.finishEvent(ctx, event.EventID, "completed")
}

func (worker *AccountLifecycle) claimEvent(ctx context.Context) (accountLifecycleEvent, bool, error) {
	var event accountLifecycleEvent
	found := false
	now := worker.now().UTC().Truncate(time.Microsecond)
	err := worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		query := `SELECT event_id, business_key, payload, occurred_at, attempts FROM files_account_lifecycle_events
			WHERE (state = 'queued' OR (state = 'failed' AND attempts < 3 AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (state = 'claimed' AND claim_expires_at <= ?)) ORDER BY occurred_at, event_id LIMIT 1`
		if worker.db.Dialect() == database.DialectPostgres {
			query += ` FOR UPDATE SKIP LOCKED`
		}
		err := tx.QueryRowContext(ctx, query, now, now).Scan(&event.EventID, &event.BusinessKey, &event.Payload, &event.OccurredAt, &event.Attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		event.ClaimToken = uuid.NewString()
		result, err := tx.ExecContext(ctx, `UPDATE files_account_lifecycle_events SET state = 'claimed', attempts = attempts + 1,
			claimed_at = ?, claim_token = ?, claim_expires_at = ?, last_error_code = NULL, updated_at = ?
			WHERE event_id = ?`, now, event.ClaimToken, now.Add(2*time.Minute), now, event.EventID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		found = changed == 1
		event.Attempts++
		return nil
	})
	return event, found, err
}

func parseAccountLifecycleEvent(event accountLifecycleEvent) (accountLifecycleRequest, error) {
	parts := strings.Split(event.BusinessKey, ":")
	if len(parts) != 4 || parts[0] != "account-deletion" || uuid.Validate(parts[1]) != nil || parts[2] == "" {
		return accountLifecycleRequest{}, ErrValidation
	}
	var payload struct {
		Strategy AccountDeletionStrategy `json:"strategy"`
		Version  int                     `json:"version"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload.Version != 1 {
		return accountLifecycleRequest{}, ErrValidation
	}
	request := accountLifecycleRequest{DeletionID: parts[1], AccountID: parts[2], Strategy: payload.Strategy}
	switch payload.Strategy {
	case AccountDeletionTransfer:
		if parts[3] == "none" || parts[3] == request.AccountID {
			return accountLifecycleRequest{}, ErrValidation
		}
		request.TransferTargetID = parts[3]
	case AccountDeletionPurge:
		if parts[3] != "none" {
			return accountLifecycleRequest{}, ErrValidation
		}
	default:
		return accountLifecycleRequest{}, ErrValidation
	}
	return request, nil
}

func (worker *AccountLifecycle) transfer(ctx context.Context, sourceID, targetID string) error {
	if sourceID == "" || targetID == "" || sourceID == targetID {
		return ErrValidation
	}
	now := worker.now().UTC().Truncate(time.Microsecond)
	return worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		for _, accountID := range []string{sourceID, targetID} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO files_capacity_counters(scope_kind, scope_id, reserved_bytes, reserved_objects)
				VALUES ('account', ?, 0, 0) ON CONFLICT (scope_kind, scope_id) DO NOTHING`, accountID); err != nil {
				return err
			}
		}
		if err := worker.lockCapacity(ctx, tx, sourceID, targetID); err != nil {
			return err
		}
		var bytes, objects, unsettled int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes), 0), COUNT(*),
			COALESCE(SUM(CASE WHEN state <> 'ready' THEN 1 ELSE 0 END), 0) FROM files_objects WHERE owner_account_id = ?`, sourceID).
			Scan(&bytes, &objects, &unsettled); err != nil {
			return err
		}
		if unsettled != 0 {
			return ErrConflict
		}
		var targetBytes, targetObjects int64
		if err := tx.QueryRowContext(ctx, `SELECT reserved_bytes, reserved_objects FROM files_capacity_counters
			WHERE scope_kind = 'account' AND scope_id = ?`, targetID).Scan(&targetBytes, &targetObjects); err != nil {
			return err
		}
		if targetBytes+bytes > worker.policy.MaximumAccountBytes || targetObjects+objects > worker.policy.MaximumAccountObjects {
			return ErrQuotaExceeded
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files_objects SET owner_account_id = ?, revision = revision + 1, updated_at = ?
			WHERE owner_account_id = ? AND state = 'ready'`, targetID, now, sourceID); err != nil {
			return err
		}
		sourceResult, err := tx.ExecContext(ctx, `UPDATE files_capacity_counters SET reserved_bytes = reserved_bytes - ?, reserved_objects = reserved_objects - ?
			WHERE scope_kind = 'account' AND scope_id = ? AND reserved_bytes >= ? AND reserved_objects >= ?`, bytes, objects, sourceID, bytes, objects)
		if err != nil {
			return err
		}
		if err := requireLifecycleRow(sourceResult); err != nil {
			return err
		}
		targetResult, err := tx.ExecContext(ctx, `UPDATE files_capacity_counters SET reserved_bytes = reserved_bytes + ?, reserved_objects = reserved_objects + ?
			WHERE scope_kind = 'account' AND scope_id = ?`, bytes, objects, targetID)
		if err != nil {
			return err
		}
		return requireLifecycleRow(targetResult)
	})
}

func requireLifecycleRow(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}

func (worker *AccountLifecycle) lockCapacity(ctx context.Context, tx database.Tx, sourceID, targetID string) error {
	if worker.db.Dialect() != database.DialectPostgres {
		return nil
	}
	ids := []string{sourceID, targetID}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	for _, key := range [][2]string{{"global", "global"}, {"account", ids[0]}, {"account", ids[1]}} {
		var locked int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM files_capacity_counters WHERE scope_kind = ? AND scope_id = ? FOR UPDATE`, key[0], key[1]).Scan(&locked); err != nil {
			return err
		}
	}
	return nil
}

func (worker *AccountLifecycle) purge(ctx context.Context, accountID string) error {
	for {
		record, found, err := worker.nextOwnedFile(ctx, accountID)
		if err != nil || !found {
			return err
		}
		if err := storageProvider(worker.storage, record.ProviderID).Delete(ctx, record.StorageKey); err != nil {
			return err
		}
		if record.TemporaryKey != nil {
			if err := storageProvider(worker.storage, record.ProviderID).Abort(ctx, *record.TemporaryKey); err != nil {
				return err
			}
		}
		if err := worker.removePurgedFile(ctx, accountID, record); err != nil {
			return err
		}
	}
}

func (worker *AccountLifecycle) nextOwnedFile(ctx context.Context, accountID string) (fileRecord, bool, error) {
	var record fileRecord
	found := false
	err := worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		query := `SELECT id, owner_account_id, storage_key, provider_id, temporary_key, size_bytes, state
			FROM files_objects WHERE owner_account_id = ? ORDER BY id LIMIT 1`
		if worker.db.Dialect() == database.DialectPostgres {
			query += ` FOR UPDATE`
		}
		err := tx.QueryRowContext(ctx, query, accountID).Scan(&record.ID, &record.OwnerAccountID, &record.StorageKey, &record.ProviderID, &record.TemporaryKey, &record.SizeBytes, &record.State)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	return record, found, err
}

func (worker *AccountLifecycle) removePurgedFile(ctx context.Context, accountID string, record fileRecord) error {
	return worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var currentOwner string
		var currentBytes int64
		query := `SELECT owner_account_id, size_bytes FROM files_objects WHERE id = ?`
		if worker.db.Dialect() == database.DialectPostgres {
			query += ` FOR UPDATE`
		}
		err := tx.QueryRowContext(ctx, query, record.ID).Scan(&currentOwner, &currentBytes)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if currentOwner != accountID {
			return ErrConflict
		}
		if err := decrementCapacity(ctx, tx, accountID, currentBytes, 1); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM files_objects WHERE id = ? AND owner_account_id = ?`, record.ID, accountID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (worker *AccountLifecycle) fail(ctx context.Context, eventID, code, deletionID string, cause error) error {
	now := worker.now().UTC().Truncate(time.Microsecond)
	owned := false
	err := worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE files_account_lifecycle_events SET state='failed',last_error_code=?,updated_at=?,next_attempt_at=?,claim_token=NULL,claim_expires_at=NULL WHERE event_id=? AND state='claimed' AND claim_token=?`, code, now, now.Add(time.Minute), eventID, ctx.Value(lifecycleClaimKey{}))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		owned = n == 1
		return err
	})
	if err == nil && owned && deletionID != "" {
		_ = worker.iam.FailAccountDeletion(ctx, deletionID, code)
	}
	return worker.normalize(ctx, cause)
}

func (worker *AccountLifecycle) finishEvent(ctx context.Context, eventID, state string) error {
	if state != "completed" && state != "canceled" {
		return ErrInternal
	}
	now := worker.now().UTC().Truncate(time.Microsecond)
	err := worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE files_account_lifecycle_events SET state = ?, completed_at = ?,
			last_error_code = NULL, claim_token = NULL, claim_expires_at = NULL, updated_at = ? WHERE event_id = ? AND state = 'claimed' AND claim_token = ?`, state, now, now, eventID, ctx.Value(lifecycleClaimKey{}))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrConflict
		}
		return nil
	})
	return worker.normalize(ctx, err)
}

func (worker *AccountLifecycle) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, stable := range []error{context.Canceled, context.DeadlineExceeded, ErrValidation, ErrConflict, ErrQuotaExceeded} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	return ErrInternal
}

type lifecycleClaimKey struct{}

// renewClaim 防止长时间文件处理被其他实例重复认领；丢失租约立即取消当前工作。
func (worker *AccountLifecycle) renewClaim(ctx context.Context, cancel context.CancelFunc, event accountLifecycleEvent) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := worker.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
			now := worker.now().UTC()
			return requireLifecycleResult(tx.ExecContext(ctx, `UPDATE files_account_lifecycle_events SET claim_expires_at = ? WHERE event_id = ? AND state = 'claimed' AND claim_token = ?`, now.Add(2*time.Minute), event.EventID, event.ClaimToken))
		})
		if err != nil {
			cancel()
			return
		}
	}
}
func requireLifecycleResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	return requireLifecycleRow(result)
}
