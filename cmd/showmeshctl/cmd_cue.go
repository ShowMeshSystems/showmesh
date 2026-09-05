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
	case "delete":
		return cmdCueDelete(rest, stdout, stderr, clock)
	case "activate":
		return cmdCueActivate(rest, stdout, stderr, clock)
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
  delete --confirm <id>  tombstone this cue (write); revision history stays
                          readable via "revisions". A show.playlist entry
                          naming this cue afterward is not refused here; it
                          reports the gap when actually resolved for
                          dispatch, never a crash
  activate <id>           fire this cue's activation directly, on every
                          node it resolves to (requires cue:activate) -
                          never through a playlist or an FPP observation

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
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
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
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read of this cue), refusing with a")
		_, _ = fmt.Fprintln(stderr, "409 if it changed since it was read.")
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

	apiPath := "/api/v1/config/show.cue/" + url.PathEscape(id)
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r showCueConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "cue set", err)
	}

	body := configShowCue{Show: show, Name: name, Outputs: json.RawMessage(outputsJSON)}
	var resp showCueConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
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

// cmdCueDelete mirrors cmdSurfaceDelete's own shape (cmd_surface.go):
// --confirm is required and checked locally before any request is sent. A
// tombstone, not a hard delete: revision history stays readable through
// "cue revisions" afterward.
func cmdCueDelete(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cue delete", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms deletion of this cue")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cue delete --confirm <cue-id>")
		_, _ = fmt.Fprintln(stderr, "\nDelete a show.cue object (DELETE /api/v1/config/show.cue/{id}). This")
		_, _ = fmt.Fprintln(stderr, "is a tombstone, not a hard delete: the object's revision history still")
		_, _ = fmt.Fprintln(stderr, "reads through \"cue revisions\" afterward. Requires config:write and")
		_, _ = fmt.Fprintln(stderr, "--confirm.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cue delete", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl cue delete: refusing to delete "+id+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cue delete", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configObjectDeleteRequest{Confirm: true}
	if err := c.deleteJSON(ctx, "/api/v1/config/show.cue/"+url.PathEscape(id), body, nil); err != nil {
		return reportError(stderr, "cue delete", err)
	}

	_, _ = fmt.Fprintf(stdout, "cue %s deleted\n", id)
	return exitOK
}

// cueActivateResponse mirrors v1.CueActivateResponse field for field.
// This program's own copy (client/server independence - cmd_cuecatalog.go's
// identical reasoning one file over): showmeshctl never imports the
// coordinator's internal v1 package.
type cueActivateResponse struct {
	ServerTime time.Time                  `json:"serverTime"`
	CueID      string                     `json:"cueId"`
	Nodes      []cueActivationNodeOutcome `json:"nodes"`
}

// cueActivationNodeOutcome mirrors v1.CueActivationNodeOutcome field for
// field.
type cueActivationNodeOutcome struct {
	NodeID        string `json:"nodeId"`
	Dispatched    bool   `json:"dispatched"`
	Confirmed     bool   `json:"confirmed"`
	Outcome       string `json:"outcome"`
	OutcomeReason string `json:"outcomeReason,omitempty"`
}

// cmdCueActivate implements "showmeshctl cue activate <cue-id>":
// POST /api/v1/cues/{id}/activate, requiring cue:activate. Fires cueID
// directly on every node it resolves to, exactly as Live Control's own
// "Fire" control does - never through a playlist or an FPP observation.
// Takes no request body: every call is a fresh dispatch, matching the
// route's own reasoning (cuefire.go's doc comment) that a manual fire is
// never a retry-replay of an earlier one.
func cmdCueActivate(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cue activate", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cue activate [flags] <cue-id>")
		_, _ = fmt.Fprintln(stderr, "\nFire one Cue's activation directly, on every node it resolves to")
		_, _ = fmt.Fprintln(stderr, "(POST /api/v1/cues/{id}/activate, requires cue:activate). This is the")
		_, _ = fmt.Fprintln(stderr, "same direct-fire path Live Control's Announcements control uses,")
		_, _ = fmt.Fprintln(stderr, "never through a playlist or an FPP observation.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cue activate", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cue activate", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp cueActivateResponse
	if err := c.postJSON(ctx, "/api/v1/cues/"+url.PathEscape(id)+"/activate", nil, &resp); err != nil {
		return reportError(stderr, "cue activate", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cue activate", err)
		}
		return exitCodeForCueActivateResponse(resp)
	}
	return reportCueActivateResponse(stdout, resp)
}

// reportCueActivateResponse prints one line per node, never a single
// collapsed verdict, mirroring reportEmergencyStopResult's identical
// per-instance reasoning one file over, then returns the worst outcome's
// exit code.
func reportCueActivateResponse(stdout io.Writer, resp cueActivateResponse) int {
	if len(resp.Nodes) == 0 {
		_, _ = fmt.Fprintf(stdout, "%s: accepted, but no node resolves this cue (nothing to report)\n", resp.CueID)
		return exitOK
	}
	for _, n := range resp.Nodes {
		_, _ = fmt.Fprintf(stdout, "%s: %s %s: %s\n", n.Outcome, resp.CueID, n.NodeID, n.OutcomeReason)
	}
	return exitCodeForCueActivateResponse(resp)
}

// cueActivationOutcomeSeverity ranks one node outcome word so
// exitCodeForCueActivateResponse can take the WORST across every node,
// emergencyStopOutcomeSeverity's identical ranking one file over
// (cmd_emergency_stop.go), narrowed to this route's own three-word
// vocabulary (this route never reports "unconfirmable").
func cueActivationOutcomeSeverity(outcome string) int {
	switch outcome {
	case "confirmed":
		return 0
	case "unconfirmed", "":
		return 1
	case "refused":
		return 2
	case "failed":
		return 3
	default:
		return 1
	}
}

// exitCodeForCueActivateResponse takes the worst outcome across every
// node in resp.Nodes; an empty Nodes list is a real, honest "nothing to
// report" (no node resolves this cue) and exits 0.
func exitCodeForCueActivateResponse(resp cueActivateResponse) int {
	worst := "confirmed"
	for _, n := range resp.Nodes {
		if cueActivationOutcomeSeverity(n.Outcome) > cueActivationOutcomeSeverity(worst) {
			worst = n.Outcome
		}
	}
	switch worst {
	case "confirmed":
		return exitOK
	case "refused":
		return exitActionRefused
	case "failed":
		return exitActionFailed
	default: // "unconfirmed"
		return exitCommandUnconfirmed
	}
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
