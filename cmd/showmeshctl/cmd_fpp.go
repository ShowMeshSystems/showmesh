package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// fppWriteSubcommands routes "showmeshctl fpp <verb> ..." to its own flag
// set and handler before this function's own flag set ever parses args —
// the same way "showmeshctl session" and "showmeshctl audit" are
// top-level subcommands with their own flags rather than flags of some
// other command. Every verb here lives under "fpp" — not as its own
// top-level verb — because each is FPP-specific in the same way "fpp
// [id]" already is, and because BUILD-PLAN's own framing named the first
// of them that way: "showmeshctl fpp stop-playlist <instanceId>".
// stop-playlist (Step 7 seam C) is the only one of the eight
// docs/bench/fpp-command-vocabulary.md section 4 primitives that predates
// this map; the other seven are Step 8's own addition, dispatched
// identically.
var fppWriteSubcommands = map[string]func(args []string, stdout, stderr io.Writer, clock func() time.Time) int{
	"stop-playlist":            cmdFPPStopPlaylist,
	"start-playlist":           cmdFPPStartPlaylist,
	"stop-playlist-gracefully": cmdFPPStopPlaylistGracefully,
	"pause-playlist":           cmdFPPPausePlaylist,
	"resume-playlist":          cmdFPPResumePlaylist,
	"next-playlist-item":       cmdFPPNextPlaylistItem,
	"prev-playlist-item":       cmdFPPPrevPlaylistItem,
	"set-volume":               cmdFPPSetVolume,
	// reset-observation-sequence (TRACK-H-H2-SPEC.md §5.1) is not one of
	// docs/bench/fpp-command-vocabulary.md section 4's eight primitives —
	// it never dispatches an FPP command at all, it clears coordinator-
	// side evidence — but it shares this map's dispatch shape (a single
	// write subcommand keyed by verb name) and its own fpp:command scope.
	"reset-observation-sequence": cmdFPPResetObservationSequence,
}

func cmdFPP(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) > 0 {
		if handler, ok := fppWriteSubcommands[args[0]]; ok {
			return handler(args[1:], stdout, stderr, clock)
		}
		// "playlist-definitions" is read-only (FPP-PLUGIN-COORDINATOR-CONTRACTS.md
		// §3.6, TRACK-H-H2-SPEC.md §4 step 2): its own three
		// sub-subcommands (list/get/entries) live in
		// cmd_fpp_playlist_definition.go, dispatched here rather than
		// through fppWriteSubcommands because that map's own doc comment
		// scopes it to the eight write primitives.
		if args[0] == "playlist-definitions" {
			return cmdFPPPlaylistDefinitions(args[1:], stdout, stderr, clock)
		}
		// "playlist-entry-observations" is read-only (FPP-PLUGIN-COORDINATOR-CONTRACTS.md
		// §1.1, TRACK-H-H2-SPEC.md §5): its own two sub-subcommands
		// (list/reconciliation) live in
		// cmd_fpp_playlist_entry_observations.go, dispatched here for the
		// identical reason "playlist-definitions" is one line up.
		if args[0] == "playlist-entry-observations" {
			return cmdFPPPlaylistEntryObservations(args[1:], stdout, stderr, clock)
		}
		// "playlist-readiness" is read-only (TRACK-H-H2-SPEC.md §6, §7):
		// its own single command lives in cmd_fpp_playlist_readiness.go,
		// dispatched here for the identical reason the two entries above
		// are.
		if args[0] == "playlist-readiness" {
			return cmdFPPPlaylistReadiness(args[1:], stdout, stderr, clock)
		}
	}

	fs, g := newFlagSet("showmeshctl fpp", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp [flags] [instance-id]")
		_, _ = fmt.Fprintln(stderr, "       showmeshctl fpp <verb> [flags] <instance-id> [args...]")
		_, _ = fmt.Fprintln(stderr, "\nList configured FPP instances (GET /api/v1/fpp), or show one instance")
		_, _ = fmt.Fprintln(stderr, "in detail if instance-id is given (GET /api/v1/fpp/{instanceId}).")
		_, _ = fmt.Fprintln(stderr, "\n<verb> dispatches one of docs/bench/fpp-command-vocabulary.md section 4's")
		_, _ = fmt.Fprintln(stderr, "eight primitive FPP commands and confirms it by evidence (ADR-003):")
		_, _ = fmt.Fprintln(stderr, "  stop-playlist              <instance-id>")
		_, _ = fmt.Fprintln(stderr, "  start-playlist             <instance-id> <playlist-name> [--repeat] [--if-busy refuse|replace]")
		_, _ = fmt.Fprintln(stderr, "  stop-playlist-gracefully   <instance-id> [--after-loop]")
		_, _ = fmt.Fprintln(stderr, "  pause-playlist             <instance-id>")
		_, _ = fmt.Fprintln(stderr, "  resume-playlist            <instance-id>")
		_, _ = fmt.Fprintln(stderr, "  next-playlist-item         <instance-id>")
		_, _ = fmt.Fprintln(stderr, "  prev-playlist-item         <instance-id>")
		_, _ = fmt.Fprintln(stderr, "  set-volume                 <instance-id> <volume 0-100>")
		_, _ = fmt.Fprintln(stderr, "  reset-observation-sequence --confirm <instance-id>  (TRACK-H-H2-SPEC.md §5.1)")
		_, _ = fmt.Fprintln(stderr, "\n<verb> playlist-definitions dispatches FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3's")
		_, _ = fmt.Fprintln(stderr, "read-only playlist definition surface:")
		_, _ = fmt.Fprintln(stderr, "  playlist-definitions list")
		_, _ = fmt.Fprintln(stderr, "  playlist-definitions get     <instance-id> <playlist-hash>")
		_, _ = fmt.Fprintln(stderr, "  playlist-definitions entries <instance-id> <playlist-hash>")
		_, _ = fmt.Fprintln(stderr, "\n<verb> playlist-entry-observations dispatches TRACK-H-H2-SPEC.md §5's")
		_, _ = fmt.Fprintln(stderr, "read-only observation and reconciliation surface:")
		_, _ = fmt.Fprintln(stderr, "  playlist-entry-observations list")
		_, _ = fmt.Fprintln(stderr, "  playlist-entry-observations reconciliation <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\n<verb> playlist-readiness dispatches TRACK-H-H2-SPEC.md §6's")
		_, _ = fmt.Fprintln(stderr, "read-only readiness surface:")
		_, _ = fmt.Fprintln(stderr, "  playlist-readiness <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nEach <verb> above RAISES the global --timeout to its own, larger minimum")
		_, _ = fmt.Fprintln(stderr, "(currently 35s) when given a smaller value, and says so on stderr: the")
		_, _ = fmt.Fprintln(stderr, "coordinator holds a dispatched command's response open for its own")
		_, _ = fmt.Fprintln(stderr, "confirmation deadline before answering, so a shorter budget could only")
		_, _ = fmt.Fprintln(stderr, "ever abort a healthy conversation early and misreport it as failed.")
		_, _ = fmt.Fprintln(stderr, "\nRun \"showmeshctl fpp <verb> --help\" for a write subcommand's own flags.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp", err)
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	if len(rest) == 1 {
		return cmdFPPOne(ctx, c, rest[0], g, stdout, stderr, clock)
	}
	return cmdFPPList(ctx, c, g, stdout, stderr, clock)
}

