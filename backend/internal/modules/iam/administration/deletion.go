package administration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

const AccountDeletionRequestedTopic = "iam.account-deletion.requested.v1"

type DeletionStrategy string

const (
	DeletionStrategyTransfer DeletionStrategy = "transfer"
	DeletionStrategyPurge    DeletionStrategy = "purge"
)

type DeletionStatus string

const (
	DeletionStatusQueued    DeletionStatus = "queued"
	DeletionStatusClaimed   DeletionStatus = "claimed"
	DeletionStatusCompleted DeletionStatus = "completed"
	DeletionStatusFailed    DeletionStatus = "failed"
)

type StartDeletion struct {
	AccountID        string
	Strategy         DeletionStrategy
	TransferTargetID string
	PurgeConfirmed   bool
}

type Deletion struct {
	ID, AccountID, TransferTargetID, AuditReference, EventID string
	Strategy                                                 DeletionStrategy
	Status                                                   DeletionStatus
	CreatedAt, UpdatedAt                                     time.Time
}

type AccountDeletionClaim struct {
	AccountID, TransferTargetID string
	Strategy                    DeletionStrategy
}

type deletionEventWriter interface {
	Enqueue(context.Context, database.Tx, outbox.Event) (bool, error)
}

type DeletionService struct {
	db         Database
	authorizer *authorization.Service
	events     deletionEventWriter
	now        func() time.Time
	newID      func() string
}

type DeletionOption func(*DeletionService)

func WithDeletionClock(clock func() time.Time) DeletionOption {
	return func(service *DeletionService) { service.now = clock }
}

func NewDeletionService(db Database, events deletionEventWriter, options ...DeletionOption) (*DeletionService, error) {
	if db == nil || events == nil {
		return nil, errors.New("iam account deletion dependencies are required")
	}
	service := &DeletionService{
		db: db, authorizer: authorization.NewService(db), events: events,
		now: time.Now, newID: uuid.NewString,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	if service.now == nil || service.newID == nil {
		return nil, errors.New("iam account deletion options are invalid")
	}
	return service, nil
}

func AccountDeletionRequestedTopicSchema() outbox.TopicSchema {
	return outbox.TopicSchema{
		Topic: AccountDeletionRequestedTopic,
		Payload: []outbox.PayloadFieldSchema{
			{Name: "strategy", Kind: outbox.PayloadString, Required: true, AllowedStrings: []string{"transfer", "purge"}},
			{Name: "version", Kind: outbox.PayloadNumber, Required: true},
		},
		BusinessKey: outbox.BusinessKeySchema{Prefix: "account-deletion", MinParts: 3, MaxParts: 3},
	}
}

var deletionKeyPartPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (service *DeletionService) StartDeletion(ctx context.Context, actorID string, input StartDeletion) (Deletion, error) {
	if service == nil || !validDeletionInput(actorID, input) {
		return Deletion{}, ErrValidation
	}
	now := service.now().UTC().Truncate(time.Microsecond)
	deletionID, eventID := service.newID(), service.newID()
	targetPart := input.TransferTargetID
	if input.Strategy == DeletionStrategyPurge {
		targetPart = "none"
	}
	businessKey := strings.Join([]string{"account-deletion", deletionID, input.AccountID, targetPart}, ":")
	payload, err := json.Marshal(struct {
		Strategy DeletionStrategy `json:"strategy"`
		Version  int              `json:"version"`
	}{Strategy: input.Strategy, Version: 1})
	if err != nil {
		return Deletion{}, ErrInternal
	}
	result := Deletion{
		ID: deletionID, AccountID: input.AccountID, TransferTargetID: input.TransferTargetID,
		AuditReference: "deleted-account:" + deletionID, EventID: eventID,
		Strategy: input.Strategy, Status: DeletionStatusQueued, CreatedAt: now, UpdatedAt: now,
	}
	err = service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := service.lockAdministratorInvariant(ctx, tx); err != nil {
			return err
		}
		decision, err := service.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersDelete)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll || actorID == input.AccountID {
			return ErrConflict
		}
		if err := checkExpectedRevision(ctx, tx); err != nil {
			return err
		}
		if err := service.lockAdministratorInvariant(ctx, tx); err != nil {
			return err
		}
		lifecycles, err := service.lockAccountLifecycles(ctx, tx, input.AccountID, input.TransferTargetID)
		if err != nil {
			return err
		}
		lifecycle := lifecycles[input.AccountID]
		if lifecycle == "deletion-pending" || lifecycle == "deleted" {
			return ErrConflict
		}
		var targetReferences int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_account_deletions
			WHERE transfer_target_id = ? AND status IN ('queued','claimed','failed')`, input.AccountID).Scan(&targetReferences); err != nil {
			return err
		}
		if targetReferences != 0 {
			return ErrConflict
		}
		if input.Strategy == DeletionStrategyTransfer {
			targetLifecycle := lifecycles[input.TransferTargetID]
			if targetLifecycle == "deletion-pending" || targetLifecycle == "deleted" {
				return ErrConflict
			}
		}
		last, err := service.isLastEnabledAdministrator(ctx, tx, input.AccountID)
		if err != nil {
			return err
		}
		if last {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_account_deletions
			(id, account_id, strategy, transfer_target_id, status, audit_ref, event_id, business_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, result.ID, result.AccountID, result.Strategy,
			nullableDeletionTarget(result.TransferTargetID), DeletionStatusQueued, result.AuditReference, result.EventID, businessKey, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE iam_accounts SET lifecycle_state = 'deletion-pending', disabled_at = COALESCE(disabled_at, ?),
			session_generation = session_generation + 1, updated_at = ? WHERE id = ?`, now, now, input.AccountID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_account_recovery_blocks(account_id, blocked_at) VALUES (?, ?)
			ON CONFLICT (account_id) DO NOTHING`, input.AccountID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE account_id = ? AND state = 'active'`, now, input.AccountID); err != nil {
			return err
		}
		_, err = service.events.Enqueue(ctx, tx, outbox.Event{
			ID: eventID, Topic: AccountDeletionRequestedTopic, BusinessKey: businessKey, Payload: payload, OccurredAt: now,
		})
		return err
	})
	return result, service.normalize(ctx, err)
}

