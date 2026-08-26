package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// showmeshctl surface for the "show.cue" configuration kind
// (TRACK-H-H1-SPEC.md section 2): list (optional --show filter), get, full-
// replacement set, revisions. Declares its own wire types, matching
// cmd_surface.go's reasoning.
//
// "cue set" is a full replacement: this command never reads the current
// definition first. Because outputs.* is a set of independent optional
// members, --outputs-json takes the whole "outputs" object as raw JSON
// rather than a wall of per-field flags one per nested member — the
// alternative (one flag per render/audio/ltc/announcement field, each only
// sometimes present) would need its own presence-tracking scheme that
// duplicates what a JSON object already expresses directly.

// configShowCue mirrors v1.ConfigShowCue.
type configShowCue struct {
	Show    string          `json:"show"`
	Name    string          `json:"name"`
	Outputs json.RawMessage `json:"outputs"`
}

// showCueConfigResponse is the body of GET and PUT /config/show.cue/{id}.
type showCueConfigResponse struct {
	ServerTime             time.Time     `json:"serverTime"`
	Kind                   string        `json:"kind"`
	ID                     string        `json:"id"`
	Revision               int64         `json:"revision"`
	Payload                configShowCue `json:"payload"`
	UpdatedAt              time.Time     `json:"updatedAt"`
	CreatedByPrincipalID   *string       `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string       `json:"createdByPrincipalName"`
	Source                 string        `json:"source"`
}

func cmdCue(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printCueUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printCueUsage(stdout)
		return exitOK
	case "list":
		return cmdCueList(rest, stdout, stderr, clock)
	case "get":
		return cmdCueGet(rest, stdout, stderr, clock)
	case "set":
		return cmdCueSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdCueRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl cue: unknown subcommand %q\n\n", sub)
		printCueUsage(stderr)
		return exitUsage
	}
}

func printCueUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl cue <subcommand> [flags]

Read or write the coordinator's "show.cue" configuration objects (Track H,
ADR-043: a Cue is the show-scoped, runner-agnostic intent for one
synchronized playback item). Reads require show:macro:run OR config:write;
writes require config:write.

Subcommands:
  list [--show <id>]     enumerate cue objects, optionally narrowed to one show
  get <id>               show one cue's full definition
  set <id>               write a new cue revision (write, full replacement)
  revisions <id>         list revision history, newest first

Run "showmeshctl cue <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdCueList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cue list", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the list to cues belonging to this show id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cue list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show.cue objects (GET /api/v1/config/show.cue).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cue list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cue list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var query url.Values
	if show != "" {
		query = url.Values{"show": {show}}
	}

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.cue", query, &resp); err != nil {
		return reportError(stderr, "cue list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cue list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdCueGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cue get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cue get [flags] <cue-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one cue's full definition (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.cue/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cue get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cue get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showCueConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.cue/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "cue get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cue get", err)
		}
		return exitOK
	}
	printCueDetail(stdout, resp)
	return exitOK
}

func cmdCueSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cue set", stderr)
	var show, name, outputsJSON string
	fs.StringVar(&show, "show", "", "the show this cue belongs to (required)")
	fs.StringVar(&name, "name", "", "the cue's operator-facing name (required)")
	fs.StringVar(&outputsJSON, "outputs-json", "", "the cue's \"outputs\" object, as raw JSON (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cue set [flags] <cue-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.cue revision (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.cue/{id}). Requires config:write.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT: this command never reads the cue's current")
		_, _ = fmt.Fprintln(stderr, "definition first. --outputs-json is the whole outputs object, e.g.:")
		_, _ = fmt.Fprintln(stderr, `  '{"render":{"sequence":"thriller"},"audio":{"asset":"thriller-audience","startOffsetMillis":0}}'`)
		_, _ = fmt.Fprintln(stderr, "\nEach of outputs.audio/ltc/announcement also accepts an optional \"target\"")
		_, _ = fmt.Fprintln(stderr, "naming an audio.node id (ADR-045); omitted, it resolves later to the")
		_, _ = fmt.Fprintln(stderr, "installation's single program+ltc audio.node.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cue set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	var missing []string
	if show == "" {
		missing = append(missing, "--show")
	}
	if name == "" {
		missing = append(missing, "--name")
	}
	if outputsJSON == "" {
		missing = append(missing, "--outputs-json")
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl cue set: missing required flag(s): %v\n", missing)
		return exitUsage
	}
	if !json.Valid([]byte(outputsJSON)) {
		_, _ = fmt.Fprintln(stderr, "showmeshctl cue set: --outputs-json is not valid JSON")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cue set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configShowCue{Show: show, Name: name, Outputs: json.RawMessage(outputsJSON)}
	var resp showCueConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/show.cue/"+url.PathEscape(id), body, &resp); err != nil {
		return reportError(stderr, "cue set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cue set", err)
		}
		return exitOK
	}
	printCueDetail(stdout, resp)
	return exitOK
}

func cmdCueRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cue revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cue revisions [flags] <cue-id>")
		_, _ = fmt.Fprintln(stderr, "\nList show.cue revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.cue/{id}/revisions). Metadata only, no payload.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cue revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cue revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.cue/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "cue revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cue revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func printCueDetail(w io.Writer, resp showCueConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Cue ID:       %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:         %s\n", p.Show)
	_, _ = fmt.Fprintf(w, "Name:         %s\n", p.Name)
	_, _ = fmt.Fprintf(w, "Outputs:      %s\n", string(p.Outputs))
	_, _ = fmt.Fprintf(w, "Revision:     %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:      %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:   %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:   (no principal recorded)\n")
	}
}
