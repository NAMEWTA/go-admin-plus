package config

import (
	"fmt"
	"log/slog"
)

const (
	DefaultLogMaximumBytes int64 = 10 * 1024 * 1024
	DefaultLogBackups            = 5
)

type LogMode string

const (
	LogModeJSON         LogMode = "json"
	LogModeConsole      LogMode = "console"
	LogModeRotatingFile LogMode = "rotating-file"
)

type LoggingPolicy struct {
	level        slog.Level
	mode         LogMode
	maximumBytes int64
	backups      int
}

func NewLoggingPolicy(profile Profile, level string, development bool) (LoggingPolicy, error) {
	if err := validateLogLevel(level); err != nil {
		return LoggingPolicy{}, err
	}
	parsed := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}[level]
	mode := LogModeJSON
	if development {
		mode = LogModeConsole
	} else if profile == ProfileDesktopSQLite {
		mode = LogModeRotatingFile
	}
	if profile != ProfileServerPostgres && profile != ProfileServerSQLite && profile != ProfileDesktopSQLite {
		return LoggingPolicy{}, fmt.Errorf("configuration logging.profile: unsupported profile")
	}
	return LoggingPolicy{level: parsed, mode: mode, maximumBytes: DefaultLogMaximumBytes, backups: DefaultLogBackups}, nil
}

func (policy LoggingPolicy) Level() slog.Level   { return policy.level }
func (policy LoggingPolicy) Mode() LogMode       { return policy.mode }
func (policy LoggingPolicy) MaximumBytes() int64 { return policy.maximumBytes }
func (policy LoggingPolicy) Backups() int        { return policy.backups }
func (policy LoggingPolicy) String() string      { return "logging policy redacted" }
func (policy LoggingPolicy) GoString() string    { return "config.LoggingPolicy{redacted}" }
func (policy LoggingPolicy) LogValue() slog.Value {
	return slog.GroupValue(slog.String("values", "redacted"))
}
