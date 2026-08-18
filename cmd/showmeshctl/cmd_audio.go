package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is showmeshctl's audio surface: "audio settings
// get|set|revisions" over GET/PUT /api/v1/config/audio.settings, and
// "audio node list|get|set|revisions" over
// GET/PUT /api/v1/config/audio.node[/{id}[/revisions]]. Declares its own
// wire types rather than importing internal/coordinator/api/v1 (the
// import-graph test forbids it), matching cmd_render.go/cmd_surface.go's
// identical precedent one kind over. showConfigObjectsListResponse and
// configRevisionsResponse (types_macro.go, types.go) are already
// kind-agnostic and reused verbatim rather than declared a third time.

// configAudioSettingsPayload mirrors v1.ConfigAudioSettingsPayload.
type configAudioSettingsPayload struct {
	DriftIgnoreThresholdMs   int     `json:"driftIgnoreThresholdMs"`
	DefaultFadeCurve         string  `json:"defaultFadeCurve"`
	DefaultFadeDurationMs    int     `json:"defaultFadeDurationMs"`
	DefaultMaxBackgroundGain float64 `json:"defaultMaxBackgroundGain"`
}

type audioSettingsConfigResponse struct {
	ServerTime             time.Time                  `json:"serverTime"`
	Kind                   string                     `json:"kind"`
	Revision               int64                      `json:"revision"`
	Payload                configAudioSettingsPayload `json:"payload"`
	UpdatedAt              time.Time                  `json:"updatedAt"`
	CreatedByPrincipalID   *string                    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                    `json:"createdByPrincipalName"`
	Source                 string                     `json:"source"`
}

// configAudioNode mirrors v1.ConfigAudioNode.
type configAudioNode struct {
	ProgramRoute          string `json:"programRoute"`
	LTCRoute              string `json:"ltcRoute"`
	ClockDomain           string `json:"clockDomain"`
	ClockDomainProvenance string `json:"clockDomainProvenance"`
}

type audioNodeConfigResponse struct {
	ServerTime             time.Time       `json:"serverTime"`
	Kind                   string          `json:"kind"`
	ID                     string          `json:"id"`
	Revision               int64           `json:"revision"`
	Payload                configAudioNode `json:"payload"`
	UpdatedAt              time.Time       `json:"updatedAt"`
	CreatedByPrincipalID   *string         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string         `json:"createdByPrincipalName"`
	Source                 string          `json:"source"`
}

func cmdAudio(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioUsage(stdout)
		return exitOK
	case "settings":
		return cmdAudioSettings(rest, stdout, stderr, clock)
	case "node":
		return cmdAudioNode(rest, stdout, stderr, clock)
	case "session":
		return cmdAudioSession(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio: unknown subcommand %q\n\n", sub)
		printAudioUsage(stderr)
		return exitUsage
	}
}

func printAudioUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio <subcommand> [flags]

The audio engine's own configuration (Track C, ADR-039). "settings" is the
audio.settings singleton: engine-wide operator defaults (drift ignore
threshold, default fade curve/duration, default background gain ceiling).
"node" is the audio.node collection: which discovered output route on one
node carries program, which carries LTC, and the operator-declared clock
domain they share. A write to "node" is refused unless the node has
ALREADY ADVERTISED both routes in its own capability report — the
coordinator never accepts a route name on the operator's claim alone.

Subcommands:
  settings get|set|revisions   audio.settings configuration (see
                                "showmeshctl audio settings --help")
  node list|get|set|revisions  audio.node configuration (see
                                "showmeshctl audio node --help")
  session <op>                 dispatch a playback session command (see
                                "showmeshctl audio session --help")

