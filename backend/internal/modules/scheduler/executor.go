package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/coordination"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const taskSavepoint = "scheduler_task_effect"

type ExecutorConfig struct {
	Owner       string
	BatchSize   int
	TaskTimeout time.Duration
	Clock       Clock
	Observer    Observer
}

type ExecuteResult struct {
	Triggered int
	Succeeded int
	Failed    int
}

type Executor struct {
	db         *database.Database
	repository repository
	registry   *Registry
	config     ExecutorConfig
}

type transactionExecutor interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
}

func NewExecutor(db *database.Database, registry *Registry, config ExecutorConfig) (*Executor, error) {
	if db == nil || registry == nil || !ownerPattern(config.Owner) || config.BatchSize < 1 || config.BatchSize > 100 || config.TaskTimeout <= 0 || config.TaskTimeout > time.Hour || config.Clock == nil {
		return nil, errors.New("scheduler executor config is invalid")
	}
	if _, err := utcNow(config.Clock); err != nil {
		return nil, errors.New("scheduler executor clock is invalid")
	}
	return &Executor{db: db, repository: repository{dialect: db.Dialect()}, registry: registry, config: config}, nil
}

// RunAvailable 通过数据库行锁/CAS 认领任务。多个 PostgreSQL worker 可以并行处理不同任务。
func (e *Executor) RunAvailable(ctx context.Context) (ExecuteResult, error) {
	return e.runOnce(ctx, e.db)
}

// RunOnce 保留显式协调边界供需要绑定运行租约的调用者使用；任务执行本身不持有协调事务。
func (e *Executor) RunOnce(ctx context.Context, lease *coordination.Lease) (ExecuteResult, error) {
	if e == nil || lease == nil {
		return ExecuteResult{}, errors.New("scheduler coordination lease is required")
	}
	if err := lease.Authorize(e.db, e.config.Owner); err != nil {
		return ExecuteResult{}, err
	}
	return e.RunAvailable(ctx)
}

type runClaim struct {
	ID, Token string
	Record    definitionRecord
	Scheduled time.Time
	Attempts  int
}

func (e *Executor) runOnce(ctx context.Context, transactions transactionExecutor) (ExecuteResult, error) {
	result := ExecuteResult{}
	for result.Triggered < e.config.BatchSize {
		now, err := utcNow(e.config.Clock)
		if err != nil {
			return result, ErrInternal
		}
		var claim runClaim
		err = transactions.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
			// 超时的认领可重试；业务失败在 finish 中直接终结，不自动重试。
			query := `SELECT id, definition_id, definition_revision, task_type, parameters_json, scheduled_for, attempts FROM scheduler_runs WHERE state = 'queued' OR (state = 'running' AND lease_until <= ?) ORDER BY scheduled_for, id LIMIT 1`
			if e.db.Dialect() == database.DialectPostgres {
				query += ` FOR UPDATE SKIP LOCKED`
			}
			err := tx.QueryRowContext(ctx, query, now).Scan(&claim.ID, &claim.Record.ID, &claim.Record.Revision, &claim.Record.TaskType, &claim.Record.ParametersJSON, &claim.Scheduled, &claim.Attempts)
			if errors.Is(err, sql.ErrNoRows) {
				record, dueErr := e.repository.dueDefinition(ctx, tx, now)
				if dueErr != nil {
					return dueErr
				}
				claim = runClaim{ID: uuid.NewString(), Record: record, Scheduled: record.NextRunAt.Time.UTC()}
				if _, err := tx.ExecContext(ctx, `INSERT INTO scheduler_runs(id, definition_id, definition_revision, task_type, parameters_json, scheduled_for, state, created_at) VALUES (?, ?, ?, ?, ?, ?, 'queued', ?)`, claim.ID, record.ID, record.Revision, record.TaskType, record.ParametersJSON, claim.Scheduled, now); err != nil {
					return err
				}
				schedule, err := unmarshalSchedule(record.ScheduleJSON)
				if err != nil {
					return err
				}
				next, ok := nextOccurrence(schedule, now)
				if !ok {
					return ErrInternal
				}
				if err := e.repository.advanceDefinition(ctx, tx, record.ID, next, now); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			claim.Token = uuid.NewString()
			claim.Attempts++
			_, err = tx.ExecContext(ctx, `UPDATE scheduler_runs SET state = 'running', claim_token = ?, lease_until = ?, attempts = ? WHERE id = ?`, claim.Token, now.Add(e.config.TaskTimeout+time.Minute), claim.Attempts, claim.ID)
			return err
		})
		if errors.Is(err, sql.ErrNoRows) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		result.Triggered++
		status, code := ExecutionFailed, "retry_exhausted"
		if claim.Attempts <= 3 {
			// Handler 的事务只覆盖其所属数据修改，不包含调度扫描、下次时间计算或网络 I/O。
			// 执行 ID 在故障恢复时不变；外部副作用应由 handler 写入 outbox 后异步发送。
			err = e.db.WithinTx(context.WithValue(ctx, executionIDKey{}, claim.ID), func(ctx context.Context, tx database.Tx) error {
				var token string
				lockDefinition := `SELECT id FROM scheduler_definitions WHERE id=?`
				if e.db.Dialect() == database.DialectPostgres {
					lockDefinition += ` FOR UPDATE`
				}
				var definitionID string
				if err := tx.QueryRowContext(ctx, lockDefinition, claim.Record.ID).Scan(&definitionID); err != nil {
					return err
				}
				q := `SELECT claim_token FROM scheduler_runs WHERE id = ? AND state = 'running'`
				if e.db.Dialect() == database.DialectPostgres {
					q += ` FOR UPDATE`
				}
				if err := tx.QueryRowContext(ctx, q, claim.ID).Scan(&token); err != nil {
					return err
				}
				if token != claim.Token {
					return coordination.ErrLeaseLost
				}
				var err error
				status, code, err = e.runTask(ctx, tx, claim.Record)
				if err != nil {
					return err
				}
				return e.finish(ctx, tx, claim, now, status, code)
			})
		} else {
			err = e.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error { return e.finish(ctx, tx, claim, now, status, code) })
		}
		if err != nil {
			return result, err
		}
		if status == ExecutionSucceeded {
			result.Succeeded++
		} else {
			result.Failed++
		}
	}
	return result, nil
}

