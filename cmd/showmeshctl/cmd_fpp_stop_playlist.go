package main

import (
	"fmt"
	"io"
	"time"
)

// This file is Step 7 seam C's proof that the write endpoint it built has
// an actual, working non-UI caller — the exact failure Step 6 shipped
// three times (BUILD-PLAN: "a capability that compiles, tests green, and
// has no caller"). It is also this contract's honest client: a `200` from
// the coordinator is never printed or exited as unqualified success
// (ADR-003) — see [reportFPPCommandResult] in cmd_fpp_command.go.
//
// The request/response plumbing this file used to own directly —
// minStopPlaylistClientTimeout, effectiveStopPlaylistTimeout,
// newIdempotencyKey, reportFPPCommandResult, exitCodeForCommandResult —
// moved to cmd_fpp_command.go under generalized names
// (minFPPCommandClientTimeout, effectiveFPPCommandTimeout, ...) when Step 8
// added seven more "fpp <verb>" subcommands sharing the identical timeout
// derivation and outcome-reporting rules; see that file's own doc comment
// for why one shared copy is correct rather than eight independently
// drifting ones. This subcommand's own wire shape, behavior, and exit
// codes are UNCHANGED by that move — cmd_fpp_stop_playlist_test.go is
// updated only to follow the rename, not to assert anything new.

// cmdFPPStopPlaylist implements "showmeshctl fpp stop-playlist
// <instance-id>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"stopPlaylist"}. It mints its own idempotency key per
// invocation (RES-015 section 7.3: FPP supplies nothing a coordinator
// could derive one from, so the CALLER mints it) — never accepts one as a
// flag, because reusing a key across two genuinely different invocations
// would make the second one a replay of the first, which is exactly the
// footgun an idempotency key exists to prevent a caller from stepping on
// accidentally.
func cmdFPPStopPlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp stop-playlist", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp stop-playlist [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Stop Now\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that playback actually")
		_, _ = fmt.Fprintln(stderr, "stopped. A 200 response is not itself success (ADR-003): this command")
		_, _ = fmt.Fprintln(stderr, "prints and exits on the response body's own \"confirmed\"/\"unconfirmed\"")
		_, _ = fmt.Fprintln(stderr, "outcome, never on the HTTP status alone. A fresh idempotency key is")
		_, _ = fmt.Fprintln(stderr, "minted for every invocation of this command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp stop-playlist", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp stop-playlist", instanceID, "stopPlaylist", nil)
}
