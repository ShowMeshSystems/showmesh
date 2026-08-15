package main

import "fmt"

// problem is an RFC 9457 application/problem+json document, per contract
// §6.6. showmeshctl reads title and detail from this, not from the status
// code alone: the status code alone cannot distinguish "unsupported API
// version" from "invalid parameter" from "resource not found", and the
// problem document exists specifically so a client does not have to guess.
type problem struct {
	Type              string `json:"type"`
	Title             string `json:"title"`
	Status            int    `json:"status"`
	Detail            string `json:"detail"`
	SupportedVersions []int  `json:"supportedVersions,omitempty"`
}

// Problem type URIs named in contract §6.6. Matched by exact string, not by
// path suffix: the contract pins these as the stable identifier of the
// error class, and matching on anything looser would let a coordinator's
// unrelated 404 (a typo'd path, a proxy's own error page) get misread as
// "resource not found" when it isn't RFC 9457 at all.
const (
	problemUnsupportedAPIVersion = "https://showmesh.dev/problems/unsupported-api-version"
	problemResourceNotFound      = "https://showmesh.dev/problems/resource-not-found"
	problemInvalidParameter      = "https://showmesh.dev/problems/invalid-parameter"
	problemUnauthorized          = "https://showmesh.dev/problems/unauthorized"

	// The four ADR-024 additions (internal/coordinator/api/problem.go's
	// exact same four constants; matched by exact string, not by path
	// suffix, for the same "do not misread an unrelated 404" reason the
	// original four are).

	// problemForbidden is decision 4's 403: authenticated, but missing a
	// scope. Distinct from problemUnauthorized (401, no valid credential
	// at all) so this CLI can send an operator to "ask for a scope" rather
	// than "check your token" — see exitForbidden and decodeProblemError.
	problemForbidden = "https://showmesh.dev/problems/forbidden"
	// problemCSRFRejected is decision 6's same-origin rule for a
	// cookie-authenticated write. showmeshctl never presents a cookie
	// (see cmd_session.go's doc comment), so this CLI cannot itself
	// trigger it, but it is still classified explicitly rather than
	// falling through to a generic 403 handler, in case a future command
	// ever does add cookie support.
	problemCSRFRejected = "https://showmesh.dev/problems/csrf-rejected"
	// problemTooManyRequests is decision 8's login concurrency-bound
	// rejection, carrying a Retry-After response header this client
	// surfaces explicitly (decodeProblemError) rather than leaving an
	// operator to guess how long to wait.
	problemTooManyRequests = "https://showmesh.dev/problems/too-many-requests"
	// problemCredentialInURL is decision 1's URL rule: a request whose
	// query string carried the token prefix. showmeshctl never puts a
	// token in a query string (see client.go's applyHeaders, the only
	// place this program ever attaches a credential), so this should be
	// unreachable in practice; classified anyway so a defect that ever
	// did put one there is reported as a usage bug in this CLI rather than
	// an opaque server error.
	problemCredentialInURL = "https://showmesh.dev/problems/credential-in-url"

	// problemConflict is Step 8's own addition: internal/coordinator/api's
	// ProblemTypeConflict, a 409 meaning "the request itself is valid, but
	// this coordinator's current state makes it unsafe or meaningless to
	// act on right now" — every "fpp start-playlist" ifBusy=refuse guard
	// where a DIFFERENT playlist is confirmed playing, and a replayed
	// idempotency key reused against a different action/target/params,
	// share this one type (internal/coordinator/api/problem.go's own
	// comment on fppEndpointsEnvVarSetProblem: "all three [now several]
	// are the identical RFC 9457 shape... detail is what tells them
	// apart"). Step 8 review defect: this had no case below and fell to
	// exitAPIError (6, "the coordinator returned some other error"),
	// which a script cannot distinguish from an actual coordinator
	// malfunction — see exitConflict.
	problemConflict = "https://showmesh.dev/problems/conflict"

	// problemFPPStartPlaylistEvidenceNotCurrent is a task-2b addition to
	// internal/coordinator/api/problem.go: "fpp start-playlist" ifBusy
	// =refuse's OTHER 409 — the coordinator could not tell what is
	// playing, rather than confirming a DIFFERENT playlist is playing —
	// used to share [problemConflict]'s own type, distinguishable only by
	// an Operator UI matching a substring of `detail` (this program never
	// did that; it always just prints `detail` verbatim). Mapped to the
	// SAME [exitConflict] as [problemConflict]: both name a deliberate
	// refusal an operator can retry, and main.go's own --help text for
	// exit 10 already covers "the evidence needed to decide that is not
	// current" as one of conflict's causes, so this needs no new exit
	// code, only an explicit case below rather than falling through to
	// the (also-correct, but implicit) 409 status fallback.
	problemFPPStartPlaylistEvidenceNotCurrent = "https://showmesh.dev/problems/fpp-start-playlist-evidence-not-current"

	// problemFPPStartPlaylistBusy is Step 8 review finding 8's "finish the
	// split" fix: fppStartPlaylistBusyProblem ("fpp start-playlist"
	// ifBusy=refuse's guard refusing because a DIFFERENT playlist IS
	// confirmed playing) used to share [problemConflict]'s own type too —
	// the split that gave [problemFPPStartPlaylistEvidenceNotCurrent] its
	// own type left this sibling case behind, still indistinguishable from
	// an idempotency-key conflict except by `detail` prose (this program
	// never parses that; an Operator UI reviewing findings did). Mapped to
	// the SAME [exitConflict] for the identical reason
	// problemFPPStartPlaylistEvidenceNotCurrent is: this CLI prints
	// `detail` verbatim rather than branching UI-style on the remedy, so
	// one exit code covers every "deliberate, retryable refusal" 409 —
	// this constant and case exist so the mapping is explicit rather than
	// silently relying on the (also-correct) 409 status fallback, matching
	// this file's own established pattern for every 409 type above.
	problemFPPStartPlaylistBusy = "https://showmesh.dev/problems/fpp-start-playlist-busy"
)

