package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

// "showmeshctl sitepower": the show.sitepower configuration (ADR-065) and
// the System power on/off test commands.

type sitePowerStep struct {
	Action              string `json:"action,omitempty"`
	RequireConfirmation bool   `json:"requireConfirmation,omitempty"`
	PowerDomain         string `json:"powerDomain,omitempty"`
	DomainProvenance    string `json:"domainProvenance,omitempty"`
	DelayMs             int    `json:"delayMs,omitempty"`
}

type sitePowerSequence struct {
	ID    string          `json:"id"`
	Label string          `json:"label"`
	Steps []sitePowerStep `json:"steps"`
}

type sitePowerConfigResponse struct {
	ServerTime             time.Time `json:"serverTime"`
	Kind                   string    `json:"kind"`
	Revision               int64     `json:"revision"`
	Source                 string    `json:"source"`
	UpdatedAt              string    `json:"updatedAt"`
	CreatedByPrincipalID   *string   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string   `json:"createdByPrincipalName"`
	Payload                struct {
		ShutdownSequences []sitePowerSequence `json:"shutdownSequences"`
		PowerOnActions    []string            `json:"powerOnActions"`
	} `json:"payload"`
}

type sitePowerRunResponse struct {
	ServerTime time.Time `json:"serverTime"`
	RunID      string    `json:"runId"`
	Started    []string  `json:"started"`
	Message    string    `json:"message"`
}

const sitePowerConfigPath = "/api/v1/config/show.sitepower"

func cmdSitePower(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		return cmdSitePowerGet(nil, stdout, stderr, clock)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printSitePowerUsage(stdout)
		return exitOK
	case "get":
		return cmdSitePowerGet(rest, stdout, stderr, clock)
	case "set":
		return cmdSitePowerSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdSitePowerRevisions(rest, stdout, stderr, clock)
	case "on":
		return cmdSitePowerRun(rest, "on", stdout, stderr, clock)
	case "off":
		return cmdSitePowerRun(rest, "off", stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl sitepower: unknown subcommand %q\n\n", sub)
		printSitePowerUsage(stderr)
		return exitUsage
	}
}

func printSitePowerUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl sitepower [get|set|revisions|on|off] [flags]

Shutdown sequences and power-on actions for the whole site (show.sitepower).

Subcommands:
  get         show the active configuration (the default)
  set         write a new full-replacement revision from a JSON payload
              (--file <path>, or stdin if omitted), requires config:write:
              {"shutdownSequences":[{"id":"projectors","label":"Projectors",
                "steps":[{"action":"lamps-off","requireConfirmation":true},
                         {"delayMs":300000},
                         {"action":"projectors-off","powerDomain":"presentation",
                          "domainProvenance":"operator-declared"}]}],
               "powerOnActions":["lawn-power-on"]}
  revisions   list revision history, newest first
  on          System power on: run the power-on actions (test command,
              refused while a night is open, requires show:emergencystop:invoke)
  off         System power off: run every shutdown sequence (test command,
              refused while a night is open, requires show:emergencystop:invoke)
`)
}

func sitePowerArgs(cmdLabel string, args []string, stderr io.Writer, extra func(*flag.FlagSet)) (*globalFlags, int, bool) {
	fs, g := newFlagSet(cmdLabel, stderr)
	if extra != nil {
		extra(fs)
	}
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, flagParseExit(err), false
	}
	if err := validateOutput(g); err != nil {
		return nil, reportError(stderr, cmdLabel, err), false
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return nil, exitUsage, false
	}
	return g, exitOK, true
}

func cmdSitePowerGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "sitepower get"
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, nil)
	if !ok {
		return code
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var raw json.RawMessage
	if err := c.getJSON(ctx, sitePowerConfigPath, nil, &raw); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	return printSitePowerConfig(stdout, stderr, cmdLabel, g, raw, clock)
}

func cmdSitePowerSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "sitepower set"
	var file string
	var ifMatchFlag func() (int64, bool)
	var forceFlag func() bool
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&file, "file", "", "path to a JSON show.sitepower payload; reads stdin if not given")
		ifMatchFlag, forceFlag = registerIfMatchFlags(fs)
	})
	if !ok {
		return code
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
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, payloadRevision, func() (int64, error) {
		var r sitePowerConfigResponse
		if err := c.getJSON(ctx, sitePowerConfigPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	// Nothing saved yet is revision 0, which If-Match cannot name.
	if ifMatch == ifMatchHeaderValue(0) {
		ifMatch = ""
	}
	var out json.RawMessage
	if err := c.putJSON(ctx, sitePowerConfigPath, ifMatch, json.RawMessage(raw), &out); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	return printSitePowerConfig(stdout, stderr, cmdLabel, g, out, clock)
}

func cmdSitePowerRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "sitepower revisions"
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, nil)
	if !ok {
		return code
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp configRevisionsResponse
	if err := c.getJSON(ctx, sitePowerConfigPath+"/revisions", nil, &resp); err != nil {
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

func cmdSitePowerRun(args []string, which string, stdout, stderr io.Writer, clock func() time.Time) int {
	cmdLabel := "sitepower " + which
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, nil)
	if !ok {
		return code
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp sitePowerRunResponse
	if which == "on" {
		err = c.postJSON(ctx, "/api/v1/site-power/on", map[string]any{}, &resp)
	} else {
		err = c.postJSON(ctx, "/api/v1/site-power/off", map[string]any{}, &resp)
	}
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "%s\nRun %s started: %v\n", resp.Message, resp.RunID, resp.Started)
	return exitOK
}

func printSitePowerConfig(stdout, stderr io.Writer, cmdLabel string, g *globalFlags, raw json.RawMessage, clock func() time.Time) int {
	var resp sitePowerConfigResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, raw); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "show.sitepower revision %d (source %s)\n", resp.Revision, resp.Source)
	_, _ = fmt.Fprintf(stdout, "  power-on actions: %v\n", resp.Payload.PowerOnActions)
	if len(resp.Payload.ShutdownSequences) == 0 {
		_, _ = fmt.Fprintln(stdout, "  shutdown sequences: none")
	}
	for _, seq := range resp.Payload.ShutdownSequences {
		_, _ = fmt.Fprintf(stdout, "  sequence %s (%s):\n", seq.ID, seq.Label)
		for i, st := range seq.Steps {
			switch {
			case st.Action == "":
				_, _ = fmt.Fprintf(stdout, "    %d. wait %s\n", i+1, time.Duration(st.DelayMs)*time.Millisecond)
			default:
				note := ""
				if st.RequireConfirmation {
					note += ", must confirm"
				}
				if st.PowerDomain != "" {
					note += ", removes " + st.PowerDomain + " power"
				}
				_, _ = fmt.Fprintf(stdout, "    %d. %s%s\n", i+1, st.Action, note)
			}
		}
	}
	return exitOK
}
