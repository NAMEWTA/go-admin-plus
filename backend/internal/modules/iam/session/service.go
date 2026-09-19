package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/auditing"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session/protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

var (
	ErrAuthentication = errors.New("authentication required")
	ErrCredentials    = errors.New("credentials invalid")
	ErrCSRF           = errors.New("request authorization failed")
	ErrValidation     = errors.New("request validation failed")
	ErrConflict       = errors.New("request conflicts with current state")
	ErrInternal       = errors.New("iam operation failed")
	ErrRateLimited    = errors.New("login temporarily unavailable")
)

type rateLimitError struct{ retryAfter time.Duration }

func (e rateLimitError) Error() string        { return ErrRateLimited.Error() }
func (e rateLimitError) Is(target error) bool { return target == ErrRateLimited }

func RetryAfter(err error) (time.Duration, bool) {
	var limited rateLimitError
	if !errors.As(err, &limited) {
		return 0, false
	}
	return limited.retryAfter, true
}

var avatarReference = regexp.MustCompile(`^files/[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

type Database interface {
	WithinTx(context.Context, func(context.Context, database.Tx) error) error
	Dialect() database.Dialect
}

type LoginFactOutcome string
type LoginFactSource string

const (
	LoginSucceeded LoginFactOutcome = "succeeded"
	LoginFailed    LoginFactOutcome = "failed"
	LoginSourceWeb LoginFactSource  = "web"
)

// LoginAttemptID is generated inside Session. Its private representation prevents callers from
// substituting usernames or credential material for the stable Audit correlation identifier.
type LoginAttemptID struct{ value [16]byte }

func (id LoginAttemptID) Opaque() string { return hex.EncodeToString(id.value[:]) }
func (id LoginAttemptID) Valid() bool {
	var combined byte
	for _, value := range id.value {
		combined |= value
	}
	return combined != 0
}
func (LoginAttemptID) String() string      { return "session.LoginAttemptID{[opaque]}" }
func (id LoginAttemptID) GoString() string { return id.String() }

// LoginFact is deliberately closed: it cannot carry usernames, passwords, Session/CSRF tokens,
// request bodies, or arbitrary metadata.
type LoginFact struct {
	AttemptID  LoginAttemptID
	Outcome    LoginFactOutcome
	AccountID  string
	Source     LoginFactSource
	OccurredAt time.Time
}

type LoginFactPort interface {
	RecordLoginFact(context.Context, database.Tx, LoginFact) error
}

type Service struct {
	operationAudit auditing.OperationPort
	db             Database
	accounts       *account.Repository
	policy         config.SessionPolicy
	now            func() time.Time
	passwordWork   *account.PasswordWorkBudget
	protection     *protection.Repository
	loginFacts     LoginFactPort
	optionErr      error
	lockProbe      func(accountLockPoint)
}

type accountLockPoint uint8

const (
	accountLockHeld accountLockPoint = iota + 1
	accountRevokeLockRequested
	accountRevokeLockHeld
)

type Option func(*Service)

func WithOperationAudit(port auditing.OperationPort) Option {
	return func(s *Service) { s.operationAudit = port }
}

func WithClock(clock func() time.Time) Option { return func(s *Service) { s.now = clock } }
func WithPasswordWorkBudget(budget *account.PasswordWorkBudget) Option {
	return func(s *Service) { s.passwordWork = budget }
}
func WithLoginFactPort(port LoginFactPort) Option { return func(s *Service) { s.loginFacts = port } }
func WithLoginProtectionPolicy(policy protection.Policy) Option {
	return func(s *Service) {
		repository, err := protection.NewRepository(s.db.Dialect(), policy)
		if err != nil {
			s.optionErr = err
			return
		}
		s.protection = repository
	}
}

func NewService(db Database, policy config.SessionPolicy, options ...Option) (*Service, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if _, err := config.NewSessionPolicy(policy.IdleTimeout(), policy.AbsoluteTimeout(), policy.RotationInterval()); err != nil {
		return nil, err
	}
	loginProtection, err := protection.NewRepository(db.Dialect(), protection.DefaultPolicy())
	if err != nil {
		return nil, err
	}
	s := &Service{db: db, accounts: account.NewRepository(db.Dialect()), policy: policy, now: time.Now, passwordWork: account.ProcessPasswordWorkBudget(), protection: loginProtection}
	for _, option := range options {
		option(s)
	}
	if s.optionErr != nil {
		return nil, s.optionErr
	}
	if s.now == nil {
		return nil, errors.New("clock is required")
	}
	if s.passwordWork == nil {
		return nil, errors.New("password work budget is required")
	}
	if s.protection == nil {
		return nil, errors.New("login protection is required")
	}
	if s.loginFacts == nil {
		return nil, errors.New("login fact port is required")
	}
	return s, nil
}

type Issued struct {
	Profile account.Profile
	Token   string
	CSRF    string
	Rotated bool
}

func (i Issued) String() string   { return "session.Issued{Token:[redacted], CSRF:[redacted]}" }
func (i Issued) GoString() string { return i.String() }
func (i Issued) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Profile account.Profile `json:"profile"`
		Rotated bool            `json:"rotated"`
	}{Profile: i.Profile, Rotated: i.Rotated})
}

type record struct {
	ID, AccountID, TokenHash, CSRFHash, CSRFToken, State, FamilyID    string
	Generation                                                        int64
	CreatedAt, LastSeenAt, IdleExpiresAt, AbsoluteExpiresAt, RotateAt time.Time
}

func (s *Service) Login(ctx context.Context, username, password string) (Issued, error) {
	return s.LoginFrom(ctx, LoginCommand{Username: username, Password: password, Source: protection.DefaultSource()})
}

type LoginCommand struct {
	Username string
	Password string
	Source   protection.Source
}

func (s *Service) LoginFrom(ctx context.Context, command LoginCommand) (Issued, error) {
	username, password := command.Username, command.Password
	attemptID, err := newLoginAttemptID()
	if err != nil {
		return Issued{}, ErrInternal
	}
	now := s.now().UTC()
	var decision protection.Decision
	err = s.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var consumeErr error
		decision, consumeErr = s.protection.Consume(ctx, tx, username, command.Source, now)
		return consumeErr
	})
	if err != nil {
		return Issued{}, sanitize(err)
	}
	if !decision.Allowed {
		if factErr := s.failedLogin(ctx, attemptID, now); !errors.Is(factErr, ErrCredentials) {
			return Issued{}, factErr
		}
		return Issued{}, rateLimitError{retryAfter: decision.RetryAfter}
	}
	if len(strings.TrimSpace(username)) < 3 || len(username) > 64 || len(password) < account.MinimumPasswordLength || len(password) > 128 {
		_ = account.VerifyPassword(dummyPasswordHash, password)
		return Issued{}, s.failedLogin(ctx, attemptID, now)
	}
	releasePasswordWork, acquired := s.passwordWork.TryAcquire()
	if !acquired {
		return Issued{}, s.failedLogin(ctx, attemptID, now)
	}
	defer releasePasswordWork()
	var observed account.Credential
	err = s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		credential, err := s.accounts.FindCredential(ctx, tx, username, false)
		if err == nil {
			observed = credential
		}
		return err
	})
	if errors.Is(err, account.ErrNotFound) {
		// A fixed valid hash keeps absent and wrong-password paths in the same expensive class.
		_ = account.VerifyPassword(dummyPasswordHash, password)
		return Issued{}, s.failedLogin(ctx, attemptID, now)
	}
	if err != nil {
		return Issued{}, sanitize(err)
	}
	passwordValid := account.VerifyPassword(observed.PasswordHash, password)
	if observed.Disabled || !passwordValid {
		return Issued{}, s.failedLogin(ctx, attemptID, now)
	}
	releasePasswordWork()

	var result Issued
	err = s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		credential, err := s.accounts.FindCredential(ctx, tx, username, true)
		if errors.Is(err, account.ErrNotFound) || credential.Disabled {
			return ErrCredentials
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(credential.PasswordHash), []byte(observed.PasswordHash)) != 1 {
			return ErrCredentials
		}
		issued, err := s.create(ctx, tx, credential.Profile, credential.SessionGeneration, now, now.Add(s.policy.AbsoluteTimeout()))
		if err != nil {
			return err
		}
		if err := s.recordLoginFact(ctx, tx, LoginFact{AttemptID: attemptID, Outcome: LoginSucceeded, AccountID: credential.Profile.ID, Source: LoginSourceWeb, OccurredAt: now}); err != nil {
			return err
		}
		result = issued
		return nil
	})
	if errors.Is(err, ErrCredentials) {
		return Issued{}, s.failedLogin(ctx, attemptID, now)
	}
	if err != nil {
		return Issued{}, sanitize(err)
	}
	return result, nil
}

func (s *Service) failedLogin(ctx context.Context, attemptID LoginAttemptID, occurredAt time.Time) error {
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return s.recordLoginFact(ctx, tx, LoginFact{AttemptID: attemptID, Outcome: LoginFailed, Source: LoginSourceWeb, OccurredAt: occurredAt})
	})
	if err != nil {
		return sanitize(err)
	}
	return ErrCredentials
}

func (s *Service) recordLoginFact(ctx context.Context, tx database.Tx, fact LoginFact) error {
	if err := s.loginFacts.RecordLoginFact(ctx, tx, fact); err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return errors.New("login fact recording failed")
	}
	return nil
}

func (s *Service) Current(ctx context.Context, token string) (Issued, error) {
	var result Issued
	now := s.now().UTC()
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, credential, err := s.activeRead(ctx, tx, token, now)
		if err != nil {
			return err
		}
		result = Issued{Profile: credential.Profile, CSRF: rec.CSRFToken}
		return nil
	})
	return result, sanitize(err)
}

func (s *Service) Profile(ctx context.Context, token string) (Issued, error) {
	var result Issued
	now := s.now().UTC()
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, credential, err := s.activeRead(ctx, tx, token, now)
		if err != nil {
			return err
		}
		result = Issued{Profile: credential.Profile, CSRF: rec.CSRFToken}
		return nil
	})
	return result, sanitize(err)
}

// AuthorizeRequest keeps reads observational and touches idle time only after a mutation is authorized.
func (s *Service) AuthorizeRequest(ctx context.Context, token, csrf string, mutation bool) (Issued, error) {
	var result Issued
	now := s.now().UTC()
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		var rec record
		var credential account.Credential
		var err error
		if mutation {
			rec, credential, err = s.authorizeMutation(ctx, tx, token, csrf, now)
		} else {
			rec, credential, err = s.activeRead(ctx, tx, token, now)
		}
		if err != nil {
			return err
		}
		result = Issued{Profile: credential.Profile, CSRF: rec.CSRFToken}
		if !mutation {
			return nil
		}
		return s.touch(ctx, tx, rec, now)
	})
	return result, sanitize(err)
}

func (s *Service) UpdateProfile(ctx context.Context, token, csrf, displayName, email string, avatar *string) (Issued, error) {
	displayName = strings.TrimSpace(displayName)
	email = strings.ToLower(strings.TrimSpace(email))
	parsedEmail, emailErr := mail.ParseAddress(email)
	if displayName == "" || len(displayName) > 80 || emailErr != nil || parsedEmail.Address != email || len(email) > 254 || (avatar != nil && !avatarReference.MatchString(*avatar)) {
		return Issued{}, ErrValidation
	}
	var result Issued
	now := s.now().UTC()
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, _, err := s.authorizeMutation(ctx, tx, token, csrf, now)
		if err != nil {
			return err
		}
		profile, err := s.accounts.UpdateProfile(ctx, tx, rec.AccountID, displayName, email, avatar, now)
		if errors.Is(err, account.ErrNotFound) {
			return ErrAuthentication
		}
		if err != nil {
			return err
		}
		if err := s.touch(ctx, tx, rec, now); err != nil {
			return err
		}
		result = Issued{Profile: profile, CSRF: csrf}
		return auditing.Write(s.operationAudit, ctx, tx, auditing.Operation{Action: "update", ResourceType: "iam_user", ResourceID: rec.AccountID, ActorID: rec.AccountID, Code: "UpdateProfile"})
	})
	return result, sanitize(err)
}

func (s *Service) Heartbeat(ctx context.Context, token, csrf string) (Issued, error) {
	return s.renew(ctx, token, csrf)
}

func (s *Service) Renew(ctx context.Context, token, csrf string) (Issued, error) {
	return s.renew(ctx, token, csrf)
}

func (s *Service) renew(ctx context.Context, token, csrf string) (Issued, error) {
	var result Issued
	now := s.now().UTC()
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, credential, err := s.authorizeMutation(ctx, tx, token, csrf, now)
		if err != nil {
			return err
		}
		if err := s.touch(ctx, tx, rec, now); err != nil {
			return err
		}
		result = Issued{Profile: credential.Profile, CSRF: rec.CSRFToken}
		return nil
	})
	return result, sanitize(err)
}

func (s *Service) ChangePassword(ctx context.Context, token, csrf, current, replacement string) error {
	if len(current) < account.MinimumPasswordLength || len(current) > 128 || len(replacement) < account.MinimumPasswordLength || len(replacement) > 128 {
		return ErrValidation
	}
	var accountID, observedHash string
	var observedGeneration int64
	// Fence cheaply first, then perform the memory-hard work without holding a database transaction.
	err := s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, credential, err := s.authorizeMutation(ctx, tx, token, csrf, s.now().UTC())
		if err != nil {
			return err
		}
		accountID, observedHash, observedGeneration = rec.AccountID, credential.PasswordHash, credential.SessionGeneration
		return nil
	})
	if err != nil {
		return sanitize(err)
	}
	releasePasswordWork, acquired := s.passwordWork.TryAcquire()
	if !acquired {
		return ErrConflict
	}
	defer releasePasswordWork()
	if !account.VerifyPassword(observedHash, current) {
		return ErrCredentials
	}
	newHash, err := account.HashPassword(replacement)
	if err != nil {
		return sanitize(err)
	}
	releasePasswordWork()
	now := s.now().UTC()
	err = s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, credential, err := s.authorizeMutation(ctx, tx, token, csrf, now)
		if err != nil {
			return err
		}
		if rec.AccountID != accountID {
			return ErrAuthentication
		}
		if credential.SessionGeneration != observedGeneration || subtle.ConstantTimeCompare([]byte(credential.PasswordHash), []byte(observedHash)) != 1 {
			return ErrConflict
		}
		if err := s.accounts.UpdatePasswordAndAdvanceGeneration(ctx, tx, accountID, newHash, rec.Generation, now); err != nil {
			return err
		}
		revoked, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE account_id = ? AND generation <= ? AND state = 'active'`, now, accountID, rec.Generation)
		if err != nil {
			return normalizeSQLError(err, "session revoke failed")
		}
		count, countErr := revoked.RowsAffected()
		if countErr != nil {
			return normalizeSQLError(countErr, "session revoke result failed")
		}
		if count < 1 {
			return ErrConflict
		}
		return auditing.Write(s.operationAudit, ctx, tx, auditing.Operation{Action: "update", ResourceType: "iam_user", ResourceID: accountID, ActorID: accountID, Code: "ChangePassword"})
	})
	return sanitize(err)
}

