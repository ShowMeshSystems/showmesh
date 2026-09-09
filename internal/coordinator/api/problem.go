package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// problemBaseURI is the fixed prefix contract section 6.6 anchors every
// problem "type" URI under. These are identifiers, not fetchable
// documentation pages; nothing in this package or its tests dereferences
// them over the network.
//
// ProblemBaseURI is the same value, exported. Step 9 wave 2's own finding
// (the orchestrator, section 5b item 3 of this wave's brief): the macro
// executor package (internal/coordinator/macro, which imports this
// package per macro_seam.go's forced import direction) needed this exact
// prefix for its own new problem type URIs
// (ProblemTypeMacroRunAlreadyInFlight and its two idempotency-conflict
// siblings, macro/problems.go) and, finding no exported constant to
// reference, minted a second copy of the literal instead. Exporting it
// here is "one base string, one place" as far as this package's own
// boundary allows; macro/problems.go still holds its own copy of the
// value rather than importing this constant, because reconciling that is
// an edit to internal/coordinator/macro, a file this wave's build order
// assigns to a different builder and marks not-to-be-touched — see this
// task's own report.
const (
	problemBaseURI = "https://showmesh.dev/problems/"
	ProblemBaseURI = problemBaseURI
)

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

	// ProblemTypeConflict is a 409: the request itself is valid, but this
	// coordinator's current state makes it unsafe or meaningless to act on
	// right now. Step 7 seam B's own use is
	// [discoveryRunConflictProblem]: a second discovery run refused while
	// one is already in flight, never queued. Distinct from
	// [ProblemTypeInvalidParameter] (a property of the request itself)
	// because the identical request would succeed at a different moment.
	ProblemTypeConflict = problemBaseURI + "conflict"

	// ProblemTypeInternalError matches the literal handlers.go's
	// writeInternalError already writes (problemBaseURI +
	// "internal-error"); handlers.go is owned by a different task in this
	// review pass, so this constant exists here for any code in this
	// package (methodNotAllowedProblem's sibling, and any future writer)
	// to use without re-deriving the string, but switching
	// writeInternalError itself to reference it is a one-line change left
	// to that file's owner — see this task's report.
	ProblemTypeInternalError = problemBaseURI + "internal-error"

	// ProblemTypeFPPStartPlaylistEvidenceNotCurrent is startPlaylist's own
	// ifBusy "refuse" guard (docs/bench/fpp-command-vocabulary.md section
	// 5) refusing because the evidence it would need to decide "is
	// something else currently playing?" is not itself current — never
	// proceeding on the grounds that it could not tell (CLAUDE.md: absence
	// of evidence is not evidence of absence). Deliberately its own type,
	// distinct from [ProblemTypeConflict] AND from
	// [ProblemTypeFPPStartPlaylistBusy] below: a review finding caught that
	// this case and [fppStartPlaylistBusyProblem]'s ("a DIFFERENT
	// playlist IS confirmed playing") both carried ProblemTypeConflict,
	// differing only in Title/Detail prose, which meant the only way a
	// client could tell them apart was by matching a substring of the
	// server's own English detail text — a client parsing prose across a
	// versioned contract boundary. See [fppStartPlaylistEvidenceNotCurrentProblem].
	ProblemTypeFPPStartPlaylistEvidenceNotCurrent = problemBaseURI + "fpp-start-playlist-evidence-not-current"

	// ProblemTypeFPPStartPlaylistBusy is [fppStartPlaylistBusyProblem]'s
	// own type (Step 8 review finding 8, this is the "finish the split"
	// half): startPlaylist's ifBusy="refuse" guard refusing because a
	// DIFFERENT playlist is CONFIRMED currently playing. Split out of
	// [ProblemTypeConflict] for the identical reason
	// [ProblemTypeFPPStartPlaylistEvidenceNotCurrent] was split out of it
	// one review finding earlier — that fix was only half-applied: this
	// case kept sharing ProblemTypeConflict with
	// [fppCommandReplayConflictProblem] and
	// [fppCommandReplayParamsConflictProblem], two idempotency-key
	// conflicts whose remedy ("mint a fresh key") is the OPPOSITE of this
	// one's ("resend with ifBusy: replace") — indistinguishable except by
	// matching `detail` prose, the exact defect the sibling type exists to
	// rule out. See [fppStartPlaylistBusyProblem].
	ProblemTypeFPPStartPlaylistBusy = problemBaseURI + "fpp-start-playlist-busy"

	// ProblemTypeUnsupportedObservationSchemaVersion is FPP-PLUGIN-COORDINATOR-CONTRACTS.md's own §1.6
	// step 5 refusal: POST /api/v1/integrations/fpp/playlist-entry-observations'
	// schemaVersion is not 1. Distinct from [ProblemTypeInvalidParameter]
	// because a client on this type can tell "this coordinator does not
	// speak the schema version you sent" apart from every other malformed
	// field, which has a different remedy (upgrade the plugin, not fix a
	// typo).
	ProblemTypeUnsupportedObservationSchemaVersion = problemBaseURI + "unsupported-observation-schema-version"

	// ProblemTypeObservationEntryKeyMismatch is FPP-PLUGIN-COORDINATOR-CONTRACTS.md's own §1.6 step 8
	// refusal: the coordinator re-derived entryKey from the submitted
	// identity fields (pkg/fppidentity.DeriveEntryKey) and it disagreed
	// with what the plugin sent. Distinct from
	// [ProblemTypeInvalidParameter]: this is not a malformed field, it is
	// two independent computations of the same value disagreeing, which a
	// client needs to be able to tell apart from an ordinary validation
	// error (contract §1.3: "a disagreement...is a blocker...never a
	// unilateral change on either side").
	ProblemTypeObservationEntryKeyMismatch = problemBaseURI + "observation-entry-key-mismatch"

	// ProblemTypeUnsupportedDefinitionSchemaVersion is
	// FPP-PLUGIN-COORDINATOR-CONTRACTS.md's own §3.4 step 5 refusal:
	// POST /api/v1/integrations/fpp/playlist-definitions' schemaVersion is
	// not 1. Mirrors [ProblemTypeUnsupportedObservationSchemaVersion]'s
	// identical reasoning for the sibling route.
	ProblemTypeUnsupportedDefinitionSchemaVersion = problemBaseURI + "unsupported-definition-schema-version"

	// ProblemTypeDefinitionHashMismatch is FPP-PLUGIN-COORDINATOR-CONTRACTS.md's own
	// §3.4 step 7 refusal, the load-bearing one: the coordinator
	// canonicalized the submitted `definition` and its SHA-256 disagreed
	// with the declared `playlistHash`. Distinct from
	// [ProblemTypeInvalidParameter] for the same reason
	// [ProblemTypeObservationEntryKeyMismatch] is: a client needs to tell
	// "the definition does not match its own declared hash" apart from an
	// ordinary malformed field.
	ProblemTypeDefinitionHashMismatch = problemBaseURI + "definition-hash-mismatch"
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
		// ADR-024 decision 6: a cookie-authenticated write must come from
		// the same origin. A bearer-token request carries no ambient
		// cookie for a cross-site page to ride, so it is exempt.
		Detail: "A cookie-authenticated write must come from the same origin. This request carried neither Sec-Fetch-Site: same-origin nor an Origin header naming the host it was addressed as. A bearer-token request is exempt from this check.",
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
		// ADR-024 decision 6 (owner decision 2026-08-12: strict), applied
		// to signing in itself: no bearer exemption here, unlike
		// [csrfProblem], because signing in has no pre-existing credential
		// yet that could be bearer-shaped in the first place.
		Detail: "Signing in must come from the same origin. This request carried neither Sec-Fetch-Site: same-origin nor an Origin header naming the host it was addressed as. There is no bearer-token exemption for this request.",
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

