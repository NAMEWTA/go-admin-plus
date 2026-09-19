package database

import (
	"errors"
	"testing"
)

type fakeLock struct {
	locked   bool
	lockErr  error
	unlocked bool
}

func (f *fakeLock) TryLock() (bool, error) { return f.locked, f.lockErr }
func (f *fakeLock) Unlock() error {
	f.unlocked = true
	return nil
}

func TestAcquireInstanceLock(t *testing.T) {
	t.Parallel()

	t.Run("conflict", func(t *testing.T) {
		_, err := acquireInstanceLock(&fakeLock{})
		if !errors.Is(err, ErrInstanceLocked) {
			t.Fatalf("error = %v, want ErrInstanceLocked", err)
		}
	})
	t.Run("diagnostic is sanitized", func(t *testing.T) {
		_, err := acquireInstanceLock(&fakeLock{lockErr: errors.New("/private/secret.db.lock")})
		if err == nil || err.Error() != "sqlite instance lock failed" {
			t.Fatalf("error = %v, want sanitized failure", err)
		}
	})
	t.Run("release", func(t *testing.T) {
		owner := &fakeLock{locked: true}
		release, err := acquireInstanceLock(owner)
		if err != nil {
			t.Fatalf("acquireInstanceLock() error = %v", err)
		}
		if err := release(); err != nil {
			t.Fatalf("release() error = %v", err)
		}
		if !owner.unlocked {
			t.Fatal("release did not unlock owner")
		}
	})
}
