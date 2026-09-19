package session_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	sessionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0010-session-schema"
	sessionprotectionmigration "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/migrations/0040-session-protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session/protection"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/config"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/migrations"
)

type sessionTestLoginFactNoop struct{}

func (sessionTestLoginFactNoop) RecordLoginFact(context.Context, database.Tx, session.LoginFact) error {
	return nil
}

func TestHTTPLoginUsesHostCookieAndDoesNotReturnToken(t *testing.T) {
	_, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute))
	handler, err := session.NewHTTPHandler(service, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/iam/session/login", strings.NewReader(`{"username":"admin","password":"correct horse battery"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authentication response is cacheable")
	}
	cookie := response.Header().Get("Set-Cookie")
	for _, required := range []string{"__Host-go-admin-session=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(cookie, required) {
			t.Fatalf("cookie attribute missing: %q", required)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	encoded := response.Body.String()
	if strings.Contains(encoded, "sessionToken") || strings.Contains(encoded, "password") || strings.Contains(encoded, cookieValue(cookie)) {
		t.Fatal("sensitive session material reached JSON")
	}
	if body["csrfToken"] == "" || response.Header().Get("X-CSRF-Token") != body["csrfToken"] {
		t.Fatal("CSRF bootstrap contract is inconsistent")
	}
	change := httptest.NewRequest(http.MethodPut, "/iam/account/password", strings.NewReader(`{"currentPassword":"wrong current password","newPassword":"replacement password value"}`))
	change.Header.Set("Content-Type", "application/json")
	change.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
	change.Header.Set("X-CSRF-Token", body["csrfToken"].(string))
	changeResponse := httptest.NewRecorder()
	handler.ServeHTTP(changeResponse, change)
	if changeResponse.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password changed identity state: %d %s", changeResponse.Code, changeResponse.Body.String())
	}
	profile := httptest.NewRequest(http.MethodGet, "/iam/account/profile", nil)
	profile.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
	profileResponse := httptest.NewRecorder()
	handler.ServeHTTP(profileResponse, profile)
	if profileResponse.Code != http.StatusOK {
		t.Fatalf("wrong current password logged out active identity: %d", profileResponse.Code)
	}

	wrongUser := loginFailure(t, handler, `{"username":"missing","password":"incorrect password"}`)
	wrongPassword := loginFailure(t, handler, `{"username":"admin","password":"incorrect password"}`)
	if wrongUser != wrongPassword {
		t.Fatalf("login failure enumerates account: %s != %s", wrongUser, wrongPassword)
	}
}

