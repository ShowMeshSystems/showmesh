package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// problemBaseURI is the fixed prefix contract section 6.6 anchors every
// problem "type" URI under. These are identifiers, not fetchable
// documentation pages; nothing in this package or its tests dereferences
// them over the network.
const problemBaseURI = "https://showmesh.dev/problems/"

// Problem type URIs for the six classes this API produces (contract
// section 6.6, plus the 500 and 405 classes finding 2.4/2.8 of the Step 3
// review added). Exported as constants, not scattered string literals, so
// api/openapi.yaml's Problem.type enum and this package's writers cannot
// silently drift apart from each other — that enum now actually exists
// (api/openapi.yaml, components.schemas.Problem.properties.type.enum) and
// lists exactly these six values, so this comment's guarantee is
// something TestOpenAPIProblemSchemaMatchesEveryClass and
// TestOpenAPISchemasMatchRealResponses in openapi_test.go can and do
// enforce, not merely an assertion made in prose.
const (
	ProblemTypeUnsupportedAPIVersion = problemBaseURI + "unsupported-api-version"
	ProblemTypeResourceNotFound      = problemBaseURI + "resource-not-found"
	ProblemTypeInvalidParameter      = problemBaseURI + "invalid-parameter"
	ProblemTypeUnauthorized          = problemBaseURI + "unauthorized"
	ProblemTypeMethodNotAllowed      = problemBaseURI + "method-not-allowed"

	// ProblemTypeForbidden is ADR-024 decision 4's "403 means authenticated
	// but missing a scope" class, added by this step. Distinct from
	// [ProblemTypeUnauthorized] ("401 means no valid credential"): a client
	// dispatching on type, not status alone, can tell "log in" apart from
	// "this principal cannot do that" without parsing prose.
	ProblemTypeForbidden = problemBaseURI + "forbidden"

	// ProblemTypeCSRFRejected is ADR-024 decision 6's same-origin rule: a
	// cookie-authenticated write with no (or a non-"same-origin")
	// Sec-Fetch-Site header. Kept distinct from ProblemTypeForbidden
	// (missing scope) even though both are 403s, because the fix for each
	// is completely different — one names a role change, the other names a
	// deployment/browser problem — and collapsing them would send an
	// operator debugging "the buttons do nothing" to the wrong place.
	ProblemTypeCSRFRejected = problemBaseURI + "csrf-rejected"

	// ProblemTypeTooManyRequests is ADR-024 decision 8's login
	// concurrency-bound rejection: a queued login attempt that would
	// exceed [Options.LoginConcurrency]'s wait bound. Carries a
	// Retry-After response header, per that decision's "rejected with a
	// retry-after".
	ProblemTypeTooManyRequests = problemBaseURI + "too-many-requests"

	// ProblemTypeCredentialInURL is ADR-024 decision 1's URL rule: a
	// request whose query string contains [identity.TokenPrefix]. The
	// detail text never echoes the offending query string — see
	// withIdentity in auth.go, the only caller.
	ProblemTypeCredentialInURL = problemBaseURI + "credential-in-url"

	// ProblemTypeInternalError matches the literal handlers.go's
	// writeInternalError already writes (problemBaseURI +
	// "internal-error"); handlers.go is owned by a different task in this
	// review pass, so this constant exists here for any code in this
	// package (methodNotAllowedProblem's sibling, and any future writer)
	// to use without re-deriving the string, but switching
	// writeInternalError itself to reference it is a one-line change left
	// to that file's owner — see this task's report.
	ProblemTypeInternalError = problemBaseURI + "internal-error"
)

// supportedAPIVersions is the fixed, single-element list this coordinator
// serves. A slice (not a scalar) because the wire field is a list per
// contract section 6.6's pinned example, anticipating v2 existing someday
// alongside v1 rather than replacing it outright.
var supportedAPIVersions = []int{1}

// writeProblem writes p as application/problem+json at p.Status, per
// contract section 6.6, stamping p.ServerTime from now first. This is the
// one and only place ServerTime is set on any [v1.Problem] this API
// produces — an orchestrator correction: contract section 6.2 requires
// serverTime on every response with no exception, and an error is a
// response. Every problem constructor below deliberately leaves
// ServerTime unset, so a call site cannot forget it; only writeProblem
// can supply it, and every problem in this package goes through
// writeProblem to reach the wire.
//
// It never fails loudly: if encoding itself somehow errors (it cannot, for
// a value this package builds from string/int fields), the already-written
// status code and headers stand and the body is simply short — there is
// no second response to fall back to once WriteHeader has been called.
func writeProblem(w http.ResponseWriter, logger *slog.Logger, now time.Time, p v1.Problem) {
	p.ServerTime = formatTime(now)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(p); err != nil {
		if logger != nil {
			logger.Warn("failed to encode problem response", "error", err, "type", p.Type)
		}
	}
}

func unsupportedAPIVersionProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:              ProblemTypeUnsupportedAPIVersion,
		Title:             "Unsupported API version",
		Status:            http.StatusBadRequest,
		Detail:            detail,
		SupportedVersions: supportedAPIVersions,
	}
}

func resourceNotFoundProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeResourceNotFound,
		Title:  "Resource not found",
		Status: http.StatusNotFound,
		Detail: detail,
	}
}

func invalidParameterProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeInvalidParameter,
		Title:  "Invalid parameter",
		Status: http.StatusBadRequest,
		Detail: detail,
	}
}

func unauthorizedProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeUnauthorized,
		Title:  "Unauthorized",
		Status: http.StatusUnauthorized,
		Detail: detail,
	}
}

// forbiddenProblem is ADR-024 decision 4's 403: authenticated, but scope
// does not name a scope the requesting principal holds. detail names the
// missing scope by value — decision 4 requires this explicitly ("its RFC
// 9457 problem document names the missing scope"), not just "forbidden".
func forbiddenProblem(scope identity.Scope) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeForbidden,
		Title:  "Forbidden",
		Status: http.StatusForbidden,
		Detail: "this principal does not hold the required scope: " + string(scope),
	}
}

// csrfProblem is ADR-024 decision 6's same-origin rejection for a
// cookie-authenticated write, used by [handlers.writeGuard]. Do not reuse
// this for [handlers.loginCSRFGuard] — see [loginCSRFProblem], its own
// accurate detail, and why the two must not share one.
func csrfProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeCSRFRejected,
		Title:  "CSRF check failed",
		Status: http.StatusForbidden,
		Detail: "a cookie-authenticated write requires the Sec-Fetch-Site: same-origin request header (ADR-024 decision 6); a bearer-token-authenticated request is exempt",
	}
}

// loginCSRFProblem is [handlers.loginCSRFGuard]'s own rejection for
// POST /api/v1/session and POST /api/v1/bootstrap (Step 7 seam 0, S0-2).
// Same Type/Title/Status as [csrfProblem] — both are ADR-024 decision
// 6-shaped 403s, and api/openapi.yaml's Problem.type enum has only one
// "csrf-rejected" value for both — but a DIFFERENT Detail, because
// csrfProblem's text is false for these two endpoints: they are
// unauthenticated by construction (ADR-024 decision 8, no principal exists
// yet for a credential to name), so there is no "cookie-authenticated
// write" to describe, and there is deliberately no bearer exemption here
// either — see [handlers.loginCSRFGuard]'s doc comment for why a login
// request has no pre-existing credential that could BE bearer-shaped in
// the first place. Reusing csrfProblem's Detail for this rejection told a
// `curl` caller the exact opposite of the truth on both counts; this
// exists so the wire and api/openapi.yaml's LoginCSRFRejected description
// agree instead of contradicting each other.
func loginCSRFProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeCSRFRejected,
		Title:  "CSRF check failed",
		Status: http.StatusForbidden,
		Detail: "POST /api/v1/session and POST /api/v1/bootstrap require the Sec-Fetch-Site: same-origin request header (ADR-024 decision 6, owner decision 2026-08-12: strict); there is no bearer exemption for this rejection, because both endpoints are unauthenticated by construction and carry no pre-existing credential that could be bearer-shaped",
	}
}

// tooManyRequestsProblem is ADR-024 decision 8's login-concurrency
// rejection. The caller (session.go) sets the Retry-After header
// separately, before calling writeProblem, since [v1.Problem] carries no
// field for it (it belongs on the HTTP response, not the problem body).
func tooManyRequestsProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeTooManyRequests,
		Title:  "Too many requests",
		Status: http.StatusTooManyRequests,
		Detail: detail,
	}
}

// credentialInURLProblem is ADR-024 decision 1's URL rule. detail must
// never echo the request's actual query string — see withIdentity in
// auth.go, the only caller.
func credentialInURLProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeCredentialInURL,
		Title:  "Credential in URL",
		Status: http.StatusBadRequest,
		Detail: detail,
	}
}

// methodNotAllowedProblem is contract section 6.6's fifth problem class,
// added by the Step 3 review (finding 2.8): a path this coordinator
// serves, requested with a method it does not. detail names the methods
// that path does serve — middleware.go's
// methodNotAllowedInterceptor is the only caller, and it derives that
// list from net/http.ServeMux's own Allow header rather than a
// hand-maintained one, so this constructor never invents it itself.
func methodNotAllowedProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeMethodNotAllowed,
		Title:  "Method not allowed",
		Status: http.StatusMethodNotAllowed,
		Detail: detail,
	}
}
