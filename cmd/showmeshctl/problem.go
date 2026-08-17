package main

import "fmt"

// problem is an RFC 9457 application/problem+json document, per contract
// §6.6: the status code alone cannot distinguish "unsupported API version"
// from "invalid parameter" from "resource not found", so this CLI reads
// Title/Detail rather than guessing from Status alone.
type problem struct {
	Type              string `json:"type"`
	Title             string `json:"title"`
	Status            int    `json:"status"`
	Detail            string `json:"detail"`
	SupportedVersions []int  `json:"supportedVersions,omitempty"`
}

// Problem type URIs named in contract §6.6, matched by exact string, not
// path suffix: a coordinator's unrelated 404 (a typo'd path, a proxy's own
// error page) must never be misread as one of these classes.
const (
	problemUnsupportedAPIVersion = "https://showmesh.dev/problems/unsupported-api-version"
	problemResourceNotFound      = "https://showmesh.dev/problems/resource-not-found"
	problemInvalidParameter      = "https://showmesh.dev/problems/invalid-parameter"
	problemUnauthorized          = "https://showmesh.dev/problems/unauthorized"

	// problemForbidden: authenticated, but missing a scope. Distinct from
	// problemUnauthorized (no valid credential at all) so this CLI can
	// send an operator to "ask for a scope" rather than "check your token".
	problemForbidden = "https://showmesh.dev/problems/forbidden"
	// problemCSRFRejected: the same-origin rule for a cookie-authenticated
	// write. showmeshctl never presents a cookie, so this cannot trigger
	// it today, but is classified explicitly rather than left to a
	// generic 403 handler in case a future command adds cookie support.
	problemCSRFRejected = "https://showmesh.dev/problems/csrf-rejected"
	// problemTooManyRequests: the login concurrency bound was exceeded;
	// its Retry-After header is surfaced explicitly rather than left for
	// the operator to guess.
	problemTooManyRequests = "https://showmesh.dev/problems/too-many-requests"
	// problemCredentialInURL: a request whose query string carried the
	// token prefix. showmeshctl never does this itself (see client.go's
	// applyHeaders), but is classified anyway so a defect that does is
	// reported as a usage bug in this CLI, not an opaque server error.
	problemCredentialInURL = "https://showmesh.dev/problems/credential-in-url"

	// problemConflict: the request is valid, but this coordinator's
	// current state makes it unsafe or meaningless to act on right now —
	// a different playlist confirmed playing under ifBusy=refuse, or a
	// replayed idempotency key reused against different action/target/params.
	problemConflict = "https://showmesh.dev/problems/conflict"

	// problemFPPStartPlaylistEvidenceNotCurrent: "fpp start-playlist"
	// ifBusy=refuse's OTHER 409 — the coordinator could not tell what is
	// playing, rather than confirming a different playlist is. Shares
	// exitConflict with problemConflict: both are a deliberate, retryable
	// refusal, with the specific remedy in stderr.
	problemFPPStartPlaylistEvidenceNotCurrent = "https://showmesh.dev/problems/fpp-start-playlist-evidence-not-current"

	// problemFPPStartPlaylistBusy: the sibling case — a DIFFERENT
	// playlist IS confirmed playing under ifBusy=refuse. Same exitConflict
	// mapping, for the same reason.
	problemFPPStartPlaylistBusy = "https://showmesh.dev/problems/fpp-start-playlist-busy"

	// The three Step 9 additions below are internal/coordinator/macro/problems.go's
	// own ProblemTypeMacroRunAlreadyInFlight/
	// ProblemTypeMacroRunIdempotencyMacroConflict/
	// ProblemTypeMacroRunIdempotencyRevisionConflict (STEP-9-SPEC.md
	// section 2.6 and section 6.2), matched by exact string for the same
	// "do not misread an unrelated 404/409" reason every constant above
	// is. All three are 409s and all three map to [exitConflict] below,
	// matching this file's own established "one exit code covers every
	// deliberate, retryable refusal" pattern (see
	// problemFPPStartPlaylistEvidenceNotCurrent's doc comment) — each gets
	// its own named case anyway, rather than relying on the (also correct)
	// generic 409 status fallback, so a maintainer reading this switch sees
	// every problem type this program actually classifies rather than
	// three of them hiding behind "whatever falls through".

	// problemMacroRunAlreadyInFlight is ADR-031 decision 6's overlap
	// refusal: a second run of a macro already running.
	problemMacroRunAlreadyInFlight = "https://showmesh.dev/problems/macro-run-already-in-flight"
	// problemMacroRunIdempotencyMacroConflict is the same idempotency key
	// reused for a different macro id.
	problemMacroRunIdempotencyMacroConflict = "https://showmesh.dev/problems/macro-run-idempotency-macro-conflict"
	// problemMacroRunIdempotencyRevisionConflict is the same idempotency
	// key reused for the same macro at a different pinned revision (the
	// macro was edited between the two submissions).
	problemMacroRunIdempotencyRevisionConflict = "https://showmesh.dev/problems/macro-run-idempotency-revision-conflict"
)

