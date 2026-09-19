package scheduler

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type definitionRecord struct {
	ID             string
	Name           string
	NameKey        string
	TaskType       string
	ScheduleJSON   []byte
	ParametersJSON []byte
	Enabled        bool
	Revision       int64
	NextRunAt      sql.NullTime
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type repository struct{ dialect database.Dialect }

func (r repository) definitions(ctx context.Context, tx database.Tx, query DefinitionQuery) (DefinitionPage, error) {
	where := ` WHERE deleted_at IS NULL`
	arguments := []any{}
	if query.Search != "" {
		contains := `instr(name_key, ?) > 0`
		if r.dialect == database.DialectPostgres {
			contains = `strpos(name_key, ?) > 0`
		}
		where += ` AND ` + contains
		arguments = append(arguments, normalizedNameKey(query.Search))
	}
	var result DefinitionPage
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_definitions`+where, arguments...).Scan(&result.Total); err != nil {
		return DefinitionPage{}, err
	}
	order := `name_key COLLATE BINARY, id COLLATE BINARY`
	if r.dialect == database.DialectPostgres {
		order = `name_key COLLATE "C", id`
	}
	arguments = append(arguments, query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := tx.QueryContext(ctx, `SELECT id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at FROM scheduler_definitions`+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, arguments...)
	if err != nil {
		return DefinitionPage{}, err
	}
	defer rows.Close()
	result.Rows = []Definition{}
	for rows.Next() {
		value, err := scanDefinition(rows)
		if err != nil {
			return DefinitionPage{}, err
		}
		result.Rows = append(result.Rows, value)
	}
	return result, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func (r repository) definition(ctx context.Context, tx database.Tx, id string, lock bool) (definitionRecord, error) {
	query := `SELECT id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at FROM scheduler_definitions WHERE id = ? AND deleted_at IS NULL`
	if lock && r.dialect == database.DialectPostgres {
		query += ` FOR UPDATE`
	}
	return scanDefinitionRecord(tx.QueryRowContext(ctx, query, id))
}

func (r repository) dueDefinition(ctx context.Context, tx database.Tx, now time.Time) (definitionRecord, error) {
	order := `next_run_at, id COLLATE BINARY`
	if r.dialect == database.DialectPostgres {
		order = `next_run_at, id`
	}
	query := `SELECT id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at FROM scheduler_definitions WHERE deleted_at IS NULL AND enabled = ? AND next_run_at <= ? AND NOT EXISTS (SELECT 1 FROM scheduler_runs run WHERE run.definition_id = scheduler_definitions.id AND run.state IN ('queued','running')) ORDER BY ` + order + ` LIMIT 1`
	if r.dialect == database.DialectPostgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	return scanDefinitionRecord(tx.QueryRowContext(ctx, query, true, now))
}

func (repository) insertDefinition(ctx context.Context, tx database.Tx, value definitionRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scheduler_definitions(id, name, name_key, task_type, schedule_json, parameters_json, enabled, revision, next_run_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, value.ID, value.Name, value.NameKey, value.TaskType, value.ScheduleJSON, value.ParametersJSON, value.Enabled, value.Revision, nullableTime(value.NextRunAt), value.CreatedAt, value.UpdatedAt)
	return err
}

func (repository) updateDefinition(ctx context.Context, tx database.Tx, value definitionRecord, expectedRevision int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_definitions SET name = ?, name_key = ?, task_type = ?, schedule_json = ?, parameters_json = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND enabled = ? AND revision = ?`, value.Name, value.NameKey, value.TaskType, value.ScheduleJSON, value.ParametersJSON, value.UpdatedAt, value.ID, false, expectedRevision)
	return exactlyOne(result, err)
}

func (repository) enableDefinition(ctx context.Context, tx database.Tx, id string, revision int64, next, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_definitions SET enabled = ?, next_run_at = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND enabled = ? AND revision = ?`, true, next, now, id, false, revision)
	return exactlyOne(result, err)
}

func (repository) stopDefinition(ctx context.Context, tx database.Tx, id string, revision int64, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_definitions SET enabled = ?, next_run_at = NULL, revision = revision + 1, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND enabled = ? AND revision = ?`, false, now, id, true, revision)
	return exactlyOne(result, err)
}

