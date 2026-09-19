package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	audit "github.com/NAMEWTA/go-admin-plus/backend/internal/platform/auditing"
	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type Service struct {
	recorder   audit.OperationPort
	db         Database
	authorizer Authorizer
	registry   *Registry
	repository repository
	clock      Clock
}

type Option func(*Service)

func WithAuditRecorder(recorder audit.OperationPort) Option {
	return func(s *Service) { s.recorder = recorder }
}

func NewService(db Database, authorizer Authorizer, registry *Registry, clock Clock, options ...Option) (*Service, error) {
	if db == nil || authorizer == nil || registry == nil || clock == nil {
		return nil, errors.New("scheduler service dependencies are required")
	}
	if _, err := utcNow(clock); err != nil {
		return nil, err
	}
	service := &Service{db: db, authorizer: authorizer, registry: registry, repository: repository{dialect: db.Dialect()}, clock: clock}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

func (s *Service) TaskTypes(ctx context.Context, actorID string) ([]TaskType, error) {
	result := []TaskType{}
	err := s.transact(ctx, actorID, PermissionDefinitionsRead, func(context.Context, database.Tx) error {
		result = s.registry.TaskTypes()
		return nil
	})
	return result, err
}

func (s *Service) ListDefinitions(ctx context.Context, actorID string, query DefinitionQuery) (DefinitionPage, error) {
	query.Search = strings.TrimSpace(query.Search)
	if !validPage(query.Page, query.PageSize) || !validText(query.Search, 0, 100) {
		return DefinitionPage{}, ErrValidation
	}
	var result DefinitionPage
	err := s.transact(ctx, actorID, PermissionDefinitionsRead, func(ctx context.Context, tx database.Tx) error {
		var err error
		result, err = s.repository.definitions(ctx, tx, query)
		return err
	})
	return result, err
}

func (s *Service) CreateDefinition(ctx context.Context, actorID string, input DefinitionInput) (Definition, error) {
	value, record, err := s.prepareDefinition(uuid.NewString(), input, 1)
	if err != nil {
		return Definition{}, err
	}
	now, err := utcNow(s.clock)
	if err != nil {
		return Definition{}, err
	}
	record.CreatedAt, record.UpdatedAt = now, now
	value.CreatedAt, value.UpdatedAt = now, now
	err = s.transact(ctx, actorID, PermissionDefinitionsWrite, func(ctx context.Context, tx database.Tx) error {
		return s.repository.insertDefinition(ctx, tx, record)
	}, audit.Operation{Action: "create", ResourceType: "scheduler_task", ResourceID: record.ID, ActorID: actorID, Code: "CreateDefinition"})
	return value, err
}

func (s *Service) UpdateDefinition(ctx context.Context, actorID, id string, revision int64, input DefinitionInput) (Definition, error) {
	if !validID(id) || revision < 1 {
		return Definition{}, ErrValidation
	}
	value, record, err := s.prepareDefinition(id, input, revision+1)
	if err != nil {
		return Definition{}, err
	}
	now, err := utcNow(s.clock)
	if err != nil {
		return Definition{}, err
	}
	record.UpdatedAt, value.UpdatedAt = now, now
	err = s.transact(ctx, actorID, PermissionDefinitionsWrite, func(ctx context.Context, tx database.Tx) error {
		current, err := s.repository.definition(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if current.Revision != revision || current.Enabled {
			return ErrConflict
		}
		record.CreatedAt, value.CreatedAt = current.CreatedAt, current.CreatedAt
		return s.repository.updateDefinition(ctx, tx, record, revision)
	}, audit.Operation{Action: "update", ResourceType: "scheduler_task", ResourceID: id, ActorID: actorID, Code: "UpdateDefinition"})
	return value, err
}

func (s *Service) EnableDefinition(ctx context.Context, actorID, id string, revision int64) (Definition, error) {
	return s.changeState(ctx, actorID, id, revision, true)
}

func (s *Service) StopDefinition(ctx context.Context, actorID, id string, revision int64) (Definition, error) {
	return s.changeState(ctx, actorID, id, revision, false)
}

func (s *Service) changeState(ctx context.Context, actorID, id string, revision int64, enable bool) (Definition, error) {
	if !validID(id) || revision < 1 {
		return Definition{}, ErrValidation
	}
	now, err := utcNow(s.clock)
	if err != nil {
		return Definition{}, err
	}
	var result Definition
	err = s.transact(ctx, actorID, PermissionDefinitionsWrite, func(ctx context.Context, tx database.Tx) error {
		current, err := s.repository.definition(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if current.Revision != revision || current.Enabled == enable {
			return ErrConflict
		}
		if enable {
			schedule, err := unmarshalSchedule(current.ScheduleJSON)
			if err != nil {
				return err
			}
			next, ok := nextOccurrence(schedule, now)
			if !ok {
				return ErrInternal
			}
			if err := s.repository.enableDefinition(ctx, tx, id, revision, next, now); err != nil {
				return err
			}
			current.Enabled = true
			current.NextRunAt = sql.NullTime{Time: next, Valid: true}
		} else {
			if err := s.repository.stopDefinition(ctx, tx, id, revision, now); err != nil {
				return err
			}
			current.Enabled = false
			current.NextRunAt = sql.NullTime{}
		}
		current.Revision++
		current.UpdatedAt = now
		result, err = current.definition()
		return err
	}, audit.Operation{Action: "update", ResourceType: "scheduler_task", ResourceID: id, ActorID: actorID, Code: "changeState"})
	return result, err
}

func (s *Service) DeleteDefinition(ctx context.Context, actorID, id string, revision int64) error {
	if !validID(id) || revision < 1 {
		return ErrValidation
	}
	now, err := utcNow(s.clock)
	if err != nil {
		return err
	}
	return s.transact(ctx, actorID, PermissionDefinitionsDelete, func(ctx context.Context, tx database.Tx) error {
		current, err := s.repository.definition(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return ErrConflict
		}
		return s.repository.deleteDefinition(ctx, tx, id, revision, now)
	}, audit.Operation{Action: "delete", ResourceType: "scheduler_task", ResourceID: id, ActorID: actorID, Code: "DeleteDefinition"})
}

func (s *Service) ListExecutions(ctx context.Context, actorID string, query ExecutionQuery) (ExecutionPage, error) {
	if !validPage(query.Page, query.PageSize) || query.DefinitionID != "" && !validID(query.DefinitionID) || query.Status != "" && query.Status != ExecutionSucceeded && query.Status != ExecutionFailed {
		return ExecutionPage{}, ErrValidation
	}
	var result ExecutionPage
	err := s.transact(ctx, actorID, PermissionExecutionsRead, func(ctx context.Context, tx database.Tx) error {
		var err error
		result, err = s.repository.executions(ctx, tx, query)
		return err
	})
	return result, err
}

func (s *Service) prepareDefinition(id string, input DefinitionInput, revision int64) (Definition, definitionRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.TaskType = strings.TrimSpace(input.TaskType)
	if !validText(input.Name, 1, 100) || !taskKeyPattern.MatchString(input.TaskType) {
		return Definition{}, definitionRecord{}, ErrValidation
	}
	schedule, ok := normalizeSchedule(input.Schedule)
	if !ok {
		return Definition{}, definitionRecord{}, ErrValidation
	}
	raw, err := json.Marshal(input.Parameters)
	if err != nil || len(raw) > maximumParametersBytes {
		return Definition{}, definitionRecord{}, ErrValidation
	}
	canonical, err := s.registry.normalize(input.TaskType, raw)
	if err != nil {
		return Definition{}, definitionRecord{}, ErrValidation
	}
	parameters, err := decodeParameterMap(canonical)
	if err != nil {
		return Definition{}, definitionRecord{}, ErrInternal
	}
	scheduleJSON, err := marshalSchedule(schedule)
	if err != nil {
		return Definition{}, definitionRecord{}, ErrInternal
	}
	value := Definition{ID: id, Name: input.Name, TaskType: input.TaskType, Schedule: schedule, Parameters: parameters, Revision: revision}
	record := definitionRecord{ID: id, Name: input.Name, NameKey: normalizedNameKey(input.Name), TaskType: input.TaskType, ScheduleJSON: scheduleJSON, ParametersJSON: canonical, Revision: revision}
	return value, record, nil
}

func (s *Service) transact(ctx context.Context, actorID, permission string, operation func(context.Context, database.Tx) error, facts ...audit.Operation) error {
	if actorID == "" || operation == nil {
		return ErrDenied
	}
	err := s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		decision, err := s.authorizer.RequireInTx(ctx, tx, actorID, permission)
		if err != nil {
			return err
		}
		if decision.Scope != ScopeAll {
			return ErrDenied
		}
		if err := operation(ctx, tx); err != nil {
			return err
		}
		if len(facts) > 0 {
			return audit.Write(s.recorder, ctx, tx, facts[0])
		}
		return nil
	})
	if len(facts) > 0 {
		audit.RecordFailure(s.recorder, ctx, s.db, facts[0], err)
	}
	return s.normalize(ctx, err)
}

func (s *Service) normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	for _, stable := range []error{context.Canceled, context.DeadlineExceeded, ErrDenied, ErrNotFound, ErrValidation, ErrConflict} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if constraintConflict(s.db.Dialect(), err) {
		return ErrConflict
	}
	return ErrInternal
}

