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
)

// Exit codes. Documented in --help (see usage.go) so a script wrapping this
// tool can branch on $? instead of scraping stderr, per task spec §3
// ("Exit codes are meaningful").
const (
	exitOK                  = 0
	exitUsage               = 1
	exitUnreachable         = 2
	exitUnauthorized        = 3
	exitVersionIncompatible = 4
	exitNotFound            = 5
	exitAPIError            = 6
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
		case problemInvalidParameter:
			return exitUsage
		}
	}
	switch status {
	case 401:
		return exitUnauthorized
	case 404:
		return exitNotFound
	case 400:
		return exitUsage
	default:
		return exitAPIError
	}
}