func TestHTTPInfrastructureFailureIsNotReportedAsLogout(t *testing.T) {
	service, err := session.NewService(failingDatabase{}, mustPolicy(t, time.Hour, 2*time.Hour, time.Hour), session.WithLoginFactPort(sessionTestLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := session.NewHTTPHandler(service, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/iam/session/login", strings.NewReader(`{"username":"admin","password":"correct horse battery"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"category":"internal"`) {
		t.Fatalf("dependency failure was misclassified: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "database") || strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "path") {
		t.Fatal("internal detail escaped the stable problem")
	}
	protected := httptest.NewRequest(http.MethodGet, "/iam/session/current", nil)
	protected.AddCookie(&http.Cookie{Name: session.CookieName, Value: strings.Repeat("a", 43)})
	protectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, protected)
	if protectedResponse.Code != http.StatusInternalServerError || !strings.Contains(protectedResponse.Body.String(), `"category":"internal"`) {
		t.Fatalf("protected dependency failure was misclassified: status=%d", protectedResponse.Code)
	}
}

func TestNewServiceRequiresExplicitLoginFactPort(t *testing.T) {
	policy := mustPolicy(t, time.Hour, 2*time.Hour, time.Hour)
	for _, test := range []struct {
		name    string
		options []session.Option
	}{
		{name: "absent"},
		{name: "nil", options: []session.Option{session.WithLoginFactPort(nil)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := session.NewService(failingDatabase{}, policy, test.options...)
			if err == nil || service != nil || !strings.Contains(err.Error(), "login fact port is required") {
				t.Fatalf("NewService without an explicit LoginFactPort = %#v, %v", service, err)
			}
		})
	}
}

func TestLoginFactPortCommitsWithSuccessAndRecordsFailureSynchronously(t *testing.T) {
	const (
		username = "admin"
		password = "correct horse battery"
	)
	attemptPattern := regexp.MustCompile(`^[a-f0-9]{32}$`)

	t.Run("success", func(t *testing.T) {
		probe := &loginFactProbe{}
		db, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithLoginFactPort(probe))
		createLoginFactProbeTable(t, db)
		issued, err := service.Login(context.Background(), username, password)
		if err != nil || issued.Token == "" || issued.CSRF == "" {
			t.Fatalf("login with fact = %#v, %v", issued, err)
		}
		if countRows(t, db, "iam_sessions") != 1 || countRows(t, db, "login_fact_probe") != 1 {
			t.Fatal("successful Session and login fact did not commit together")
		}
		fact := probe.single(t)
		if !fact.AttemptID.Valid() || !attemptPattern.MatchString(fact.AttemptID.Opaque()) || strings.Contains(fact.AttemptID.Opaque(), username) ||
			fact.Outcome != session.LoginSucceeded || fact.AccountID != "account-00000001" || fact.Source != session.LoginSourceWeb {
			t.Fatalf("success fact = %#v", fact)
		}
		assertLoginFactHasNoCredentialMaterial(t, fact, username, password, issued.Token, issued.CSRF)
	})

	t.Run("failed credentials", func(t *testing.T) {
		probe := &loginFactProbe{}
		db, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithLoginFactPort(probe))
		createLoginFactProbeTable(t, db)
		if _, err := service.Login(context.Background(), username, "incorrect password"); !errors.Is(err, session.ErrCredentials) {
			t.Fatalf("failed login = %v", err)
		}
		if countRows(t, db, "iam_sessions") != 0 || countRows(t, db, "login_fact_probe") != 1 {
			t.Fatal("failed login fact was not synchronously committed without a Session")
		}
		fact := probe.single(t)
		if !fact.AttemptID.Valid() || !attemptPattern.MatchString(fact.AttemptID.Opaque()) || fact.Outcome != session.LoginFailed || fact.AccountID != "" {
			t.Fatalf("failed fact = %#v", fact)
		}
		assertLoginFactHasNoCredentialMaterial(t, fact, username, "incorrect password")
	})
}

func TestLoginFactFailureRollsBackSessionAndReturnsNoCredentialMaterial(t *testing.T) {
	probe := &loginFactProbe{fail: true}
	db, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithLoginFactPort(probe))
	createLoginFactProbeTable(t, db)
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if !errors.Is(err, session.ErrInternal) || issued.Token != "" || issued.CSRF != "" {
		t.Fatalf("login fact failure = %#v, %v", issued, err)
	}
	if countRows(t, db, "iam_sessions") != 0 || countRows(t, db, "login_fact_probe") != 0 {
		t.Fatal("login fact failure committed a Session or partial fact")
	}
}

type loginFactProbe struct {
	fail  bool
	facts []session.LoginFact
}

func (probe *loginFactProbe) RecordLoginFact(ctx context.Context, tx database.Tx, fact session.LoginFact) error {
	probe.facts = append(probe.facts, fact)
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_fact_probe(attempt_id, outcome, account_id, source, occurred_at) VALUES (?, ?, ?, ?, ?)`, fact.AttemptID.Opaque(), fact.Outcome, fact.AccountID, fact.Source, fact.OccurredAt); err != nil {
		return err
	}
	if probe.fail {
		return errors.New("private audit failure")
	}
	return nil
}

func (probe *loginFactProbe) single(t *testing.T) session.LoginFact {
	t.Helper()
	if len(probe.facts) != 1 {
		t.Fatalf("login facts = %d", len(probe.facts))
	}
	return probe.facts[0]
}

func createLoginFactProbeTable(t *testing.T, db *database.Database) {
	t.Helper()
	if _, err := db.Bun().ExecContext(context.Background(), `CREATE TABLE login_fact_probe(attempt_id TEXT PRIMARY KEY, outcome TEXT NOT NULL, account_id TEXT NOT NULL, source TEXT NOT NULL, occurred_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, db *database.Database, table string) int {
	t.Helper()
	var count int
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertLoginFactHasNoCredentialMaterial(t *testing.T, fact session.LoginFact, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(encoded), value) {
			t.Fatal("login fact serialized credential or identity input")
		}
	}
	for _, field := range []string{"username", "password", "token", "csrf", "request"} {
		if strings.Contains(strings.ToLower(string(encoded)), field) {
			t.Fatalf("login fact exposed forbidden field %q", field)
		}
	}
}

