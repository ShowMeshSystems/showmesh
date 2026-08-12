package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file implements BUILD-PLAN Step 7 seam B's three showmeshctl
// subcommands: `discover` (POST /api/v1/discovery/runs), `declare` (POST
// /api/v1/nodes/{nodeId}/declaration), and `undeclare` (DELETE
// /api/v1/nodes/{nodeId}/declaration). These are the first WRITE
// subcommands this program has ever had — doc.go's own claim ("there is no
// write or command subcommand, matching the API it talks to") is now
// narrower than it once was; see this task's report.
//
// Every write below is a real HTTP POST/DELETE, gated by config:write on
// the coordinator side (ADR-024 decision 4) exactly like any other client:
// this program holds no special privilege, and a --token minted for a
// principal without config:write gets the identical 403 a curl call would.

func cmdDiscover(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl discover", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl discover [flags]")
		_, _ = fmt.Fprintln(stderr, "\nRun a discovery pass (POST /api/v1/discovery/runs) and print what it found.")
		_, _ = fmt.Fprintln(stderr, "\nRequires config:write (ADR-024 decision 4). A discovery run reads what this")
		_, _ = fmt.Fprintln(stderr, "coordinator already observes — agent hellos already in inventory, and")
		_, _ = fmt.Fprintln(stderr, "configured FPP instances — and proposes what is not currently declared. It")
		_, _ = fmt.Fprintln(stderr, "performs NO active probing (no mDNS, no subnet sweep, no MultiSync discover")
		_, _ = fmt.Fprintln(stderr, "ping) and cannot find equipment that has never talked to ShowMesh. It never")
		_, _ = fmt.Fprintln(stderr, "creates, modifies, or deletes a declaration by itself (RES-008 D2/D6) — use")
		_, _ = fmt.Fprintln(stderr, "`showmeshctl declare` to promote a proposal.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "discover", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "discover", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp discoveryRunResponse
	if err := c.postJSON(ctx, "/api/v1/discovery/runs", nil, &resp); err != nil {
		return reportError(stderr, "discover", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "discover", err)
		}
		return exitOK
	}
	printDiscoveryRunResult(stdout, resp)
	return exitOK
}

func cmdDeclare(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl declare", stderr)
	var label, notes string
	fs.StringVar(&label, "label", "", "label to set on the declaration")
	fs.StringVar(&notes, "notes", "", "notes to set on the declaration")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl declare [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nPromote a node to declared, or update its label/notes")
		_, _ = fmt.Fprintln(stderr, "(POST /api/v1/nodes/{nodeId}/declaration). Requires config:write (ADR-024")
		_, _ = fmt.Fprintln(stderr, "decision 4). Idempotent: declaring an already-declared node updates its")
		_, _ = fmt.Fprintln(stderr, "label/notes without disturbing who first declared it or when.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "declare", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "declare", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nodeDeclarationResponse
	body := declareNodeRequest{Label: label, Notes: notes}
	if err := c.postJSON(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID)+"/declaration", body, &resp); err != nil {
		return reportError(stderr, "declare", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "declare", err)
		}
		return exitOK
	}
	printNodeDeclarationResult(stdout, resp)
	return exitOK
}

func cmdUndeclare(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl undeclare", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms removal of this node's declaration")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl undeclare --confirm <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nRemove a node's declaration (DELETE /api/v1/nodes/{nodeId}/declaration).")
		_, _ = fmt.Fprintln(stderr, "Requires config:write (ADR-024 decision 4) and --confirm — a mis-issued")
		_, _ = fmt.Fprintln(stderr, "call cannot quietly remove inventory. This is the ONLY path that removes a")
		_, _ = fmt.Fprintln(stderr, "declaration: a discovery run never deletes one (RES-008 D6).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "undeclare", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl undeclare: refusing to remove the declaration for "+nodeID+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "undeclare", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := deleteNodeDeclarationRequest{Confirm: true}
	if err := c.deleteJSON(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID)+"/declaration", body, nil); err != nil {
		return reportError(stderr, "undeclare", err)
	}

	_, _ = fmt.Fprintf(stdout, "declaration removed for %s\n", nodeID)
	return exitOK
}