// fppEndpointsEnvVarSetProblem is Step 7 seam A review defect 3a's 409:
// [handlers.handlePutFPPEndpointsConfig]'s refusal while
// [Dependencies.FPPEndpointsEnvVarSet] is true. Shares [ProblemTypeConflict]
// with [discoveryRunConflictProblem]/[fppCommandReplayConflictProblem]
// below rather than minting a second "conflict" type URI — all three are
// the identical RFC 9457 shape this constant's own doc comment names ("the
// request itself is valid, but this coordinator's current state makes it
// unsafe or meaningless to act on right now"); detail is what tells them
// apart for a caller reading the body, exactly like every other problem
// class in this file that shares a Type/Status pair (see loginCSRFProblem
// vs. csrfProblem). detail names the exact variable and the two-step
// remedy (remove it, restart once) rather than a generic "conflict" — an
// operator hitting this needs to know it is action-shaped, not a
// permission or validation problem. RES-008 D1 is the decision that this
// coordinator's store must never accept a write that would disagree with
// SHOWMESH_FPP_ENDPOINTS on the very next restart.
func fppEndpointsEnvVarSetProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: SHOWMESH_FPP_ENDPOINTS is still set",
		Status: http.StatusConflict,
		Detail: "This write is refused because SHOWMESH_FPP_ENDPOINTS is still set in this coordinator's environment " +
			"— accepting it now would conflict with that variable on the next restart. Remove SHOWMESH_FPP_ENDPOINTS " +
			"and restart this coordinator once, then retry.",
	}
}