// Exit codes. Documented in --help (see usage.go) so a script wrapping this
// tool can branch on $? instead of scraping stderr, per task spec §3
// ("Exit codes are meaningful").
//
// exitForbidden and exitRateLimited are ADR-024 additions, deliberately
// distinct from exitUnauthorized: conflating "no valid credential" (401),
// "authenticated but missing a scope" (403), and "rate limited, retry
// later" (429) into one exit code would send an operator's retry script or
// runbook to the wrong branch — the task's own framing for why this
// distinction matters ("conflating them sends an operator to the wrong
// place").
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

	// exitCommandUnconfirmed is Step 7 seam C's own addition: "fpp
	// stop-playlist" completed a full, successful HTTP round trip (the
	// coordinator answered 200 with a real command result), but that
	// result's own outcome was "unconfirmed" — ADR-003, ADR-020: a `200`
	// is never conflated with the command actually having taken effect.
	// Distinct from exitAPIError (6, the coordinator itself failed or
	// returned something this program could not even parse): a script
	// checking $? needs to tell "the request failed" apart from "the
	// request succeeded and told you, honestly, that the command's effect
	// was not confirmed."
	exitCommandUnconfirmed = 9

	// exitConflict is Step 8's own addition: a 409 carrying
	// [problemConflict] — a deliberate refusal because this coordinator's
	// current state makes the request unsafe or meaningless right now (a
	// different playlist is playing and ifBusy=refuse, the evidence
	// needed to evaluate that guard is not current, or an idempotency key
	// was reused against a different action/target/params). Before this
	// exit code existed, exitCodeForProblem had no case for it and no 409
	// in its status fallback, so it fell to exitAPIError (6) — "the
	// coordinator returned some other error" — which a script cannot
	// distinguish from an actual coordinator malfunction. This is a
	// DIFFERENT fact: the coordinator is healthy and answered correctly,
	// and the operator's own remedy is in stderr (e.g. "retry with
	// --if-busy=replace").
	exitConflict = 10

	// exitActionUnconfirmable is Track D seam D-3/B's own addition: a
	// "resolume action <verb>" write subcommand completed a full,
	// successful HTTP round trip and the coordinator answered "unconfirmable"
	// — the action was dispatched, but its own effect could not be told
	// apart from its pre-dispatch state (e.g. launching a clip that was
	// already playing), so no post-dispatch evidence can confirm or refute
	// it (ADR-029: an action whose effect cannot be observed reports as
	// unconfirmable, never as success). Distinct from
	// [exitCommandUnconfirmed] (a deadline expired with no evidence either
	// way) and from [exitOK] (this is deliberately NOT treated as success,
	// even though nothing went wrong): a script checking $? needs to tell
	// "this coordinator could not tell you" apart from both "it told you no"
	// and "it told you yes."
	exitActionUnconfirmable = 11

	// exitActionFailed is Track D seam D-3/B's own addition: a
	// "resolume action <verb>" write subcommand completed a full,
	// successful HTTP round trip and the coordinator answered "failed" —
	// dispatch was attempted and the attempt itself failed (a transport
	// error talking to Resolume, or an unexpected response from it).
	// Deliberately distinct from [exitAPIError] (6): that code means THIS
	// PROGRAM's own request to the coordinator produced an error response;
	// this one means the coordinator answered normally (200) and reported,
	// honestly, that ITS OWN attempt to reach Resolume did not work.
	exitActionFailed = 12
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
		case problemConflict, problemFPPStartPlaylistEvidenceNotCurrent, problemFPPStartPlaylistBusy:
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
