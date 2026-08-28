package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is showmeshctl's node.clock surface (Track I seam I1):
// "node-clock list|get|set|revisions" over GET/PUT
// /api/v1/config/node.clock[/{id}[/revisions]]. Declares its own wire
// types rather than importing internal/coordinator/api/v1 (the
// import-graph test forbids it), matching cmd_audio.go's identical
// precedent one kind over. showConfigObjectsListResponse and
// configRevisionsResponse (types_macro.go, types.go) are already
// kind-agnostic and reused verbatim rather than declared a third time.

// configNodeClock mirrors v1.ConfigNodeClock.
type configNodeClock struct {
	Provider  string `json:"provider"`
	Interface string `json:"interface"`
	Domain    int    `json:"domain"`

	ClientOnly           bool `json:"clientOnly,omitempty"`
	HoldoverLimitSeconds int  `json:"holdoverLimitSeconds,omitempty"`
	Priority1            int  `json:"priority1,omitempty"`
	HardwareTimestamping bool `json:"hardwareTimestamping,omitempty"`

	ExternalUDSAddress string `json:"externalUdsAddress,omitempty"`
	FPPBaseURL         string `json:"fppBaseUrl,omitempty"`
}

type nodeClockConfigResponse struct {
	ServerTime             time.Time       `json:"serverTime"`
	Kind                   string          `json:"kind"`
	ID                     string          `json:"id"`
	Revision               int64           `json:"revision"`
	Payload                configNodeClock `json:"payload"`
	UpdatedAt              time.Time       `json:"updatedAt"`
	CreatedByPrincipalID   *string         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string         `json:"createdByPrincipalName"`
	Source                 string          `json:"source"`
}

func cmdNodeClock(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printNodeClockUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printNodeClockUsage(stdout)
		return exitOK
	case "list":
		return cmdNodeClockList(rest, stdout, stderr, clock)
	case "get":
		return cmdNodeClockGet(rest, stdout, stderr, clock)
	case "set":
		return cmdNodeClockSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdNodeClockRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl node-clock: unknown subcommand %q\n\n", sub)
		printNodeClockUsage(stderr)
		return exitUsage
	}
}

func printNodeClockUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl node-clock <subcommand> [flags]

Read or write the coordinator's node.clock configuration objects, one per
node (Track I seam I1, RES-019, ADR-039): which PTP provider this node
runs, its interface, declared domain, role policy, and holdover limit.
Reads and writes both require config:write, admin only. A node with no
node.clock object reports "unsynchronized" and behaves exactly as it did
before this seam existed.

"set" is a FULL REPLACEMENT: --provider, --interface, and --domain are
always required. --fpp-base-url is required when --provider is "fpp".

Subcommands:
  list             enumerate node.clock objects (id is the node id)
  get <node-id>    show one node's full clock configuration
  set <node-id>    write a new node.clock revision (write, full
                   replacement)
  revisions <node-id>
                   list revision history, newest first

Run "showmeshctl node-clock <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdNodeClockList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node-clock list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node-clock list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate node.clock objects (GET /api/v1/config/node.clock).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node-clock list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node-clock list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/node.clock", nil, &resp); err != nil {
		return reportError(stderr, "node-clock list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "node-clock list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdNodeClockGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node-clock get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node-clock get [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one node's clock configuration (GET /api/v1/config/node.clock/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node-clock get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node-clock get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nodeClockConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/node.clock/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "node-clock get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "node-clock get", err)
		}
		return exitOK
	}
	printNodeClockDetail(stdout, resp)
	return exitOK
}

func cmdNodeClockSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node-clock set", stderr)
	var provider, iface, externalUDSAddress, fppBaseURL string
	var domain, holdoverLimitSeconds, priority1 int
	var clientOnly, hardwareTimestamping bool
	fs.StringVar(&provider, "provider", "", `which PTP provider this node runs: "managed", "external", or "fpp" (required)`)
	fs.StringVar(&iface, "interface", "", "the network interface this node's clock provider observes (required)")
	fs.IntVar(&domain, "domain", 0, "the declared PTP domain number, 0-255 (required)")
	fs.BoolVar(&clientOnly, "client-only", false, "managed only: this node never attempts to become the domain's own grandmaster")
	fs.IntVar(&holdoverLimitSeconds, "holdover-limit-seconds", 0, "how long a lost lock is reported as holdover before this node reports unsynchronized (default 60)")
	fs.IntVar(&priority1, "priority1", 0, "managed only: overrides the BMCA priority1 (default 248)")
	fs.BoolVar(&hardwareTimestamping, "hardware-timestamping", false, "managed only: request hardware timestamping with a software fallback attempt")
	fs.StringVar(&externalUDSAddress, "external-uds-address", "", "external only: overrides the read-only management socket path (default /var/run/ptp/ptp4lro)")
	fs.StringVar(&fppBaseURL, "fpp-base-url", "", `fpp only: the FPP 10 host's own base URL (required when --provider is "fpp")`)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node-clock set [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new node.clock revision (PUT /api/v1/config/node.clock/{id}).")
		_, _ = fmt.Fprintln(stderr, "Requires config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT: this command never reads the node's current")
		_, _ = fmt.Fprintln(stderr, "definition first. --provider, --interface, and --domain are always required.")
		_, _ = fmt.Fprintln(stderr, "--fpp-base-url is required when --provider is \"fpp\".")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node-clock set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	if provider == "" || iface == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl node-clock set: --provider and --interface are required")
		return exitUsage
	}
	if provider == "fpp" && fppBaseURL == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl node-clock set: --fpp-base-url is required when --provider is \"fpp\"")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node-clock set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configNodeClock{
		Provider: provider, Interface: iface, Domain: domain,
		ClientOnly: clientOnly, HoldoverLimitSeconds: holdoverLimitSeconds,
		Priority1: priority1, HardwareTimestamping: hardwareTimestamping,
		ExternalUDSAddress: externalUDSAddress, FPPBaseURL: fppBaseURL,
	}
	var resp nodeClockConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/node.clock/"+url.PathEscape(id), body, &resp); err != nil {
		return reportError(stderr, "node-clock set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "node-clock set", err)
		}
		return exitOK
	}
	printNodeClockDetail(stdout, resp)
	return exitOK
}

func cmdNodeClockRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node-clock revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node-clock revisions [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nList node.clock revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/node.clock/{id}/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node-clock revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node-clock revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/node.clock/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "node-clock revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "node-clock revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func printNodeClockDetail(w io.Writer, resp nodeClockConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Node ID:                 %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Provider:                %s\n", p.Provider)
	_, _ = fmt.Fprintf(w, "Interface:               %s\n", p.Interface)
	_, _ = fmt.Fprintf(w, "Domain:                  %d\n", p.Domain)
	_, _ = fmt.Fprintf(w, "Client-only:             %v\n", p.ClientOnly)
	_, _ = fmt.Fprintf(w, "Holdover limit (s):      %d\n", p.HoldoverLimitSeconds)
	if p.Provider == "managed" {
		_, _ = fmt.Fprintf(w, "Priority1:               %d\n", p.Priority1)
		_, _ = fmt.Fprintf(w, "Hardware timestamping:   %v\n", p.HardwareTimestamping)
	}
	if p.Provider == "external" {
		_, _ = fmt.Fprintf(w, "External UDS address:    %s\n", p.ExternalUDSAddress)
	}
	if p.Provider == "fpp" {
		_, _ = fmt.Fprintf(w, "FPP base URL:            %s\n", p.FPPBaseURL)
	}
	_, _ = fmt.Fprintf(w, "Revision:                %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:                 %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:              %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintln(w, "Created by:              (unknown)")
	}
	_, _ = fmt.Fprintf(w, "Source:                  %s\n", resp.Source)
}
