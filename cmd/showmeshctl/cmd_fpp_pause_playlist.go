package main

import (
	"fmt"
	"io"
	"time"
)

// cmdFPPPausePlaylist implements "showmeshctl fpp pause-playlist
// <instance-id>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"pausePlaylist"} — no parameters. See cmd_fpp_command.go's
// dispatchFPPCommand for the shared request/response core.
//
// docs/bench/fpp-command-vocabulary.md section 2: FPP answers 200 "Playlist
// Paused" even while idle, with nothing actually paused. This command's
// confirmation predicate (fpp.status = "paused") reports that case as
// "unconfirmed" rather than trusting FPP's own encouraging response text.
func cmdFPPPausePlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp pause-playlist", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp pause-playlist [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Pause Playlist\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that playback actually")
		_, _ = fmt.Fprintln(stderr, "paused (fpp.status = \"paused\"). FPP answers 200 even while idle, with")
		_, _ = fmt.Fprintln(stderr, "nothing paused (docs/bench/fpp-command-vocabulary.md section 2) — this")
		_, _ = fmt.Fprintln(stderr, "command reports that case \"unconfirmed\", never \"confirmed\" on the HTTP")
		_, _ = fmt.Fprintln(stderr, "status alone (ADR-003). A fresh idempotency key is minted for every")
		_, _ = fmt.Fprintln(stderr, "invocation of this command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp pause-playlist", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp pause-playlist", instanceID, "pausePlaylist", nil)
}