type failingDatabase struct{}

func (failingDatabase) WithinTx(context.Context, func(context.Context, database.Tx) error) error {
	return errors.New("database secret/path must not escape")
}
func (failingDatabase) Dialect() database.Dialect { return database.DialectSQLite }

func loginFailure(t *testing.T, handler http.Handler, body string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/iam/session/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("failure status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func cookieValue(header string) string {
	value := strings.TrimPrefix(strings.SplitN(header, ";", 2)[0], session.CookieName+"=")
	return value
}

func TestArgon2idPolicyRejectsLegacyAndWrongCredentials(t *testing.T) {
	hash, err := account.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatal("password hash does not use the required Argon2id parameters")
	}
	if !account.VerifyPassword(hash, "correct horse battery") {
		t.Fatal("correct password rejected")
	}
	if account.VerifyPassword(hash, "wrong password") || account.VerifyPassword("$2a$legacy", "correct horse battery") {
		t.Fatal("invalid password representation accepted")
	}
}

func TestPasswordWorkBudgetFailsFastWithoutEnumeratingAccounts(t *testing.T) {
	budget, err := account.NewPasswordWorkBudget(1)
	if err != nil {
		t.Fatal(err)
	}
	release, acquired := budget.TryAcquire()
	if !acquired {
		t.Fatal("failed to reserve password work slot")
	}
	defer release()

	_, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithPasswordWorkBudget(budget))
	start := make(chan struct{})
	results := make(chan error, 8)
	for index := range 8 {
		username := "admin"
		if index%2 == 1 {
			username = "missing"
		}
		go func() {
			<-start
			_, loginErr := service.Login(context.Background(), username, "incorrect password")
			results <- loginErr
		}()
	}
	close(start)
	for range 8 {
		if loginErr := <-results; !errors.Is(loginErr, session.ErrCredentials) {
			t.Fatalf("saturated login returned a distinguishable error class: %v", loginErr)
		}
	}
}

func TestPersistentAccountAndSourceBucketsSurviveServiceRestart(t *testing.T) {
	policy := protection.Policy{AccountLimit: 2, SourceLimit: 3, Window: 10 * time.Minute}
	db, service, clock := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithLoginProtectionPolicy(policy))
	source, err := protection.NewSource("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	for _, username := range []string{"admin", "missing-account"} {
		_, got := service.LoginFrom(context.Background(), session.LoginCommand{Username: username, Password: "wrong password value", Source: source})
		if !errors.Is(got, session.ErrCredentials) || errors.Is(got, session.ErrRateLimited) {
			t.Fatalf("ordinary credential failure for %q = %v", username, got)
		}
	}
	_, err = service.LoginFrom(context.Background(), session.LoginCommand{Username: "admin", Password: "wrong password value", Source: source})
	if !errors.Is(err, session.ErrCredentials) {
		t.Fatalf("second account attempt = %v", err)
	}
	_, err = service.LoginFrom(context.Background(), session.LoginCommand{Username: "admin", Password: "correct horse battery", Source: source})
	if !errors.Is(err, session.ErrRateLimited) {
		t.Fatalf("account bucket did not reject = %v", err)
	}
	if retry, ok := session.RetryAfter(err); !ok || retry != 10*time.Minute {
		t.Fatalf("coarse retry = %s, %t", retry, ok)
	}

	restarted, err := session.NewService(db, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute),
		session.WithClock(func() time.Time { return *clock }), session.WithLoginFactPort(sessionTestLoginFactNoop{}), session.WithLoginProtectionPolicy(policy))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.LoginFrom(context.Background(), session.LoginCommand{Username: "admin", Password: "correct horse battery", Source: source})
	if !errors.Is(err, session.ErrRateLimited) {
		t.Fatalf("restart lost account bucket = %v", err)
	}

	otherSource, _ := protection.NewSource("203.0.113.8")
	for index, username := range []string{"unknown-a", "unknown-b", "unknown-c", "unknown-d"} {
		_, got := restarted.LoginFrom(context.Background(), session.LoginCommand{Username: username, Password: "wrong password value", Source: otherSource})
		if index < 3 && !errors.Is(got, session.ErrCredentials) {
			t.Fatalf("source attempt %d = %v", index+1, got)
		}
		if index == 3 && !errors.Is(got, session.ErrRateLimited) {
			t.Fatalf("source bucket did not reject = %v", got)
		}
	}
}