Run "showmeshctl audio <subcommand> --help" for flags specific to one
subcommand.
`)
}

// --- audio settings ---

func cmdAudioSettings(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioSettingsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioSettingsUsage(stdout)
		return exitOK
	case "get":
		return cmdAudioSettingsGet(rest, stdout, stderr, clock)
	case "set":
		return cmdAudioSettingsSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdAudioSettingsRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio settings: unknown subcommand %q\n\n", sub)
		printAudioSettingsUsage(stderr)
		return exitUsage
	}
}

func printAudioSettingsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio settings <subcommand> [flags]

Read or write the coordinator's audio.settings configuration (ADR-039):
driftIgnoreThresholdMs (never measured — a starting point, not a tuned
value), defaultFadeCurve (only "linear" ships today), defaultFadeDurationMs,
and defaultMaxBackgroundGain (a linear multiplier, 1.0 is unity gain).
Every subcommand requires the config:write scope (admin only) — there is
no config:read scope.

This never 404s: nothing ever written reports the built-in default with
revision 0 and source "default".

Subcommands:
  get         show the active configuration (or the built-in default)
  set         write a new configuration revision — a FULL REPLACEMENT
              (reads a payload from --file, or from stdin if --file is
              not given); every field is required
  revisions   list revision history, newest first

Run "showmeshctl audio settings <subcommand> --help" for flags specific to
one subcommand.
`)
}

func cmdAudioSettingsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio settings get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio settings get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active audio.settings configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio settings get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio settings get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioSettingsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.settings", nil, &resp); err != nil {
		return reportError(stderr, "audio settings get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio settings get", err)
		}
		return exitOK
	}
	printAudioSettingsConfig(stdout, resp)
	return exitOK
}

// cmdAudioSettingsSet implements `audio settings set`: a FULL REPLACEMENT
// — every field of the payload is required. The payload is read from
// --file, or from stdin when --file is not given, and sent to the
// coordinator verbatim as json.RawMessage after only a shape check (a JSON
// object) — this command never decodes it into configAudioSettingsPayload
// and re-encodes, because that would silently turn an operator's omitted
// field into an explicit zero value the server would then accept, making
// the server's field_required/field_null refusals unreachable through this
// emergency-path client. Matches cmd_action.go/cmd_macro.go's identical
// pass-through precedent one config kind over.
func cmdAudioSettingsSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio settings set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON file matching configAudioSettingsPayload; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio settings set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new audio.settings configuration revision (requires config:write,")
		_, _ = fmt.Fprintln(stderr, "admin only). A FULL REPLACEMENT: every field is required — an absent field")
		_, _ = fmt.Fprintln(stderr, "is refused by name, never silently defaulted or carried forward from the")
		_, _ = fmt.Fprintln(stderr, "previous revision.")
		_, _ = fmt.Fprintln(stderr, "Validated before activation: an invalid payload is rejected and appends no")
		_, _ = fmt.Fprintln(stderr, "revision (ADR-009).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio settings set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "audio settings set", newCLIError(exitUsage, "%v", err))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return reportError(stderr, "audio settings set", newCLIError(exitUsage, "payload must be a JSON object matching configAudioSettingsPayload: %v", err))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio settings set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioSettingsConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/audio.settings", json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, "audio settings set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio settings set", err)
		}
		return exitOK
	}
	printAudioSettingsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl audio settings set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

func cmdAudioSettingsRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio settings revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio settings revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList audio.settings revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/audio.settings/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio settings revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio settings revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.settings/revisions", nil, &resp); err != nil {
		return reportError(stderr, "audio settings revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio settings revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// --- audio node ---

func cmdAudioNode(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioNodeUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioNodeUsage(stdout)
		return exitOK
	case "list":
		return cmdAudioNodeList(rest, stdout, stderr, clock)
	case "get":
		return cmdAudioNodeGet(rest, stdout, stderr, clock)
	case "set":
		return cmdAudioNodeSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdAudioNodeRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio node: unknown subcommand %q\n\n", sub)
		printAudioNodeUsage(stderr)
		return exitUsage
	}
}

func printAudioNodeUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio node <subcommand> [flags]

Read or write the coordinator's audio.node configuration objects, one per
node (ADR-018, ADR-039): which discovered output route carries program,
which carries LTC, and the clock domain the operator declares them to
share (never inferred — no software call proves two outputs share a
hardware clock). Reads and writes both require config:write, admin only.

"set" is refused with the node's own advertised routes named in the error
unless BOTH --program-route and --ltc-route are already present in that
node's own capability advertisement (audio.output.local / audio.output.ltc)
— never accepted on the operator's claim alone. Advertise the node first
(the agent must be running and have probed its audio hardware) before
configuring it here.

