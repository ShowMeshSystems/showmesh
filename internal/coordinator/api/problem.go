package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
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