func (s *Service) Logout(ctx context.Context, token, csrf string) error {
	return sanitize(s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		rec, _, err := s.authorizeMutation(ctx, tx, token, csrf, s.now().UTC())
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE id = ? AND state = 'active'`, s.now().UTC(), rec.ID)
		if err != nil {
			return normalizeSQLError(err, "session revoke failed")
		}
		n, countErr := result.RowsAffected()
		if countErr != nil {
			return normalizeSQLError(countErr, "session revoke result failed")
		}
		if n != 1 {
			return ErrAuthentication
		}
		return nil
	}))
}

func (s *Service) RevokeAccount(ctx context.Context, accountID string) error {
	return sanitize(s.withinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		now := s.now().UTC()
		s.probeLock(accountRevokeLockRequested)
		credential, err := s.accounts.FindByID(ctx, tx, accountID, true)
		if errors.Is(err, account.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		s.probeLock(accountRevokeLockHeld)
		if err := s.accounts.AdvanceSessionGeneration(ctx, tx, accountID, credential.SessionGeneration, now); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET state = 'revoked', revoked_at = ? WHERE account_id = ? AND generation <= ? AND state = 'active'`, now, accountID, credential.SessionGeneration)
		if err != nil {
			return normalizeSQLError(err, "session revoke failed")
		}
		if _, err := result.RowsAffected(); err != nil {
			return normalizeSQLError(err, "session revoke result failed")
		}
		return nil
	}))
}

