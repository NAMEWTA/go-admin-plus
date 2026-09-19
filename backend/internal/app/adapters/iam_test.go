package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/account"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler"
)

type sessionServiceStub struct {
	issued session.Issued
	err    error
}

func (stub sessionServiceStub) AuthorizeRequest(context.Context, string, string, bool) (session.Issued, error) {
	return stub.issued, stub.err
}

type requestIdentityResult struct {
	actorID, csrf string
	cookie        *string
	err           error
}

func TestSessionCompositionAdaptersMapCanonicalIdentityAndFailures(t *testing.T) {
	csrf := strings.Repeat("c", 43)
	issued := session.Issued{Profile: account.Profile{ID: "account-product-admin"}, Token: "opaque-replacement", CSRF: csrf, Rotated: true}

	type adapterCase struct {
		name                 string
		authentication, csrf error
		invoke               func(*iamSessionAdapter) requestIdentityResult
	}
	cases := []adapterCase{
		{name: "files", authentication: files.ErrAuthentication, csrf: files.ErrCSRF, invoke: func(base *iamSessionAdapter) requestIdentityResult {
			value, err := (filesSessionAdapter{base}).AuthorizeRequest(context.Background(), "token", csrf, true)
			return requestIdentityResult{value.ActorID, value.CSRF, value.ReplacementCookie, err}
		}},
		{name: "scheduler", authentication: scheduler.ErrAuthentication, csrf: scheduler.ErrCSRF, invoke: func(base *iamSessionAdapter) requestIdentityResult {
			value, err := (schedulerSessionAdapter{base}).AuthorizeRequest(context.Background(), "token", csrf, true)
			return requestIdentityResult{value.ActorID, value.CSRF, value.ReplacementCookie, err}
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"/success", func(t *testing.T) {
			base, err := newIAMSessionAdapter(sessionServiceStub{issued: issued})
			if err != nil {
				t.Fatal(err)
			}
			result := testCase.invoke(base)
			if result.err != nil || result.actorID != issued.Profile.ID || result.csrf != csrf || result.cookie == nil {
				t.Fatalf("identity = %#v", result)
			}
			for _, attribute := range []string{session.CookieName + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict"} {
				if !strings.Contains(*result.cookie, attribute) {
					t.Fatalf("replacement cookie missing %s", attribute)
				}
			}
		})
		for _, failure := range []struct {
			name     string
			upstream error
			want     error
		}{{"authentication", session.ErrAuthentication, testCase.authentication}, {"csrf", session.ErrCSRF, testCase.csrf}} {
			t.Run(testCase.name+"/"+failure.name, func(t *testing.T) {
				base, err := newIAMSessionAdapter(sessionServiceStub{err: failure.upstream})
				if err != nil {
					t.Fatal(err)
				}
				if result := testCase.invoke(base); !errors.Is(result.err, failure.want) {
					t.Fatalf("mapped error = %v, want %v", result.err, failure.want)
				}
			})
		}
	}
}

func TestSessionCompositionAdapterRequiresProvider(t *testing.T) {
	if _, err := newIAMSessionAdapter(nil); err == nil {
		t.Fatal("nil session provider accepted")
	}
}
