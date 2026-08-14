package main

import (
	"fmt"
	"io"
	"time"
)

// cmdFPPNextPlaylistItem implements "showmeshctl fpp next-playlist-item
// <instance-id>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"nextPlaylistItem"} — no parameters. See cmd_fpp_command.go's
// dispatchFPPCommand for the shared request/response core.
//
// docs/bench/fpp-command-vocabulary.md section 3.5: "Next Playlist Item"
// past the LAST item ends the playlist entirely — on a one-item playlist a
// single "next" stops the show. The coordinator's own confirmation
// predicate accepts EITHER the index moving OR fpp.status reaching "idle"
// as confirmation (section 4), and states in its outcome reason, either
// way, that the underlying counter also advances on FPP's OWN item
// boundaries — movement is not uniquely attributable to this command. This
// subcommand prints that reason on every confirmed outcome
// (reportFPPCommandResult, cmd_fpp_command.go), not only on unconfirmed
// ones, so that caveat is never silently dropped.
func cmdFPPNextPlaylistItem(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp next-playlist-item", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp next-playlist-item [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Next Playlist Item\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that fpp.playlist.index moved")
		_, _ = fmt.Fprintln(stderr, "from its pre-dispatch value, OR that fpp.status reached \"idle\" — \"Next\"")
		_, _ = fmt.Fprintln(stderr, "past the LAST item ends the playlist entirely, which this command accepts")
		_, _ = fmt.Fprintln(stderr, "as confirmation of its own largest possible effect")
		_, _ = fmt.Fprintln(stderr, "(docs/bench/fpp-command-vocabulary.md section 3.5). Neither signal is")
		_, _ = fmt.Fprintln(stderr, "uniquely attributable to this command — both also advance on FPP's own")
		_, _ = fmt.Fprintln(stderr, "item boundaries — and the printed outcome reason says so.")
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
		return reportError(stderr, "fpp next-playlist-item", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp next-playlist-item", instanceID, "nextPlaylistItem", nil)
}
