package logging

import (
	"log/slog"
	"time"
)

// EventFields is the stable operational event schema shared by request and worker adapters.
// Empty request or job identifiers are retained so downstream processors do not infer fields.
type EventFields struct {
	TraceID, RequestID, JobID, Route, Module, Database, ErrorClass string
	Status                                                         int
	Latency                                                        time.Duration
}

func (fields EventFields) Attrs() []any {
	return []any{
		slog.String("trace_id", fields.TraceID),
		slog.String("request_id", fields.RequestID),
		slog.String("job_id", fields.JobID),
		slog.String("route", fields.Route),
		slog.String("module", fields.Module),
		slog.Int("status", fields.Status),
		slog.Int64("latency_ms", fields.Latency.Milliseconds()),
		slog.String("database", fields.Database),
		slog.String("error_class", fields.ErrorClass),
	}
}
