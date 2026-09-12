package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is the CLI dispatch for "showmeshctl audio alignment-run
// start|stop|list|get". Declares its own wire types rather than
// importing internal/coordinator/api/v1, matching cmd_audio_node_silence.go.

type audioAlignmentRun struct {
	ID                   string  `json:"id"`
	NodeID               string  `json:"nodeId"`
	StartedAt            string  `json:"startedAt"`
	StoppedAt            *string `json:"stoppedAt"`
	StartedBy            string  `json:"startedBy"`
	StartedByPrincipalID string  `json:"startedByPrincipalId"`
	StoppedBy            *string `json:"stoppedBy"`
	StoppedByPrincipalID *string `json:"stoppedByPrincipalId"`
	StopReason           *string `json:"stopReason"`
}

type audioAlignmentSample struct {
	SampledAt string  `json:"sampledAt"`
	OffsetMs  float64 `json:"offsetMs"`
	SessionID string  `json:"sessionId"`
}

type audioAlignmentRunSummary struct {
	SampleCount                int      `json:"sampleCount"`
	FirstSampleAt              *string  `json:"firstSampleAt"`
	LastSampleAt               *string  `json:"lastSampleAt"`
	MaxExcursionOffsetMs       *float64 `json:"maxExcursionOffsetMs"`
	MaxExcursionSampledAt      *string  `json:"maxExcursionSampledAt"`
	DriftRateMsPerHour         *float64 `json:"driftRateMsPerHour"`
	DriftRateUnavailableReason string   `json:"driftRateUnavailableReason,omitempty"`
}

type audioAlignmentRunStopRequest struct {
	Reason string `json:"reason,omitempty"`
}

type audioAlignmentRunResponse struct {
	ServerTime time.Time         `json:"serverTime"`
	Run        audioAlignmentRun `json:"run"`
}

type audioAlignmentRunListResponse struct {
	ServerTime time.Time           `json:"serverTime"`
	Runs       []audioAlignmentRun `json:"runs"`
}

type audioAlignmentRunDetailResponse struct {
	ServerTime time.Time                `json:"serverTime"`
	Run        audioAlignmentRun        `json:"run"`
	Samples    []audioAlignmentSample   `json:"samples"`
	Truncated  bool                     `json:"truncated"`
	Summary    audioAlignmentRunSummary `json:"summary"`
}

func cmdAudioAlignmentRun(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioAlignmentRunUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioAlignmentRunUsage(stdout)
		return exitOK
	case "start":
		return cmdAudioAlignmentRunStart(rest, stdout, stderr, clock)
	case "stop":
		return cmdAudioAlignmentRunStop(rest, stdout, stderr, clock)
	case "list":
		return cmdAudioAlignmentRunList(rest, stdout, stderr, clock)
	case "get":
		return cmdAudioAlignmentRunGet(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio alignment-run: unknown subcommand %q\n\n", sub)
		printAudioAlignmentRunUsage(stderr)
		return exitUsage
	}
}

func printAudioAlignmentRunUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio alignment-run <subcommand> [flags]

A long-run program-to-LTC drift recording: starts recording the
samples a node's own node.audio.clock.alignment reports already carry,
and reads the series back afterward with the max excursion and the
end-to-end drift rate. Coordinator-side only.

Subcommands:
  start --node <id>                start recording (requires audio:command)
  stop --node <id> --run <id>      stop recording (requires audio:command)
  list --node <id>                 list a node's runs, newest first (requires observation:read)
  get --node <id> --run <id>       one run's series and summary (requires observation:read)

