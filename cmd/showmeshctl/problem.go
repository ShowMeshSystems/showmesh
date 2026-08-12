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
	default:
		return exitAPIError
	}
}
