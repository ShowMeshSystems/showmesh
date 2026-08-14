package main

import (
	"fmt"
	"io"
	"time"
)

// fppIfBusyRefuse and fppIfBusyReplace are the two values "fpp
// start-playlist --if-busy" accepts on the wire, re-declared independently
// here rather than imported from internal/coordinator/api (this package's
// own fppIfBusyRefuse/fppIfBusyReplace constants) — the same independence
// this whole file's doc comment and importgraph_test.go's forbiddenImports
// enforce for every other wire vocabulary member. "refuse" is the default:
// docs/bench/fpp-command-vocabulary.md section 5's own decision, because
// FPP's own "Start Playlist" always silently replaces whatever is
// running, and this project decided that starting the wrong playlist over
// a running show is a worse failure mode than a refusal an operator has
// to retry with --if-busy=replace.
const (
	fppIfBusyRefuse  = "refuse"
	fppIfBusyReplace = "replace"
)

// cmdFPPStartPlaylist implements "showmeshctl fpp start-playlist
// <instance-id> <playlist-name>": POST /api/v1/fpp/{instanceId}/commands
// with {"action":"startPlaylist","params":{"playlist":...,"repeat":...,
// "ifBusy":...}}. See cmd_fpp_command.go's dispatchFPPCommand for the
// shared request/response core and cmd_fpp_stop_playlist.go's own doc
// comment for why that plumbing lives there rather than here.
//
// --if-busy defaults to "refuse" (capture section 5): the coordinator
// itself refuses with a 409 naming what is currently playing when a
// DIFFERENT playlist is confirmed running — this subcommand does not
// duplicate that decision locally, it only validates the flag is one of
// the two values the wire vocabulary defines, so a typo is a fast usage
// error rather than a round trip that comes back as an unrelated 400.
func cmdFPPStartPlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp start-playlist", stderr)
	var repeat bool
	var ifBusy string
	fs.BoolVar(&repeat, "repeat", false, "loop the playlist after it finishes")
	fs.StringVar(&ifBusy, "if-busy", fppIfBusyRefuse,
		"what to do if a DIFFERENT playlist is currently playing: \"refuse\" (default) or \"replace\"")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp start-playlist [flags] <instance-id> <playlist-name>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Start Playlist\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that this playlist is actually")
		_, _ = fmt.Fprintln(stderr, "playing (fpp.status = \"playing\" AND fpp.playlist.name = the requested")
		_, _ = fmt.Fprintln(stderr, "name — capture section 4: status alone would credit this command with a")
		_, _ = fmt.Fprintln(stderr, "start FPP's own scheduler performed on its own).")
		_, _ = fmt.Fprintln(stderr, "\nFPP's own \"Start Playlist\" silently replaces whatever is currently")
		_, _ = fmt.Fprintln(stderr, "playing (docs/bench/fpp-command-vocabulary.md section 3.2). This command")
		_, _ = fmt.Fprintln(stderr, "does not: by default (--if-busy=refuse) it is refused with a 409 naming")
		_, _ = fmt.Fprintln(stderr, "what is currently playing when a DIFFERENT playlist is confirmed running.")
		_, _ = fmt.Fprintln(stderr, "Pass --if-busy=replace to interrupt the running show deliberately. This")
		_, _ = fmt.Fprintln(stderr, "guard is evaluated against the coordinator's own evidence, which can be")
		_, _ = fmt.Fprintln(stderr, "stale, and does not prevent a race against FPP's own scheduler.")
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
		return reportError(stderr, "fpp start-playlist", err)
	}
	if ifBusy != fppIfBusyRefuse && ifBusy != fppIfBusyReplace {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp start-playlist: invalid --if-busy value %q: must be %q or %q\n",
			ifBusy, fppIfBusyRefuse, fppIfBusyReplace)
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, playlist := rest[0], rest[1]
	if playlist == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp start-playlist: playlist name must not be empty")
		return exitUsage
	}

	params := map[string]any{
		"playlist": playlist,
		"repeat":   repeat,
		"ifBusy":   ifBusy,
	}
	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp start-playlist", instanceID, "startPlaylist", params)
}
