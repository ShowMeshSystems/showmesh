package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

func cmdNodes(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl nodes", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl nodes [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList the node inventory (GET /api/v1/nodes).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "nodes", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "nodes", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nodesResponse
	raw, err := c.getJSONKeepingRaw(ctx, "/api/v1/nodes", nil, &resp)
	if err != nil {
		return reportError(stderr, "nodes", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSONBody(stdout, raw); err != nil {
			return reportError(stderr, "nodes", err)
		}
		return exitOK
	}
	printNodesTable(stdout, resp)
	return exitOK
}

func cmdNode(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one node in detail (GET /api/v1/nodes/{nodeId}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body, err := c.getRaw(ctx, "/api/v1/nodes/"+url.PathEscape(nodeID), nil)
	if err != nil {
		return reportError(stderr, "node", err)
	}

	n, serverTime, err := decodeSingleNode(body)
	if err != nil {
		return reportError(stderr, "node", err)
	}
	printClockSkew(stderr, serverTime, clock())

	// --output json prints the response body AS THE COORDINATOR SENT IT
	// (the owner ruling this package now follows): the wrapped
	// {"serverTime":..., "node":{...}} shape contract section 6.10 pins,
	// not the unwrapped node object this used to print. A script that
	// read .id from this command's JSON output must now read .node.id --
	// this is a deliberate, breaking re-nesting for existing scripted
	// consumers, not merely new fields appearing.
	if g.output == outputJSON {
		if err := printJSONBody(stdout, body); err != nil {
			return reportError(stderr, "node", err)
		}
		return exitOK
	}
	printNodeDetail(stdout, n, serverTime)
	return exitOK
}

// decodeSingleNode decodes the body of GET /api/v1/nodes/{id} against the
// contract §6.10-pinned wrapped shape ({"serverTime":…, "node": {…}}) —
// and ONLY that shape. Step 3's wiring pass fixed the API side to always
// wrap it (contract §6.2's "every response body carries serverTime" has no
// exception), so this decoder no longer tolerates a bare, serverTime-less
// object as a fallback: a client that quietly tolerated a server contract
// violation is how the contract stops being a contract, because the
// violation becomes invisible and the next one is easier. This CLI's job
// is to notice a violation loudly, not paper over it — see doc.go.
func decodeSingleNode(body []byte) (n node, serverTime time.Time, err error) {
	var wrapped nodeResponse
	if jsonErr := json.Unmarshal(body, &wrapped); jsonErr != nil {
		return node{}, time.Time{}, newCLIError(exitAPIError,
			"decoding node response as {\"serverTime\":..., \"node\":...}: %v", jsonErr)
	}
	if wrapped.ServerTime.IsZero() {
		return node{}, time.Time{}, newCLIError(exitAPIError,
			"node response is missing serverTime, violating contract section 6.2 (every response body must carry it)")
	}
	if wrapped.Node.NodeID == "" {
		return node{}, time.Time{}, newCLIError(exitAPIError,
			"node response's \"node\" object is missing nodeId")
	}
	return wrapped.Node, wrapped.ServerTime, nil
}