func (service *DeletionService) GetDeletion(ctx context.Context, actorID, accountID string) (Deletion, error) {
	if service == nil || actorID == "" || !deletionKeyPartPattern.MatchString(accountID) {
		return Deletion{}, ErrValidation
	}
	var result Deletion
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := service.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersRead)
		if err != nil {
			return err
		}
		if decision.Scope == authorization.ScopeSelf && actorID != accountID {
			return ErrDenied
		}
		return scanDeletion(tx.QueryRowContext(ctx, `SELECT id, account_id, strategy, transfer_target_id, status, audit_ref, event_id, created_at, updated_at
			FROM iam_account_deletions WHERE account_id = ? AND status <> 'canceled' ORDER BY created_at DESC LIMIT 1`, accountID), &result)
	})
	return result, service.normalize(ctx, err)
}

func (service *DeletionService) CancelDeletion(ctx context.Context, actorID, accountID string) error {
	if service == nil || actorID == "" || !deletionKeyPartPattern.MatchString(accountID) {
		return ErrValidation
	}
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := service.lockAdministratorInvariant(ctx, tx); err != nil {
			return err
		}
		decision, err := service.authorizer.RequireInTx(ctx, tx, actorID, authorization.PermissionUsersDelete)
		if err != nil {
			return err
		}
		if decision.Scope != authorization.ScopeAll {
			return ErrDenied
		}
		if err := checkExpectedRevision(ctx, tx); err != nil {
			return err
		}
		var deletionID string
		if err := service.rowForUpdate(ctx, tx, `SELECT id FROM iam_account_deletions WHERE account_id = ? AND status IN ('queued','claimed','failed') ORDER BY created_at DESC LIMIT 1`, accountID).Scan(&deletionID); err != nil {
			return err
		}
		var status string
		if err := service.rowForUpdate(ctx, tx, `SELECT status FROM iam_account_deletions WHERE id = ?`, deletionID).Scan(&status); err != nil {
			return err
		}
		if status != string(DeletionStatusQueued) {
			return ErrConflict
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if _, err := tx.ExecContext(ctx, `UPDATE iam_account_deletions SET status = 'canceled', updated_at = ? WHERE id = ? AND status = 'queued'`, now, deletionID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE iam_accounts SET lifecycle_state = 'disabled', disabled_at = COALESCE(disabled_at, ?), updated_at = ?
			WHERE id = ? AND lifecycle_state = 'deletion-pending'`, now, now, accountID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM iam_account_recovery_blocks WHERE account_id = ?`, accountID)
		return err
	})
	return service.normalize(ctx, err)
}