func cmdFPPList(ctx context.Context, c *client, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) int {
	var resp fppResponse
	if err := c.getJSON(ctx, "/api/v1/fpp", nil, &resp); err != nil {
		return reportError(stderr, "fpp", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp", err)
		}
		return exitOK
	}
	printFPPTable(stdout, resp)
	return exitOK
}

func cmdFPPOne(ctx context.Context, c *client, instanceID string, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) int {
	body, err := c.getRaw(ctx, "/api/v1/fpp/"+url.PathEscape(instanceID), nil)
	if err != nil {
		return reportError(stderr, "fpp", err)
	}

	inst, serverTime, err := decodeSingleFPPInstance(body)
	if err != nil {
		return reportError(stderr, "fpp", err)
	}
	printClockSkew(stderr, serverTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, inst); err != nil {
			return reportError(stderr, "fpp", err)
		}
		return exitOK
	}
	printFPPTable(stdout, fppResponse{ServerTime: serverTime, Instances: []fppInstance{inst}})
	return exitOK
}

// decodeSingleFPPInstance decodes the body of GET /api/v1/fpp/{instanceId}
// against the contract §6.10-pinned wrapped shape ({"serverTime":…,
// "instance": {…}}) — and ONLY that shape. See [decodeSingleNode]'s doc
// comment: the same contract-violation tolerance was removed here for the
// same reason, once the API side was fixed to always wrap it.
func decodeSingleFPPInstance(body []byte) (inst fppInstance, serverTime time.Time, err error) {
	var wrapped fppInstanceResponse
	if jsonErr := json.Unmarshal(body, &wrapped); jsonErr != nil {
		return fppInstance{}, time.Time{}, newCLIError(exitAPIError,
			"decoding fpp instance response as {\"serverTime\":..., \"instance\":...}: %v", jsonErr)
	}
	if wrapped.ServerTime.IsZero() {
		return fppInstance{}, time.Time{}, newCLIError(exitAPIError,
			"fpp instance response is missing serverTime, violating contract section 6.2 (every response body must carry it)")
	}
	if wrapped.Instance.InstanceID == "" {
		return fppInstance{}, time.Time{}, newCLIError(exitAPIError,
			"fpp instance response's \"instance\" object is missing instanceId")
	}
	return wrapped.Instance, wrapped.ServerTime, nil
}