func constraintConflict(dialect database.Dialect, err error) bool {
	if dialect == database.DialectPostgres {
		var state interface{ SQLState() string }
		return errors.As(err, &state) && (state.SQLState() == "23505" || state.SQLState() == "23503" || state.SQLState() == "23514")
	}
	if dialect == database.DialectSQLite {
		var coded interface{ Code() int }
		return errors.As(err, &coded) && (coded.Code() == 1555 || coded.Code() == 2067 || coded.Code() == 787 || coded.Code() == 275)
	}
	return false
}

func utcNow(clock Clock) (time.Time, error) {
	if clock == nil {
		return time.Time{}, ErrInternal
	}
	now := clock.Now()
	if now.IsZero() || now.Location() != time.UTC {
		return time.Time{}, ErrInternal
	}
	return now, nil
}

func validPage(page, pageSize int) bool {
	return page >= 1 && page <= maximumPage && pageSize >= 1 && pageSize <= 100
}
func validID(id string) bool { _, err := uuid.Parse(id); return err == nil }
func runeLength(value string) int {
	if !utf8.ValidString(value) {
		return -1
	}
	return utf8.RuneCountInString(value)
}

func validText(value string, minimum, maximum int) bool {
	length := runeLength(value)
	if length < minimum || length > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// RunDefinition 固化参数快照并排队，不在 HTTP 请求中执行 handler。
func (s *Service) RunDefinition(ctx context.Context, actorID, id string, revision int64) (string, error) {
	if !validID(id) || revision < 1 {
		return "", ErrValidation
	}
	runID := uuid.NewString()
	err := s.transact(ctx, actorID, PermissionDefinitionsWrite, func(ctx context.Context, tx database.Tx) error {
		record, err := s.repository.definition(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if record.Revision != revision {
			return ErrConflict
		}
		now, err := utcNow(s.clock)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scheduler_runs(id, definition_id, definition_revision, task_type, parameters_json, scheduled_for, state, created_at) VALUES (?, ?, ?, ?, ?, ?, 'queued', ?)`, runID, id, record.Revision, record.TaskType, record.ParametersJSON, now, now)
		return err
	}, audit.Operation{Action: "create", ResourceType: "scheduler_run", ResourceID: runID, ActorID: actorID, Code: "RunDefinition"})
	return runID, err
}

// EnsureBuiltinDefinition 只初始化一次；用户停用或删除后不会在重启时被重新启用。
func (s *Service) EnsureBuiltinDefinition(ctx context.Context, id string, input DefinitionInput) error {
	_, record, err := s.prepareDefinition(id, input, 1)
	if err != nil {
		return err
	}
	now, err := utcNow(s.clock)
	if err != nil {
		return err
	}
	next, ok := nextOccurrence(input.Schedule, now)
	if !ok {
		return ErrValidation
	}
	record.CreatedAt = now
	record.UpdatedAt = now
	record.Enabled = true
	record.NextRunAt = sql.NullTime{Time: next, Valid: true}
	return s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_definitions WHERE id=?`, id).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		return s.repository.insertDefinition(ctx, tx, record)
	})
}
