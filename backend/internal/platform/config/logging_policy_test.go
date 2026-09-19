package config

import (
	"log/slog"
	"testing"
)

func TestLoggingPolicyMapsProfilesAndDevelopmentMode(t *testing.T) {
	for _, test := range []struct {
		profile     Profile
		development bool
		wantMode    LogMode
	}{
		{ProfileServerPostgres, false, LogModeJSON},
		{ProfileServerSQLite, false, LogModeJSON},
		{ProfileDesktopSQLite, false, LogModeRotatingFile},
		{ProfileServerSQLite, true, LogModeConsole},
	} {
		policy, err := NewLoggingPolicy(test.profile, "warn", test.development)
		if err != nil || policy.Mode() != test.wantMode || policy.Level() != slog.LevelWarn || policy.MaximumBytes() <= 0 || policy.Backups() <= 0 {
			t.Fatalf("policy=%#v err=%v", policy, err)
		}
	}
}

func TestLoggingPolicyRejectsUnsupportedValuesAndRedactsFormatting(t *testing.T) {
	if _, err := NewLoggingPolicy("unknown", "info", false); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if _, err := NewLoggingPolicy(ProfileServerSQLite, "verbose-secret", false); err == nil {
		t.Fatal("unknown level accepted")
	}
	policy, err := NewLoggingPolicy(ProfileDesktopSQLite, "debug", false)
	if err != nil {
		t.Fatal(err)
	}
	if policy.String() != "logging policy redacted" || policy.GoString() != "config.LoggingPolicy{redacted}" {
		t.Fatalf("unsafe formatting: %s / %#v", policy, policy)
	}
}