func TestConcurrentLoginAttemptsCannotOverrunPersistentBudgets(t *testing.T) {
	policy := protection.Policy{AccountLimit: 2, SourceLimit: 20, Window: 10 * time.Minute}
	_, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithLoginProtectionPolicy(policy))
	source, err := protection.NewSource("198.51.100.22")
	if err != nil || strings.Contains(fmt.Sprintf("%#v", source), "198.51.100.22") {
		t.Fatal("trusted source was invalid or printable")
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			_, loginErr := service.LoginFrom(context.Background(), session.LoginCommand{Username: "admin", Password: "wrong password value", Source: source})
			results <- loginErr
		}()
	}
	close(start)
	ordinary, limited := 0, 0
	for range 8 {
		switch loginErr := <-results; {
		case errors.Is(loginErr, session.ErrRateLimited):
			limited++
		case errors.Is(loginErr, session.ErrCredentials):
			ordinary++
		default:
			t.Fatalf("unexpected concurrent login result: %v", loginErr)
		}
	}
	if ordinary != 2 || limited != 6 {
		t.Fatalf("persistent account budget admitted=%d limited=%d", ordinary, limited)
	}
}

func TestSanitizePreservesContextTermination(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		service, err := session.NewService(errorDatabase{err: sentinel}, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute), session.WithLoginFactPort(sessionTestLoginFactNoop{}))
		if err != nil {
			t.Fatal(err)
		}
		_, loginErr := service.Login(context.Background(), "admin", "correct horse battery")
		if !errors.Is(loginErr, sentinel) {
			t.Fatalf("context sentinel was sanitized to a different error: %v", loginErr)
		}
	}
}

func TestCallbackSQLFailuresPreserveContextTermination(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, stage := range []string{"query-scan", "exec", "rows-affected"} {
			t.Run(sentinel.Error()+"/"+stage, func(t *testing.T) {
				policy := mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute)
				db, healthy, clock := newFixture(t, policy)
				failure := callbackFailureDatabase{db: db}
				switch stage {
				case "query-scan":
					failure.queryErr = sentinel
				case "exec":
					failure.execErr = sentinel
				case "rows-affected":
					failure.resultErr = sentinel
				}
				service, err := session.NewService(failure, policy, session.WithClock(func() time.Time { return *clock }), session.WithLoginFactPort(sessionTestLoginFactNoop{}))
				if err != nil {
					t.Fatal(err)
				}
				var operationErr error
				switch stage {
				case "query-scan", "exec":
					_, operationErr = service.Login(context.Background(), "admin", "correct horse battery")
				case "rows-affected":
					issued, loginErr := healthy.Login(context.Background(), "admin", "correct horse battery")
					if loginErr != nil {
						t.Fatal(loginErr)
					}
					_, operationErr = service.UpdateProfile(context.Background(), issued.Token, issued.CSRF, "Updated", "updated@example.test", nil)
				}
				if !errors.Is(operationErr, sentinel) {
					t.Fatalf("SQL callback sentinel was lost at %s: %v", stage, operationErr)
				}
			})
		}
	}
}

func TestCallbackSQLDetailsAreSanitized(t *testing.T) {
	policy := mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute)
	db, _, _ := newFixture(t, policy)
	service, err := session.NewService(callbackFailureDatabase{db: db, execErr: errors.New("private SQL path and value")}, policy, session.WithLoginFactPort(sessionTestLoginFactNoop{}))
	if err != nil {
		t.Fatal(err)
	}
	_, operationErr := service.Login(context.Background(), "admin", "correct horse battery")
	if !errors.Is(operationErr, session.ErrInternal) || strings.Contains(operationErr.Error(), "private") {
		t.Fatal("SQL detail was not reduced to the stable internal error")
	}
}

