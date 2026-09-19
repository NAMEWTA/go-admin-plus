package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizedMigrationErrorPreservesCancellationForEveryStage(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{"migration execution failed", "migration version check failed"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			err := sanitizedMigrationError(context.Background(), stage, fmt.Errorf("private sql value: %w", cause))
			if !errors.Is(err, cause) {
				t.Fatalf("%s error = %v, want cause %v", stage, err, cause)
			}
			if strings.Contains(err.Error(), "private sql value") {
				t.Fatalf("%s exposed source diagnostic: %v", stage, err)
			}
		}
	}
}
