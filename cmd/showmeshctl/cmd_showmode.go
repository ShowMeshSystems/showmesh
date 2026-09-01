package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

// This file is showmeshctl's ADR-033 surface: "show mode [get]|set|
// revisions", over GET/PUT /api/v1/config/show.mode and its revisions
// list. ADR-033 decision 3 requires showmeshctl to report the mode, and
// ADR-030/ADR-039 require the CLI to be able to drive anything the UI can.
//
// The read needs only observation:read, unlike every other configuration
// kind this program reads. That is the coordinator's own deliberate
// departure (internal/coordinator/api/showmode.go's header comment): an
// operator who cannot see which mode the installation is in is in the trap
// ADR-033 decision 3 exists to close. The WRITE still needs config:write.

// configShowModePayload mirrors v1.ConfigShowModePayload field for field.
// This program's own independent transcription, never a shared type with
// the coordinator: importgraph_test.go forbids importing any
// internal/coordinator package.
type configShowModePayload struct {
	Mode string `json:"mode"`
}

type showModeConfigResponse struct {
	ServerTime             time.Time             `json:"serverTime"`
	Kind                   string                `json:"kind"`
	Revision               int64                 `json:"revision"`
	Payload                configShowModePayload `json:"payload"`
	UpdatedAt              time.Time             `json:"updatedAt"`
	CreatedByPrincipalID   *string               `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string               `json:"createdByPrincipalName"`
	Source                 string                `json:"source"`

	ResolumeWebSocketEffect string                 `json:"resolumeWebSocketEffect"`
	CueActivationPin        configCueActivationPin `json:"cueActivationPin"`
}

// configCueActivationPin mirrors v1.CueActivationPin field for field, this
// program's own independent transcription (this file's own header comment
// gives the reason: no shared type with the coordinator). Whether a
// show.cue edit saved right now is STAGED, invisible to every node until
// the show restarts, or applies live: an operator working through
// showmeshctl mid-show has no other way to see that, so this is not an
// optional field to drop for a smaller struct.
type configCueActivationPin struct {
	Pinned     bool   `json:"pinned"`
	Show       string `json:"show,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	PinnedAt   string `json:"pinnedAt,omitempty"`
	Effect     string `json:"effect"`
}

// showModeValues is ADR-033's closed vocabulary, checked here so a typo is
// refused with a usage error naming both members rather than travelling to
// the coordinator to come back as a 400. The coordinator validates
// independently; this is a convenience, never the enforcement.
var showModeValues = []string{"program", "show"}

func validShowModeValue(v string) bool {
	for _, m := range showModeValues {
		if v == m {
			return true
		}
	}
	return false
}

// cmdShowMode implements "showmeshctl show mode". A bare "show mode" reads
// the current value, because reading the mode is what an operator does far
// more often than writing it and ADR-033 decision 3 is about that read.
func cmdShowMode(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		return cmdShowModeGet(nil, stdout, stderr, clock)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printShowModeUsage(stdout)
		return exitOK
	case "get":
		return cmdShowModeGet(rest, stdout, stderr, clock)
	case "set":
		return cmdShowModeSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdShowModeRevisions(rest, stdout, stderr, clock)
	default:
		// A bare "show mode --output json" has to keep working, so an
		// argument that starts with a dash is a flag for "get" rather than
		// an unknown subcommand.
		if len(sub) > 0 && sub[0] == '-' {
			return cmdShowModeGet(args, stdout, stderr, clock)
		}
		_, _ = fmt.Fprintf(stderr, "showmeshctl show mode: unknown subcommand %q\n\n", sub)
		printShowModeUsage(stderr)
		return exitUsage
	}
}

func printShowModeUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl show mode [get|set|revisions] [flags]

Read or write the installation-wide operating mode (ADR-033): one value for
the whole installation, "program" or "show". Not per-node, not per-device,
and never a per-subsystem flag.

Reading requires observation:read, which every signed-in role holds. That is
deliberate and it is the one configuration kind read this way: a mode nobody
can see is a trap, because every surface behaves differently and nothing
says why. Writing requires config:write (admin only) and is audited like any
other configuration write.

