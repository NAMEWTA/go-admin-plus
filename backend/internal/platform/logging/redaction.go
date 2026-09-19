package logging

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode"
)

type redactingHandler struct{ next slog.Handler }

func (handler redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, redactString(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(redactAttr(attr))
		return true
	})
	return handler.next.Handle(ctx, clean)
}

func (handler redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		clean = append(clean, redactAttr(attr))
	}
	return redactingHandler{next: handler.next.WithAttrs(clean)}
}

func (handler redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: handler.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	key := normalizeKey(attr.Key)
	if key == "error" || key == "err" || strings.HasSuffix(key, "error") {
		return slog.String(attr.Key, errorClass(attr.Value))
	}
	if sensitiveKey(key) {
		return slog.String(attr.Key, "[redacted]")
	}
	if attr.Value.Kind() == slog.KindGroup {
		children := attr.Value.Group()
		clean := make([]slog.Attr, 0, len(children))
		for _, child := range children {
			clean = append(clean, redactAttr(child))
		}
		return slog.Group(attr.Key, attrsToAny(clean)...)
	}
	if attr.Value.Kind() == slog.KindString {
		return slog.String(attr.Key, redactString(attr.Value.String()))
	}
	if attr.Value.Kind() == slog.KindAny {
		return slog.String(attr.Key, "[redacted]")
	}
	return attr
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for index := range attrs {
		values[index] = attrs[index]
	}
	return values
}

func normalizeKey(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, value)
}

func sensitiveKey(key string) bool {
	for _, fragment := range []string{"password", "passwd", "secret", "token", "cookie", "csrf", "session", "authorization", "requestbody", "responsebody", "dsn", "databaseurl", "connectionstring", "privatekey"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func redactString(value string) string {
	lower := strings.ToLower(value)
	for _, marker := range []string{"postgres://", "postgresql://", "mysql://", "mongodb://", "authorization:", "cookie:", "password=", "passwd=", "secret=", "token=", `"secret":`, "csrf"} {
		if strings.Contains(lower, marker) {
			return "[redacted]"
		}
	}
	return value
}

func errorClass(value slog.Value) string {
	if value.Kind() == slog.KindAny {
		if err, ok := value.Any().(error); ok {
			switch {
			case errors.Is(err, context.Canceled):
				return "canceled"
			case errors.Is(err, context.DeadlineExceeded):
				return "timeout"
			}
		}
	}
	return "internal"
}