func (e *Executor) finish(ctx context.Context, tx database.Tx, claim runClaim, started time.Time, status ExecutionStatus, code string) error {
	finished, err := utcNow(e.config.Clock)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_runs SET state = ?, claim_token = NULL, lease_until = NULL WHERE id = ? AND claim_token = ? AND state = 'running'`, string(status), claim.ID, claim.Token)
	if err := exactlyOne(result, err); err != nil {
		return err
	}
	// 完成后跳过运行期间错过的触发点，禁止积压补跑形成风暴。
	var enabled bool
	var scheduleJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT enabled,schedule_json FROM scheduler_definitions WHERE id=?`, claim.Record.ID).Scan(&enabled, &scheduleJSON); err != nil {
		return err
	}
	if enabled {
		schedule, err := unmarshalSchedule(scheduleJSON)
		if err != nil {
			return err
		}
		next, ok := nextOccurrence(schedule, finished)
		if !ok {
			return ErrInternal
		}
		if err := e.repository.advanceDefinition(ctx, tx, claim.Record.ID, next, finished); err != nil {
			return err
		}
	}
	return e.repository.insertExecution(ctx, tx, Execution{ID: claim.ID, DefinitionID: claim.Record.ID, DefinitionRevision: claim.Record.Revision, TaskType: claim.Record.TaskType, ScheduledFor: claim.Scheduled, StartedAt: started, FinishedAt: finished, Status: status, ErrorCode: code, ExecutorOwner: e.config.Owner})
}

type executionIDKey struct{}

// ExecutionID 是稳定的幂等键，同一次执行在进程崩溃后的重试中保持不变。
func ExecutionID(ctx context.Context) string {
	value, _ := ctx.Value(executionIDKey{}).(string)
	return value
}

func (e *Executor) runTask(ctx context.Context, tx database.Tx, record definitionRecord) (ExecutionStatus, string, error) {
	task, exists := e.registry.task(record.TaskType)
	if !exists {
		return ExecutionFailed, "task_unregistered", nil
	}
	if _, err := task.normalize(record.ParametersJSON); err != nil {
		return ExecutionFailed, "parameters_invalid", nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT `+taskSavepoint); err != nil {
		return "", "", err
	}
	taskContext, cancel := context.WithTimeout(ctx, e.config.TaskTimeout)
	err := safelyRun(taskContext, task, tx, record.ParametersJSON)
	taskContextErr := taskContext.Err()
	cancel()
	if taskContextErr == nil {
		if afterCancel := taskContext.Err(); errors.Is(afterCancel, context.DeadlineExceeded) {
			taskContextErr = afterCancel
		}
	}
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if err == nil && taskContextErr == nil {
		if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+taskSavepoint); releaseErr != nil {
			return "", "", releaseErr
		}
		return ExecutionSucceeded, "", nil
	}
	if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT `+taskSavepoint); rollbackErr != nil {
		return "", "", rollbackErr
	}
	if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+taskSavepoint); releaseErr != nil {
		return "", "", releaseErr
	}
	var taskFailure TaskFailure
	switch {
	case errors.Is(taskContextErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return ExecutionFailed, "task_timeout", nil
	case errors.As(err, &taskFailure) && errorCodePattern.MatchString(taskFailure.Code):
		return ExecutionFailed, taskFailure.Code, nil
	default:
		return ExecutionFailed, "task_failed", nil
	}
}

func safelyRun(ctx context.Context, task registeredTask, tx database.Tx, parameters []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("scheduler task panicked")
		}
	}()
	return task.run(ctx, tx, parameters)
}

type RunOptions struct {
	PollInterval   time.Duration
	FailureBackoff time.Duration
	Wait           func(context.Context, time.Duration) error
}

func (e *Executor) Run(ctx context.Context, lease *coordination.Lease, options RunOptions) error {
	if options.PollInterval <= 0 || options.FailureBackoff <= 0 {
		return errors.New("scheduler loop options are invalid")
	}
	wait := options.Wait
	if wait == nil {
		wait = waitFor
	}
	for {
		_, err := e.RunOnce(ctx, lease)
		if errors.Is(err, coordination.ErrLeaseLost) || errors.Is(err, coordination.ErrNotLeader) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		delay := options.PollInterval
		if err != nil {
			delay = options.FailureBackoff
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}

func waitFor(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *Executor) observe(outcome string, triggered int, lost bool) {
	if e.config.Observer != nil {
		e.config.Observer.Observe(Observation{Outcome: outcome, Triggered: triggered, ActiveExecutor: true, LostLock: lost})
	}
}

func (e *Executor) observeLease(err error, triggered int) {
	if errors.Is(err, coordination.ErrLeaseLost) || errors.Is(err, coordination.ErrNotLeader) {
		e.observe(OutcomeLeaseLost, triggered, true)
	}
}

func ownerPattern(owner string) bool {
	if owner == "" || len(owner) > 255 {
		return false
	}
	for index, value := range owner {
		if index == 0 && !asciiAlphaNumeric(value) || index > 0 && !(asciiAlphaNumeric(value) || value == '.' || value == '_' || value == ':' || value == '/' || value == '-') {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value rune) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}