// fppEndpointsMigrationDeferredProblem is [fppEndpointsEnvVarSetProblem]'s
// remedy corrected for the one state in which that remedy is destructive.
// Both are the same 409 for the same reason (the variable is set, so a
// write cannot survive this coordinator's own disagreement rule), and the
// only difference is what the operator is told to do next.
//
// The standard detail says: remove SHOWMESH_FPP_ENDPOINTS, restart once,
// retry the write. That is safe once the migration has landed, because
// removing the variable then leaves the migrated store configuration
// behind. It is NOT safe while the startup migration is deferred
// (internal/coordinator/configsync.go), because no store configuration
// exists: removing the variable and restarting resolves this coordinator
// to ZERO endpoints, and the retried write then fails on the same
// unwritable store that deferred the migration in the first place. The
// operator would have followed the API's own written instruction and lost
// every configured endpoint. A review of the deferral fix caught this;
// the sequence closes with no mistake at any step.
func fppEndpointsMigrationDeferredProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: the startup migration of SHOWMESH_FPP_ENDPOINTS was deferred",
		Status: http.StatusConflict,
		Detail: "This write is refused because the SHOWMESH_FPP_ENDPOINTS migration could not be saved on this boot, " +
			"so the store holds no endpoint configuration yet. Do NOT remove SHOWMESH_FPP_ENDPOINTS — it is " +
			"currently the only copy of this coordinator's endpoint list, and removing it now would leave zero " +
			"endpoints on the next restart. Check the coordinator's data volume (often full, read-only, or damaged) " +
			"and restart; the migration retries on every boot. Once it succeeds, remove the variable and retry.",
	}
}

// resolumeInstancesEnvVarSetProblem is [fppEndpointsEnvVarSetProblem]'s
// mirror for Track G seam G-2 (ADR-039 decision 4): refuses PUT
// /api/v1/config/resolume.instances with 409 while SHOWMESH_RESOLUME_URL is
// still set, for the identical reason — a write accepted now cannot
// survive this coordinator's own next restart.
func resolumeInstancesEnvVarSetProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: SHOWMESH_RESOLUME_URL is still set",
		Status: http.StatusConflict,
		Detail: "This write is refused because SHOWMESH_RESOLUME_URL is still set in this coordinator's environment " +
			"— accepting it now would conflict with that variable on the next restart. Remove SHOWMESH_RESOLUME_URL " +
			"and SHOWMESH_RESOLUME_ID and restart this coordinator once, then retry.",
	}
}

// resolumeInstancesMigrationDeferredProblem is
// [fppEndpointsMigrationDeferredProblem]'s mirror: the standard remedy
// above is not safe while the startup migration is deferred, because no
// store configuration exists yet — removing the variable and restarting
// would resolve this coordinator to zero Resolume instances.
func resolumeInstancesMigrationDeferredProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: the startup migration of SHOWMESH_RESOLUME_URL was deferred",
		Status: http.StatusConflict,
		Detail: "This write is refused because the SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID migration could not be " +
			"saved on this boot, so the store holds no resolume.instances configuration yet. Do NOT remove " +
			"SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID — they are currently the only copy of this coordinator's " +
			"Resolume instance. Check the coordinator's data volume (often full, read-only, or damaged) and restart; " +
			"the migration retries on every boot. Once it succeeds, remove the variables and retry.",
	}
}