// Exit codes, documented in --help (usage.go) so a script wrapping this
// tool can branch on $? instead of scraping stderr.
//
// exitForbidden and exitRateLimited are deliberately distinct from
// exitUnauthorized: conflating "no valid credential" (401), "missing a
// scope" (403), and "rate limited" (429) into one code would send a retry
// script to the wrong branch.
const (
	exitOK                  = 0
	exitUsage               = 1
	exitUnreachable         = 2
	exitUnauthorized        = 3
	exitVersionIncompatible = 4
	exitNotFound            = 5
	exitAPIError            = 6
	exitForbidden           = 7
	exitRateLimited         = 8

	// exitCommandUnconfirmed: an "fpp <verb>" write subcommand completed a
	// successful HTTP round trip, but its own result was "unconfirmed" —
	// ADR-003: a 200 is never conflated with the command having taken
	// effect. Distinct from exitAPIError (6): that means the REQUEST
	// itself failed; this means it succeeded and said so honestly.
	exitCommandUnconfirmed = 9

	// exitConflict: a 409 — this coordinator's current state makes the
	// request unsafe or meaningless right now (a different playlist
	// playing under ifBusy=refuse, stale evidence, or a replayed
	// idempotency key against different params). Distinct from
	// exitAPIError: the coordinator is healthy and declined on purpose,
	// with its own remedy in stderr.
	exitConflict = 10

	// exitActionUnconfirmable: a "resolume action <verb>" subcommand
	// dispatched successfully, but its effect could not be told apart
	// from its pre-dispatch state (ADR-029: unconfirmable, never success).
	// Distinct from exitCommandUnconfirmed (a deadline expired with no
	// evidence either way) and from exitOK (this is deliberately not
	// treated as success).
	exitActionUnconfirmable = 11

	// exitActionFailed: a "resolume action <verb>" subcommand dispatched,
	// and the attempt itself failed (a transport error to Resolume, or an
	// unexpected response). Distinct from exitAPIError: the coordinator
	// answered normally and reported that ITS OWN attempt failed.
	exitActionFailed = 12

	// exitActionRefused: a "resolume action <verb>" subcommand answered
	// "refused" — a 200 whose body says no request ever reached Resolume
	// (e.g. a clip's deck was not selected, or identity was not
	// confirmed). Distinct from exitConflict: not an idempotency-key
	// conflict, so a fresh key will not help; the remedy is in stderr.
	exitActionRefused = 13

	// exitFollowStillWatching: "macro run --follow" and "run show --follow"
	// stopped watching because the idle window elapsed with no run event
	// and no successful poll — never because a total duration was exceeded
	// (STEP-9-SPEC.md section 9: "a total timeout is forbidden: it
	// reintroduces exactly the client/server timeout inversion Step 7
	// shipped as a defect"). The request itself never failed, and the run
	// may still be in progress or may already have finished; this program
	// does not know and says so rather than guessing. A script can tell "I
	// stopped watching" (this code) apart from "the run is done" (exitOK,
	// exitCommandUnconfirmed, or exitMacroRunAborted below, all only ever
	// returned once a run has reached a terminal state).
	//
	// Numbered 14 rather than 11: this and exitMacroRunAborted were issued
	// as 11 and 12 on the step-9-wave-3 branch while the three action codes
	// above were issued with the same numbers on main. Renumbered here on
	// the merge, 2026-08-15. Nothing outside this repository had consumed
	// either set: origin/main topped out at exit 10.
	exitFollowStillWatching = 14

	// exitMacroRunAborted: a macro run reached its terminal state with
	// completed=false (STEP-9-SPEC.md section 2.3) — a step failed and the
	// remainder was not dispatched, or a step's target evaporated mid-run
	// (section 5.6). Distinct from exitCommandUnconfirmed (9), which this
	// program still uses for completed=true, confirmed=false (section
	// 2.3's OTHER combination: every step dispatched and none aborted, but
	// at least one produced no confirming evidence) — conflating the two
	// would lose exactly the distinction section 2.3 says the UI, and by
	// the same argument this CLI, must keep legible: "a run that completed
	// without confirmation must not render the same as" a run that aborted.
	exitMacroRunAborted = 15

	// exitAssetsNotReady: "assets manifest --require-ready" found at least
	// one node not_ready — a fresh, complete inventory report is MISSING a
	// named asset. "checked, and it is missing."
	exitAssetsNotReady = 20

	// exitAssetsUnknown: "assets manifest --require-ready" found at least
	// one node unknown, and none not_ready. "cannot tell" — no report has
	// ever arrived, the last one is stale, it said complete:false, or no
	// active show is configured. Deliberately distinct from
	// exitAssetsNotReady (20): a script that collapses "I checked and it is
	// missing" into "I cannot tell" (or the reverse) will either start a
	// show it should not, or block one it should not — the exact error
	// this project keeps finding in new places (see manifest.go's own
	// "complete: true is the licence to assert absence" rule, which is
	// what keeps these two states from ever being produced for the same
	// reason in the first place).
	exitAssetsUnknown = 21

	// exitRenderUnavailable: "render transport" found the surface's
	// transport unavailable — either a real probe confirmed it (Track B
	// seam B4, [pipeline.ProbeNDISend]) or no probe evidence exists yet.
	// Both are "cannot show the operator NDI is usable right now," which is
	// what an operator running this command actually wants to know; a
	// script that needs to distinguish "confirmed absent" from "never
	// probed" should pass --output json and read the observation state
	// directly rather than branching on this exit code alone.
	exitRenderUnavailable = 22
)