func (repository) deleteDefinition(ctx context.Context, tx database.Tx, id string, revision int64, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_definitions SET enabled = ?, next_run_at = NULL, revision = revision + 1, updated_at = ?, deleted_at = ? WHERE id = ? AND deleted_at IS NULL AND revision = ?`, false, now, now, id, revision)
	return exactlyOne(result, err)
}

func (repository) advanceDefinition(ctx context.Context, tx database.Tx, id string, next, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_definitions SET next_run_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND enabled = ?`, next, now, id, true)
	return exactlyOne(result, err)
}

func (repository) insertExecution(ctx context.Context, tx database.Tx, value Execution) error {
	var code any
	if value.ErrorCode != "" {
		code = value.ErrorCode
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO scheduler_executions(id, definition_id, definition_revision, task_type, scheduled_for, started_at, finished_at, status, error_code, executor_owner) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.DefinitionID, value.DefinitionRevision, value.TaskType, value.ScheduledFor, value.StartedAt, value.FinishedAt, value.Status, code, value.ExecutorOwner)
	return err
}

func (r repository) executions(ctx context.Context, tx database.Tx, query ExecutionQuery) (ExecutionPage, error) {
	where := ` WHERE 1 = 1`
	arguments := []any{}
	if query.DefinitionID != "" {
		where += ` AND definition_id = ?`
		arguments = append(arguments, query.DefinitionID)
	}
	if query.Status != "" {
		where += ` AND status = ?`
		arguments = append(arguments, query.Status)
	}
	var result ExecutionPage
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_executions`+where, arguments...).Scan(&result.Total); err != nil {
		return ExecutionPage{}, err
	}
	order := `started_at DESC, id COLLATE BINARY DESC`
	if r.dialect == database.DialectPostgres {
		order = `started_at DESC, id DESC`
	}
	arguments = append(arguments, query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := tx.QueryContext(ctx, `SELECT id, definition_id, definition_revision, task_type, scheduled_for, started_at, finished_at, status, COALESCE(error_code, ''), executor_owner FROM scheduler_executions`+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, arguments...)
	if err != nil {
		return ExecutionPage{}, err
	}
	defer rows.Close()
	result.Rows = []Execution{}
	for rows.Next() {
		var value Execution
		if err := rows.Scan(&value.ID, &value.DefinitionID, &value.DefinitionRevision, &value.TaskType, &value.ScheduledFor, &value.StartedAt, &value.FinishedAt, &value.Status, &value.ErrorCode, &value.ExecutorOwner); err != nil {
			return ExecutionPage{}, err
		}
		result.Rows = append(result.Rows, value)
	}
	return result, rows.Err()
}

func scanDefinition(scanner rowScanner) (Definition, error) {
	record, err := scanDefinitionRecord(scanner)
	if err != nil {
		return Definition{}, err
	}
	return record.definition()
}

func scanDefinitionRecord(scanner rowScanner) (definitionRecord, error) {
	var value definitionRecord
	err := scanner.Scan(&value.ID, &value.Name, &value.NameKey, &value.TaskType, &value.ScheduleJSON, &value.ParametersJSON, &value.Enabled, &value.Revision, &value.NextRunAt, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (value definitionRecord) definition() (Definition, error) {
	schedule, err := unmarshalSchedule(value.ScheduleJSON)
	if err != nil {
		return Definition{}, err
	}
	parameters, err := decodeParameterMap(value.ParametersJSON)
	if err != nil {
		return Definition{}, err
	}
	result := Definition{ID: value.ID, Name: value.Name, TaskType: value.TaskType, Schedule: schedule, Parameters: parameters, Enabled: value.Enabled, Revision: value.Revision, CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC()}
	if value.NextRunAt.Valid {
		next := value.NextRunAt.Time.UTC()
		result.NextRunAt = &next
	}
	return result, nil
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

func nullableTime(value sql.NullTime) any {
	if value.Valid {
		return value.Time
	}
	return nil
}

func normalizedNameKey(value string) string {
	const hexadecimal = "0123456789abcdef"
	bytes := []byte(strings.ToLower(value))
	var result strings.Builder
	result.Grow(len(bytes) * 3)
	for _, value := range bytes {
		result.WriteByte(hexadecimal[value>>4])
		result.WriteByte(hexadecimal[value&0x0f])
		result.WriteByte('.')
	}
	return result.String()
}
