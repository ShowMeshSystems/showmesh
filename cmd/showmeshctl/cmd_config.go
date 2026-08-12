package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// This file is Step 7 seam A's showmeshctl surface: `config get`, `config
// set`, and `config revisions`, over GET/PUT /api/v1/config/fpp.endpoints
// and GET /api/v1/config/fpp.endpoints/revisions. This is the FIRST write
// this CLI has ever issued — every prior command is GET-only (see
// main.go's top-level help text, corrected by this seam) — so `config set`
// is also the first exercise of [client.putJSON].
//
// Only "fpp.endpoints" exists as a configuration kind today (RES-008 D1),
// so none of these three subcommands takes a kind argument; a future
// config kind is a CLI change, not something this shape has to anticipate
// now.

func cmdConfig(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printConfigUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printConfigUsage(stdout)
		return exitOK
	case "get":
		return cmdConfigGet(rest, stdout, stderr, clock)
	case "set":
		return cmdConfigSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdConfigRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl config: unknown subcommand %q\n\n", sub)
		printConfigUsage(stderr)
		return exitUsage
	}
}

func printConfigUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl config <subcommand> [flags]

Read or write the coordinator's fpp.endpoints configuration (Step 7,
RES-008 D1): the list of FPP instances the coordinator polls, moved out
of SHOWMESH_FPP_ENDPOINTS into the coordinator's authoritative store.
Every subcommand requires the config:write scope (admin only) — there is
no config:read scope; reading this surface is exactly as sensitive as
writing it.

Subcommands:
  get         show the active configuration
  set         write a new configuration revision (reads a payload from
              --file, or from stdin if --file is not given)
  revisions   list revision history, newest first

A configuration change here does NOT take effect until the coordinator's
next restart: this coordinator does not hot-reload configuration.
"showmeshctl config set" and "showmeshctl config get" both print this
fact; do not skip it when scripting against this command.

Run "showmeshctl config <subcommand> --help" for flags specific to one
subcommand.
`)
}

// cmdConfigGet implements `showmeshctl config get`
// (GET /api/v1/config/fpp.endpoints).
func cmdConfigGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl config get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl config get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active fpp.endpoints configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "config get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "config get", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppEndpointsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/fpp.endpoints", nil, &resp); err != nil {
		return reportError(stderr, "config get", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "config get", err)
		}
		return exitOK
	}
	printFPPEndpointsConfig(stdout, resp)
	return exitOK
}

// cmdConfigSet implements `showmeshctl config set`
// (PUT /api/v1/config/fpp.endpoints): this CLI's first write. The payload
// is read from --file, or from stdin when --file is not given, so it
// composes naturally with `showmeshctl config get --output json` piped
// through an edit and back in, or with a hand-written file kept under
// version control.
func cmdConfigSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl config set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON file matching {\"endpoints\":[{\"id\":string,\"url\":string},...]}; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl config set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new fpp.endpoints configuration revision (requires config:write,")
		_, _ = fmt.Fprintln(stderr, "admin only). Validated before activation: an invalid payload is rejected")
		_, _ = fmt.Fprintln(stderr, "and appends no revision (ADR-009).")
		_, _ = fmt.Fprintln(stderr, "\nThis does NOT take effect until the coordinator's next restart.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "config set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "config set", newCLIError(exitUsage, "%v", err))
	}

	var payload configFPPEndpointsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return reportError(stderr, "config set", newCLIError(exitUsage,
			`payload must be JSON matching {"endpoints":[{"id":string,"url":string},...]}: %v`, err))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "config set", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppEndpointsConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/fpp.endpoints", payload, &resp); err != nil {
		return reportError(stderr, "config set", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "config set", err)
		}
		return exitOK
	}
	printFPPEndpointsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl config set: revision %d is now active. %s\n", resp.Revision, resp.RestartRequiredReason)
	return exitOK
}

// readConfigPayload reads file's contents, or stdin when file is "".
func readConfigPayload(file string) ([]byte, error) {
	if file == "" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading payload from stdin: %w", err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading payload from %s: %w", file, err)
	}
	return raw, nil
}

// cmdConfigRevisions implements `showmeshctl config revisions`
// (GET /api/v1/config/fpp.endpoints/revisions).
func cmdConfigRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl config revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl config revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList fpp.endpoints revision history, newest first. Metadata only —")
		_, _ = fmt.Fprintln(stderr, "no payload; rollback tooling is deliberately out of scope (RES-008).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "config revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "config revisions", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/fpp.endpoints/revisions", nil, &resp); err != nil {
		return reportError(stderr, "config revisions", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "config revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}
