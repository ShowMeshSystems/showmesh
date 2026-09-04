package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"
)

// This file is Track G seam G-4's showmeshctl surface (ADR-039):
// `assets settings get|set`, over GET/PUT /api/v1/config/assets.settings
// and GET /api/v1/config/assets.settings/revisions — the four
// SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/
// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL variables
// promoted to store-backed configuration. SHOWMESH_ASSET_DIR has no flag
// here — it stays environment-only (ADR-039 decision 2).
//
// Unlike "resolume instance set"'s whole-object replace, "assets settings
// set" sends ONLY the flags the operator actually passed: each of the four
// flags is independently optional, and an unset flag means "leave the
// stored value alone" on the server (ADR-039 decision 5) — mirroring the
// API's own PUT /config/assets.settings partial-update contract exactly,
// via fs.Visit rather than a zero-value check, so "set --max-upload-bytes 0"
// (a deliberate, if rejected, value) is never confused with "not passed".

// assetsSettingsPayload is the "assets.settings" configuration kind's
// payload: every field OPTIONAL on the wire (a pointer, or omitted
// entirely) for a PUT, always present for a GET/PUT response. Built by
// hand into a map for requests (see cmdAssetsSettingsSet) rather than
// through this struct, because encoding/json has no way to omit a
// *string/*int64 field conditionally beyond "omitempty", which would also
// drop an explicit zero — exactly the ambiguity ADR-039 decision 5 forbids.
// SyncIntervalSeconds/InventoryIntervalSeconds are float64, not an
// integer: the coordinator encodes these as seconds.Seconds() so a
// sub-second interval (this program's own showmeshctl assets settings set
// --sync-interval 750ms, or the integration test harness's identical
// SHOWMESH_ASSET_SYNC_INTERVAL) round-trips exactly rather than truncating
// to zero.
type assetsSettingsPayload struct {
	ContentBaseURL           string  `json:"contentBaseUrl"`
	MaxUploadBytes           int64   `json:"maxUploadBytes"`
	SyncIntervalSeconds      float64 `json:"syncIntervalSeconds"`
	InventoryIntervalSeconds float64 `json:"inventoryIntervalSeconds"`
}

// assetsSettingsConfigResponse is the body of GET and PUT
// /api/v1/config/assets.settings, mirroring
// resolumeInstancesConfigResponse's shape exactly.
type assetsSettingsConfigResponse struct {
	ServerTime             time.Time             `json:"serverTime"`
	Kind                   string                `json:"kind"`
	Revision               int64                 `json:"revision"`
	Payload                assetsSettingsPayload `json:"payload"`
	UpdatedAt              time.Time             `json:"updatedAt"`
	CreatedByPrincipalID   *string               `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string               `json:"createdByPrincipalName"`
	Source                 string                `json:"source"`
	RestartRequired        bool                  `json:"restartRequired"`
	RestartRequiredReason  string                `json:"restartRequiredReason"`
}

// cmdAssetsSettings implements "showmeshctl assets settings".
func cmdAssetsSettings(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAssetsSettingsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAssetsSettingsUsage(stdout)
		return exitOK
	case "get":
		return cmdAssetsSettingsGet(rest, stdout, stderr, clock)
	case "set":
		return cmdAssetsSettingsSet(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl assets settings: unknown subcommand %q\n\n", sub)
		printAssetsSettingsUsage(stderr)
		return exitUsage
	}
}

func printAssetsSettingsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl assets settings <subcommand> [flags]

Read or write the coordinator's assets.settings configuration (Track G
seam G-4, ADR-039): the asset store's content base URL, upload byte limit,
and sync/inventory intervals, moved out of SHOWMESH_ASSET_CONTENT_BASE_URL/
SHOWMESH_ASSET_MAX_UPLOAD_BYTES/SHOWMESH_ASSET_SYNC_INTERVAL/
SHOWMESH_ASSET_INVENTORY_INTERVAL into the coordinator's authoritative
store. SHOWMESH_ASSET_DIR is unaffected — it stays environment-only.
Every subcommand requires the config:write scope (admin only) — there is
no config:read scope; reading this surface is exactly as sensitive as
writing it.

Subcommands:
  get   show the active configuration
  set   write a new configuration revision; only the flags you pass are
        changed — an omitted flag leaves the stored value alone

A configuration change here takes effect without a restart (ADR-036): the
live asset sync service follows within about ten seconds. "assets settings
set" and "assets settings get" both print this fact.

While any of the four SHOWMESH_ASSET_* settings variables is still set in
the coordinator's own environment, "set" is refused outright (409): remove
all four (SHOWMESH_ASSET_DIR excepted) and restart the coordinator once,
then retry.

Run "showmeshctl assets settings <subcommand> --help" for flags specific
to one subcommand.
`)
}

func cmdAssetsSettingsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets settings get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets settings get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active assets.settings configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets settings get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "assets settings get", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp assetsSettingsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/assets.settings", nil, &resp); err != nil {
		return reportError(stderr, "assets settings get", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "assets settings get", err)
		}
		return exitOK
	}
	printAssetsSettingsConfig(stdout, resp)
	return exitOK
}

func cmdAssetsSettingsSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets settings set", stderr)
	var contentBaseURL string
	var maxUploadBytes int64
	var syncInterval, inventoryInterval time.Duration
	fs.StringVar(&contentBaseURL, "content-base-url", "", `the base URL nodes fetch asset bytes from, e.g. http://coordinator:8080 (pass "" to disable sync); must be reachable from every node, never a loopback/localhost address`)
	fs.Int64Var(&maxUploadBytes, "max-upload-bytes", 0, "the maximum size of a single asset upload, in bytes")
	fs.DurationVar(&syncInterval, "sync-interval", 0, "how often the sync service recomputes every node's gap, e.g. 5m")
	fs.DurationVar(&inventoryInterval, "inventory-interval", 0, "this coordinator's own copy of the agent's inventory-report cadence, e.g. 2m")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets settings set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new assets.settings configuration revision (requires config:write,")
		_, _ = fmt.Fprintln(stderr, "admin only). Only the flags you pass are changed — an omitted flag leaves")
		_, _ = fmt.Fprintln(stderr, "the stored (or default) value alone. Validated before activation: an")
		_, _ = fmt.Fprintln(stderr, "invalid payload appends no revision (ADR-009).")
		_, _ = fmt.Fprintln(stderr, "\nThis takes effect without a restart (ADR-036): the live asset sync")
		_, _ = fmt.Fprintln(stderr, "service follows within about ten seconds.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "configuration changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets settings set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	// fs.Visit walks only the flags actually passed on THIS invocation —
	// this is what lets an omitted flag mean "leave it alone" while an
	// explicitly passed value (including a zero one, which the server
	// will reject on its own terms) is sent through. Never fs.VisitAll,
	// which would walk every flag regardless of whether it was set.
	body := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "content-base-url":
			body["contentBaseUrl"] = contentBaseURL
		case "max-upload-bytes":
			body["maxUploadBytes"] = maxUploadBytes
		case "sync-interval":
			body["syncIntervalSeconds"] = syncInterval.Seconds()
		case "inventory-interval":
			body["inventoryIntervalSeconds"] = inventoryInterval.Seconds()
		}
	})
	if len(body) == 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl assets settings set: pass at least one of --content-base-url, --max-upload-bytes, --sync-interval, --inventory-interval")
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "assets settings set", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const apiPath = "/api/v1/config/assets.settings"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r assetsSettingsConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "assets settings set", err)
	}

	var resp assetsSettingsConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "assets settings set", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "assets settings set", err)
		}
		return exitOK
	}
	printAssetsSettingsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl assets settings set: revision %d is now active. %s\n", resp.Revision, resp.RestartRequiredReason)
	return exitOK
}
