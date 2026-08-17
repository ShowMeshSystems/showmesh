package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is showmeshctl's Track B seam B2c surface: "render settings
// get|set|revisions", over GET/PUT /api/v1/config/render.settings and its
// own revisions list. ADR-030/ADR-039: the CLI is the "the show is broken
// and the UI is down" path, and this is render.settings' own path. The
// "settings" subcommand's own dispatch is structured so a later seam can
// add "render status|apply|clear|restart" as SIBLING subcommands of
// cmdRender without restructuring this switch.

// renderRestartPolicy mirrors v1.ConfigRenderRestartPolicy field for field —
// this program's own independent transcription, never a shared type with
// the coordinator (the import-graph guard forbids importing any
// internal/coordinator package).
type renderRestartPolicy struct {
	InitialDelaySeconds        int `json:"initialDelaySeconds"`
	MaxDelaySeconds            int `json:"maxDelaySeconds"`
	MaxConsecutiveFastFailures int `json:"maxConsecutiveFastFailures"`
}

type configRenderSettingsPayload struct {
	IdleOutput    string              `json:"idleOutput"`
	RestartPolicy renderRestartPolicy `json:"restartPolicy"`
}

type renderSettingsConfigResponse struct {
	ServerTime             time.Time                   `json:"serverTime"`
	Kind                   string                      `json:"kind"`
	Revision               int64                       `json:"revision"`
	Payload                configRenderSettingsPayload `json:"payload"`
	UpdatedAt              time.Time                   `json:"updatedAt"`
	CreatedByPrincipalID   *string                     `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                     `json:"createdByPrincipalName"`
	Source                 string                      `json:"source"`
}

// cmdRender implements "showmeshctl render". Currently only "settings"
// exists; "status", "apply", "clear", and "restart" are a later seam's
// additions to this same switch.
func cmdRender(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printRenderUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printRenderUsage(stdout)
		return exitOK
	case "settings":
		return cmdRenderSettings(rest, stdout, stderr, clock)
	case "status":
		return cmdRenderStatus(rest, stdout, stderr, clock)
	case "apply":
		return cmdRenderApply(rest, stdout, stderr, clock)
	case "clear":
		return cmdRenderClear(rest, stdout, stderr, clock)
	case "restart":
		return cmdRenderRestart(rest, stdout, stderr, clock)
	case "transport":
		return cmdRenderTransport(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl render: unknown subcommand %q\n\n", sub)
		printRenderUsage(stderr)
		return exitUsage
	}
}

func printRenderUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl render <subcommand> [flags]

The render node's own configuration and control (Track B). "settings" is
the render.settings configuration kind (ADR-039): what a surface draws
while the MultiSync timeline is stopped, opened, or unknown (idleOutput),
and the pipeline supervisor's bounded restart backoff (restartPolicy).
"status", "apply", "clear", and "restart" (seam B2b-front) drive the
render pipeline itself over the node's allowlisted render.* operations
(internal/agent/renderops.go): render.surface.apply, render.surface.clear,
render.pipeline.restart. Every dispatch requires the render:command scope
and confirms by evidence (ADR-003) — a 200 is never conflated with the
pipeline having actually reached the state asked for. "transport" reads a
surface's most recently probed output-transport evidence (seam B4):
render.transport.probe is a real gst-launch-1.0 state transition, not an
element-existence check, and exits 22 when the transport is unavailable or
was never probed.

Subcommands:
  settings get|set|revisions   render.settings configuration (see
                                "showmeshctl render settings --help")
  status <node-id>             show per-surface render evidence for one
                                node (GET /api/v1/nodes/{nodeId}'s "render"
                                field): pipeline state, frame counters,
                                transport, restart/failure counts
  apply <node-id> <surface-id> <sequence-id>
                                dispatch render.surface.apply: the
                                coordinator resolves the surface's
                                complete assignment (including its current
                                FSEQ asset for sequence-id, by identity —
                                ADR-028) and refuses outright, naming what
                                could not be resolved, rather than ever
                                sending a partial one
  clear <node-id> <surface-id>   dispatch render.surface.clear
  restart <node-id> <surface-id> dispatch render.pipeline.restart
  transport <surface-id>       show a surface's most recently probed
                                transport availability and reason (open
                                read; exits 22 when unavailable or never
                                probed)

Run "showmeshctl render <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdRenderStatus(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render status", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render status [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow per-surface render evidence for one node (GET /api/v1/nodes/{nodeId}).")
		_, _ = fmt.Fprintln(stderr, "Exits 22 (unavailable) if this node has never published a render report at")
		_, _ = fmt.Fprintln(stderr, "all — a node that HAS reported, even if stale or unknown, prints normally")
		_, _ = fmt.Fprintln(stderr, "and exits 0: reported evidence is never treated the same as no evidence.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render status", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "render status", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body, err := c.getRaw(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID), nil)
	if err != nil {
		return reportError(stderr, "render status", err)
	}
	n, serverTime, err := decodeSingleNode(body)
	if err != nil {
		return reportError(stderr, "render status", err)
	}
	printClockSkew(stderr, serverTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, n.Render); err != nil {
			return reportError(stderr, "render status", err)
		}
		if len(n.Render) == 0 {
			return exitRenderUnavailable
		}
		return exitOK
	}
	if len(n.Render) == 0 {
		// "cannot tell" (never reported), never printed as though it were
		// a normal empty result — the same "no data yet" vs. "still
		// loading" distinction this project's Operator UI must keep
		// visually separate, applied here to exit-code separation instead.
		_, _ = fmt.Fprintf(stdout, "render status %s: no render report has ever been received from this node\n", nodeID)
		return exitRenderUnavailable
	}
	printRenderStatus(stdout, nodeID, n.Render)
	return exitOK
}

// printRenderStatus groups n.Render (a flat per-signal list) by surface
// and prints one block per surface, mirroring format.go's own
// grouped-evidence conventions elsewhere in this program.
func printRenderStatus(w io.Writer, nodeID string, entries []observationEntry) {
	bySurface := make(map[string][]observationEntry)
	var order []string
	for _, e := range entries {
		if _, seen := bySurface[e.Resource.ID]; !seen {
			order = append(order, e.Resource.ID)
		}
		bySurface[e.Resource.ID] = append(bySurface[e.Resource.ID], e)
	}
	_, _ = fmt.Fprintf(w, "render status for node %s:\n", nodeID)
	for _, surfaceID := range order {
		_, _ = fmt.Fprintf(w, "  surface %s:\n", surfaceID)
		for _, e := range bySurface[surfaceID] {
			reason := ""
			if e.Reason != nil {
				reason = " — " + *e.Reason
			}
			_, _ = fmt.Fprintf(w, "    %-32s %v (%s, via %s)%s\n", e.Signal, e.Value, e.State, e.Source, reason)
		}
	}
}

func cmdRenderApply(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render apply", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render apply [flags] <node-id> <surface-id> <sequence-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch render.surface.apply (requires render:command). The coordinator")
		_, _ = fmt.Fprintln(stderr, "resolves the surface's complete assignment, including its current FSEQ")
		_, _ = fmt.Fprintln(stderr, "asset for sequence-id (by identity — ADR-028, never a filename), and")
		_, _ = fmt.Fprintln(stderr, "refuses outright, naming what could not be resolved, rather than ever")
		_, _ = fmt.Fprintln(stderr, "sending a partial assignment.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render apply", err)
	}
	rest := fs.Args()
	if len(rest) != 3 {
		fs.Usage()
		return exitUsage
	}
	return dispatchRenderCommand(stdout, stderr, clock, g, "render apply", rest[0], rest[1], "apply", rest[2])
}

func cmdRenderClear(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render clear", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render clear [flags] <node-id> <surface-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch render.surface.clear (requires render:command): stop the")
		_, _ = fmt.Fprintln(stderr, "surface's pipeline and clear its persisted assignment.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render clear", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	return dispatchRenderCommand(stdout, stderr, clock, g, "render clear", rest[0], rest[1], "clear", "")
}

func cmdRenderRestart(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render restart", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render restart [flags] <node-id> <surface-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch render.pipeline.restart (requires render:command): clear any")
		_, _ = fmt.Fprintln(stderr, "fast-failure lockout and restart the surface's pipeline from its")
		_, _ = fmt.Fprintln(stderr, "currently-applied spec.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render restart", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	return dispatchRenderCommand(stdout, stderr, clock, g, "render restart", rest[0], rest[1], "restart", "")
}

func cmdRenderSettings(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printRenderSettingsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printRenderSettingsUsage(stdout)
		return exitOK
	case "get":
		return cmdRenderSettingsGet(rest, stdout, stderr, clock)
	case "set":
		return cmdRenderSettingsSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdRenderSettingsRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl render settings: unknown subcommand %q\n\n", sub)
		printRenderSettingsUsage(stderr)
		return exitUsage
	}
}

func printRenderSettingsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl render settings <subcommand> [flags]

Read or write the coordinator's render.settings configuration (Track B seam
B2c, ADR-039): idleOutput (what a surface draws while the MultiSync
timeline is stopped, opened, or unknown — one of black, hold, or
diagnostic) and restartPolicy (the render pipeline supervisor's bounded
restart backoff: initialDelaySeconds, maxDelaySeconds,
maxConsecutiveFastFailures). Every subcommand requires the config:write
scope (admin only) — there is no config:read scope.

This never 404s: nothing ever written reports the built-in default
(idleOutput "black") with revision 0 and source "default".

Subcommands:
  get         show the active configuration (or the built-in default)
  set         write a new configuration revision — a FULL REPLACEMENT
              (reads a payload from --file, or from stdin if --file is
              not given); every field is required, including every member
              of restartPolicy
  revisions   list revision history, newest first

Run "showmeshctl render settings <subcommand> --help" for flags specific to
one subcommand.
`)
}

func cmdRenderSettingsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render settings get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render settings get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active render.settings configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render settings get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "render settings get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp renderSettingsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/render.settings", nil, &resp); err != nil {
		return reportError(stderr, "render settings get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "render settings get", err)
		}
		return exitOK
	}
	printRenderSettingsConfig(stdout, resp)
	return exitOK
}

// cmdRenderSettingsSet implements `render settings set`: a FULL
// REPLACEMENT (unlike "config set"'s fpp.endpoints, this kind never merges
// against a previous revision) — every field of the payload is required,
// including every member of restartPolicy. The payload is read from
// --file, or from stdin when --file is not given, and parsed directly as
// configRenderSettingsPayload: unlike fpp.endpoints, there is no second
// "config get"-shaped envelope to tolerate, because the coordinator's own
// PUT body IS the bare {"idleOutput":...,"restartPolicy":{...}} shape.
func cmdRenderSettingsSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render settings set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON file matching {\"idleOutput\":string,\"restartPolicy\":{...}}; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render settings set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new render.settings configuration revision (requires config:write,")
		_, _ = fmt.Fprintln(stderr, "admin only). A FULL REPLACEMENT: every field is required, including every")
		_, _ = fmt.Fprintln(stderr, "member of restartPolicy — an absent field is refused by name, never")
		_, _ = fmt.Fprintln(stderr, "silently defaulted or carried forward from the previous revision.")
		_, _ = fmt.Fprintln(stderr, "Validated before activation: an invalid payload is rejected and appends no")
		_, _ = fmt.Fprintln(stderr, "revision (ADR-009).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render settings set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "render settings set", newCLIError(exitUsage, "%v", err))
	}

	payload, err := parseRenderSettingsSetPayload(raw)
	if err != nil {
		return reportError(stderr, "render settings set", newCLIError(exitUsage, "%v", err))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "render settings set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp renderSettingsConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/render.settings", payload, &resp); err != nil {
		return reportError(stderr, "render settings set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "render settings set", err)
		}
		return exitOK
	}
	printRenderSettingsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl render settings set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

