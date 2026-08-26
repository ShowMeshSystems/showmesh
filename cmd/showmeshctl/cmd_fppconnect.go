package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// This file is showmeshctl's fppconnect surface: "fppconnect settings
// get|set|revisions" over GET/PUT /api/v1/config/fppconnect.settings
// (ADR-044 decision 5). Declares its own wire types rather than importing
// internal/coordinator/api/v1 (the import-graph test forbids it), matching
// cmd_audio.go's identical precedent one kind over. configRevisionsResponse
// (types.go) is already kind-agnostic and reused verbatim rather than
// declared a second time.

// configFPPConnectSettingsPayload mirrors v1.ConfigFPPConnectSettingsPayload.
type configFPPConnectSettingsPayload struct {
	Enabled          bool  `json:"enabled"`
	MaxFileBytes     int64 `json:"maxFileBytes"`
	MaxAssetDirBytes int64 `json:"maxAssetDirBytes"`
}

type fppConnectSettingsConfigResponse struct {
	ServerTime             time.Time                       `json:"serverTime"`
	Kind                   string                          `json:"kind"`
	Revision               int64                           `json:"revision"`
	Payload                configFPPConnectSettingsPayload `json:"payload"`
	UpdatedAt              time.Time                       `json:"updatedAt"`
	CreatedByPrincipalID   *string                         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                         `json:"createdByPrincipalName"`
	Source                 string                          `json:"source"`
}

func cmdFPPConnect(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printFPPConnectUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printFPPConnectUsage(stdout)
		return exitOK
	case "settings":
		return cmdFPPConnectSettings(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl fppconnect: unknown subcommand %q\n\n", sub)
		printFPPConnectUsage(stderr)
		return exitUsage
	}
}

func printFPPConnectUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl fppconnect <subcommand> [flags]

The node's xLights FPP Connect ingestion listener (ADR-044). "settings" is
the fppconnect.settings singleton: whether the listener is enabled, the
per-file byte cap, and the total asset-directory byte cap it enforces.

Subcommands:
  settings get|set|revisions   fppconnect.settings configuration (see
                                "showmeshctl fppconnect settings --help")

Run "showmeshctl fppconnect settings --help" for flags specific to one
subcommand.
`)
}

// --- fppconnect settings ---

func cmdFPPConnectSettings(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printFPPConnectSettingsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printFPPConnectSettingsUsage(stdout)
		return exitOK
	case "get":
		return cmdFPPConnectSettingsGet(rest, stdout, stderr, clock)
	case "set":
		return cmdFPPConnectSettingsSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdFPPConnectSettingsRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl fppconnect settings: unknown subcommand %q\n\n", sub)
		printFPPConnectSettingsUsage(stderr)
		return exitUsage
	}
}

func printFPPConnectSettingsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl fppconnect settings <subcommand> [flags]

Read or write the coordinator's fppconnect.settings configuration
(ADR-044 decision 5): enabled (gates the node's xLights ingestion
listener), maxFileBytes (the per-file byte cap on one ingested upload),
and maxAssetDirBytes (the total byte cap on the node's asset directory;
must be at least maxFileBytes). Defaults: enabled true, maxFileBytes
2147483648 (2 GiB), maxAssetDirBytes 21474836480 (20 GiB). Enabled
defaulting true is a builder default, not an owner ruling.
Every subcommand requires the config:write scope (admin only); there is
no config:read scope.

This never 404s: nothing ever written reports the built-in default with
revision 0 and source "default".

Subcommands:
  get         show the active configuration (or the built-in default)
  set         write a new configuration revision, a FULL REPLACEMENT
              (reads a payload from --file, or from stdin if --file is
              not given); every field is required
  revisions   list revision history, newest first

Run "showmeshctl fppconnect settings <subcommand> --help" for flags
specific to one subcommand.
`)
}

func cmdFPPConnectSettingsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fppconnect settings get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fppconnect settings get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active fppconnect.settings configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fppconnect settings get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fppconnect settings get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppConnectSettingsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/fppconnect.settings", nil, &resp); err != nil {
		return reportError(stderr, "fppconnect settings get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fppconnect settings get", err)
		}
		return exitOK
	}
	printFPPConnectSettingsConfig(stdout, resp)
	return exitOK
}

// cmdFPPConnectSettingsSet implements `fppconnect settings set`: a FULL
// REPLACEMENT, every field of the payload is required. The payload is
// read from --file, or from stdin when --file is not given, and sent to
// the coordinator verbatim as json.RawMessage after only a shape check (a
// JSON object), this command never decodes it into
// configFPPConnectSettingsPayload and re-encodes, because that would
// silently turn an operator's omitted field into an explicit zero value
// the server would then accept, making the server's own
// field_required/field_null refusals unreachable through this
// emergency-path client. Matches cmdAudioSettingsSet's identical
// pass-through precedent one config kind over.
func cmdFPPConnectSettingsSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fppconnect settings set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON file matching configFPPConnectSettingsPayload; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fppconnect settings set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new fppconnect.settings configuration revision (requires")
		_, _ = fmt.Fprintln(stderr, "config:write, admin only). A FULL REPLACEMENT: every field is required;")
		_, _ = fmt.Fprintln(stderr, "an absent field is refused by name, never silently defaulted or carried")
		_, _ = fmt.Fprintln(stderr, "forward from the previous revision.")
		_, _ = fmt.Fprintln(stderr, "Validated before activation: an invalid payload is rejected and appends no")
		_, _ = fmt.Fprintln(stderr, "revision (ADR-009).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fppconnect settings set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "fppconnect settings set", newCLIError(exitUsage, "%v", err))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return reportError(stderr, "fppconnect settings set", newCLIError(exitUsage, "payload must be a JSON object matching configFPPConnectSettingsPayload: %v", err))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fppconnect settings set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppConnectSettingsConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/fppconnect.settings", json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, "fppconnect settings set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fppconnect settings set", err)
		}
		return exitOK
	}
	printFPPConnectSettingsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl fppconnect settings set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

func cmdFPPConnectSettingsRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fppconnect settings revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fppconnect settings revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList fppconnect.settings revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/fppconnect.settings/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fppconnect settings revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fppconnect settings revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/fppconnect.settings/revisions", nil, &resp); err != nil {
		return reportError(stderr, "fppconnect settings revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fppconnect settings revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func printFPPConnectSettingsConfig(w io.Writer, resp fppConnectSettingsConfigResponse) {
	by := "(built-in default; no revision has ever been written)"
	if resp.CreatedByPrincipalName != nil {
		by = "by " + *resp.CreatedByPrincipalName
	}
	_, _ = fmt.Fprintf(w, "fppconnect.settings revision %d (source %s, %s):\n", resp.Revision, resp.Source, by)
	_, _ = fmt.Fprintf(w, "  enabled:          %v\n", resp.Payload.Enabled)
	_, _ = fmt.Fprintf(w, "  maxFileBytes:     %d\n", resp.Payload.MaxFileBytes)
	_, _ = fmt.Fprintf(w, "  maxAssetDirBytes: %d\n", resp.Payload.MaxAssetDirBytes)
}
