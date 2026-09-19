// Package adapters owns concrete cross-module mappings used by application composition roots.
package adapters

import (
	"context"
	"errors"
	"net/http"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/audit"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type iamAuthorizationAdapter struct {
	service *authorization.Service
	dialect database.Dialect
}

// Authorization provides consumer-typed views over one IAM authorization provider.
type Authorization struct{ adapter *iamAuthorizationAdapter }

func NewAuthorization(db authorization.Database) (*Authorization, error) {
	adapter, err := newIAMAuthorizationAdapter(db)
	if err != nil {
		return nil, err
	}
	return &Authorization{adapter: adapter}, nil
}

func (adapters *Authorization) Audit() audit.Authorizer {
	return auditAuthorizationAdapter{iamAuthorizationAdapter: adapters.adapter}
}
func (adapters *Authorization) Files() files.Authorizer {
	return filesAuthorizationAdapter{iamAuthorizationAdapter: adapters.adapter}
}
func (adapters *Authorization) Scheduler() scheduler.Authorizer {
	return schedulerAuthorizationAdapter{iamAuthorizationAdapter: adapters.adapter}
}

func newIAMAuthorizationAdapter(db interface {
	authorization.Database
	Dialect() database.Dialect
}) (*iamAuthorizationAdapter, error) {
	if db == nil {
		return nil, errors.New("product authorization database is required")
	}
	return &iamAuthorizationAdapter{service: authorization.NewService(db), dialect: db.Dialect()}, nil
}

func (adapter *iamAuthorizationAdapter) Dialect() database.Dialect { return adapter.dialect }

type filesAuthorizationAdapter struct{ *iamAuthorizationAdapter }

func (adapter filesAuthorizationAdapter) RequireInTx(ctx context.Context, tx database.Tx, actorID, permission string) (files.Scope, error) {
	decision, err := adapter.service.RequireInTx(ctx, tx, actorID, permission)
	if errors.Is(err, authorization.ErrDenied) {
		return "", files.ErrDenied
	}
	if err != nil {
		return "", err
	}
	switch decision.Scope {
	case authorization.ScopeSelf:
		return files.ScopeSelf, nil
	case authorization.ScopeAll:
		return files.ScopeAll, nil
	default:
		return "", files.ErrDenied
	}
}

type schedulerAuthorizationAdapter struct{ *iamAuthorizationAdapter }

func (adapter schedulerAuthorizationAdapter) RequireInTx(ctx context.Context, tx database.Tx, actorID, permission string) (scheduler.AuthorizationDecision, error) {
	decision, err := adapter.service.RequireInTx(ctx, tx, actorID, permission)
	if errors.Is(err, authorization.ErrDenied) {
		return scheduler.AuthorizationDecision{}, scheduler.ErrDenied
	}
	if err != nil {
		return scheduler.AuthorizationDecision{}, err
	}
	switch decision.Scope {
	case authorization.ScopeSelf:
		return scheduler.AuthorizationDecision{Scope: scheduler.ScopeSelf}, nil
	case authorization.ScopeAll:
		return scheduler.AuthorizationDecision{Scope: scheduler.ScopeAll}, nil
	default:
		return scheduler.AuthorizationDecision{}, scheduler.ErrDenied
	}
}

type auditAuthorizationAdapter struct{ *iamAuthorizationAdapter }

func (adapter auditAuthorizationAdapter) Authorize(ctx context.Context, tx database.Tx, principal audit.Principal, permission audit.Permission) (audit.AuthorizationDecision, error) {
	_, err := adapter.service.RequireInTx(ctx, tx, principal.ID, string(permission))
	if errors.Is(err, authorization.ErrDenied) {
		return audit.AuthorizationDenied, nil
	}
	if err != nil {
		return audit.AuthorizationDenied, err
	}
	return audit.AuthorizationGranted, nil
}

type sessionRequestService interface {
	AuthorizeRequest(context.Context, string, string, bool) (session.Issued, error)
}

type iamSessionAdapter struct{ service sessionRequestService }

// Session provides consumer-typed request authentication views over one IAM Session provider.
type Session struct{ adapter *iamSessionAdapter }

func NewSession(service sessionRequestService) (*Session, error) {
	adapter, err := newIAMSessionAdapter(service)
	if err != nil {
		return nil, err
	}
	return &Session{adapter: adapter}, nil
}

func (adapters *Session) Audit() audit.RequestAuthorizer {
	return auditRequestAdapter{iamSessionAdapter: adapters.adapter}
}
func (adapters *Session) Files() files.RequestAuthenticator {
	return filesSessionAdapter{iamSessionAdapter: adapters.adapter}
}
func (adapters *Session) Scheduler() scheduler.RequestAuthenticator {
	return schedulerSessionAdapter{iamSessionAdapter: adapters.adapter}
}

func newIAMSessionAdapter(service sessionRequestService) (*iamSessionAdapter, error) {
	if service == nil {
		return nil, errors.New("product session service is required")
	}
	return &iamSessionAdapter{service: service}, nil
}

func (*iamSessionAdapter) CookieName() string { return session.CookieName }