func (s *Service) create(ctx context.Context, tx database.Tx, profile account.Profile, generation int64, now, absolute time.Time) (Issued, error) {
	token, err := randomSecret()
	if err != nil {
		return Issued{}, err
	}
	csrf, err := randomSecret()
	if err != nil {
		return Issued{}, err
	}
	id, err := randomSecret()
	if err != nil {
		return Issued{}, err
	}
	familyID, err := randomSecret()
	if err != nil {
		return Issued{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO iam_sessions
		(id, account_id, token_hash, generation, csrf_hash, csrf_token, state, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_at, family_id, renewed_at, renew_after_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?)`, id, profile.ID, tokenDigest(token), generation, tokenDigest(csrf), csrf, now, now,
		minTime(now.Add(s.policy.IdleTimeout()), absolute), absolute, absolute, familyID, now, minTime(now.Add(s.policy.RotationInterval()), absolute))
	if err != nil {
		return Issued{}, normalizeSQLError(err, "session creation failed")
	}
	return Issued{Profile: profile, Token: token, CSRF: csrf}, nil
}

func (s *Service) activeRead(ctx context.Context, tx database.Tx, token string, now time.Time) (record, account.Credential, error) {
	if len(token) != 43 {
		return record{}, account.Credential{}, ErrAuthentication
	}
	rec, err := s.findSession(ctx, tx, token, false)
	if err != nil {
		return record{}, account.Credential{}, err
	}
	credential, err := s.accounts.FindByID(ctx, tx, rec.AccountID, false)
	if errors.Is(err, account.ErrNotFound) || credential.Disabled {
		return record{}, account.Credential{}, ErrAuthentication
	}
	if err != nil {
		return record{}, account.Credential{}, err
	}
	if rec.State != "active" || rec.Generation != credential.SessionGeneration || !now.Before(rec.IdleExpiresAt) || !now.Before(rec.AbsoluteExpiresAt) {
		return record{}, account.Credential{}, ErrAuthentication
	}
	return rec, credential, nil
}

func (s *Service) activeLocked(ctx context.Context, tx database.Tx, token string, now time.Time) (record, account.Credential, error) {
	if len(token) != 43 {
		return record{}, account.Credential{}, ErrAuthentication
	}
	digest := tokenDigest(token)
	var accountID string
	if err := tx.QueryRowContext(ctx, `SELECT account_id FROM iam_sessions WHERE token_hash = ?`, digest).Scan(&accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record{}, account.Credential{}, ErrAuthentication
		}
		return record{}, account.Credential{}, normalizeSQLError(err, "session lookup failed")
	}
	credential, err := s.accounts.FindByID(ctx, tx, accountID, true)
	if errors.Is(err, account.ErrNotFound) || credential.Disabled {
		return record{}, account.Credential{}, ErrAuthentication
	}
	if err != nil {
		return record{}, account.Credential{}, err
	}
	s.probeLock(accountLockHeld)
	rec, err := s.findSession(ctx, tx, token, true)
	if err != nil {
		return record{}, account.Credential{}, err
	}
	if rec.State != "active" || rec.AccountID != credential.ID || rec.Generation != credential.SessionGeneration {
		return record{}, account.Credential{}, ErrAuthentication
	}
	if !now.Before(rec.IdleExpiresAt) || !now.Before(rec.AbsoluteExpiresAt) {
		return record{}, account.Credential{}, ErrAuthentication
	}
	return rec, credential, nil
}

func (s *Service) findSession(ctx context.Context, tx database.Tx, token string, lock bool) (record, error) {
	query := `SELECT id, account_id, token_hash, generation, csrf_hash, COALESCE(csrf_token, ''), state, COALESCE(family_id, ''), created_at, last_seen_at, idle_expires_at, absolute_expires_at, rotate_at
		FROM iam_sessions WHERE token_hash = ?`
	if lock && s.db.Dialect() == database.DialectPostgres {
		query += " FOR UPDATE"
	}
	var rec record
	err := tx.QueryRowContext(ctx, query, tokenDigest(token)).Scan(&rec.ID, &rec.AccountID, &rec.TokenHash, &rec.Generation, &rec.CSRFHash, &rec.CSRFToken, &rec.State, &rec.FamilyID, &rec.CreatedAt, &rec.LastSeenAt, &rec.IdleExpiresAt, &rec.AbsoluteExpiresAt, &rec.RotateAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, ErrAuthentication
	}
	if err != nil {
		return record{}, normalizeSQLError(err, "session lookup failed")
	}
	return rec, nil
}

func (s *Service) touch(ctx context.Context, tx database.Tx, rec record, now time.Time) error {
	updated, err := tx.ExecContext(ctx, `UPDATE iam_sessions SET last_seen_at = ?, idle_expires_at = ?, renewed_at = ?, renew_after_at = ? WHERE id = ? AND generation = ? AND state = 'active'`,
		now, minTime(now.Add(s.policy.IdleTimeout()), rec.AbsoluteExpiresAt), now, minTime(now.Add(s.policy.RotationInterval()), rec.AbsoluteExpiresAt), rec.ID, rec.Generation)
	if err != nil {
		return normalizeSQLError(err, "session renewal failed")
	}
	count, countErr := updated.RowsAffected()
	if countErr != nil {
		return normalizeSQLError(countErr, "session renewal result failed")
	}
	if count != 1 {
		return ErrAuthentication
	}
	return nil
}

func (s *Service) withinTx(ctx context.Context, fn func(context.Context, database.Tx) error) error {
	return s.db.WithinTx(ctx, fn)
}

func (s *Service) authorizeMutation(ctx context.Context, tx database.Tx, token, csrf string, now time.Time) (record, account.Credential, error) {
	rec, credential, err := s.activeLocked(ctx, tx, token, now)
	if err != nil {
		return record{}, account.Credential{}, err
	}
	got := tokenDigest(csrf)
	if subtle.ConstantTimeCompare([]byte(got), []byte(rec.CSRFHash)) != 1 {
		return record{}, account.Credential{}, ErrCSRF
	}
	return rec, credential, nil
}

func randomSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("secure random unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newLoginAttemptID() (LoginAttemptID, error) {
	var id LoginAttemptID
	if _, err := rand.Read(id.value[:]); err != nil {
		return LoginAttemptID{}, errors.New("secure random unavailable")
	}
	return id, nil
}

func tokenDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Service) probeLock(point accountLockPoint) {
	if s.lockProbe != nil {
		s.lockProbe(point)
	}
}

func normalizeSQLError(err error, fallback string) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(fallback)
}

func sanitize(err error) error {
	if err == nil || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrCredentials) || errors.Is(err, ErrCSRF) || errors.Is(err, ErrValidation) || errors.Is(err, ErrConflict) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, account.ErrConflict) {
		return ErrConflict
	}
	return ErrInternal
}

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHRzb21lc2FsdA$Bf7uFvyZ/+DZ3JZXG5q83gVsEScJTiI4btfDrxbJKUA"
