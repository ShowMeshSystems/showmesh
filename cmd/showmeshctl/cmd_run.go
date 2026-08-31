package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// This file is "showmeshctl run": read back a macro run's own state
// (STEP-9-SPEC.md section 6.6), separate from "macro run" (cmd_macro.go),
// which submits one. "run" is deliberately its own top-level command
// rather than nested under "macro run <id> --status" or similar: a run
// outlives the macro submission that created it (an operator, a script, or
// the FPP plugin may all want to check on a run they did not themselves
// submit, hours later, by run id alone), matching how this program already
// treats "node" as its own top-level command distinct from whatever
// discovered it.

func cmdRun(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printRunUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printRunUsage(stdout)
		return exitOK
	case "show":
		return cmdRunShow(rest, stdout, stderr, clock)
	case "list":
		return cmdRunList(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl run: unknown subcommand %q\n\n", sub)
		printRunUsage(stderr)
		return exitUsage
	}
}

func printRunUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl run <subcommand> [flags]

Read back a macro run's own state (a run is created by "showmeshctl macro
run <macro-id>", or by the Operator UI, a schedule, or the FPP plugin).
Reads require show:macro:run OR config:write.

Subcommands:
  show <runId>   show one run, including every step's outcome
  list           list runs, most recent first (filterable by macro id/state/show)

Run "showmeshctl run <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdRunShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl run show", stderr)
	var follow bool
	var pollInterval, idleTimeout time.Duration
	fs.BoolVar(&follow, "follow", false, "watch the run until it finishes (idle timeout, never a total one)")
	fs.DurationVar(&pollInterval, "poll-interval", defaultMacroFollowPollInterval, "with --follow: how often to poll the run's state")
	fs.DurationVar(&idleTimeout, "idle-timeout", defaultMacroFollowIdleTimeout, "with --follow: stop watching after this long with no update (NOT a total run-duration limit)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl run show [flags] <runId>")
		_, _ = fmt.Fprintln(stderr, "\nShow one macro run (GET /api/v1/macro-runs/{runId}), including every")
		_, _ = fmt.Fprintln(stderr, "step's own outcome. A run's two facts, \"completed\" and \"confirmed\", are")
		_, _ = fmt.Fprintln(stderr, "reported separately and never collapsed into one")
		_, _ = fmt.Fprintln(stderr, "word: a run that completed without confirmation renders differently from")
		_, _ = fmt.Fprintln(stderr, "one that aborted, which renders differently from one that confirmed.")
		_, _ = fmt.Fprintln(stderr, "\n--follow watches on an IDLE timeout, never a total one — see")
		_, _ = fmt.Fprintln(stderr, "\"showmeshctl macro run --help\" for the identical behavior and its")
		_, _ = fmt.Fprintln(stderr, "reasoning; both subcommands share one implementation.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "run show", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	runID := rest[0]
	if pollInterval <= 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl run show: --poll-interval must be positive")
		return exitUsage
	}
	if idleTimeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl run show: --idle-timeout must be positive")
		return exitUsage
	}

	if follow {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		c, err := newRequestClient(g)
		if err != nil {
			return reportError(stderr, "run show", err)
		}
		return followMacroRun(ctx, c, g, "run show", runID, pollInterval, idleTimeout, stdout, stderr, clock)
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "run show", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveMacroClientTimeout(g.timeout))
	defer cancel()

	var resp macroRunResponse
	if err := c.getJSON(ctx, "/api/v1/macro-runs/"+url.PathEscape(runID), nil, &resp); err != nil {
		return reportError(stderr, "run show", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "run show", err)
		}
		return exitOK
	}
	printMacroRunDetail(stdout, resp.Run)

	// Exit code reflects the outcome only once the run has actually
	// reached a terminal state (STEP-9-SPEC.md section 2.3) — reading a
	// still-"running" run is not itself a failure this program reports as
	// one; see exitCodeForMacroRun's own doc comment.
	if resp.Run.State == "finished" {
		return exitCodeForMacroRun(resp.Run)
	}
	return exitOK
}

func cmdRunList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl run list", stderr)
	var macroID, show, state string
	var limit int
	fs.StringVar(&macroID, "macro", "", "filter to runs of this macro id")
	fs.StringVar(&show, "show", "", "filter to runs of this show id")
	fs.StringVar(&state, "state", "", `filter to "running" or "finished"`)
	fs.IntVar(&limit, "limit", 0, "maximum number of runs to return (0 = server default)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl run list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList macro runs, most recent first (GET /api/v1/macro-runs). Steps are")
		_, _ = fmt.Fprintln(stderr, "not included in a list — fetch one run with \"showmeshctl run show <id>\"")
		_, _ = fmt.Fprintln(stderr, "for step detail.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "run list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	if state != "" && state != "running" && state != "finished" {
		_, _ = fmt.Fprintln(stderr, `showmeshctl run list: --state must be "running" or "finished"`)
		return exitUsage
	}
	if limit < 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl run list: --limit must not be negative")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "run list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveMacroClientTimeout(g.timeout))
	defer cancel()

	query := url.Values{}
	if macroID != "" {
		query.Set("macroId", macroID)
	}
	if show != "" {
		query.Set("show", show)
	}
	if state != "" {
		query.Set("state", state)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	var resp macroRunsListResponse
	if err := c.getJSON(ctx, "/api/v1/macro-runs", query, &resp); err != nil {
		return reportError(stderr, "run list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "run list", err)
		}
		return exitOK
	}
	printMacroRunsTable(stdout, resp)
	return exitOK
}
