package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

// This file is Track G seam G-2's showmeshctl surface (ADR-039):
// `resolume instance list|set|remove`, over GET/PUT
// /api/v1/config/resolume.instances and GET
// /api/v1/config/resolume.instances/revisions. Deliberately its own
// top-level subcommand tree under "resolume instance", not folded into
// `showmeshctl config` — that command's help text, payload shapes, and
// `409` behaviour are all fpp.endpoints-specific (cmd_config.go's own top
// comment), and this kind's payload, remedy text, and limit (at most one
// instance) all differ.
//
// At most one instance is accepted today (validated server-side —
// config.ValidateResolumeInstances), so "set" takes one (id, url) pair
// rather than a file of many, and "remove" takes none: it always PUTs an
// empty list, which is the only way to deliberately configure zero
// instances (mirroring `config set`'s own "an absent/null endpoints key is
// refused, an empty array is accepted" rule for its own kind).

// resolumeInstanceConfig is one element of resolumeInstancesPayload.instances
// (Track G seam G-2): the same (id, url) pair SHOWMESH_RESOLUME_URL/
// SHOWMESH_RESOLUME_ID carried.
type resolumeInstanceConfig struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// resolumeInstancesPayload is the "resolume.instances" configuration
// kind's payload: the body PUT /api/v1/config/resolume.instances accepts,
// and the "payload" member of GET /api/v1/config/resolume.instances'
// response.
type resolumeInstancesPayload struct {
	Instances []resolumeInstanceConfig `json:"instances"`
}

// resolumeInstancesConfigResponse is the body of GET and PUT
// /api/v1/config/resolume.instances, mirroring fppEndpointsConfigResponse's
// shape exactly.
type resolumeInstancesConfigResponse struct {
	ServerTime             time.Time                `json:"serverTime"`
	Kind                   string                   `json:"kind"`
	Revision               int64                    `json:"revision"`
	Payload                resolumeInstancesPayload `json:"payload"`
	UpdatedAt              time.Time                `json:"updatedAt"`
	CreatedByPrincipalID   *string                  `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                  `json:"createdByPrincipalName"`
	Source                 string                   `json:"source"`
	RestartRequired        bool                     `json:"restartRequired"`
	RestartRequiredReason  string                   `json:"restartRequiredReason"`
}

// cmdResolumeInstance implements "showmeshctl resolume instance".
func cmdResolumeInstance(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printResolumeInstanceUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printResolumeInstanceUsage(stdout)
		return exitOK
	case "list":
		return cmdResolumeInstanceList(rest, stdout, stderr, clock)
	case "set":
		return cmdResolumeInstanceSet(rest, stdout, stderr, clock)
	case "remove":
		return cmdResolumeInstanceRemove(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl resolume instance: unknown subcommand %q\n\n", sub)
		printResolumeInstanceUsage(stderr)
		return exitUsage
	}
}

func printResolumeInstanceUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl resolume instance <subcommand> [flags]

Read or write the coordinator's resolume.instances configuration (Track G
seam G-2, ADR-039): which Resolume Arena instance this coordinator
connects to, moved out of SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID into
the coordinator's authoritative store. Every subcommand requires the
config:write scope (admin only) — there is no config:read scope; reading
this surface is exactly as sensitive as writing it.

Subcommands:
  list      show the active configuration
  set       write a new configuration revision naming exactly one instance
            (--id, --url); at most one instance is supported today
  remove    write a new configuration revision naming zero instances

A configuration change here takes effect without a restart (ADR-036): the
Resolume collector set follows within about ten seconds. "resolume
instance set" and "resolume instance list" both print this fact.

While SHOWMESH_RESOLUME_URL is still set in the coordinator's own
environment, "set" and "remove" are both refused outright (409): remove
SHOWMESH_RESOLUME_URL and SHOWMESH_RESOLUME_ID from the coordinator's
environment and restart the coordinator once, then retry.

Run "showmeshctl resolume instance <subcommand> --help" for flags specific
to one subcommand.
`)
}

// cmdResolumeInstanceList implements `showmeshctl resolume instance list`
// (GET /api/v1/config/resolume.instances).
func cmdResolumeInstanceList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume instance list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume instance list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active resolume.instances configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume instance list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume instance list", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp resolumeInstancesConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/resolume.instances", nil, &resp); err != nil {
		return reportError(stderr, "resolume instance list", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume instance list", err)
		}
		return exitOK
	}
	printResolumeInstancesConfig(stdout, resp)
	return exitOK
}

// cmdResolumeInstanceSet implements `showmeshctl resolume instance set`
// (PUT /api/v1/config/resolume.instances with exactly one instance).
func cmdResolumeInstanceSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume instance set", stderr)
	var id, url string
	fs.StringVar(&id, "id", "", "the instance id (required)")
	fs.StringVar(&url, "url", "", "the Resolume REST base URL, e.g. http://host:8080 (required)")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume instance set --id ID --url URL [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new resolume.instances configuration revision naming exactly")
		_, _ = fmt.Fprintln(stderr, "one instance (requires config:write, admin only). Validated before")
		_, _ = fmt.Fprintln(stderr, "activation: an invalid payload, or one colliding with a configured")
		_, _ = fmt.Fprintln(stderr, "fpp.endpoints id, is rejected and appends no revision (ADR-009).")
		_, _ = fmt.Fprintln(stderr, "\nThis takes effect without a restart (ADR-036): the collector set")
		_, _ = fmt.Fprintln(stderr, "follows within about ten seconds.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "configuration changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume instance set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	if id == "" || url == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl resolume instance set: --id and --url are both required")
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume instance set", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const apiPath = "/api/v1/config/resolume.instances"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r resolumeInstancesConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "resolume instance set", err)
	}

	payload := resolumeInstancesPayload{Instances: []resolumeInstanceConfig{{ID: id, URL: url}}}
	var resp resolumeInstancesConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, payload, &resp); err != nil {
		return reportError(stderr, "resolume instance set", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume instance set", err)
		}
		return exitOK
	}
	printResolumeInstancesConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl resolume instance set: revision %d is now active. %s\n", resp.Revision, resp.RestartRequiredReason)
	return exitOK
}

// cmdResolumeInstanceRemove implements `showmeshctl resolume instance
// remove` (PUT /api/v1/config/resolume.instances with an empty list —
// the only way to deliberately configure zero instances; an absent or
// null "instances" key is refused server-side, never treated as "leave it
// alone" or "wipe implicitly").
func cmdResolumeInstanceRemove(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume instance remove", stderr)
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume instance remove [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new resolume.instances configuration revision naming zero")
		_, _ = fmt.Fprintln(stderr, "instances (requires config:write, admin only).")
		_, _ = fmt.Fprintln(stderr, "\nThis takes effect without a restart (ADR-036): the collector set")
		_, _ = fmt.Fprintln(stderr, "stops within about ten seconds.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "configuration changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume instance remove", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume instance remove", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const apiPath = "/api/v1/config/resolume.instances"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r resolumeInstancesConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "resolume instance remove", err)
	}

	payload := resolumeInstancesPayload{Instances: []resolumeInstanceConfig{}}
	var resp resolumeInstancesConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, payload, &resp); err != nil {
		return reportError(stderr, "resolume instance remove", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume instance remove", err)
		}
		return exitOK
	}
	printResolumeInstancesConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl resolume instance remove: revision %d is now active. %s\n", resp.Revision, resp.RestartRequiredReason)
	return exitOK
}
