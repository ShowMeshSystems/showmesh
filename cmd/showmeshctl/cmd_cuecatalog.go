package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is TRACK-H-H3-SPEC.md section 4/8's own showmeshctl surface:
// GET /api/v1/nodes/{nodeId}/cue-catalog and POST
// /api/v1/nodes/{nodeId}/cue-catalog/acknowledge. Both verbs mirror
// cmd_assets.go's "assets manifest --node" precedent's shape: a node id
// positional argument, a --json escape hatch, and a clock-skew line on
// stderr before the table.

// --- wire mirrors (mirrors internal/coordinator/api/v1.CueCatalog*) ---

type cueCatalogRenderOutputRecord struct {
	Sequence    string   `json:"sequence"`
	AssetHashes []string `json:"assetHashes"`
}

type cueCatalogAudioOutputRecord struct {
	Asset             string   `json:"asset"`
	StartOffsetMillis int      `json:"startOffsetMillis"`
	AssetHashes       []string `json:"assetHashes"`
}

type cueCatalogLTCOutputRecord struct {
	StartOffsetMillis int `json:"startOffsetMillis"`
}

type cueCatalogAnnouncementOutputRecord struct {
	Policy     string   `json:"policy"`
	DuckGainDb *float64 `json:"duckGainDb,omitempty"`
	FadeMillis int      `json:"fadeMillis"`
}

type cueCatalogOutputsRecord struct {
	Render       *cueCatalogRenderOutputRecord       `json:"render,omitempty"`
	Audio        *cueCatalogAudioOutputRecord        `json:"audio,omitempty"`
	LTC          *cueCatalogLTCOutputRecord          `json:"ltc,omitempty"`
	Announcement *cueCatalogAnnouncementOutputRecord `json:"announcement,omitempty"`
}

type cueCatalogEntryRecord struct {
	CueID       string                  `json:"cueId"`
	CueRevision int64                   `json:"cueRevision"`
	Outputs     cueCatalogOutputsRecord `json:"outputs"`
}

type cueCatalogResponse struct {
	ServerTime time.Time               `json:"serverTime"`
	Node       string                  `json:"node"`
	Configured bool                    `json:"configured"`
	Show       string                  `json:"show,omitempty"`
	Generation *int64                  `json:"generation,omitempty"`
	Revision   string                  `json:"revision,omitempty"`
	Entries    []cueCatalogEntryRecord `json:"entries"`

	AcknowledgedStatus   string     `json:"acknowledgedStatus"`
	AcknowledgedRevision string     `json:"acknowledgedRevision,omitempty"`
	AcknowledgedAt       *time.Time `json:"acknowledgedAt,omitempty"`
}

type cueCatalogAcknowledgeRequest struct {
	Revision   string `json:"revision"`
	Show       string `json:"show"`
	Generation int64  `json:"generation"`
}

type cueCatalogAcknowledgeResponse struct {
	ServerTime           time.Time `json:"serverTime"`
	Node                 string    `json:"node"`
	Configured           bool      `json:"configured"`
	Status               string    `json:"status"`
	AcknowledgedRevision string    `json:"acknowledgedRevision"`
	CurrentRevision      string    `json:"currentRevision,omitempty"`
	AcknowledgedAt       time.Time `json:"acknowledgedAt"`
}

type cueCatalogDeployRequest struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

type cueCatalogDeployResult struct {
	CommandID            string     `json:"commandId"`
	IdempotencyKey       string     `json:"idempotencyKey"`
	Node                 string     `json:"node"`
	Replay               bool       `json:"replay"`
	Show                 string     `json:"show"`
	Generation           int64      `json:"generation"`
	Revision             string     `json:"revision"`
	Outcome              string     `json:"outcome"`
	Reason               string     `json:"reason,omitempty"`
	AcknowledgedRevision string     `json:"acknowledgedRevision,omitempty"`
	DispatchedAt         time.Time  `json:"dispatchedAt"`
	ResolvedAt           *time.Time `json:"resolvedAt,omitempty"`
}

type cueCatalogDeployResponse struct {
	ServerTime time.Time              `json:"serverTime"`
	Command    cueCatalogDeployResult `json:"command"`
}

// --- dispatch ---

func cmdCueCatalog(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printCueCatalogUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printCueCatalogUsage(stdout)
		return exitOK
	case "get":
		return cmdCueCatalogGet(rest, stdout, stderr, clock)
	case "acknowledge":
		return cmdCueCatalogAcknowledge(rest, stdout, stderr, clock)
	case "deploy":
		return cmdCueCatalogDeploy(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl cuecatalog: unknown subcommand %q\n\n", sub)
		printCueCatalogUsage(stderr)
		return exitUsage
	}
}

func printCueCatalogUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl cuecatalog <subcommand> [flags]

Track H seam H3: a node's resolved Cue catalog and its acknowledgement.

Subcommands:
  get <nodeId>                          GET /api/v1/nodes/<nodeId>/cue-catalog
  acknowledge <nodeId> <revision>       POST .../cue-catalog/acknowledge (write)
  deploy <nodeId>                       POST .../cue-catalog/deploy (write)