Subcommands:
  list             enumerate audio.node objects (id is the node id)
  get <node-id>    show one node's full audio placement
  set <node-id>    write a new audio.node revision (write, full
                   replacement)
  revisions <node-id>
                   list revision history, newest first

Run "showmeshctl audio node <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdAudioNodeList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate audio.node objects (GET /api/v1/config/audio.node).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.node", nil, &resp); err != nil {
		return reportError(stderr, "audio node list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdAudioNodeGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node get [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one node's audio placement (GET /api/v1/config/audio.node/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioNodeConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.node/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "audio node get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node get", err)
		}
		return exitOK
	}
	printAudioNodeDetail(stdout, resp)
	return exitOK
}

func cmdAudioNodeSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node set", stderr)
	var programRoute, ltcRoute, clockDomain, clockDomainProvenance string
	fs.StringVar(&programRoute, "program-route", "", "the advertised output route to carry program audio (required)")
	fs.StringVar(&ltcRoute, "ltc-route", "", "the advertised output route to carry LTC (required)")
	fs.StringVar(&clockDomain, "clock-domain", "", "the operator's own name for the shared clock domain (required)")
	fs.StringVar(&clockDomainProvenance, "clock-domain-provenance", "", "the stated basis for the clock domain declaration (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node set [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new audio.node revision (PUT /api/v1/config/audio.node/{id}).")
		_, _ = fmt.Fprintln(stderr, "Requires config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT: all four flags are required on every call and")
		_, _ = fmt.Fprintln(stderr, "this command never reads the node's current definition first. Refused")
		_, _ = fmt.Fprintln(stderr, "unless the node has already advertised both routes in its own capability")
		_, _ = fmt.Fprintln(stderr, "report — never accepted on the operator's claim alone.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if programRoute == "" || ltcRoute == "" || clockDomain == "" || clockDomainProvenance == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl audio node set: --program-route, --ltc-route, --clock-domain, and --clock-domain-provenance are all required")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configAudioNode{
		ProgramRoute: programRoute, LTCRoute: ltcRoute,
		ClockDomain: clockDomain, ClockDomainProvenance: clockDomainProvenance,
	}
	var resp audioNodeConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/audio.node/"+url.PathEscape(id), body, &resp); err != nil {
		return reportError(stderr, "audio node set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node set", err)
		}
		return exitOK
	}
	printAudioNodeDetail(stdout, resp)
	return exitOK
}

func cmdAudioNodeRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node revisions [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nList audio.node revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/audio.node/{id}/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.node/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "audio node revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// --- printers ---

func printAudioSettingsConfig(w io.Writer, resp audioSettingsConfigResponse) {
	by := "(built-in default; no revision has ever been written)"
	if resp.CreatedByPrincipalName != nil {
		by = "by " + *resp.CreatedByPrincipalName
	}
	_, _ = fmt.Fprintf(w, "audio.settings revision %d (source %s, %s):\n", resp.Revision, resp.Source, by)
	_, _ = fmt.Fprintf(w, "  driftIgnoreThresholdMs:   %d\n", resp.Payload.DriftIgnoreThresholdMs)
	_, _ = fmt.Fprintf(w, "  defaultFadeCurve:         %s\n", resp.Payload.DefaultFadeCurve)
	_, _ = fmt.Fprintf(w, "  defaultFadeDurationMs:    %d\n", resp.Payload.DefaultFadeDurationMs)
	_, _ = fmt.Fprintf(w, "  defaultMaxBackgroundGain: %v\n", resp.Payload.DefaultMaxBackgroundGain)
}

func printAudioNodeDetail(w io.Writer, resp audioNodeConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Node ID:                %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Program route:          %s\n", p.ProgramRoute)
	_, _ = fmt.Fprintf(w, "LTC route:              %s\n", p.LTCRoute)
	_, _ = fmt.Fprintf(w, "Clock domain:           %s\n", p.ClockDomain)
	_, _ = fmt.Fprintf(w, "Clock domain provenance: %s\n", p.ClockDomainProvenance)
	_, _ = fmt.Fprintf(w, "Revision:               %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:                %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:             %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintln(w, "Created by:             (unknown)")
	}
}