Run "showmeshctl audio alignment-run <subcommand> --help" for flags
specific to one subcommand.
`)
}

func cmdAudioAlignmentRunStart(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio alignment-run start", stderr)
	var nodeID string
	fs.StringVar(&nodeID, "node", "", "the node to record (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio alignment-run start --node <id> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nStart a run (POST .../audio/alignment-runs). Refused with a conflict")
		_, _ = fmt.Fprintln(stderr, "if the node already has one active.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio alignment-run start", err)
	}
	if nodeID == "" {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio alignment-run start", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioAlignmentRunResponse
	startPath := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/audio/alignment-runs"
	if err := c.postJSON(ctx, startPath, nil, &resp); err != nil {
		return reportError(stderr, "audio alignment-run start", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio alignment-run start", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "started: run %s on %s at %s\n", resp.Run.ID, resp.Run.NodeID, resp.Run.StartedAt)
	return exitOK
}

func cmdAudioAlignmentRunStop(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio alignment-run stop", stderr)
	var nodeID, runID, reason string
	fs.StringVar(&nodeID, "node", "", "the node the run belongs to (required)")
	fs.StringVar(&runID, "run", "", "the run to stop (required)")
	fs.StringVar(&reason, "reason", "", "optional operator-stated reason")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio alignment-run stop --node <id> --run <id> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nStop a run (POST .../audio/alignment-runs/{runId}/stop). An unknown or")
		_, _ = fmt.Fprintln(stderr, "already-stopped run id is not found.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio alignment-run stop", err)
	}
	if nodeID == "" || runID == "" {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio alignment-run stop", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioAlignmentRunResponse
	stopPath := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/audio/alignment-runs/" + url.PathEscape(runID) + "/stop"
	if err := c.postJSON(ctx, stopPath, audioAlignmentRunStopRequest{Reason: reason}, &resp); err != nil {
		return reportError(stderr, "audio alignment-run stop", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio alignment-run stop", err)
		}
		return exitOK
	}
	stoppedAt := ""
	if resp.Run.StoppedAt != nil {
		stoppedAt = *resp.Run.StoppedAt
	}
	_, _ = fmt.Fprintf(stdout, "stopped: run %s on %s at %s\n", resp.Run.ID, resp.Run.NodeID, stoppedAt)
	return exitOK
}

func cmdAudioAlignmentRunList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio alignment-run list", stderr)
	var nodeID string
	fs.StringVar(&nodeID, "node", "", "the node to list runs for (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio alignment-run list --node <id> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList a node's runs, newest first (GET .../audio/alignment-runs).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio alignment-run list", err)
	}
	if nodeID == "" {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio alignment-run list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioAlignmentRunListResponse
	listPath := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/audio/alignment-runs"
	if err := c.getJSON(ctx, listPath, nil, &resp); err != nil {
		return reportError(stderr, "audio alignment-run list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio alignment-run list", err)
		}
		return exitOK
	}
	if len(resp.Runs) == 0 {
		_, _ = fmt.Fprintln(stdout, "no runs")
		return exitOK
	}
	for _, run := range resp.Runs {
		stoppedAt := "(active)"
		if run.StoppedAt != nil {
			stoppedAt = *run.StoppedAt
		}
		_, _ = fmt.Fprintf(stdout, "%s  started %s  stopped %s\n", run.ID, run.StartedAt, stoppedAt)
	}
	return exitOK
}

func cmdAudioAlignmentRunGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio alignment-run get", stderr)
	var nodeID, runID string
	fs.StringVar(&nodeID, "node", "", "the node the run belongs to (required)")
	fs.StringVar(&runID, "run", "", "the run to read back (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio alignment-run get --node <id> --run <id> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nRead one run back: the summary (sample count, max excursion, drift")
		_, _ = fmt.Fprintln(stderr, "rate), then the series, one sample per line.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio alignment-run get", err)
	}
	if nodeID == "" || runID == "" {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio alignment-run get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioAlignmentRunDetailResponse
	getPath := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/audio/alignment-runs/" + url.PathEscape(runID)
	if err := c.getJSON(ctx, getPath, nil, &resp); err != nil {
		return reportError(stderr, "audio alignment-run get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio alignment-run get", err)
		}
		return exitOK
	}
	printAudioAlignmentRunDetail(stdout, resp)
	return exitOK
}

// printAudioAlignmentRunDetail prints the summary first (max excursion,
// drift rate), then the series one sample per line.
func printAudioAlignmentRunDetail(stdout io.Writer, resp audioAlignmentRunDetailResponse) {
	s := resp.Summary
	_, _ = fmt.Fprintf(stdout, "run %s on %s: %d sample(s)\n", resp.Run.ID, resp.Run.NodeID, s.SampleCount)
	if resp.Truncated {
		_, _ = fmt.Fprintf(stdout, "(showing the first %d of %d samples; summary covers all of them)\n", len(resp.Samples), s.SampleCount)
	}
	if s.MaxExcursionOffsetMs != nil {
		_, _ = fmt.Fprintf(stdout, "max excursion: %.3f ms at %s\n", *s.MaxExcursionOffsetMs, *s.MaxExcursionSampledAt)
	}
	if s.DriftRateMsPerHour != nil {
		_, _ = fmt.Fprintf(stdout, "drift rate: %.3f ms/hour\n", *s.DriftRateMsPerHour)
	} else {
		_, _ = fmt.Fprintf(stdout, "drift rate: unavailable (%s)\n", s.DriftRateUnavailableReason)
	}
	for _, sample := range resp.Samples {
		_, _ = fmt.Fprintf(stdout, "%s  %.3f ms  session %s\n", sample.SampledAt, sample.OffsetMs, sample.SessionID)
	}
}
