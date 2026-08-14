package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is "showmeshctl action list"/"action show", read-only against
// the show.action configuration kind (STEP-9-SPEC.md section 5.3). It
// falls out of the identical GET /config/{kind}[/{id}] shape "macro
// list"/"macro show" already use, over show.action instead of show.macro —
// see printShowConfigObjectsTable (macro_print.go), which both list
// commands share. Writing an action definition is Track E's authoring
// surface (ADR-030), not this step's, exactly as for show.macro.

func cmdAction(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printActionUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printActionUsage(stdout)
		return exitOK
	case "list":
		return cmdActionList(rest, stdout, stderr, clock)
	case "show":
		return cmdActionShow(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl action: unknown subcommand %q\n\n", sub)
		printActionUsage(stderr)
		return exitUsage
	}
}

func printActionUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl action <subcommand> [flags]

Read the coordinator's show.action configuration objects: the logical
actions ("Projectors on", "Stop main show") that a show.macro's steps
reference. Reads require show:macro:run OR config:write.

Subcommands:
  list         enumerate action objects (id, label, show, revision)
  show <id>    show one action's full definition, including its target

Writing an action definition is not this program's job: the show-authoring
surface owns PUT for both show.action and show.macro.

Run "showmeshctl action <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdActionList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show.action objects (GET /api/v1/config/show.action).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "action list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.action", nil, &resp); err != nil {
		return reportError(stderr, "action list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "action list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdActionShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action show", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action show [flags] <action-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one action's full definition (GET /api/v1/config/show.action/{id}),")
		_, _ = fmt.Fprintln(stderr, "including its safetyClass and its fpp/mqtt target.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action show", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "action show", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showActionConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.action/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "action show", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "action show", err)
		}
		return exitOK
	}
	printShowActionDetail(stdout, resp)
	return exitOK
}