// fppMQTTEnvVarSetProblem is [fppEndpointsEnvVarSetProblem]'s mirror for
// Track G seam G-3 (ADR-039 decision 4): refuses PUT
// /api/v1/config/fpp.mqtt with 409 while SHOWMESH_FPP_MQTT_BROKER_URL is
// still set, for the identical reason — a write accepted now cannot
// survive this coordinator's own next restart.
func fppMQTTEnvVarSetProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: SHOWMESH_FPP_MQTT_BROKER_URL is still set",
		Status: http.StatusConflict,
		Detail: "This write is refused because SHOWMESH_FPP_MQTT_BROKER_URL is still set in this coordinator's " +
			"environment — accepting it now would conflict with that variable on the next restart. Remove " +
			"SHOWMESH_FPP_MQTT_BROKER_URL, SHOWMESH_FPP_MQTT_USERNAME, SHOWMESH_FPP_MQTT_PASSWORD, " +
			"SHOWMESH_FPP_MQTT_TOPIC_PREFIX, and SHOWMESH_FPP_MQTT_HOSTS and restart this coordinator once, then retry.",
	}
}

// fppMQTTMigrationDeferredProblem is [fppEndpointsMigrationDeferredProblem]'s
// mirror: the standard remedy above is not safe while the startup
// migration is deferred, because no store configuration exists yet —
// removing the variables and restarting would resolve this coordinator to
// an unconfigured fpp.mqtt collector.
func fppMQTTMigrationDeferredProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: the startup migration of SHOWMESH_FPP_MQTT_BROKER_URL was deferred",
		Status: http.StatusConflict,
		Detail: "This write is refused because the SHOWMESH_FPP_MQTT_* migration could not be saved on this boot, " +
			"so the store holds no fpp.mqtt configuration yet. Do NOT remove SHOWMESH_FPP_MQTT_* — they are " +
			"currently the only copy of this coordinator's FPP MQTT configuration. Check the coordinator's data " +
			"volume (often full, read-only, or damaged) and restart; the migration retries on every boot. Once it " +
			"succeeds, remove the variables and retry.",
	}
}

// assetsSettingsEnvVarSetProblem is [fppEndpointsEnvVarSetProblem]'s mirror
// for Track G seam G-4 (ADR-039 decision 4): refuses PUT
// /api/v1/config/assets.settings with 409 while any of the four
// SHOWMESH_ASSET_* settings variables is still set, for the identical
// reason — a write accepted now cannot survive this coordinator's own next
// restart.
func assetsSettingsEnvVarSetProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: one or more SHOWMESH_ASSET_* settings variables are still set",
		Status: http.StatusConflict,
		Detail: "This write is refused because one or more of SHOWMESH_ASSET_CONTENT_BASE_URL, " +
			"SHOWMESH_ASSET_MAX_UPLOAD_BYTES, SHOWMESH_ASSET_SYNC_INTERVAL, or SHOWMESH_ASSET_INVENTORY_INTERVAL is " +
			"still set in this coordinator's environment — accepting it now would conflict with those variables on " +
			"the next restart. Remove all four from your environment (SHOWMESH_ASSET_DIR is unaffected — it stays " +
			"environment-only) and restart this coordinator once, then retry.",
	}
}

// assetsSettingsMigrationDeferredProblem is
// [resolumeInstancesMigrationDeferredProblem]'s mirror: the standard remedy
// above is not safe while the startup migration is deferred, because no
// store configuration exists yet — removing the variables and restarting
// would resolve this coordinator to config.DefaultAssetSettings rather than
// to what it is actually using right now.
func assetsSettingsMigrationDeferredProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Configuration write refused: the startup migration of the SHOWMESH_ASSET_* settings variables was deferred",
		Status: http.StatusConflict,
		Detail: "This write is refused because the SHOWMESH_ASSET_* settings migration could not be saved on this " +
			"boot, so the store holds no assets.settings configuration yet. Do NOT remove those variables — they are " +
			"currently the only copy of this coordinator's asset store settings. Check the coordinator's data volume " +
			"(often full, read-only, or damaged) and restart; the migration retries on every boot. Once it succeeds, " +
			"remove the variables and retry.",
	}
}