func (service *DeletionService) ClaimDeletion(ctx context.Context, deletionID string) (AccountDeletionClaim, error) {
	if service == nil || uuid.Validate(deletionID) != nil {
		return AccountDeletionClaim{}, ErrValidation
	}
	var claim AccountDeletionClaim
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var status string
		var target sql.NullString
		if err := service.rowForUpdate(ctx, tx, `SELECT account_id, strategy, transfer_target_id, status FROM iam_account_deletions WHERE id = ?`, deletionID).
			Scan(&claim.AccountID, &claim.Strategy, &target, &status); err != nil {
			return err
		}
		claim.TransferTargetID = target.String
		switch DeletionStatus(status) {
		case DeletionStatusQueued, DeletionStatusFailed:
			now := service.now().UTC().Truncate(time.Microsecond)
			_, err := tx.ExecContext(ctx, `UPDATE iam_account_deletions SET status = 'claimed', claimed_at = COALESCE(claimed_at, ?),
				failure_code = NULL, updated_at = ? WHERE id = ? AND status IN ('queued','failed')`, now, now, deletionID)
			return err
		case DeletionStatusClaimed:
			return nil
		case DeletionStatusCompleted:
			claim = AccountDeletionClaim{}
			return nil
		default:
			claim = AccountDeletionClaim{}
			return nil
		}
	})
	return claim, service.normalize(ctx, err)
}

// ClaimAccountDeletion is the Files consumer-owned port. Primitive return values keep the module
// boundary free of imports in either direction.
func (service *DeletionService) ClaimAccountDeletion(ctx context.Context, deletionID string) (string, string, string, error) {
	claim, err := service.ClaimDeletion(ctx, deletionID)
	return claim.AccountID, string(claim.Strategy), claim.TransferTargetID, err
}

func (service *DeletionService) FailAccountDeletion(ctx context.Context, deletionID, failureCode string) error {
	if service == nil || uuid.Validate(deletionID) != nil || !deletionFailureCodePattern.MatchString(failureCode) {
		return ErrValidation
	}
	now := service.now().UTC().Truncate(time.Microsecond)
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE iam_account_deletions SET status = 'failed', failure_code = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed'`, failureCode, now, deletionID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			var status string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM iam_account_deletions WHERE id = ?`, deletionID).Scan(&status); err != nil {
				return err
			}
			if status != "failed" && status != "completed" {
				return ErrConflict
			}
		}
		return nil
	})
	return service.normalize(ctx, err)
}

