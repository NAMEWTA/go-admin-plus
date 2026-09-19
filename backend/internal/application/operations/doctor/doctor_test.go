package doctor_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/application/operations/doctor"
)

func TestRunnerReturnsStableOrderedMachineReportWithoutErrorDetails(t *testing.T) {
	runner, err := doctor.New(doctor.Config{Profile: "server-postgres", Version: "v-test", Timeout: time.Second},
		doctor.Check{Name: doctor.CheckConfiguration, Critical: true, FailureClass: doctor.ClassInvalidConfiguration, Run: func(context.Context) error { return nil }},
		doctor.Check{Name: doctor.CheckDatabase, Critical: true, FailureClass: doctor.ClassUnavailable, Run: func(context.Context) error {
			return errors.New("postgres://operator:doctor-secret@example.invalid/private")
		}},
		doctor.Check{Name: doctor.CheckWorker, Critical: false, FailureClass: doctor.ClassUnavailable, Run: func(context.Context) error { return nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	report := runner.Run(context.Background())
	if report.Exit != doctor.ExitUnavailable || len(report.Checks) != 3 || report.Checks[0].Name != doctor.CheckConfiguration || report.Checks[1].Name != doctor.CheckDatabase {
		t.Fatalf("report=%#v", report)
	}
	if report.Checks[1].Status != doctor.StatusFailed || report.Checks[1].Class != doctor.ClassUnavailable {
		t.Fatalf("database result=%#v", report.Checks[1])
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "doctor-secret") || strings.Contains(string(encoded), "postgres://") {
		t.Fatalf("report leaked checker error: %s", encoded)
	}
}

func TestRunnerBoundsCheckerTimeoutAndClassifiesNonCriticalFailure(t *testing.T) {
	runner, err := doctor.New(doctor.Config{Profile: "desktop-sqlite", Version: "v-test", Timeout: 80 * time.Millisecond},
		doctor.Check{Name: doctor.CheckDisk, Critical: true, Timeout: 20 * time.Millisecond, Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		doctor.Check{Name: doctor.CheckFilesRoot, Critical: false, FailureClass: doctor.ClassUnavailable, Run: func(context.Context) error { return errors.New("private path") }},
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	report := runner.Run(context.Background())
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("doctor was not bounded: %s", elapsed)
	}
	if report.Exit != doctor.ExitUnavailable || report.Checks[0].Class != doctor.ClassTimeout || report.Checks[1].Status != doctor.StatusWarning {
		t.Fatalf("report=%#v", report)
	}
}

func TestRunnerValidationAndExitClasses(t *testing.T) {
	valid := doctor.Check{Name: doctor.CheckVersion, Run: func(context.Context) error { return nil }}
	for _, test := range []struct {
		name   string
		config doctor.Config
		checks []doctor.Check
	}{
		{"profile", doctor.Config{Profile: "unknown", Version: "v1", Timeout: time.Second}, []doctor.Check{valid}},
		{"version", doctor.Config{Profile: "server-sqlite", Timeout: time.Second}, []doctor.Check{valid}},
		{"duplicate", doctor.Config{Profile: "server-sqlite", Version: "v1", Timeout: time.Second}, []doctor.Check{valid, valid}},
		{"unknown check", doctor.Config{Profile: "server-sqlite", Version: "v1", Timeout: time.Second}, []doctor.Check{{Name: "private", Run: valid.Run}}},
		{"nil check", doctor.Config{Profile: "server-sqlite", Version: "v1", Timeout: time.Second}, []doctor.Check{{Name: doctor.CheckSchema}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := doctor.New(test.config, test.checks...); err == nil {
				t.Fatal("invalid runner accepted")
			}
		})
	}
	runner, err := doctor.New(doctor.Config{Profile: "server-sqlite", Version: "v1", Timeout: time.Second}, valid)
	if err != nil || runner.Run(context.Background()).Exit != doctor.ExitHealthy {
		t.Fatalf("healthy runner err=%v", err)
	}
}
