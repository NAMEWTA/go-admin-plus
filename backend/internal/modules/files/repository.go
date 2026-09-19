package files

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	statePending  = "pending"
	stateReady    = "ready"
	stateDeleting = "deleting"
)

type fileRecord struct {
	ID, OwnerAccountID, OriginalName, NameKey, MediaType, SHA256, StorageKey, ProviderID, State string
	TemporaryKey                                                                                *string
	SizeBytes, Revision                                                                         int64
	ClaimToken                                                                                  *string
	ClaimExpiresAt                                                                              *time.Time
	CreatedAt, UpdatedAt                                                                        time.Time
}

type capacityReservation struct {
	ID, OwnerAccountID   string
	ReservedBytes        int64
	CreatedAt, ExpiresAt time.Time
}

type repository struct{ dialect database.Dialect }

func (repository) insert(ctx context.Context, tx database.Tx, record fileRecord) error {
	if record.ProviderID == "" {
		record.ProviderID = "local"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO files_objects
		(id, owner_account_id, original_name, name_key, media_type, size_bytes, sha256, storage_key, provider_id, temporary_key, state, revision, created_at, updated_at, claim_token, claim_expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ID, record.OwnerAccountID, record.OriginalName, record.NameKey,
		record.MediaType, record.SizeBytes, record.SHA256, record.StorageKey, record.ProviderID, record.TemporaryKey, record.State, record.Revision, record.CreatedAt, record.UpdatedAt, record.ClaimToken, record.ClaimExpiresAt)
	return err
}

func (repository repository) reserve(ctx context.Context, tx database.Tx, id, owner string, bytes int64, createdAt, expiresAt time.Time, policy CapacityPolicy) error {
	if id == "" || owner == "" || bytes < 1 || !policy.valid() {
		return ErrValidation
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO files_capacity_counters(scope_kind, scope_id, reserved_bytes, reserved_objects)
		VALUES (?, ?, 0, 0) ON CONFLICT (scope_kind, scope_id) DO NOTHING`, "account", owner); err != nil {
		return err
	}
	if repository.dialect == database.DialectPostgres {
		for _, key := range [][2]string{{"global", "global"}, {"account", owner}} {
			var locked int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM files_capacity_counters WHERE scope_kind = ? AND scope_id = ? FOR UPDATE`, key[0], key[1]).Scan(&locked); err != nil {
				return err
			}
		}
	}
	if ok, err := incrementCapacity(ctx, tx, "global", "global", bytes, policy.MaximumTotalBytes, policy.MaximumTotalObjects); err != nil {
		return err
	} else if !ok {
		return ErrQuotaExceeded
	}
	if ok, err := incrementCapacity(ctx, tx, "account", owner, bytes, policy.MaximumAccountBytes, policy.MaximumAccountObjects); err != nil {
		return err
	} else if !ok {
		return ErrQuotaExceeded
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO files_capacity_reservations(id, owner_account_id, reserved_bytes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, id, owner, bytes, createdAt, expiresAt)
	return err
}