type errorDatabase struct{ err error }

func (value errorDatabase) WithinTx(context.Context, func(context.Context, database.Tx) error) error {
	return value.err
}
func (errorDatabase) Dialect() database.Dialect { return database.DialectSQLite }

type callbackFailureDatabase struct {
	db                           *database.Database
	queryErr, execErr, resultErr error
}

func (failure callbackFailureDatabase) WithinTx(_ context.Context, fn func(context.Context, database.Tx) error) error {
	return failure.db.WithinTx(context.Background(), func(ctx context.Context, tx database.Tx) error {
		return fn(ctx, callbackFailureTx{Tx: tx, queryErr: failure.queryErr, execErr: failure.execErr, resultErr: failure.resultErr})
	})
}
func (failure callbackFailureDatabase) Dialect() database.Dialect { return failure.db.Dialect() }

type callbackFailureTx struct {
	database.Tx
	queryErr, execErr, resultErr error
}

func (tx callbackFailureTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if tx.queryErr == nil {
		return tx.Tx.QueryContext(ctx, query, args...)
	}
	failed, cancel := contextForFailure(tx.queryErr)
	defer cancel()
	return tx.Tx.QueryContext(failed, query, args...)
}

func (tx callbackFailureTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if tx.queryErr == nil {
		return tx.Tx.QueryRowContext(ctx, query, args...)
	}
	failed, cancel := contextForFailure(tx.queryErr)
	defer cancel()
	return tx.Tx.QueryRowContext(failed, query, args...)
}

func (tx callbackFailureTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx.execErr != nil {
		return nil, tx.execErr
	}
	if tx.resultErr != nil {
		return callbackFailureResult{err: tx.resultErr}, nil
	}
	return tx.Tx.ExecContext(ctx, query, args...)
}

type callbackFailureResult struct{ err error }

func (result callbackFailureResult) LastInsertId() (int64, error) { return 0, result.err }
func (result callbackFailureResult) RowsAffected() (int64, error) { return 0, result.err }

