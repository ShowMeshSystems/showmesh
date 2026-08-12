package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

func cmdFPP(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	// "stop-playlist <instance-id>" (Step 7 seam C) is dispatched before
	// this subcommand's own flag set ever parses args: it has its own
	// flag set (cmdFPPStopPlaylist), the same way "showmeshctl session"
	// and "showmeshctl audit" are top-level subcommands with their own
	// flags rather than flags of some other command. It lives under
	// "fpp" — not as a fourth top-level verb — because it is FPP-specific
	// in the same way "fpp [id]" already is, and because BUILD-PLAN's own
	// framing names it that way: "showmeshctl fpp stop-playlist
	// <instanceId>".
	if len(args) > 0 && args[0] == "stop-playlist" {
		return cmdFPPStopPlaylist(args[1:], stdout, stderr, clock)
	}

	fs, g := newFlagSet("showmeshctl fpp", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp [flags] [instance-id]")
		_, _ = fmt.Fprintln(stderr, "       showmeshctl fpp stop-playlist [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nList configured FPP instances (GET /api/v1/fpp), or show one instance")
		_, _ = fmt.Fprintln(stderr, "in detail if instance-id is given (GET /api/v1/fpp/{instanceId}).")
		_, _ = fmt.Fprintln(stderr, "\nRun \"showmeshctl fpp stop-playlist --help\" for the write subcommand.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp", err)
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	if len(rest) == 1 {
		return cmdFPPOne(ctx, c, rest[0], g, stdout, stderr, clock)
	}
	return cmdFPPList(ctx, c, g, stdout, stderr, clock)
}

func cmdFPPList(ctx context.Context, c *client, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) int {
	var resp fppResponse
	if err := c.getJSON(ctx, "/api/v1/fpp", nil, &resp); err != nil {
		return reportError(stderr, "fpp", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp", err)
		}
		return exitOK
	}
	printFPPTable(stdout, resp)
	return exitOK
}

func cmdFPPOne(ctx context.Context, c *client, instanceID string, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) int {
	body, err := c.getRaw(ctx, "/api/v1/fpp/"+url.PathEscape(instanceID), nil)
	if err != nil {
		return reportError(stderr, "fpp", err)
	}

	inst, serverTime, err := decodeSingleFPPInstance(body)
	if err != nil {
		return reportError(stderr, "fpp", err)
	}
	printClockSkew(stderr, serverTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, inst); err != nil {
			return reportError(stderr, "fpp", err)
		}
		return exitOK
	}
	printFPPTable(stdout, fppResponse{ServerTime: serverTime, Instances: []fppInstance{inst}})
	return exitOK
}

// decodeSingleFPPInstance decodes the body of GET /api/v1/fpp/{instanceId}
// against the contract §6.10-pinned wrapped shape ({"serverTime":…,
// "instance": {…}}) — and ONLY that shape. See [decodeSingleNode]'s doc
// comment: the same contract-violation tolerance was removed here for the
// same reason, once the API side was fixed to always wrap it.
func decodeSingleFPPInstance(body []byte) (inst fppInstance, serverTime time.Time, err error) {
	var wrapped fppInstanceResponse
	if jsonErr := json.Unmarshal(body, &wrapped); jsonErr != nil {
		return fppInstance{}, time.Time{}, newCLIError(exitAPIError,
			"decoding fpp instance response as {\"serverTime\":..., \"instance\":...}: %v", jsonErr)
	}
	if wrapped.ServerTime.IsZero() {
		return fppInstance{}, time.Time{}, newCLIError(exitAPIError,
			"fpp instance response is missing serverTime, violating contract section 6.2 (every response body must carry it)")
	}
	if wrapped.Instance.InstanceID == "" {
		return fppInstance{}, time.Time{}, newCLIError(exitAPIError,
			"fpp instance response's \"instance\" object is missing instanceId")
	}
	return wrapped.Instance, wrapped.ServerTime, nil
}
