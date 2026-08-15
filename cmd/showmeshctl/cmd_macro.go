package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// against the show.macro configuration kind (GET only — writing a macro
// definition is Track E's authoring surface, ADR-030, not this step's) and
// the run submission route (STEP-9-SPEC.md section 6.6).
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
  list         enumerate macro objects (id, label, show, revision)
  show <id>    show one macro's full definition, including its steps
  run <id>     submit a run (write; accepted asynchronously, 202)

Writing a macro definition is not this program's job — see
"showmeshctl action --help": the show-authoring surface owns PUT for both
show.macro and show.action.

Run "showmeshctl macro <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdMacroList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl macro list", stderr)
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

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.macro", nil, &resp); err != nil {
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
