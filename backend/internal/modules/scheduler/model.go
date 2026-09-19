// Package scheduler owns controlled task definitions and execution history.
package scheduler

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	PermissionDefinitionsRead   = "scheduler.definitions.read"
	PermissionDefinitionsWrite  = "scheduler.definitions.write"
	PermissionDefinitionsDelete = "scheduler.definitions.delete"
	PermissionExecutionsRead    = "scheduler.executions.read"
	maximumPage                 = 1_000_000
	maximumParametersBytes      = 16 << 10
	minimumSafeInteger          = -9_007_199_254_740_991
	maximumSafeInteger          = 9_007_199_254_740_991
)

var (
	ErrDenied        = errors.New("scheduler authorization denied")
	ErrValidation    = errors.New("scheduler request invalid")
	ErrNotFound      = errors.New("scheduler resource not found")
	ErrConflict      = errors.New("scheduler resource conflict")
	ErrInternal      = errors.New("scheduler operation failed")
	taskKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9.-]{2,126}$`)
	errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
)

type Clock interface{ Now() time.Time }
type ClockFunc func() time.Time

func (clock ClockFunc) Now() time.Time { return clock() }

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type Authorizer interface {
	RequireInTx(context.Context, database.Tx, string, string) (AuthorizationDecision, error)
}

type Scope string

const (
	ScopeSelf Scope = "self"
	ScopeAll  Scope = "all"
)

type AuthorizationDecision struct{ Scope Scope }

type Definition struct {
	ID, Name, TaskType   string
	Schedule             Schedule
	Parameters           map[string]any
	Enabled              bool
	Revision             int64
	NextRunAt            *time.Time
	CreatedAt, UpdatedAt time.Time
}

type DefinitionInput struct {
	Name, TaskType string
	Schedule       Schedule
	Parameters     map[string]any
}

type DefinitionPage struct {
	Rows  []Definition
	Total int
}

type ExecutionStatus string

const (
	ExecutionSucceeded ExecutionStatus = "succeeded"
	ExecutionFailed    ExecutionStatus = "failed"
)

type Execution struct {
	ID, DefinitionID, TaskType, ExecutorOwner string
	DefinitionRevision                        int64
	ScheduledFor, StartedAt, FinishedAt       time.Time
	Status                                    ExecutionStatus
	ErrorCode                                 string
}

type ExecutionPage struct {
	Rows  []Execution
	Total int
}
type DefinitionQuery struct {
	Search         string
	Page, PageSize int
}
type ExecutionQuery struct {
	DefinitionID   string
	Status         ExecutionStatus
	Page, PageSize int
}

type TaskFailure struct{ Code string }

func (failure TaskFailure) Error() string { return "scheduler task failed" }
func NewTaskFailure(code string) error {
	if !errorCodePattern.MatchString(code) {
		return ErrValidation
	}
	return TaskFailure{Code: code}
}

type Observer interface{ Observe(Observation) }
type ObserveFunc func(Observation)

func (function ObserveFunc) Observe(value Observation) { function(value) }

type Observation struct {
	Outcome                  string
	Triggered                int
	ActiveExecutor, LostLock bool
}

const (
	OutcomeIdle       = "idle"
	OutcomeExecuted   = "executed"
	OutcomeFailed     = "failed"
	OutcomeDependency = "dependency_failure"
	OutcomeLeaseLost  = "lease_lost"
)