func contextForFailure(sentinel error) (context.Context, context.CancelFunc) {
	if errors.Is(sentinel, context.DeadlineExceeded) {
		return context.WithDeadline(context.Background(), time.Unix(0, 0))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, func() {}
}

func TestSessionLifecyclePersistsOpaqueTokenDigestAndStableCSRFProjection(t *testing.T) {
	db, service, clock := newFixture(t, mustPolicy(t, 2*time.Hour, 8*time.Hour, time.Hour))
	issued, err := service.Login(context.Background(), "ADMIN", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.Token) != 43 || len(issued.CSRF) != 43 {
		t.Fatal("session material is not high entropy")
	}
	var tokenHash, csrfHash, csrfToken string
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT token_hash, csrf_hash, csrf_token FROM iam_sessions`).Scan(&tokenHash, &csrfHash, &csrfToken); err != nil {
		t.Fatal(err)
	}
	if tokenHash == issued.Token || csrfHash == issued.CSRF || len(tokenHash) != 64 || len(csrfHash) != 64 || csrfToken != issued.CSRF {
		t.Fatal("session persistence projection is inconsistent")
	}
	serialized, err := json.Marshal(issued)
	if err != nil || strings.Contains(string(serialized), issued.Token) || strings.Contains(string(serialized), issued.CSRF) {
		t.Fatal("issued session serialization leaked secrets")
	}

	*clock = clock.Add(61 * time.Minute)
	current, err := service.Current(context.Background(), issued.Token)
	if err != nil || current.Rotated || current.Token != "" || current.CSRF != issued.CSRF {
		t.Fatalf("read exposed replacement credentials: %#v %v", current, err)
	}
	renewed, err := service.Renew(context.Background(), issued.Token, issued.CSRF)
	if err != nil || renewed.CSRF != issued.CSRF || renewed.Token != "" || renewed.Rotated {
		t.Fatalf("renew changed family credentials: %#v %v", renewed, err)
	}

	if err := service.Logout(context.Background(), issued.Token, issued.CSRF); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Current(context.Background(), issued.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatalf("revoked token recovered: %v", err)
	}
}

func TestCSRFPasswordAndTimeoutFailuresArePermanentAndAtomic(t *testing.T) {
	db, service, clock := newFixture(t, mustPolicy(t, 10*time.Minute, 30*time.Minute, 5*time.Minute))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	beforeAccess, err := service.Profile(context.Background(), issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	var idleBefore time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT idle_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&idleBefore); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if _, err := service.UpdateProfile(context.Background(), issued.Token, "wrong-csrf", "Changed", "changed@example.test", nil); !errors.Is(err, session.ErrCSRF) {
		t.Fatalf("expected CSRF rejection: %v", err)
	}
	var idleAfter time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT idle_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&idleAfter); err != nil {
		t.Fatal(err)
	}
	if !idleAfter.Equal(idleBefore) {
		t.Fatal("CSRF failure refreshed idle timeout")
	}
	afterAccess, _ := service.Profile(context.Background(), issued.Token)
	if beforeAccess.Profile != afterAccess.Profile {
		t.Fatal("CSRF failure mutated profile")
	}

	if err := service.ChangePassword(context.Background(), issued.Token, issued.CSRF, "correct horse battery", "replacement password value"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Current(context.Background(), issued.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatal("password change did not revoke current session")
	}
	if _, err := service.Login(context.Background(), "admin", "correct horse battery"); !errors.Is(err, session.ErrCredentials) {
		t.Fatal("old password remains valid")
	}
	second, err := service.Login(context.Background(), "admin", "replacement password value")
	if err != nil {
		t.Fatal(err)
	}

	*clock = clock.Add(11 * time.Minute)
	if _, err := service.Current(context.Background(), second.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatalf("idle expiry accepted: %v", err)
	}
	if _, err := service.Current(context.Background(), second.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatalf("idle-expired token recovered: %v", err)
	}
}

func TestProtectedReadsAreZeroWriteAndHeartbeatCannotExtendAbsoluteExpiry(t *testing.T) {
	db, service, clock := newFixture(t, mustPolicy(t, 10*time.Minute, 25*time.Minute, 10*time.Minute))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	var originalIdle time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT idle_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&originalIdle); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(8 * time.Minute)
	var changesBefore int64
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Profile(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	var changesAfter int64
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT total_changes()`).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if changesAfter != changesBefore {
		t.Fatalf("authenticated GET wrote %d rows", changesAfter-changesBefore)
	}
	var touchedIdle time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT idle_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&touchedIdle); err != nil {
		t.Fatal(err)
	}
	if !touchedIdle.Equal(originalIdle) {
		t.Fatal("successful protected read refreshed idle timeout")
	}
	if _, err := service.Heartbeat(context.Background(), issued.Token, issued.CSRF); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(18 * time.Minute)
	if _, err := service.Profile(context.Background(), issued.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatalf("absolute expiry accepted: %v", err)
	}
	var state string
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT state FROM iam_sessions`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "active" {
		t.Fatalf("expired GET mutated state: %q", state)
	}
}

func TestSessionFamilyKeepsTokenAndCSRFAcrossReadsRenewalsAndWrites(t *testing.T) {
	_, service, clock := newFixture(t, mustPolicy(t, 2*time.Hour, 8*time.Hour, time.Hour))
	first, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(61 * time.Minute)
	read, err := service.Profile(context.Background(), first.Token)
	if err != nil || read.Token != "" || read.CSRF != first.CSRF || read.Rotated {
		t.Fatalf("profile read changed credentials: %#v %v", read, err)
	}
	renewed, err := service.Renew(context.Background(), first.Token, first.CSRF)
	if err != nil || renewed.CSRF != first.CSRF || renewed.Token != "" || renewed.Rotated {
		t.Fatalf("renew changed credentials: %#v %v", renewed, err)
	}
	written, err := service.UpdateProfile(context.Background(), first.Token, first.CSRF, "Updated", "updated@example.test", nil)
	if err != nil || written.CSRF != first.CSRF || written.Token != "" || written.Rotated {
		t.Fatalf("business write changed credentials: %#v %v", written, err)
	}
}

func TestConcurrentRenewalsKeepAbsoluteExpiryCSRFAndFamily(t *testing.T) {
	db, service, _ := newFixture(t, mustPolicy(t, 2*time.Hour, 8*time.Hour, time.Hour))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	var familyBefore, csrfBefore string
	var absoluteBefore time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT family_id, csrf_hash, absolute_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&familyBefore, &csrfBefore, &absoluteBefore); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			continued, renewErr := service.Renew(context.Background(), issued.Token, issued.CSRF)
			if renewErr == nil && (continued.Token != "" || continued.CSRF != issued.CSRF || continued.Rotated) {
				renewErr = errors.New("renewal changed credentials")
			}
			results <- renewErr
		}()
	}
	close(start)
	for range 8 {
		if renewErr := <-results; renewErr != nil {
			t.Fatal(renewErr)
		}
	}
	var familyAfter, csrfAfter string
	var absoluteAfter time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT family_id, csrf_hash, absolute_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&familyAfter, &csrfAfter, &absoluteAfter); err != nil {
		t.Fatal(err)
	}
	if familyBefore == "" || familyAfter != familyBefore || csrfAfter != csrfBefore || !absoluteAfter.Equal(absoluteBefore) {
		t.Fatal("concurrent renewal changed the session family, CSRF, or absolute expiry")
	}
}

func TestAuthorizeRequestFencesCSRFFromIdleTouch(t *testing.T) {
	db, service, clock := newFixture(t, mustPolicy(t, 2*time.Hour, 8*time.Hour, time.Hour))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	var idleBefore time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT idle_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&idleBefore); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(30 * time.Minute)
	if _, err := service.AuthorizeRequest(context.Background(), issued.Token, "wrong-csrf", true); !errors.Is(err, session.ErrCSRF) {
		t.Fatalf("mutation CSRF fence = %v", err)
	}
	var idleAfter time.Time
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT idle_expires_at FROM iam_sessions WHERE state = 'active'`).Scan(&idleAfter); err != nil {
		t.Fatal(err)
	}
	if !idleAfter.Equal(idleBefore) {
		t.Fatal("rejected generic mutation touched idle timeout")
	}

	read, err := service.AuthorizeRequest(context.Background(), issued.Token, "", false)
	if err != nil || read.Profile.ID != issued.Profile.ID || read.CSRF != issued.CSRF {
		t.Fatalf("generic protected read = %#v, %v", read, err)
	}
	*clock = clock.Add(61 * time.Minute)
	continued, err := service.AuthorizeRequest(context.Background(), issued.Token, issued.CSRF, true)
	if err != nil || continued.Rotated || continued.Token != "" || continued.CSRF != issued.CSRF {
		t.Fatalf("generic mutation changed credentials = %#v, %v", continued, err)
	}
}

