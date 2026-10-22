package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// "showmeshctl seqtest": a show's sequence test list (show.sequencetest,
// ADR-067).

type seqTestConfigResponse struct {
	ServerTime       time.Time `json:"serverTime"`
	Kind             string    `json:"kind"`
	ID               string    `json:"id"`
	Revision         int64     `json:"revision"`
	Source           string    `json:"source"`
	Running          bool      `json:"running"`
	NotRunningReason string    `json:"notRunningReason"`
	Payload          struct {
		Show    string `json:"show"`
		Entries []struct {
			SequenceFilename string `json:"sequenceFilename"`
			Cue              string `json:"cue"`
		} `json:"entries"`
	} `json:"payload"`
}

func seqTestConfigPath(show string) string {
	return "/api/v1/config/show.sequencetest/" + url.PathEscape(show)
}

func cmdSeqTest(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printSeqTestUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printSeqTestUsage(stdout)
		return exitOK
	case "get":
		return cmdSeqTestGet(rest, stdout, stderr, clock)
	case "set":
		return cmdSeqTestSet(rest, stdout, stderr, clock)
	case "sequences":
		return cmdSeqTestSequences(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl seqtest: unknown subcommand %q\n\n", sub)
		printSeqTestUsage(stderr)
		return exitUsage
	}
}

func printSeqTestUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl seqtest [get|set] --show <id> [flags]
       showmeshctl seqtest sequences [flags]

A show's sequence test list (show.sequencetest). While the show is active and
no night session is open, FPP playing a listed sequence file runs its cue.

Subcommands:
  get   show the list and whether it runs now
  set   write a new full-replacement list from a JSON payload
        (--file <path>, or stdin if omitted), requires config:write:
        {"entries":[{"sequenceFilename":"Test1.fseq","cue":"intro"}]}
  sequences  list the sequence files the configured FPPs hold
`)
}

func cmdSeqTestGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "seqtest get"
	var show string
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&show, "show", "", "show id (required)")
	})
	if !ok {
		return code
	}
	if show == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "--show is required"))
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var raw json.RawMessage
	if err := c.getJSON(ctx, seqTestConfigPath(show), nil, &raw); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	return printSeqTestConfig(stdout, stderr, cmdLabel, g, raw, clock)
}

func cmdSeqTestSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "seqtest set"
	var show, file string
	var ifMatchFlag func() (int64, bool)
	var forceFlag func() bool
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&show, "show", "", "show id (required)")
		fs.StringVar(&file, "file", "", "path to a JSON show.sequencetest payload; reads stdin if not given")
		ifMatchFlag, forceFlag = registerIfMatchFlags(fs)
	})
	if !ok {
		return code
	}
	if show == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "--show is required"))
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
		var r seqTestConfigResponse
		if err := c.getJSON(ctx, seqTestConfigPath(show), nil, &r); err != nil {
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
	if err := c.putJSON(ctx, seqTestConfigPath(show), ifMatch, json.RawMessage(raw), &out); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	return printSeqTestConfig(stdout, stderr, cmdLabel, g, out, clock)
}

func printSeqTestConfig(stdout, stderr io.Writer, cmdLabel string, g *globalFlags, raw json.RawMessage, clock func() time.Time) int {
	var resp seqTestConfigResponse
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
	_, _ = fmt.Fprintf(stdout, "show.sequencetest %s revision %d (source %s)\n", resp.ID, resp.Revision, resp.Source)
	if resp.Running {
		_, _ = fmt.Fprintln(stdout, "  running now")
	} else {
		_, _ = fmt.Fprintf(stdout, "  not running: %s\n", resp.NotRunningReason)
	}
	if len(resp.Payload.Entries) == 0 {
		_, _ = fmt.Fprintln(stdout, "  no test sequences")
	}
	for _, e := range resp.Payload.Entries {
		_, _ = fmt.Fprintf(stdout, "  %s -> cue %s\n", e.SequenceFilename, e.Cue)
	}
	return exitOK
}

type fppSequenceListResponse struct {
	ServerTime time.Time `json:"serverTime"`
	Sequences  []struct {
		Filename  string   `json:"filename"`
		Instances []string `json:"instances"`
	} `json:"sequences"`
	Instances []struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	} `json:"instances"`
}

func cmdSeqTestSequences(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "seqtest sequences"
	g, code, ok := sitePowerArgs(cmdLabel, args, stderr, func(*flag.FlagSet) {})
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
	if err := c.getJSON(ctx, "/api/v1/fpp/sequences", nil, &raw); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	var resp fppSequenceListResponse
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
	for _, inst := range resp.Instances {
		if inst.Error != "" {
			_, _ = fmt.Fprintf(stdout, "FPP %s: %s\n", inst.ID, inst.Error)
		}
	}
	if len(resp.Sequences) == 0 {
		_, _ = fmt.Fprintln(stdout, "no sequence files")
	}
	for _, s := range resp.Sequences {
		_, _ = fmt.Fprintf(stdout, "%s  (%s)\n", s.Filename, strings.Join(s.Instances, ", "))
	}
	return exitOK
}