// discoveryRunConflictProblem is Step 7 seam B review DEFECT 7a's 409:
// [handlers.handleStartDiscoveryRun] refuses a second discovery run while
// one is already in flight on this coordinator, rather than queuing it —
// see that handler's own doc comment for why interleaving two runs is the
// actual failure (not merely a wasted duplicate poll): it can leave every
// declared node in the installation reading not_seen, because whichever
// run's RecordNodeDiscoverySeen pass lands last wins "the most recent run"
// regardless of which one an operator actually meant to be looking at.
// principalLockoutProblem is Track G seam G-5's own 409 (requirement 3 /
// ADR-039 decision 8): the request would leave no enabled principal able
// to reach principal:write, either by disabling the last enabled admin, by
// changing its role away from principal:write, or by revoking the last
// credential able to reach that scope. Shares [ProblemTypeConflict] with
// every other "valid request, unsafe right now" refusal in this file —
// detail states which of the three triggered it and the remedy.
func principalLockoutProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Refused: this would lock out every administrator",
		Status: http.StatusConflict,
		Detail: detail,
	}
}

// cueCatalogClaimConflictProblem is [handlers.handlePostCueCatalogDeploy]'s
// own 409 (TRACK-H-cues-and-playlists.md section H5 build item 2's ruling):
// the resolved catalog for nodeID carries at least one H0.5 exclusive-claim
// conflict ([assetsync.Catalog.Conflicts]). Every conflict is named, not
// only the first, so an operator sees the whole readiness problem in one
// response rather than fixing one collision at a time by repeated deploy
// attempts.
func cueCatalogClaimConflictProblem(nodeID string, conflicts []assetsync.CatalogConflict) v1.Problem {
	details := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		details = append(details, c.Detail())
	}
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Cue catalog deploy refused: conflicting exclusive claims",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"the Cue catalog resolved for node %q cannot be deployed: %s. Fix the authoring conflict (change one "+
				"Cue's outputs, or the Playlists that reference it) and retry.",
			nodeID, strings.Join(details, "; ")),
	}
}

// showmeshAudioPlaylistConflictProblem is [handlers.handlePutShowPlaylist]'s
// own 409 (TRACK-H-cues-and-playlists.md section H5 build item 8): a Show
// may hold at most one showmesh-audio playlist, because there is exactly
// one background-audio session per node ([cueactivation.BackgroundSessionID])
// for one to run in. The old behavior silently applied whichever playlist
// sorted first by object id and logged a warning — an operator-invisible
// alphabetical choice for what plays out of a speaker. Naming both ids
// lets the operator pick which one to keep.
func showmeshAudioPlaylistConflictProblem(existingID, newID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Refused: this Show already has a showmesh-audio playlist",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"show.playlist %q is already this Show's showmesh-audio playlist; there is exactly one background-audio "+
				"session per node, so a second one (%q) is refused rather than silently choosing between them. Edit "+
				"%q instead, or change its runner, then retry.",
			existingID, newID, existingID),
	}
}

func discoveryRunConflictProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Discovery run already in progress",
		Status: http.StatusConflict,
		Detail: "another discovery run is currently in progress on this coordinator; this request is refused outright rather than queued — wait for the in-progress run to finish and try again",
	}
}

// fppCommandReplayConflictProblem is Step 7 seam C review defect 6's own
// refusal: an idempotency key is scoped to the exact (action, target) it
// was first used against — schemaV6's UNIQUE constraint on
// commands.idempotency_key alone cannot express that, so this handler
// checks it before ever treating a matching key as a replay. Reusing a key
// against a DIFFERENT action or a DIFFERENT instanceId is not a replay (a
// replay dispatches nothing and returns the ORIGINAL command's own result
// under its OWN target); answering it as one would report a stored outcome
// under a target that request never actually named — see
// fppcommand_handler.go's handleFPPCommandReplay for the full accounting
// of why that is a false statement about the system, not a convenience.
func fppCommandReplayConflictProblem(existingID, existingAction, existingTargetID, requestedAction, requestedTargetID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a different command",
		Status: http.StatusConflict,
		// An idempotency key is scoped to the exact (action, instanceId) it
		// was first used against — this is that scope stated in the wire
		// text, not a citation of where the rule is defined.
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q, instance %q); this request names a "+
				"different action %q or instance %q. Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, existingAction, existingTargetID, requestedAction, requestedTargetID),
	}
}