This never 404s: nothing ever written reports the built-in default
("program") with revision 0 and source "default". A fresh install is by
definition being set up.

The mode changes what the system does, never who may do it, and it gates no
command path: no mode may refuse, delay, or degrade blackout, stop, or
power-off (ADR-033 decision 4).

What reads the mode today: the Resolume WebSocket wake-up channel, held open
in program and closed in show, applied without a coordinator restart in both
directions. Nodes are told the current mode so later work can read it at the
point of decision; a node that has never been told reads "unknown", which
behaves as "show".

Subcommands:
  get         show the active mode (the default when "show mode" is run
              with no subcommand)
  set <mode>  write a new mode revision, where <mode> is program or show
  revisions   list revision history, newest first (requires config:write)
`)
}

func cmdShowModeGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show mode get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show mode [get] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the installation-wide operating mode (requires observation:read).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show mode get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show mode get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showModeConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.mode", nil, &resp); err != nil {
		return reportError(stderr, "show mode get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show mode get", err)
		}
		return exitOK
	}
	printShowModeConfig(stdout, resp)
	return exitOK
}

// cmdShowModeSet implements "show mode set <program|show>". The value is a
// positional argument rather than a JSON payload read from a file: this
// kind's whole payload is one member of a two-member closed enum, and
// making an operator author JSON to flip it would be exactly the friction
// ADR-033 decision 3 argues against.
func cmdShowModeSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show mode set", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show mode set [flags] <program|show>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.mode revision (requires config:write, admin only).")
		_, _ = fmt.Fprintln(stderr, "A full replacement, validated before activation: an invalid value is")
		_, _ = fmt.Fprintln(stderr, "rejected and appends no revision (ADR-009). Applies without a")
		_, _ = fmt.Fprintln(stderr, "coordinator restart, in both directions.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show mode set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	mode := rest[0]
	if !validShowModeValue(mode) {
		return reportError(stderr, "show mode set", newCLIError(exitUsage,
			"mode must be one of program or show, not %q", mode))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show mode set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showModeConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/show.mode", configShowModePayload{Mode: mode}, &resp); err != nil {
		return reportError(stderr, "show mode set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show mode set", err)
		}
		return exitOK
	}
	printShowModeConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl show mode set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

func cmdShowModeRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show mode revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show mode revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList show.mode revision history, newest first (requires config:write).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show mode revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show mode revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.mode/revisions", nil, &resp); err != nil {
		return reportError(stderr, "show mode revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show mode revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// printShowModeConfig renders resp as human-readable text. The effect line
// is printed rather than hidden behind --output json: ADR-033 decision 3
// requires a behaviour caused by the mode to state the mode as its reason,
// and this is where a CLI operator reads it.
func printShowModeConfig(w io.Writer, resp showModeConfigResponse) {
	by := "(built-in default; no revision has ever been written)"
	if resp.CreatedByPrincipalName != nil {
		by = "set by " + *resp.CreatedByPrincipalName
	}
	_, _ = fmt.Fprintf(w, "show mode: %s\n", resp.Payload.Mode)
	_, _ = fmt.Fprintf(w, "  revision %d (source %s, %s)\n", resp.Revision, resp.Source, by)
	if resp.Revision > 0 {
		_, _ = fmt.Fprintf(w, "  updated at %s\n", resp.UpdatedAt.Format(time.RFC3339))
	}
	if resp.ResolumeWebSocketEffect != "" {
		_, _ = fmt.Fprintf(w, "  effect: %s\n", resp.ResolumeWebSocketEffect)
	}
	if resp.CueActivationPin.Pinned {
		_, _ = fmt.Fprintf(w, "  cue activation: STAGED, held to show %q generation %d since %s\n",
			resp.CueActivationPin.Show, resp.CueActivationPin.Generation, resp.CueActivationPin.PinnedAt)
		_, _ = fmt.Fprintf(w, "    a show.cue edit saved now will NOT reach any node until the show is stopped and restarted\n")
	} else if resp.CueActivationPin.Effect != "" {
		_, _ = fmt.Fprintf(w, "  cue activation: %s\n", resp.CueActivationPin.Effect)
	}
}