func (service *DeletionService) CompleteAccountDeletion(ctx context.Context, deletionID string) error {
	if service == nil || uuid.Validate(deletionID) != nil {
		return ErrValidation
	}
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var accountID, auditReference, status string
		if err := service.rowForUpdate(ctx, tx, `SELECT account_id, audit_ref, status FROM iam_account_deletions WHERE id = ?`, deletionID).
			Scan(&accountID, &auditReference, &status); err != nil {
			return err
		}
		if status == string(DeletionStatusCompleted) {
			return nil
		}
		if status != string(DeletionStatusClaimed) && status != string(DeletionStatusFailed) {
			return ErrConflict
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		anonymousKey := strings.ReplaceAll(deletionID, "-", "")
		if _, err := tx.ExecContext(ctx, `DELETE FROM iam_account_roles WHERE account_id = ?`, accountID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE iam_accounts SET username = ?, display_name = 'Deleted account', email = ?, avatar_ref = NULL,
			lifecycle_state = 'deleted', deleted_at = ?, audit_ref = ?, disabled_at = COALESCE(disabled_at, ?), updated_at = ?
			WHERE id = ? AND lifecycle_state = 'deletion-pending'`, "deleted-"+anonymousKey, "deleted+"+anonymousKey[:12]+"@invalid.test", now, auditReference, now, now, accountID)
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
		_, err = tx.ExecContext(ctx, `UPDATE iam_account_deletions SET status = 'completed', completed_at = ?, failure_code = NULL, updated_at = ? WHERE id = ?`, now, now, deletionID)
		return err
	})
	return service.normalize(ctx, err)
}

var deletionFailureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func validDeletionInput(actorID string, input StartDeletion) bool {
	if actorID == "" || !deletionKeyPartPattern.MatchString(input.AccountID) {
		return false
	}
	switch input.Strategy {
	case DeletionStrategyTransfer:
		return !input.PurgeConfirmed && deletionKeyPartPattern.MatchString(input.TransferTargetID) && input.TransferTargetID != input.AccountID
	case DeletionStrategyPurge:
		return input.PurgeConfirmed && input.TransferTargetID == ""
	default:
		return false
	}
}

func nullableDeletionTarget(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (service *DeletionService) lockAdministratorInvariant(ctx context.Context, tx database.Tx) error {
	if service.db.Dialect() != database.DialectPostgres {
		return nil
	}
	var id string
	return tx.QueryRowContext(ctx, `SELECT id FROM iam_roles WHERE role_key = ? FOR UPDATE`, systemAdministratorKey).Scan(&id)
}

func (service *DeletionService) lockAccountLifecycles(ctx context.Context, tx database.Tx, accountID, transferTargetID string) (map[string]string, error) {
	ids := []string{accountID}
	if transferTargetID != "" {
		ids = append(ids, transferTargetID)
		if ids[0] > ids[1] {
			ids[0], ids[1] = ids[1], ids[0]
		}
	}
	query := `SELECT id, lifecycle_state FROM iam_accounts WHERE id IN (` + placeholders(len(ids)) + `) ORDER BY id`
	if service.db.Dialect() == database.DialectPostgres {
		query += ` FOR UPDATE`
	}
	arguments := make([]any, len(ids))
	for index := range ids {
		arguments[index] = ids[index]
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string, len(ids))
	for rows.Next() {
		var id, lifecycle string
		if err := rows.Scan(&id, &lifecycle); err != nil {
			return nil, err
		}
		result[id] = lifecycle
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, sql.ErrNoRows
	}
	return result, nil
}

func (service *DeletionService) isLastEnabledAdministrator(ctx context.Context, tx database.Tx, accountID string) (bool, error) {
	var targetIsAdministrator, enabledAdministrators int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM iam_account_roles ar JOIN iam_roles r ON r.id = ar.role_id
		WHERE ar.account_id = ? AND r.role_key = ? AND r.enabled = ?`, accountID, systemAdministratorKey, true).Scan(&targetIsAdministrator); err != nil {
		return false, err
	}
	if targetIsAdministrator == 0 {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT a.id) FROM iam_accounts a
		JOIN iam_account_roles ar ON ar.account_id = a.id JOIN iam_roles r ON r.id = ar.role_id
		WHERE r.role_key = ? AND r.enabled = ? AND a.disabled_at IS NULL AND a.lifecycle_state = 'active'`, systemAdministratorKey, true).
		Scan(&enabledAdministrators); err != nil {
		return false, err
	}
	return enabledAdministrators <= 1, nil
}

func (service *DeletionService) rowForUpdate(ctx context.Context, tx database.Tx, query string, args ...any) *sql.Row {
	if service.db.Dialect() == database.DialectPostgres {
		query += ` FOR UPDATE`
	}
	return tx.QueryRowContext(ctx, query, args...)
}

func scanDeletion(row *sql.Row, result *Deletion) error {
	var target sql.NullString
	if err := row.Scan(&result.ID, &result.AccountID, &result.Strategy, &target, &result.Status, &result.AuditReference,
		&result.EventID, &result.CreatedAt, &result.UpdatedAt); err != nil {
		return err
	}
	result.TransferTargetID = target.String
	return nil
}

func (service *DeletionService) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, stable := range []error{context.Canceled, context.DeadlineExceeded, ErrDenied, ErrNotFound, ErrValidation, ErrConflict} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if constraintConflict(service.db.Dialect(), err) {
		return ErrConflict
	}
	return ErrInternal
}
