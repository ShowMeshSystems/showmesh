package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// This file is "showmeshctl emergency-stop config get|set|revisions", over
// GET/PUT /api/v1/config/show.emergencystop and its revisions list:
// authoring each level's own optional follow-up action list. The trigger
// routes ("emergency-stop stop", "stop-power-down", "hard-stop arm|fire",
// cmd_emergency_stop.go) are "someone presses the button"; this file is
// "an admin decides what happens when they do." Modeled on
// cmd_showmode.go's own get/set/revisions shape, with "set" taking a full
// JSON payload from a file or stdin the way "action put" does
// (cmd_action.go), since this kind's payload (three level objects, each
// with its own actions array) has no useful single-flag shorthand.

type configEmergencyStopLevelPayload struct {
	Actions []string `json:"actions"`
}

type configEmergencyStopPayload struct {
	Stop          configEmergencyStopLevelPayload `json:"stop"`
	StopPowerDown configEmergencyStopLevelPayload `json:"stopPowerDown"`
	HardStop      configEmergencyStopLevelPayload `json:"hardStop"`
}

type emergencyStopConfigResponse struct {
	ServerTime             time.Time                  `json:"serverTime"`
	Kind                   string                     `json:"kind"`
	Revision               int64                      `json:"revision"`
	Payload                configEmergencyStopPayload `json:"payload"`
	UpdatedAt              time.Time                  `json:"updatedAt"`
	CreatedByPrincipalID   *string                    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                    `json:"createdByPrincipalName"`
	Source                 string                     `json:"source"`
}

func cmdEmergencyStopConfig(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		return cmdEmergencyStopConfigGet(nil, stdout, stderr, clock)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printEmergencyStopConfigUsage(stdout)
		return exitOK
	case "get":
		return cmdEmergencyStopConfigGet(rest, stdout, stderr, clock)
	case "set":
		return cmdEmergencyStopConfigSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdEmergencyStopConfigRevisions(rest, stdout, stderr, clock)
	default:
		if len(sub) > 0 && sub[0] == '-' {
			return cmdEmergencyStopConfigGet(args, stdout, stderr, clock)
		}
		_, _ = fmt.Fprintf(stderr, "showmeshctl emergency-stop config: unknown subcommand %q\n\n", sub)
		printEmergencyStopConfigUsage(stderr)
		return exitUsage
	}
}

func printEmergencyStopConfigUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl emergency-stop config [get|set|revisions] [flags]

Read or write the three emergency-stop levels' own optional follow-up
action lists (each an ordered list of existing show.action ids, invoked
best-effort after that level's own immediate stop). Reading requires
config:write; writing requires config:write (admin only).

Never 404s: an unconfigured installation reports every level with an
empty actions list, revision 0, source "default".

Subcommands:
  get               show the active configuration (the default when
                     "emergency-stop config" is run with no subcommand)
  set               write a new full-replacement revision from a JSON
                     payload (--file <path>, or stdin if omitted):
                     {"stop":{"actions":[...]},"stopPowerDown":{"actions":[...]},"hardStop":{"actions":[...]}}
  revisions         list revision history, newest first
`)
}

func cmdEmergencyStopConfigGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl emergency-stop config get"
	fs, g := newFlagSet(cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp emergencyStopConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.emergencystop", nil, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	printEmergencyStopConfig(stdout, resp)
	return exitOK
}

func cmdEmergencyStopConfigSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl emergency-stop config set"
	fs, g := newFlagSet(cmdLabel, stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON show.emergencystop payload; reads stdin if not given")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.emergencystop configuration revision (a full replacement:")
		_, _ = fmt.Fprintln(stderr, "all three level keys required, each with its own required actions array).")
		_, _ = fmt.Fprintln(stderr, "Accepts either a bare payload, or the full object \"emergency-stop config get --output json\" prints.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (an operator's payload \"revision\" if the input")
		_, _ = fmt.Fprintln(stderr, "is that get command's own shape, otherwise a fresh read), refusing with a")
		_, _ = fmt.Fprintln(stderr, "409 if the configuration changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "%v", err))
	}
	payloadRevision, _ := wrapperRevision(raw)
	raw, err = unwrapConfigGetResponse(raw)
	if err != nil {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "%v", err))
	}
	if !json.Valid(raw) {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "payload must be valid JSON"))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const apiPath = "/api/v1/config/show.emergencystop"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, payloadRevision, func() (int64, error) {
		var r emergencyStopConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp emergencyStopConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	printEmergencyStopConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl emergency-stop config set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

func cmdEmergencyStopConfigRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl emergency-stop config revisions"
	fs, g := newFlagSet(cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.emergencystop/revisions", nil, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func printEmergencyStopConfig(w io.Writer, resp emergencyStopConfigResponse) {
	_, _ = fmt.Fprintf(w, "show.emergencystop revision %d (source %s)\n", resp.Revision, resp.Source)
	_, _ = fmt.Fprintf(w, "  stop:            %d follow-up action(s): %v\n", len(resp.Payload.Stop.Actions), resp.Payload.Stop.Actions)
	_, _ = fmt.Fprintf(w, "  stop-power-down: %d follow-up action(s): %v\n", len(resp.Payload.StopPowerDown.Actions), resp.Payload.StopPowerDown.Actions)
	_, _ = fmt.Fprintf(w, "  hard-stop:       %d follow-up action(s): %v\n", len(resp.Payload.HardStop.Actions), resp.Payload.HardStop.Actions)
}
