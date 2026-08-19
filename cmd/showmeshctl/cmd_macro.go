package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// This file is Step 9 wave 3's own showmeshctl surface (STEP-9-SPEC.md
// section 9): "macro list", "macro show <id>", and "macro run <id>",
// against the show.macro configuration kind and the run submission route
// (STEP-9-SPEC.md section 6.6). "macro put" (Track G seam G-6) closes the
// Class 2 gap this file's own help text used to describe: PUT
// /config/show.macro/{id} shipped in Step 9 and the Operator UI has called
// it since Track E merged, but this program refused it until now.
//
// "macro run" is deliberately asynchronous by default (ADR-031 decision 1,
// section 2.1): it prints the accepted run's id and initial state and
// returns immediately, exactly the way "showmeshctl discover" does not
// wait for anything either. --follow opts into watching, on the terms
// macro_client.go's followMacroRun documents — an IDLE timeout, never a
// total one.

func cmdMacro(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printMacroUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printMacroUsage(stdout)
		return exitOK
	case "list":
		return cmdMacroList(rest, stdout, stderr, clock)
	case "show":
		return cmdMacroShow(rest, stdout, stderr, clock)
	case "run":
		return cmdMacroRun(rest, stdout, stderr, clock)
	case "put":
		return cmdMacroPut(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl macro: unknown subcommand %q\n\n", sub)
		printMacroUsage(stderr)
		return exitUsage
	}
}

func printMacroUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl macro <subcommand> [flags]

Read the coordinator's show.macro configuration objects, or submit a run.
Reads require show:macro:run OR config:write; running requires
show:macro:run specifically (an admin who has never been granted
show:macro:run may not fire a show through config:write alone).

Subcommands:
  list [--show <id>]
               enumerate macro objects (id, label, show, revision),
               optionally narrowed to one show
  show <id>    show one macro's full definition, including its steps
  run <id>     submit a run (write; accepted asynchronously, 202)
  put <id>     write a new show.macro configuration revision (reads a
               payload from --file, or from stdin if --file is not given)

"macro put" accepts a full show.macro JSON payload (show, label,
description, steps). Validated before activation: an invalid payload, an
unknown action reference, or a fallback-class mismatch is rejected and
appends no revision (ADR-009).

Run "showmeshctl macro <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdMacroList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl macro list", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the list to macros belonging to this show id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl macro list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show.macro objects (GET /api/v1/config/show.macro).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "macro list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "macro list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveMacroClientTimeout(g.timeout))
	defer cancel()

	var query url.Values
	if show != "" {
		query = url.Values{"show": {show}}
	}

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.macro", query, &resp); err != nil {
		return reportError(stderr, "macro list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "macro list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdMacroShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl macro show", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl macro show [flags] <macro-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one macro's full definition (GET /api/v1/config/show.macro/{id}),")
		_, _ = fmt.Fprintln(stderr, "including every step's action reference, onFailure/onUnconfirmed policy")
		_, _ = fmt.Fprintln(stderr, "(always the RESOLVED value, never blank for \"default applies\"), and")
		_, _ = fmt.Fprintln(stderr, "localFallback class.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "macro show", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "macro show", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveMacroClientTimeout(g.timeout))
	defer cancel()

	var resp showMacroConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.macro/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "macro show", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "macro show", err)
		}
		return exitOK
	}
	printShowMacroDetail(stdout, resp)
	return exitOK
}

// cmdMacroPut implements "showmeshctl macro put"
// (PUT /api/v1/config/show.macro/{id}), following "action put"
// (cmdActionPut, cmd_action.go) exactly: the payload is read from --file,
// or from stdin when --file is not given (readConfigPayload,
// cmd_config.go), and sent to the coordinator verbatim as
// json.RawMessage — this command does not decode, re-encode, or
// second-guess the operator's own payload. Validation (required keys,
// unknown action references, the Resolume local-fallback rule) is
// server-side (config.DecodeShowMacroPayload, showconfig.go); the one
// thing checked here is that the payload is syntactically valid JSON at
// all, so a malformed file fails fast with a clear message rather than an
// opaque 400 from the server. An absent key, an explicit null, and an
// explicit empty value are three different things on this write surface
// (CLAUDE.md's own recurring lesson) and this command does not collapse
// them — the server's decoder is where that distinction is enforced.
func cmdMacroPut(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl macro put", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON show.macro payload; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl macro put [flags] <macro-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.macro configuration revision")
		_, _ = fmt.Fprintln(stderr, "(PUT /api/v1/config/show.macro/{id}, requires config:write, admin only).")
		_, _ = fmt.Fprintln(stderr, "The payload is a full show.macro object: show, label, description, and")
		_, _ = fmt.Fprintln(stderr, "steps. Validated before activation: an invalid payload, an unknown action")
		_, _ = fmt.Fprintln(stderr, "reference, or a fallback-class mismatch is rejected and appends no")
		_, _ = fmt.Fprintln(stderr, "revision (ADR-009).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "macro put", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "macro put", newCLIError(exitUsage, "%v", err))
	}
	if !json.Valid(raw) {
		return reportError(stderr, "macro put", newCLIError(exitUsage, "payload must be valid JSON"))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "macro put", err)
	}
	// A local SQLite write with no confirmation wait, exactly like
	// "action put" — g.timeout unmodified, not effectiveMacroClientTimeout's
	// floor, which exists for the run/follow surface's own different
	// contract (no response held open on this route either).
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showMacroConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/show.macro/"+url.PathEscape(id), json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, "macro put", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "macro put", err)
		}
		return exitOK
	}
	printShowMacroDetail(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl macro put: revision %d is now active.\n", resp.Revision)
	return exitOK
}

