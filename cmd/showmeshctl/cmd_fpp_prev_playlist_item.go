package main

import (
	"fmt"
	"io"
	"time"
)

// cmdFPPPrevPlaylistItem implements "showmeshctl fpp prev-playlist-item
// <instance-id>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"prevPlaylistItem"} — no parameters. See cmd_fpp_command.go's
// dispatchFPPCommand for the shared request/response core.
//
// Unlike next-playlist-item, capture section 3.5 did not measure "Prev
// Playlist Item" ending a playlist the way Next does at the last item, so
// this command's confirmation predicate names only fpp.playlist.index
// movement — no idle fallback. Movement is still not uniquely
// attributable to this command (FPP's own item boundaries advance the
// same counter), and the printed outcome reason says so on every
// confirmed outcome, not only on unconfirmed ones.
func cmdFPPPrevPlaylistItem(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp prev-playlist-item", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp prev-playlist-item [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Prev Playlist Item\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that fpp.playlist.index moved")
		_, _ = fmt.Fprintln(stderr, "from its pre-dispatch value. Unlike next-playlist-item, there is no")
		_, _ = fmt.Fprintln(stderr, "\"idle\" fallback here — capture section 3.5 did not measure this command")
		_, _ = fmt.Fprintln(stderr, "ending a playlist the way Next does at the last item. This movement is")
		_, _ = fmt.Fprintln(stderr, "not uniquely attributable to this command — the same counter also advances")
		_, _ = fmt.Fprintln(stderr, "on FPP's own item boundaries — and the printed outcome reason says so.")
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
		return reportError(stderr, "fpp prev-playlist-item", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp prev-playlist-item", instanceID, "prevPlaylistItem", nil)
}
