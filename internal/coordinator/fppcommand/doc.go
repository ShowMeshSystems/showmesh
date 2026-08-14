// Package fppcommand is the coordinator's ONLY code path that dispatches
// a command to FPP. It is deliberately a separate package from
// internal/coordinator/collector/fpp, which is read-only and must stay
// that way — see that package's own doc comment and CLAUDE.md's Step 5
// lesson, "GET-only is not read-only": FPP invokes commands over GET at
// /api/command/{name}/{arg}/..., which is exactly the request shape a
// read-only collector must never be tricked into issuing via a followed
// redirect.
//
// This package itself dispatches over POST {baseURL}/api/command with a
// JSON body, not that GET form — see [Client.Invoke]'s doc comment and
// docs/bench/fpp-command-vocabulary.md section 1.3 for why: FPP's own
// Apache configuration rejects a percent-encoded "/" in a GET path
// segment before fppd ever sees the request (AllowEncodedSlashes is
// off), so a GET-encoded argument containing "/" — a media filename
// under a subdirectory, a script argument — is categorically
// unreachable. POST's JSON body has no argument value it cannot express,
// and it is the shape fppd itself normalizes every command to
// internally, GET or POST alike, before republishing it to its own MQTT
// command/run topic (section 1.2) — FPP's own canonical representation,
// not a translation layer this package invented. Choosing POST does not
// relax the read-only boundary above: it is still exclusively this
// package, never the collector, that ever issues a request capable of
// changing FPP's state, and this package's own CheckRedirect guard (see
// [refuseRedirects]) is if anything more load-bearing on a POST than it
// was on the GET form it replaces — a followed redirect would still
// dispatch a second, FPP-invoked command on an unnamed host.
//
// This is the whole reason it exists as its own package rather than a
// method added to the collector: a single package that both polls status
// and dispatches commands would make "the collector never sends a
// command" a claim nobody could check by reading an import list, only by
// reading every line of a large file and hoping nothing changes
// underneath that reading. With two packages, "the collector's read-only
// guarantee is unaffected by this seam's existence" is checkable
// mechanically — see collector/fpp's own
// TestPackageNeverImportsFPPCommand — and stays checkable after
// this package grows a second command.
//
// This package holds no store access, no identity/authorization
// knowledge, and no confirmation logic: it issues one HTTP request
// against one configured FPP instance's base URL and reports what FPP's
// own response was. Confirming that the command actually took effect is
// internal/coordinator/api's job (ADR-003: a 200 from FPP is not
// success), against the collector's own observations — never against
// anything this package returns.
//
// Every argument value is validated in this package before dispatch,
// never left to FPP: docs/bench/fpp-command-vocabulary.md section 1.5
// measured FPP coercing rather than rejecting a bad argument (Volume
// Set/999 clamps to 100 silently; Volume Set/abc silently becomes 0), so
// there is no version of "let FPP reject it" that works here. See
// [ValidatePlaylistName] and [ValidateVolume].
package fppcommand
