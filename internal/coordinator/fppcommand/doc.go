// Package fppcommand is the coordinator's ONLY code path that dispatches
// a command to FPP. It is deliberately a separate package from
// internal/coordinator/collector/fpp, which is read-only and must stay
// that way — see that package's own doc comment and CLAUDE.md's Step 5
// lesson, "GET-only is not read-only": FPP invokes commands over GET at
// /api/command/..., which is exactly the request shape a read-only
// collector must never be tricked into issuing via a followed redirect.
//
// This package deliberately issues that GET. That is the whole reason it
// exists as its own package rather than a method added to the collector:
// a single package that both polls status and dispatches commands would
// make "the collector never sends a command" a claim nobody could check
// by reading an import list, only by reading every line of a large file
// and hoping nothing changes underneath that reading. With two packages,
// "the collector's read-only guarantee is unaffected by this seam's
// existence" is checkable mechanically — see collector/fpp's own
// TestCollectorPackageNeverImportsFPPCommand — and stays checkable after
// this package grows a second command.
//
// This package holds no store access, no identity/authorization
// knowledge, and no confirmation logic: it issues one HTTP request
// against one configured FPP instance's base URL and reports what FPP's
// own response was. Confirming that the command actually took effect is
// internal/coordinator/api's job (ADR-003: a 200 from FPP is not
// success), against the collector's own observations — never against
// anything this package returns.
package fppcommand