// newMacroRunIdempotencyKey mints a fresh, random idempotency key exactly
// as newIdempotencyKey (cmd_fpp_command.go) does — see that function's own
// doc comment and importgraph_test.go for why this program mints its own
// value independently rather than importing pkg/command.NewIdempotencyKey.
// Declared separately (rather than reusing newIdempotencyKey directly)
// only so this surface's own doc comments can be self-contained; the
// implementation is intentionally identical.
func newMacroRunIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", newCLIError(exitAPIError, "generating an idempotency key: %v", err)
	}
	return hex.EncodeToString(buf), nil
}

func cmdMacroRun(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl macro run", stderr)
	var follow bool
	var pollInterval, idleTimeout time.Duration
	fs.BoolVar(&follow, "follow", false, "watch the run until it finishes (idle timeout, never a total one — see below)")
	fs.DurationVar(&pollInterval, "poll-interval", defaultMacroFollowPollInterval, "with --follow: how often to poll the run's state")
	fs.DurationVar(&idleTimeout, "idle-timeout", defaultMacroFollowIdleTimeout, "with --follow: stop watching after this long with no update (NOT a total run-duration limit)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl macro run [flags] <macro-id>")
		_, _ = fmt.Fprintln(stderr, "\nSubmit a macro run (POST /api/v1/macros/{id}/runs, requires show:macro:run).")
		_, _ = fmt.Fprintln(stderr, "This is asynchronous by design: a 202 means the run was ACCEPTED, never")
		_, _ = fmt.Fprintln(stderr, "that it finished, or even that its first step has dispatched yet. Without")
		_, _ = fmt.Fprintln(stderr, "--follow, this command prints the run id and its initial state and exits")
		_, _ = fmt.Fprintln(stderr, "immediately (exit 0, unless submission itself was refused) — check the")
		_, _ = fmt.Fprintln(stderr, "outcome later with \"showmeshctl run show <runId>\".")
		_, _ = fmt.Fprintln(stderr, "\n--follow watches the run and reports its outcome, but on an IDLE timeout,")
		_, _ = fmt.Fprintln(stderr, "never a total one: it keeps watching for as long as the run keeps")
		_, _ = fmt.Fprintln(stderr, "answering, however many minutes that takes, and only stops watching after")
		_, _ = fmt.Fprintln(stderr, "--idle-timeout passes with no update at all. On an idle timeout this exits")
		_, _ = fmt.Fprintln(stderr, "cleanly (exit 14) stating the run may still be in progress — never as a")
		_, _ = fmt.Fprintln(stderr, "reported failure, because it is not one.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "macro run", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	macroID := rest[0]
	if pollInterval <= 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl macro run: --poll-interval must be positive")
		return exitUsage
	}
	if idleTimeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl macro run: --idle-timeout must be positive")
		return exitUsage
	}

	timeout := effectiveMacroClientTimeout(g.timeout)
	if !follow {
		// When --follow is set, followMacroRun computes and notes this
		// SAME floor itself for its own polling requests — noting it here
		// too would print the identical stderr line twice for one
		// invocation.
		noteMacroTimeoutFloorIfRaised(stderr, "macro run", g.timeout, timeout)
	}

	c, err := newClientWithTimeout(g, timeout)
	if err != nil {
		return reportError(stderr, "macro run", err)
	}

	key, err := newMacroRunIdempotencyKey()
	if err != nil {
		return reportError(stderr, "macro run", err)
	}

	submitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	var resp macroRunSubmitResponse
	submitErr := c.postJSON(submitCtx, "/api/v1/macros/"+url.PathEscape(macroID)+"/runs",
		createMacroRunRequest{IdempotencyKey: key, Trigger: "cli"}, &resp)
	cancel()
	if submitErr != nil {
		return reportError(stderr, "macro run", submitErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if resp.Replay {
		_, _ = fmt.Fprintf(stderr, "showmeshctl macro run: this idempotency key was already used; "+
			"returning the ORIGINAL run's state (id %s), nothing new was submitted\n", resp.Run.ID)
	}

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "macro run", err)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "accepted: run %s of macro %s (state=%s)\n", resp.Run.ID, macroID, resp.Run.State)
	}

	if !follow {
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	followClient, err := newClientWithTimeout(g, timeout)
	if err != nil {
		return reportError(stderr, "macro run", err)
	}
	return followMacroRun(ctx, followClient, g, "macro run", resp.Run.ID, pollInterval, idleTimeout, stdout, stderr, clock)
}

// newClientWithTimeout is [newRequestClient] with an explicit timeout
// rather than g.timeout verbatim, so callers on this surface can apply
// [effectiveMacroClientTimeout]'s floor first.
func newClientWithTimeout(g *globalFlags, timeout time.Duration) (*client, error) {
	gg := *g
	gg.timeout = timeout
	return newRequestClient(&gg)
}