func TestHTTPReadsReturnStableCSRFWithoutCookieRotationOrSessionWrites(t *testing.T) {
	db, service, clock := newFixture(t, mustPolicy(t, 2*time.Hour, 8*time.Hour, time.Hour))
	handler, err := session.NewHTTPHandler(service, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(61 * time.Minute)
	var changesBefore int64
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/iam/account/profile", "/iam/session/current"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: issued.Token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Set-Cookie") != "" || response.Header().Get("X-CSRF-Token") != issued.CSRF {
			t.Fatalf("%s did not return the stable CSRF contract", path)
		}
	}
	var changesAfter int64
	if err := db.Bun().QueryRowContext(context.Background(), `SELECT total_changes()`).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if changesAfter != changesBefore {
		t.Fatalf("authenticated GETs wrote %d rows", changesAfter-changesBefore)
	}
}

func TestAdministrativeRevokeFencesConcurrentRenewal(t *testing.T) {
	_, service, clock := newFixture(t, mustPolicy(t, 2*time.Hour, 8*time.Hour, time.Hour))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(61 * time.Minute)
	start := make(chan struct{})
	renewed := make(chan session.Issued, 1)
	renewErr := make(chan error, 1)
	revokeErr := make(chan error, 1)
	go func() {
		<-start
		value, currentErr := service.Renew(context.Background(), issued.Token, issued.CSRF)
		renewed <- value
		renewErr <- currentErr
	}()
	go func() {
		<-start
		revokeErr <- service.RevokeAccount(context.Background(), issued.Profile.ID)
	}()
	close(start)
	value := <-renewed
	currentErr := <-renewErr
	if currentErr != nil && !errors.Is(currentErr, session.ErrAuthentication) {
		t.Fatalf("concurrent renewal returned an unexpected error: %v", currentErr)
	}
	if err := <-revokeErr; err != nil {
		t.Fatalf("concurrent revoke failed: %v", err)
	}
	if value.Token != "" {
		t.Fatal("renewal unexpectedly replaced the token")
	}
	if _, err := service.Current(context.Background(), issued.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatal("generation fence allowed a token after administrative revoke")
	}
}