func incrementCapacity(ctx context.Context, tx database.Tx, kind, id string, bytes, maximumBytes, maximumObjects int64) (bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE files_capacity_counters
		SET reserved_bytes = reserved_bytes + ?, reserved_objects = reserved_objects + 1
		WHERE scope_kind = ? AND scope_id = ? AND reserved_bytes + ? <= ? AND reserved_objects + 1 <= ?`,
		bytes, kind, id, bytes, maximumBytes, maximumObjects)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (repository repository) commitReservation(ctx context.Context, tx database.Tx, reservationID string, record fileRecord) error {
	reservation, err := repository.getReservation(ctx, tx, reservationID)
	if err != nil {
		return err
	}
	if reservation.OwnerAccountID != record.OwnerAccountID || record.SizeBytes < 0 || record.SizeBytes > reservation.ReservedBytes {
		return ErrSizeMismatch
	}
	difference := reservation.ReservedBytes - record.SizeBytes
	if difference > 0 {
		if err := decrementCapacity(ctx, tx, reservation.OwnerAccountID, difference, 0); err != nil {
			return err
		}
	}
	if err := exactlyOne(tx.ExecContext(ctx, `DELETE FROM files_capacity_reservations WHERE id = ?`, reservationID)); err != nil {
		return err
	}
	return repository.insert(ctx, tx, record)
}

func (repository repository) releaseReservation(ctx context.Context, tx database.Tx, id string) error {
	reservation, err := repository.getReservation(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := decrementCapacity(ctx, tx, reservation.OwnerAccountID, reservation.ReservedBytes, 1); err != nil {
		return err
	}
	return exactlyOne(tx.ExecContext(ctx, `DELETE FROM files_capacity_reservations WHERE id = ?`, id))
}

func (repository repository) getReservation(ctx context.Context, tx database.Tx, id string) (capacityReservation, error) {
	query := `SELECT id, owner_account_id, reserved_bytes, created_at, expires_at FROM files_capacity_reservations WHERE id = ?`
	if repository.dialect == database.DialectPostgres {
		query += ` FOR UPDATE`
	}
	var reservation capacityReservation
	err := tx.QueryRowContext(ctx, query, id).Scan(&reservation.ID, &reservation.OwnerAccountID, &reservation.ReservedBytes, &reservation.CreatedAt, &reservation.ExpiresAt)
	return reservation, err
}

func decrementCapacity(ctx context.Context, tx database.Tx, owner string, bytes, objects int64) error {
	for _, key := range [][2]string{{"global", "global"}, {"account", owner}} {
		result, err := tx.ExecContext(ctx, `UPDATE files_capacity_counters
			SET reserved_bytes = reserved_bytes - ?, reserved_objects = reserved_objects - ?
			WHERE scope_kind = ? AND scope_id = ? AND reserved_bytes >= ? AND reserved_objects >= ?`,
			bytes, objects, key[0], key[1], bytes, objects)
		if err := exactlyOne(result, err); err != nil {
			return err
		}
	}
	return nil
}

func (repository repository) releaseExpiredReservations(ctx context.Context, tx database.Tx, now time.Time, limit int) (int, error) {
	query := `SELECT id FROM files_capacity_reservations WHERE expires_at <= ? ORDER BY expires_at ASC, id ASC LIMIT ?`
	if repository.dialect == database.DialectPostgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, query, now, limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := repository.releaseReservation(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (repository) markReady(ctx context.Context, tx database.Tx, id string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE files_objects SET state = ?, temporary_key = NULL, claim_token = NULL, claim_expires_at = NULL, updated_at = ? WHERE id = ? AND state = ?`, stateReady, now, id, statePending)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (repository repository) list(ctx context.Context, tx database.Tx, actorID string, scope Scope, query ListQuery) (Page, error) {
	if !validScope(scope) {
		return Page{}, ErrDenied
	}
	clauses, args := []string{"state = ?"}, []any{stateReady}
	if scope == ScopeSelf {
		clauses, args = append(clauses, "owner_account_id = ?"), append(args, actorID)
	}
	if query.Search != "" {
		clause := "instr(name_key, ?) > 0"
		if repository.dialect == database.DialectPostgres {
			clause = "strpos(name_key, ?) > 0"
		}
		clauses, args = append(clauses, clause), append(args, nameKey(query.Search))
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	var page Page
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM files_objects`+where, args...).Scan(&page.Total); err != nil {
		return Page{}, err
	}
	columns := map[string]string{"name": "name_key", "sizeBytes": "size_bytes", "createdAt": "created_at"}
	if query.Sort == "name" {
		if repository.dialect == database.DialectPostgres {
			columns["name"] = `name_key COLLATE "C"`
		} else {
			columns["name"] = "name_key COLLATE BINARY"
		}
	}
	directions := map[string]string{"ascending": "ASC", "descending": "DESC"}
	statement := `SELECT id, original_name, media_type, size_bytes, sha256, revision, created_at, updated_at FROM files_objects` + where +
		` ORDER BY ` + columns[query.Sort] + ` ` + directions[query.Direction] + `, id ASC LIMIT ? OFFSET ?`
	args = append(args, query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page.Rows = []Metadata{}
	for rows.Next() {
		var metadata Metadata
		if err := rows.Scan(&metadata.ID, &metadata.OriginalName, &metadata.MediaType, &metadata.SizeBytes, &metadata.SHA256,
			&metadata.Revision, &metadata.CreatedAt, &metadata.UpdatedAt); err != nil {
			return Page{}, err
		}
		page.Rows = append(page.Rows, metadata)
	}
	return page, rows.Err()
}

func (repository repository) ready(ctx context.Context, tx database.Tx, id, actorID string, scope Scope) (fileRecord, error) {
	if !validScope(scope) {
		return fileRecord{}, ErrDenied
	}
	query := `SELECT id, owner_account_id, original_name, name_key, media_type, size_bytes, sha256, storage_key, provider_id, state, revision, created_at, updated_at
		FROM files_objects WHERE id = ? AND state = ?`
	args := []any{id, stateReady}
	if scope == ScopeSelf {
		query += ` AND owner_account_id = ?`
		args = append(args, actorID)
	}
	if repository.dialect == database.DialectPostgres {
		query += ` FOR SHARE`
	}
	var record fileRecord
	err := tx.QueryRowContext(ctx, query, args...).Scan(&record.ID, &record.OwnerAccountID, &record.OriginalName, &record.NameKey,
		&record.MediaType, &record.SizeBytes, &record.SHA256, &record.StorageKey, &record.ProviderID, &record.State, &record.Revision, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) && scope == ScopeSelf {
		var owner string
		ownerErr := tx.QueryRowContext(ctx, `SELECT owner_account_id FROM files_objects WHERE id = ? AND state = ?`, id, stateReady).Scan(&owner)
		if ownerErr == nil && owner != actorID {
			return fileRecord{}, ErrDenied
		}
	}
	return record, err
}

func (repository repository) markDeleting(ctx context.Context, tx database.Tx, targets []DeleteTarget, actorID string, scope Scope, now time.Time) ([]fileRecord, error) {
	if !validScope(scope) {
		return nil, ErrDenied
	}
	records := make([]fileRecord, 0, len(targets))
	for _, target := range targets {
		query := `SELECT id, owner_account_id, storage_key, provider_id, size_bytes, state, revision FROM files_objects WHERE id = ?`
		if repository.dialect == database.DialectPostgres {
			query += ` FOR UPDATE`
		}
		var record fileRecord
		err := tx.QueryRowContext(ctx, query, target.ID).Scan(&record.ID, &record.OwnerAccountID, &record.StorageKey, &record.ProviderID, &record.SizeBytes, &record.State, &record.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if scope == ScopeSelf && record.OwnerAccountID != actorID {
			return nil, ErrDenied
		}
		if record.State != stateReady || record.Revision != target.Revision {
			return nil, ErrConflict
		}
		records = append(records, record)
	}
	for _, record := range records {
		result, err := tx.ExecContext(ctx, `UPDATE files_objects SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND state = ? AND revision = ?`,
			stateDeleting, now, record.ID, stateReady, record.Revision)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			if err != nil {
				return nil, err
			}
			return nil, ErrConflict
		}
	}
	return records, nil
}

func (repository) removeDeleting(ctx context.Context, tx database.Tx, record fileRecord) error {
	if err := decrementCapacity(ctx, tx, record.OwnerAccountID, record.SizeBytes, 1); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM files_objects WHERE id = ? AND state = ?`, record.ID, stateDeleting)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (repository repository) claimRecovery(ctx context.Context, tx database.Tx, now, expires time.Time, token string, limit int) ([]fileRecord, error) {
	query := `SELECT id, owner_account_id, original_name, name_key, media_type, size_bytes, sha256, storage_key, provider_id, temporary_key, state, revision, created_at, updated_at
		FROM files_objects WHERE state <> ? AND (claim_expires_at IS NULL OR claim_expires_at < ?) ORDER BY updated_at ASC, id ASC LIMIT ?`
	if repository.dialect == database.DialectPostgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, query, stateReady, now, limit)
	if err != nil {
		return nil, err
	}
	records := []fileRecord{}
	for rows.Next() {
		var record fileRecord
		if err := rows.Scan(&record.ID, &record.OwnerAccountID, &record.OriginalName, &record.NameKey, &record.MediaType, &record.SizeBytes,
			&record.SHA256, &record.StorageKey, &record.ProviderID, &record.TemporaryKey, &record.State, &record.Revision, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range records {
		result, err := tx.ExecContext(ctx, `UPDATE files_objects SET claim_token = ?, claim_expires_at = ? WHERE id = ? AND state <> ? AND (claim_expires_at IS NULL OR claim_expires_at < ?)`, token, expires, records[index].ID, stateReady, now)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			if err != nil {
				return nil, err
			}
			return nil, ErrConflict
		}
		records[index].ClaimToken = &token
	}
	return records, nil
}

func (repository) finishClaimedReady(ctx context.Context, tx database.Tx, id, token string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE files_objects SET state = ?, temporary_key = NULL, claim_token = NULL, claim_expires_at = NULL, updated_at = ? WHERE id = ? AND state = ? AND claim_token = ?`, stateReady, now, id, statePending, token)
	return exactlyOne(result, err)
}

func (repository) removeClaimed(ctx context.Context, tx database.Tx, record fileRecord, token string) error {
	if err := decrementCapacity(ctx, tx, record.OwnerAccountID, record.SizeBytes, 1); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM files_objects WHERE id = ? AND state = ? AND claim_token = ?`, record.ID, record.State, token)
	return exactlyOne(result, err)
}

func exactlyOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func metadata(record fileRecord) Metadata {
	return Metadata{ID: record.ID, OriginalName: record.OriginalName, MediaType: record.MediaType, SizeBytes: record.SizeBytes,
		SHA256: record.SHA256, Revision: record.Revision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}
