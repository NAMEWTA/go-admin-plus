package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizedContextErrorPreservesOnlyCancellationClass(t *testing.T) {
	t.Parallel()

	for name, cause := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			err := sanitizedContextError(context.Background(), "database check failed", fmt.Errorf("private dsn: %w", cause))
			if !errors.Is(err, cause) {
				t.Fatalf("error = %v, want cause %v", err, cause)
			}
			if strings.Contains(err.Error(), "private dsn") {
				t.Fatalf("error exposed source diagnostic: %v", err)
			}
		})
	}
}
