package account

import (
	"errors"
	"sync"
)

const defaultPasswordWorkConcurrency = 2

var processPasswordWorkBudget = mustPasswordWorkBudget(defaultPasswordWorkConcurrency)

// PasswordWorkBudget bounds simultaneous memory-hard password operations.
type PasswordWorkBudget struct{ slots chan struct{} }

func NewPasswordWorkBudget(capacity int) (*PasswordWorkBudget, error) {
	if capacity < 1 || capacity > 8 {
		return nil, errors.New("password work capacity is invalid")
	}
	return &PasswordWorkBudget{slots: make(chan struct{}, capacity)}, nil
}

func ProcessPasswordWorkBudget() *PasswordWorkBudget { return processPasswordWorkBudget }

// TryAcquire is fail-fast. The returned release function is idempotent.
func (budget *PasswordWorkBudget) TryAcquire() (func(), bool) {
	if budget == nil {
		return func() {}, false
	}
	select {
	case budget.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-budget.slots }) }, true
	default:
		return func() {}, false
	}
}

func mustPasswordWorkBudget(capacity int) *PasswordWorkBudget {
	budget, err := NewPasswordWorkBudget(capacity)
	if err != nil {
		panic("invalid built-in password work capacity")
	}
	return budget
}