// fppCommandReplayParamsConflictProblem is Step 8's own extension of
// [fppCommandReplayConflictProblem]: an idempotency key reused against the
// SAME action and the SAME target, but with DIFFERENT normalized params,
// is also a conflict, never a replay. An idempotency key names ONE
// intended operation; returning the original result would report the
// outcome of (say) starting playlist A as the answer to a request to
// start playlist B, and dispatching the new request would break
// idempotency outright by running the SAME key twice for two different
// operations. Refusing outright — dispatching nothing, exactly like every
// other conflict this endpoint reports — is the only response that is
// honest about both requests. existingParamsJSON/requestedParamsJSON are
// both canonical (defaults applied, sorted keys — see
// [canonicalParamsJSON]), so this can name exactly which keys differ
// rather than dumping two opaque JSON blobs at the caller.
func fppCommandReplayParamsConflictProblem(existingID, action, targetID, existingParamsJSON, requestedParamsJSON string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used with different parameters",
		Status: http.StatusConflict,
		// An idempotency key names ONE intended operation — this is that
		// rule stated in the wire text, not a citation of where it is
		// defined.
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q, instance %q) with params %s; this request "+
				"has the SAME action and instance but DIFFERENT params: %s. Mint a fresh idempotencyKey for a "+
				"genuinely new request.",
			existingID, action, targetID, existingParamsJSON, requestedParamsJSON),
	}
}

// fppStartPlaylistEvidenceNotCurrentProblem is startPlaylist's own ifBusy
// "refuse" guard (capture section 5) refusing because the evidence it
// would need to decide "is something else currently playing?" is not
// itself current — never proceeding on the grounds that it could not
// tell (CLAUDE.md: absence of evidence is not evidence of absence, the
// project's own recurring lesson, applied here a fifth time). signal
// names which evidence was not current (fpp.status or
// fpp.playlist.name); reason is that signal's own
// [resolveConfirmationEvidence] explanation.
func fppStartPlaylistEvidenceNotCurrentProblem(instanceID, signal, reason string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeFPPStartPlaylistEvidenceNotCurrent,
		Title:  "Start Playlist refused: evidence needed to evaluate ifBusy is not current",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"Can't tell whether instance %q is busy — this needs CURRENT evidence of %s, and the most recent "+
				"reading isn't current (%s). Retry once fresh evidence arrives, or resend with ifBusy=%q to start "+
				"anyway.",
			instanceID, signal, reason, fppIfBusyReplace),
	}
}

// fppStartPlaylistBusyProblem is startPlaylist's own ifBusy=refuse (the
// default) refusal when a DIFFERENT playlist is confirmed to be
// currently playing (capture section 5): refused before anything is sent
// to FPP, naming what is currently playing. This is a GUARD, not a lock
// — it is evaluated against evidence that can go stale between this
// check and dispatch, and it cannot prevent a race against FPP's own
// scheduler; see [fppPrimitive.PreDispatchCheck]'s own doc comment on
// primitiveStartPlaylist.
//
// Type is [ProblemTypeFPPStartPlaylistBusy] (Step 8 review finding 8),
// not the plain [ProblemTypeConflict] this constructor originally used:
// that shared type made this 409 indistinguishable, except by matching
// `detail` prose, from [fppCommandReplayConflictProblem]/
// [fppCommandReplayParamsConflictProblem]'s idempotency-key conflicts —
// two conditions with OPPOSITE remedies ("mint a fresh key" versus
// "resend with ifBusy: replace") that a client could not tell apart by
// `type` alone. See [ProblemTypeFPPStartPlaylistBusy]'s own doc comment.
func fppStartPlaylistBusyProblem(instanceID, currentlyPlaying string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeFPPStartPlaylistBusy,
		Title:  "Start Playlist refused: a different playlist is currently playing",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"Instance %q is currently playing %q, so this request is refused (ifBusy=%q, the default). Resend "+
				"with ifBusy=%q to replace the running show, or wait for it to finish.",
			instanceID, currentlyPlaying, fppIfBusyRefuse, fppIfBusyReplace),
	}
}