Run "showmeshctl cuecatalog <subcommand> -h" for that subcommand's own flags.
`)
}

// --- get ---

func cmdCueCatalogGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cuecatalog get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cuecatalog get <nodeId> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nFetch one node's resolved Cue catalog (GET /api/v1/nodes/{nodeId}/cue-catalog).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cuecatalog get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cuecatalog get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp cueCatalogResponse
	if err := c.getJSON(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID)+"/cue-catalog", nil, &resp); err != nil {
		return reportError(stderr, "cuecatalog get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cuecatalog get", err)
		}
		return exitOK
	}
	printCueCatalogResponse(stdout, resp)
	return exitOK
}

func printCueCatalogResponse(w io.Writer, resp cueCatalogResponse) {
	if !resp.Configured {
		_, _ = fmt.Fprintf(w, "node %s: no active show is configured; this catalog authorizes nothing\n", resp.Node)
		return
	}
	generation := int64(0)
	if resp.Generation != nil {
		generation = *resp.Generation
	}
	_, _ = fmt.Fprintf(w, "node %s: show=%s generation=%d revision=%s entries=%d\n",
		resp.Node, resp.Show, generation, resp.Revision, len(resp.Entries))
	_, _ = fmt.Fprintf(w, "  acknowledged: status=%s revision=%s at=%s\n",
		resp.AcknowledgedStatus, emptyOrDash(resp.AcknowledgedRevision), timeOrDash(resp.AcknowledgedAt))
	for _, e := range resp.Entries {
		_, _ = fmt.Fprintf(w, "  cue %s (revision %d)\n", e.CueID, e.CueRevision)
		if e.Outputs.Render != nil {
			_, _ = fmt.Fprintf(w, "    render: sequence=%s assets=%d\n", e.Outputs.Render.Sequence, len(e.Outputs.Render.AssetHashes))
		}
		if e.Outputs.Audio != nil {
			_, _ = fmt.Fprintf(w, "    audio: asset=%s startOffsetMillis=%d assets=%d\n",
				e.Outputs.Audio.Asset, e.Outputs.Audio.StartOffsetMillis, len(e.Outputs.Audio.AssetHashes))
		}
		if e.Outputs.LTC != nil {
			_, _ = fmt.Fprintf(w, "    ltc: startOffsetMillis=%d\n", e.Outputs.LTC.StartOffsetMillis)
		}
		if e.Outputs.Announcement != nil {
			_, _ = fmt.Fprintf(w, "    announcement: policy=%s fadeMillis=%d\n", e.Outputs.Announcement.Policy, e.Outputs.Announcement.FadeMillis)
		}
	}
}

// --- acknowledge ---

func cmdCueCatalogAcknowledge(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cuecatalog acknowledge", stderr)
	var show string
	var generation int64
	fs.StringVar(&show, "show", "", "the show id this revision was resolved from (required)")
	fs.Int64Var(&generation, "generation", 0, "the generation this revision was resolved from (required, > 0)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cuecatalog acknowledge <nodeId> <revision> -show <show> -generation <n> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nReport which Cue catalog revision this node now holds")
		_, _ = fmt.Fprintln(stderr, "(POST /api/v1/nodes/{nodeId}/cue-catalog/acknowledge). This is a write:")
		_, _ = fmt.Fprintln(stderr, "requires the node:observe scope. Acknowledging a catalog is NOT readiness;")
		_, _ = fmt.Fprintln(stderr, "asset presence stays \"showmeshctl assets manifest\"'s own answer.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cuecatalog acknowledge", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	nodeID, revision := rest[0], rest[1]
	if show == "" || generation <= 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl cuecatalog acknowledge: -show and -generation (> 0) are both required")
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cuecatalog acknowledge", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	req := cueCatalogAcknowledgeRequest{Revision: revision, Show: show, Generation: generation}
	var resp cueCatalogAcknowledgeResponse
	if err := c.postJSON(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID)+"/cue-catalog/acknowledge", req, &resp); err != nil {
		return reportError(stderr, "cuecatalog acknowledge", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cuecatalog acknowledge", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "node %s: %s (acknowledged=%s current=%s)\n",
		resp.Node, resp.Status, resp.AcknowledgedRevision, resp.CurrentRevision)
	return exitOK
}

// --- deploy ---

func cmdCueCatalogDeploy(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl cuecatalog deploy", stderr)
	var idempotencyKey string
	fs.StringVar(&idempotencyKey, "idempotency-key", "", "reuse a prior request's key to replay its result instead of dispatching again")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl cuecatalog deploy <nodeId> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nResolve this coordinator's own current Cue catalog for <nodeId> and push it")
		_, _ = fmt.Fprintln(stderr, "to the node (POST /api/v1/nodes/{nodeId}/cue-catalog/deploy). This is a")
		_, _ = fmt.Fprintln(stderr, "write: requires the cuecatalog:deploy scope (admin only). On a")
		_, _ = fmt.Fprintln(stderr, "confirmed outcome, the revision the node reports holding is also")
		_, _ = fmt.Fprintln(stderr, "recorded as its acknowledgement.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "cuecatalog deploy", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "cuecatalog deploy", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	req := cueCatalogDeployRequest{IdempotencyKey: idempotencyKey}
	var resp cueCatalogDeployResponse
	if err := c.postJSON(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID)+"/cue-catalog/deploy", req, &resp); err != nil {
		return reportError(stderr, "cuecatalog deploy", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "cuecatalog deploy", err)
		}
		return exitOK
	}
	cmd := resp.Command
	_, _ = fmt.Fprintf(stdout, "node %s: %s show=%s generation=%d revision=%s", cmd.Node, cmd.Outcome, cmd.Show, cmd.Generation, cmd.Revision)
	if cmd.Reason != "" {
		_, _ = fmt.Fprintf(stdout, " reason=%q", cmd.Reason)
	}
	if cmd.AcknowledgedRevision != "" {
		_, _ = fmt.Fprintf(stdout, " acknowledgedRevision=%s", cmd.AcknowledgedRevision)
	}
	if cmd.Replay {
		_, _ = fmt.Fprint(stdout, " (replay)")
	}
	_, _ = fmt.Fprintln(stdout)
	return exitOK
}
