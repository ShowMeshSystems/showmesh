package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

func TestCredentialResolutionFailuresAreAuditedWithoutSecrets(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*http.Request)
		wantForm   identity.CredentialForm
		wantReason string
		secret     string
	}{
		{
			name: "malformed authorization",
			configure: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic should-never-be-recorded")
			},
			wantForm: identity.FormToken, wantReason: "malformed_authorization", secret: "should-never-be-recorded",
		},
		{
			name: "invalid bearer token",
			configure: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer smt_should-never-be-recorded")
			},
			wantForm: identity.FormToken, wantReason: "invalid_token", secret: "smt_should-never-be-recorded",
		},
		{
			name: "invalid session",
			configure: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-should-never-be-recorded"})
			},
			wantForm: identity.FormSession, wantReason: "invalid_session", secret: "session-should-never-be-recorded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestIdentityService(t, fixedClock(testNow))
			api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
			tt.configure(req)
			rec := httptest.NewRecorder()

			api.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /session status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}

			entries, err := svc.ListAudit(context.Background(), 0, 10)
			if err != nil {
				t.Fatalf("list audit: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("audit entries = %d, want exactly 1: %+v", len(entries), entries)
			}
			entry := entries[0]
			if entry.Kind != identity.AuditAuthFail || entry.Action != "credential.resolve" || entry.Target != "/api/v1/session" {
				t.Errorf("audit identity = kind %q action %q target %q, want auth_failure/credential.resolve//api/v1/session", entry.Kind, entry.Action, entry.Target)
			}
			if entry.Form != tt.wantForm {
				t.Errorf("audit form = %q, want %q", entry.Form, tt.wantForm)
			}
			if got, _ := entry.Params["reason"].(string); got != tt.wantReason {
				t.Errorf("audit reason = %q, want %q", got, tt.wantReason)
			}
			if serialized := fmt.Sprintf("%+v", entry); strings.Contains(serialized, tt.secret) {
				t.Fatalf("audit entry contains credential material %q: %s", tt.secret, serialized)
			}
		})
	}
}

func TestNoCredentialDoesNotCreateAuthenticationFailureAudit(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /session status = %d, want 200", rec.Code)
	}
	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("anonymous request created audit entries: %+v", entries)
	}
}

type failingCredentialAuditService struct{ identity.Service }

func (failingCredentialAuditService) WriteAudit(context.Context, identity.AuditEntry) error {
	return errors.New("simulated audit failure")
}

func TestCredentialFailureAuditOutageDoesNotChangeAuthenticationResult(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	api := New(authTestDeps(failingCredentialAuditService{Service: svc}), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	req.Header.Set("Authorization", "Bearer smt_invalid")
	rec := httptest.NewRecorder()

	api.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /audit status = %d, want 401 even when auth-failure audit write fails; body: %s", rec.Code, rec.Body.String())
	}
}