func (adapter *iamSessionAdapter) authorize(ctx context.Context, token, csrf string, mutation bool) (session.Issued, error) {
	return adapter.service.AuthorizeRequest(ctx, token, csrf, mutation)
}

func replacementSessionCookie(issued session.Issued) *string {
	if !issued.Rotated || issued.Token == "" {
		return nil
	}
	value := (&http.Cookie{Name: session.CookieName, Value: issued.Token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}).String()
	return &value
}

func requestError(err, authentication, csrf error) error {
	switch {
	case errors.Is(err, session.ErrCSRF):
		return csrf
	case errors.Is(err, session.ErrAuthentication):
		return authentication
	default:
		return err
	}
}

type filesSessionAdapter struct{ *iamSessionAdapter }

func (adapter filesSessionAdapter) AuthorizeRequest(ctx context.Context, token, csrf string, mutation bool) (files.RequestIdentity, error) {
	issued, err := adapter.authorize(ctx, token, csrf, mutation)
	if err != nil {
		return files.RequestIdentity{}, requestError(err, files.ErrAuthentication, files.ErrCSRF)
	}
	return files.RequestIdentity{ActorID: issued.Profile.ID, CSRF: issued.CSRF, ReplacementCookie: replacementSessionCookie(issued)}, nil
}

type schedulerSessionAdapter struct{ *iamSessionAdapter }

func (adapter schedulerSessionAdapter) AuthorizeRequest(ctx context.Context, token, csrf string, mutation bool) (scheduler.RequestIdentity, error) {
	issued, err := adapter.authorize(ctx, token, csrf, mutation)
	if err != nil {
		return scheduler.RequestIdentity{}, requestError(err, scheduler.ErrAuthentication, scheduler.ErrCSRF)
	}
	return scheduler.RequestIdentity{ActorID: issued.Profile.ID, CSRF: issued.CSRF, ReplacementCookie: replacementSessionCookie(issued)}, nil
}

type auditRequestAdapter struct{ *iamSessionAdapter }

func (adapter auditRequestAdapter) AuthorizeRequest(ctx context.Context, request *http.Request) (audit.AuthorizedRequest, audit.RequestFailure) {
	if request == nil {
		return audit.AuthorizedRequest{}, audit.RequestInternalFailed
	}
	token := ""
	if cookie, err := request.Cookie(session.CookieName); err == nil {
		token = cookie.Value
	}
	mutation := request.Method != http.MethodGet && request.Method != http.MethodHead
	issued, err := adapter.authorize(ctx, token, request.Header.Get("X-CSRF-Token"), mutation)
	if errors.Is(err, session.ErrCSRF) {
		return audit.AuthorizedRequest{}, audit.RequestAuthorizationFailed
	}
	if errors.Is(err, session.ErrAuthentication) {
		return audit.AuthorizedRequest{}, audit.RequestAuthenticationFailed
	}
	if err != nil {
		return audit.AuthorizedRequest{}, audit.RequestInternalFailed
	}
	authorized, err := audit.NewAuthorizedRequest(audit.Principal{ID: issued.Profile.ID}, issued.CSRF, replacementSessionCookie(issued))
	if err != nil {
		return audit.AuthorizedRequest{}, audit.RequestInternalFailed
	}
	return authorized, audit.RequestAuthorized
}

type sessionLoginFactAdapter struct{ recorder *audit.LoginRecorder }

func NewLoginFact(recorder *audit.LoginRecorder) session.LoginFactPort {
	return sessionLoginFactAdapter{recorder: recorder}
}

func (adapter sessionLoginFactAdapter) RecordLoginFact(ctx context.Context, tx database.Tx, fact session.LoginFact) error {
	if adapter.recorder == nil || !fact.AttemptID.Valid() {
		return audit.ErrInvalidArgument
	}
	mapped := audit.LoginFact{ActorType: audit.ActorAccount, Source: audit.Source(fact.Source), OccurredAt: fact.OccurredAt}
	switch fact.Outcome {
	case session.LoginSucceeded:
		actorRef := "account:" + fact.AccountID
		mapped.Outcome = audit.OutcomeSucceeded
		mapped.ActorRef = &actorRef
	case session.LoginFailed:
		if fact.AccountID != "" {
			return audit.ErrInvalidArgument
		}
		mapped.Outcome = audit.OutcomeFailed
	default:
		return audit.ErrInvalidArgument
	}
	_, err := adapter.recorder.RecordAttempt(ctx, tx, fact.AttemptID.Opaque(), mapped)
	return err
}

var (
	_ files.Authorizer               = filesAuthorizationAdapter{}
	_ scheduler.Authorizer           = schedulerAuthorizationAdapter{}
	_ audit.Authorizer               = auditAuthorizationAdapter{}
	_ files.RequestAuthenticator     = filesSessionAdapter{}
	_ scheduler.RequestAuthenticator = schedulerSessionAdapter{}
	_ audit.RequestAuthorizer        = auditRequestAdapter{}
	_ session.LoginFactPort          = sessionLoginFactAdapter{}
)
