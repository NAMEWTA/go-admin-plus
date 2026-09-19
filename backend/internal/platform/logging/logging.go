// Package logging builds profile-specific slog sinks with mandatory identity fields and redaction.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"unicode"
)

type Mode string

const (
	ModeJSON         Mode = "json"
	ModeConsole      Mode = "console"
	ModeRotatingFile Mode = "rotating-file"
)

type Sink struct {
	Mode         Mode
	Writer       io.Writer
	Fallback     io.Writer
	Directory    string
	Filename     string
	MaximumBytes int64
	Backups      int
}

type Config struct {
	Service, Version, Profile, Level string
	Sink                             Sink
}

func New(config Config) (*slog.Logger, io.Closer, error) {
	if !validIdentity(config.Service, 64) || !validIdentity(config.Version, 128) || !validProfile(config.Profile) {
		return nil, nil, errors.New("logging identity is invalid")
	}
	level, ok := parseLevel(config.Level)
	if !ok {
		return nil, nil, errors.New("logging level is invalid")
	}
	options := &slog.HandlerOptions{Level: level}
	var target slog.Handler
	closer := io.Closer(noopCloser{})
	switch config.Sink.Mode {
	case ModeJSON:
		writer := config.Sink.Writer
		if writer == nil {
			writer = os.Stdout
		}
		target = slog.NewJSONHandler(writer, options)
	case ModeConsole:
		writer := config.Sink.Writer
		if writer == nil {
			writer = os.Stdout
		}
		target = slog.NewTextHandler(writer, options)
	case ModeRotatingFile:
		writer, err := newRotatingWriter(config.Sink)
		if err != nil {
			return nil, nil, err
		}
		closer = writer
		target = slog.NewJSONHandler(writer, options)
	default:
		return nil, nil, errors.New("logging sink mode is invalid")
	}
	fallback := config.Sink.Fallback
	if fallback == nil {
		fallback = os.Stderr
	}
	handler := redactingHandler{next: fallbackHandler{next: target, sink: &fallbackSink{writer: fallback}}}
	logger := slog.New(handler).With(
		slog.String("service", config.Service),
		slog.String("version", config.Version),
		slog.String("profile", config.Profile),
	)
	return logger, closer, nil
}

func validIdentity(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func validProfile(profile string) bool {
	return profile == "server-postgres" || profile == "server-sqlite" || profile == "desktop-sqlite"
}

func parseLevel(value string) (slog.Level, bool) {
	switch value {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

type fallbackSink struct {
	mu     sync.Mutex
	writer io.Writer
}

func (sink *fallbackSink) write() {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	_, _ = io.WriteString(sink.writer, "{\"level\":\"ERROR\",\"msg\":\"log_sink_failed\"}\n")
}

type fallbackHandler struct {
	next slog.Handler
	sink *fallbackSink
}

func (handler fallbackHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler fallbackHandler) Handle(ctx context.Context, record slog.Record) error {
	if err := handler.next.Handle(ctx, record); err != nil {
		handler.sink.write()
		return nil
	}
	return nil
}

func (handler fallbackHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fallbackHandler{next: handler.next.WithAttrs(attrs), sink: handler.sink}
}

func (handler fallbackHandler) WithGroup(name string) slog.Handler {
	return fallbackHandler{next: handler.next.WithGroup(name), sink: handler.sink}
}
