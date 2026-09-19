package protection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	accountKind = "account"
	sourceKind  = "source"
)

type Source struct{ digest string }

func NewSource(value string) (Source, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return Source{}, errors.New("trusted login source is invalid")
	}
	return Source{digest: digest(value)}, nil
}

func DefaultSource() Source       { return Source{digest: digest("legacy-http-source")} }
func (Source) String() string     { return "session.protection.Source{[redacted]}" }
func (s Source) GoString() string { return s.String() }

type Policy struct {
	AccountLimit int64
	SourceLimit  int64
	Window       time.Duration
}

func DefaultPolicy() Policy {
	return Policy{AccountLimit: 5, SourceLimit: 20, Window: 15 * time.Minute}
}

func (p Policy) Validate() error {
	if p.AccountLimit < 1 || p.AccountLimit > 100 || p.SourceLimit < p.AccountLimit || p.SourceLimit > 1000 || p.Window < time.Minute || p.Window > 24*time.Hour {
		return errors.New("login protection policy is invalid")
	}
	return nil
}

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type Repository struct {
	dialect database.Dialect
	policy  Policy
}

func NewRepository(dialect database.Dialect, policy Policy) (*Repository, error) {
	if dialect != database.DialectSQLite && dialect != database.DialectPostgres {
		return nil, errors.New("login protection dialect is unsupported")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Repository{dialect: dialect, policy: policy}, nil
}

func (r *Repository) Consume(ctx context.Context, tx database.Tx, accountKey string, source Source, now time.Time) (Decision, error) {
	if source.digest == "" {
		return Decision{}, errors.New("trusted login source is required")
	}
	accountDecision, err := r.consumeOne(ctx, tx, accountKind, digest(strings.ToLower(strings.TrimSpace(accountKey))), r.policy.AccountLimit, now.UTC())
	if err != nil {
		return Decision{}, err
	}
	sourceDecision, err := r.consumeOne(ctx, tx, sourceKind, source.digest, r.policy.SourceLimit, now.UTC())
	if err != nil {
		return Decision{}, err
	}
	decision := Decision{Allowed: accountDecision.Allowed && sourceDecision.Allowed}
	if accountDecision.RetryAfter > decision.RetryAfter {
		decision.RetryAfter = accountDecision.RetryAfter
	}
	if sourceDecision.RetryAfter > decision.RetryAfter {
		decision.RetryAfter = sourceDecision.RetryAfter
	}
	return decision, nil
}

func (r *Repository) consumeOne(ctx context.Context, tx database.Tx, kind, key string, limit int64, now time.Time) (Decision, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO iam_login_buckets
		(kind, key_hash, window_started_at, attempt_count, updated_at)
		VALUES (?, ?, ?, 0, ?) ON CONFLICT (kind, key_hash) DO NOTHING`, kind, key, now, now)
	if err != nil {
		return Decision{}, normalizeError(err, "login protection update failed")
	}
	query := `SELECT window_started_at, attempt_count, blocked_until FROM iam_login_buckets WHERE kind = ? AND key_hash = ?`
	if r.dialect == database.DialectPostgres {
		query += " FOR UPDATE"
	}
	var started time.Time
	var count int64
	var blocked sql.NullTime
	if err := tx.QueryRowContext(ctx, query, kind, key).Scan(&started, &count, &blocked); err != nil {
		return Decision{}, normalizeError(err, "login protection lookup failed")
	}
	if blocked.Valid && now.Before(blocked.Time) {
		return Decision{RetryAfter: coarseRetry(blocked.Time.Sub(now))}, nil
	}
	if !now.Before(started.Add(r.policy.Window)) {
		started, count, blocked = now, 0, sql.NullTime{}
	}
	count++
	if count > limit {
		blocked = sql.NullTime{Time: now.Add(r.policy.Window), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `UPDATE iam_login_buckets SET window_started_at = ?, attempt_count = ?, blocked_until = ?, updated_at = ? WHERE kind = ? AND key_hash = ?`, started, count, blocked, now, kind, key)
	if err != nil {
		return Decision{}, normalizeError(err, "login protection update failed")
	}
	if blocked.Valid {
		return Decision{RetryAfter: coarseRetry(blocked.Time.Sub(now))}, nil
	}
	return Decision{Allowed: true}, nil
}

func coarseRetry(value time.Duration) time.Duration {
	if value < time.Minute {
		return time.Minute
	}
	return ((value + time.Minute - 1) / time.Minute) * time.Minute
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeError(err error, fallback string) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(fallback)
}