// parseRenderSettingsSetPayload decodes raw directly as
// configRenderSettingsPayload. Unlike parseConfigSetPayload
// (cmd_config.go), there is no second "config get"-response envelope to
// tolerate: "render settings get --output json" prints the FULL response
// object (revision, source, etc.), and this command does not accept that
// shape back — piping one into the other is not a supported round trip for
// this kind, because the coordinator's own PUT body is exactly the bare
// {"idleOutput":...,"restartPolicy":{...}} object, with nothing nested
// three levels down the way fpp.endpoints' "payload.endpoints" was.
func parseRenderSettingsSetPayload(raw []byte) (configRenderSettingsPayload, error) {
	var p configRenderSettingsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return configRenderSettingsPayload{}, fmt.Errorf(
			`payload must be a JSON object matching {"idleOutput":string,"restartPolicy":{"initialDelaySeconds":int,"maxDelaySeconds":int,"maxConsecutiveFastFailures":int}}: %w`, err)
	}
	return p, nil
}

func cmdRenderSettingsRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render settings revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render settings revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList render.settings revision history, newest first.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render settings revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "render settings revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/render.settings/revisions", nil, &resp); err != nil {
		return reportError(stderr, "render settings revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "render settings revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// printRenderSettingsConfig renders resp as human-readable text.
func printRenderSettingsConfig(w io.Writer, resp renderSettingsConfigResponse) {
	by := "(built-in default; no revision has ever been written)"
	if resp.CreatedByPrincipalName != nil {
		by = "by " + *resp.CreatedByPrincipalName
	}
	_, _ = fmt.Fprintf(w, "render.settings revision %d (source %s, %s):\n", resp.Revision, resp.Source, by)
	_, _ = fmt.Fprintf(w, "  idleOutput: %s\n", resp.Payload.IdleOutput)
	_, _ = fmt.Fprintf(w, "  restartPolicy: initialDelaySeconds=%d maxDelaySeconds=%d maxConsecutiveFastFailures=%d\n",
		resp.Payload.RestartPolicy.InitialDelaySeconds, resp.Payload.RestartPolicy.MaxDelaySeconds,
		resp.Payload.RestartPolicy.MaxConsecutiveFastFailures)
}
