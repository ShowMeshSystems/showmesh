package main

import (
	"fmt"
	"io"
	"time"
)

// cmdFPPStopPlaylistGracefully implements "showmeshctl fpp
// stop-playlist-gracefully <instance-id>": POST
// /api/v1/fpp/{instanceId}/commands with
// {"action":"stopPlaylistGracefully","params":{"afterLoop":...}}. See
// cmd_fpp_command.go's dispatchFPPCommand for the shared request/response
// core.
//
// docs/bench/fpp-command-vocabulary.md section 3.3 measured a graceful
// stop's terminal state as bounded by the currently playing item's own
// remaining runtime, not by any deadline ShowMesh can choose — a
// 120-second item held "stopping gracefully" indefinitely. The
// coordinator's own confirmation predicate accepts EITHER a stopping
// state or idle as "confirmed" (section 4), and its outcomeReason states
// plainly, even on a confirmed result reached via a stopping state, that
// the show has NOT stopped yet. reportFPPCommandResult (cmd_fpp_command.go)
// always surfaces that reason on stdout for exactly this reason: a bare
// "confirmed" here must never be readable as "the show stopped."
func cmdFPPStopPlaylistGracefully(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp stop-playlist-gracefully", stderr)
	var afterLoop bool
	fs.BoolVar(&afterLoop, "after-loop", false,
		"wait for the current LOOP to finish (not merely the current item) before stopping")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp stop-playlist-gracefully [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Stop Gracefully\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope). Confirms")
		_, _ = fmt.Fprintln(stderr, "on FPP entering a stopping state, NEVER only on reaching idle: a graceful")
		_, _ = fmt.Fprintln(stderr, "stop's terminal state is bounded by the currently playing item's own")
		_, _ = fmt.Fprintln(stderr, "remaining runtime, which this coordinator cannot choose a deadline for")
		_, _ = fmt.Fprintln(stderr, "(docs/bench/fpp-command-vocabulary.md section 3.3). A \"confirmed\" result")
		_, _ = fmt.Fprintln(stderr, "here does NOT mean the show has stopped — it means FPP accepted the")
		_, _ = fmt.Fprintln(stderr, "graceful stop and is winding down; the printed outcome reason says so")
		_, _ = fmt.Fprintln(stderr, "explicitly every time.")
		_, _ = fmt.Fprintln(stderr, "\nA 200 response is not itself success (ADR-003): this command prints and")
		_, _ = fmt.Fprintln(stderr, "exits on the response body's own \"confirmed\"/\"unconfirmed\" outcome, never")
		_, _ = fmt.Fprintln(stderr, "on the HTTP status alone. A fresh idempotency key is minted for every")
		_, _ = fmt.Fprintln(stderr, "invocation of this command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp stop-playlist-gracefully", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	params := map[string]any{"afterLoop": afterLoop}
	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp stop-playlist-gracefully", instanceID, "stopPlaylistGracefully", params)
}