func TestProfilePersistenceAndAdministrativeRevoke(t *testing.T) {
	_, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	avatar := "files/avatar-1"
	profileAccess, err := service.UpdateProfile(context.Background(), issued.Token, issued.CSRF, "New Name", "new@example.test", &avatar)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := service.Profile(context.Background(), issued.Token)
	if err != nil || persisted.Profile.DisplayName != profileAccess.Profile.DisplayName || persisted.Profile.Email != "new@example.test" {
		t.Fatalf("profile was not persisted: %#v %v", persisted, err)
	}
	if err := service.RevokeAccount(context.Background(), profileAccess.Profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Current(context.Background(), issued.Token); !errors.Is(err, session.ErrAuthentication) {
		t.Fatal("administrative revoke did not fence token")
	}
}

func TestDisabledAccountCannotAuthenticateOrMutate(t *testing.T) {
	db, service, clock := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute))
	issued, err := service.Login(context.Background(), "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().ExecContext(context.Background(), `UPDATE iam_accounts SET disabled_at = ? WHERE id = ?`, clock.UTC(), "account-00000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(context.Background(), "admin", "correct horse battery"); !errors.Is(err, session.ErrCredentials) {
		t.Fatalf("disabled account login was distinguishable or accepted: %v", err)
	}
	if _, err := service.UpdateProfile(context.Background(), issued.Token, issued.CSRF, "Changed", "changed@example.test", nil); !errors.Is(err, session.ErrAuthentication) {
		t.Fatalf("disabled account mutated profile: %v", err)
	}
}

func newFixture(t *testing.T, policy config.SessionPolicy, options ...session.Option) (*database.Database, *session.Service, *time.Time) {
	t.Helper()
	ctx := context.Background()
	process := database.NewProcess()
	db, err := process.Open(ctx, database.Config{Profile: config.ProfileServerSQLite, SQLitePath: t.TempDir() + "/iam.db"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner, err := migrations.NewRunner(sessionmigration.Provider{}, sessionprotectionmigration.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	hash, err := account.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	repository := account.NewRepository(db.Dialect())
	err = db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		return repository.Create(ctx, tx, account.Credential{Profile: account.Profile{ID: "account-00000001", Username: "admin", DisplayName: "Administrator", Email: "admin@example.test"}, PasswordHash: hash}, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The pointer controls the closure without exporting clock mutation into production APIs.
	clock := &now
	options = append([]session.Option{
		session.WithClock(func() time.Time { return *clock }),
		session.WithLoginFactPort(sessionTestLoginFactNoop{}),
	}, options...)
	service, err := session.NewService(db, policy, options...)
	if err != nil {
		t.Fatal(err)
	}
	return db, service, clock
}

func mustPolicy(t *testing.T, idle, absolute, rotation time.Duration) config.SessionPolicy {
	t.Helper()
	policy, err := config.NewSessionPolicy(idle, absolute, rotation)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestHTTPLoginSourceIsolationAndUntrustedForwardedHeaders(t *testing.T) {
	_, service, _ := newFixture(t, mustPolicy(t, time.Hour, 8*time.Hour, 30*time.Minute))
	handler, err := session.NewHTTPHandler(service, func(*http.Request) string { return "0123456789abcdef" })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 22; i++ {
		req := httptest.NewRequest(http.MethodPost, "/iam/session/login", strings.NewReader(fmt.Sprintf(`{"username":"unknown-%d","password":"incorrect password"}`, i)))
		req.RemoteAddr = "203.0.113.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if i >= 20 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("source bucket bypassed: %d", response.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/iam/session/login", strings.NewReader(`{"username":"admin","password":"correct horse battery"}`))
	req.RemoteAddr = "203.0.113.2:9999"
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("independent source rejected: %d", response.Code)
	}
}
