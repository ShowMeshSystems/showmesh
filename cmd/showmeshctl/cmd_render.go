package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

The render node's own configuration and control (Track B): "settings", the
render.settings configuration kind (ADR-039): what a surface draws while
the MultiSync timeline is stopped, opened, or unknown (idleOutput), and the
pipeline supervisor's bounded restart backoff (restartPolicy); and
"transport", a surface's most recently probed output-transport evidence
(Track B seam B4). A later seam adds "status", "apply", "clear", and
"restart" here for the render pipeline itself.

Subcommands:
  settings get         show the active render.settings configuration
                        (requires config:write; never 404s — reports the
                        built-in default when nothing has been written)
  settings set          write a new render.settings revision (requires
                        config:write, admin only; a full replacement — every
                        field required)
  settings revisions   list render.settings revision history, newest first
                        (requires config:write)
  transport             show a surface's most recently probed transport
                        availability and reason (open read; exits 22 when
                        unavailable or never probed)

Run "showmeshctl render <subcommand> --help" for flags specific to one
subcommand.
`)
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