// cliError carries an exit code alongside a human-readable message, so
// every failure path in this program can decide its own exit code at the
// point the failure is understood, instead of main() re-deriving one from
// an opaque error value.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

func newCLIError(code int, format string, args ...any) *cliError {
	return &cliError{code: code, msg: fmt.Sprintf(format, args...)}
}

// exitCodeForProblem classifies a decoded RFC 9457 problem into one of this
// program's exit codes, falling back to the HTTP status when the type URI
// is not one this CLI recognizes (a coordinator is free to add problem
// types additively — contract §6.2 — and an unrecognized type must not
// crash the client, just report generically).
func exitCodeForProblem(status int, p *problem) int {
	if p != nil {
		switch p.Type {
		case problemUnsupportedAPIVersion:
			return exitVersionIncompatible
		case problemResourceNotFound:
			return exitNotFound
		case problemUnauthorized:
			return exitUnauthorized
		case problemInvalidParameter, problemCredentialInURL:
			return exitUsage
		case problemForbidden, problemCSRFRejected:
			return exitForbidden
		case problemTooManyRequests:
			return exitRateLimited
		case problemConflict, problemFPPStartPlaylistEvidenceNotCurrent, problemFPPStartPlaylistBusy,
			problemMacroRunAlreadyInFlight, problemMacroRunIdempotencyMacroConflict, problemMacroRunIdempotencyRevisionConflict:
			return exitConflict
		}
	}
	switch status {
	case 401:
		return exitUnauthorized
	case 403:
		return exitForbidden
	case 404:
		return exitNotFound
	case 400:
		return exitUsage
	case 429:
		return exitRateLimited
	case 409:
		return exitConflict
	default:
		return exitAPIError
	}
}
