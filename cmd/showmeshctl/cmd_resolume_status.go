package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is Track D seam E's own showmeshctl surface: `resolume status`,
// over GET /resolume/instances and GET /resolume/instances/{instanceId}.
// ADR-030: the CLI is the "the show is broken and the UI is down" path, and
// this seam is the one that would otherwise skip it because the Operator UI
// surface (D-4) comes later. This is a read: it mints no new exit code.

// cmdResolumeStatus implements "showmeshctl resolume status [instance-id]",
// mirroring cmdFPP's list-or-one shape.
func cmdResolumeStatus(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume status", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume status [flags] [instance-id]")
		_, _ = fmt.Fprintln(stderr, "\nShow the configured Resolume instance (GET /resolume/instances), or one")
		_, _ = fmt.Fprintln(stderr, "instance in detail if instance-id is given (GET /resolume/instances/{id}).")
		_, _ = fmt.Fprintln(stderr, "Prints the instance id, health, loaded composition, and every observation")
		_, _ = fmt.Fprintln(stderr, "with its state, reason, and age. An unconfigured coordinator prints a")
		_, _ = fmt.Fprintln(stderr, "plain statement and exits 0 — that is a fact about the deployment, not")
		_, _ = fmt.Fprintln(stderr, "an error.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume status", err)
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume status", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	if len(rest) == 1 {
		return cmdResolumeStatusOne(ctx, c, rest[0], g, stdout, stderr, clock)
	}
	return cmdResolumeStatusList(ctx, c, g, stdout, stderr, clock)
}

func cmdResolumeStatusList(ctx context.Context, c *client, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) int {
	var resp resolumeInstancesResponse
	if err := c.getJSON(ctx, "/api/v1/resolume/instances", nil, &resp); err != nil {
		return reportError(stderr, "resolume status", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume status", err)
		}
		return exitOK
	}
	if len(resp.Instances) == 0 {
		_, _ = fmt.Fprintln(stdout, "No Resolume instance is configured on this coordinator (SHOWMESH_RESOLUME_URL is unset).")
		return exitOK
	}
	printResolumeInstancesTable(stdout, resp)
	return exitOK
}

func cmdResolumeStatusOne(ctx context.Context, c *client, instanceID string, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) int {
	body, err := c.getRaw(ctx, "/api/v1/resolume/instances/"+url.PathEscape(instanceID), nil)
	if err != nil {
		return reportError(stderr, "resolume status", err)
	}

	inst, serverTime, err := decodeSingleResolumeInstance(body)
	if err != nil {
		return reportError(stderr, "resolume status", err)
	}
	printClockSkew(stderr, serverTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, inst); err != nil {
			return reportError(stderr, "resolume status", err)
		}
		return exitOK
	}
	printResolumeInstancesTable(stdout, resolumeInstancesResponse{ServerTime: serverTime, Instances: []resolumeInstance{inst}})
	return exitOK
}

// decodeSingleResolumeInstance decodes the body of GET
// /resolume/instances/{instanceId} against the pinned wrapped shape
// ({"serverTime":…, "instance": {…}}) — and ONLY that shape, matching
// decodeSingleFPPInstance's identical posture one file over.
func decodeSingleResolumeInstance(body []byte) (inst resolumeInstance, serverTime time.Time, err error) {
	var wrapped resolumeInstanceResponse
	if jsonErr := json.Unmarshal(body, &wrapped); jsonErr != nil {
		return resolumeInstance{}, time.Time{}, newCLIError(exitAPIError,
			"decoding resolume instance response as {\"serverTime\":..., \"instance\":...}: %v", jsonErr)
	}
	if wrapped.ServerTime.IsZero() {
		return resolumeInstance{}, time.Time{}, newCLIError(exitAPIError,
			"resolume instance response is missing serverTime, violating contract section 6.2 (every response body must carry it)")
	}
	if wrapped.Instance.InstanceID == "" {
		return resolumeInstance{}, time.Time{}, newCLIError(exitAPIError,
			"resolume instance response's \"instance\" object is missing instanceId")
	}
	return wrapped.Instance, wrapped.ServerTime, nil
}
