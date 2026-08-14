package main

import (
	"fmt"
	"io"
	"time"
)

// cmdFPPResumePlaylist implements "showmeshctl fpp resume-playlist
// <instance-id>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"resumePlaylist"} — no parameters. See cmd_fpp_command.go's
// dispatchFPPCommand for the shared request/response core.
//
// docs/bench/fpp-command-vocabulary.md section 3.4: FPP's own response text
// is "Playlist Restarted", which is FPP's wording and not evidence that
// anything restarted — the observed index does not move. This command's
// confirmation predicate (fpp.status = "playing") is what actually decides
// the outcome, never that response text.
func cmdFPPResumePlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp resume-playlist", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp resume-playlist [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Resume Playlist\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that playback actually")
		_, _ = fmt.Fprintln(stderr, "resumed (fpp.status = \"playing\"). FPP answers 200 even while idle, with")
		_, _ = fmt.Fprintln(stderr, "nothing resumed (docs/bench/fpp-command-vocabulary.md section 2) — this")
		_, _ = fmt.Fprintln(stderr, "command reports that case \"unconfirmed\", never \"confirmed\" on the HTTP")
		_, _ = fmt.Fprintln(stderr, "status alone (ADR-003). A fresh idempotency key is minted for every")
		_, _ = fmt.Fprintln(stderr, "invocation of this command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp resume-playlist", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp resume-playlist", instanceID, "resumePlaylist", nil)
}
