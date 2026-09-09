package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// This file is "showmeshctl audio session aligned-start", the CLI half of
// POST /api/v1/audio/sessions/{sessionId}/aligned-start: start one
// session on several nodes at ONE instant on the shared media clock.
//
// It is a separate file from cmd_audio_session.go's nine per-node
// operations because it is not one of them: it takes a LIST of nodes, it
// has no node path segment, and its response is a selection plus two
// result lists rather than one command result.

type alignedAudioStartRequest struct {
	Revision       uint64   `json:"revision"`
	IdempotencyKey string   `json:"idempotencyKey"`
	NodeIDs        []string `json:"nodeIds"`
}

// alignedAudioStartSelection mirrors
// internal/coordinator/api/v1.AlignedAudioStartSelection: this program's
// own independent transcription, matching audioSessionCommandResult's
// identical rule.
type alignedAudioStartSelection struct {
	ScheduledAtNs int64  `json:"scheduledAtNs"`
	ClockNodeID   string `json:"clockNodeId"`
	LeadNs        int64  `json:"leadNs"`

	PrerollNs         int64 `json:"prerollNs"`
	PrerollReportedBy int   `json:"prerollReportedBy"`
	DeliveryBoundNs   int64 `json:"deliveryBoundNs"`
	MarginNs          int64 `json:"marginNs"`

	ClockErrorBoundKnown bool  `json:"clockErrorBoundKnown"`
	ClockErrorBoundNs    int64 `json:"clockErrorBoundNs"`
}

type alignedAudioStartResponse struct {
	ServerTime time.Time `json:"serverTime"`
	SessionID  string    `json:"sessionId"`

	Aligned         bool                        `json:"aligned"`
	UnalignedReason string                      `json:"unalignedReason"`
	Selection       *alignedAudioStartSelection `json:"selection"`

	Prepares []audioSessionCommandResult `json:"prepares"`
	Starts   []audioSessionCommandResult `json:"starts"`
}

func cmdAudioSessionAlignedStart(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "audio session aligned-start"
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	revision := fs.Uint64("revision", 0, "the desired-state revision every node's command carries; defaults to this "+
		"session's current observed revision (GET /api/v1/observations) plus one, or 1 for a session this "+
		"coordinator has never observed")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s [flags] <session-id> <node-id> [<node-id>...]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	revisionSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "revision" {
			revisionSet = true
		}
	})
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fs.Usage()
		return exitUsage
	}
	sessionID, nodeIDs := rest[0], rest[1:]

	timeout := effectiveAudioSessionCommandTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl %s: --timeout %s is below this command's own minimum request budget of %s; using %s instead.\n",
			cmdLabel, g.timeout, minAudioSessionCommandClientTimeout, timeout)
	}
	c, err := newClientWithTimeout(g, timeout)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if !revisionSet {
		cur, revErr := currentAudioSessionDesiredRevision(ctx, c, sessionID)
		if revErr != nil {
			return reportError(stderr, cmdLabel, revErr)
		}
		v := cur + 1
		revision = &v
	}
	key, err := newRenderIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	path := "/api/v1/audio/sessions/" + url.PathEscape(sessionID) + "/aligned-start"
	var resp alignedAudioStartResponse
	if reqErr := c.postJSON(ctx, path,
		alignedAudioStartRequest{Revision: *revision, IdempotencyKey: key, NodeIDs: nodeIDs}, &resp); reqErr != nil {
		return reportError(stderr, cmdLabel, reqErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForAlignedAudioStart(resp)
	}
	return reportAlignedAudioStart(stdout, cmdLabel, resp)
}

// reportAlignedAudioStart prints the selection and every node's own
// outcome. An unaligned run is stated plainly and never printed as if it
// had scheduled anything: an operator who asked for an aligned start and
// silently got an unaligned one has been told something false.
func reportAlignedAudioStart(stdout io.Writer, cmdLabel string, resp alignedAudioStartResponse) int {
	if resp.Aligned && resp.Selection != nil {
		s := resp.Selection
		_, _ = fmt.Fprintf(stdout, "showmeshctl %s: aligned on %s's media clock\n", cmdLabel, s.ClockNodeID)
		_, _ = fmt.Fprintf(stdout, "  start instant:  %d ns\n", s.ScheduledAtNs)
		_, _ = fmt.Fprintf(stdout, "  lead:           %d ns (preroll %d ns from %d node(s), delivery bound %d ns, margin %d ns)\n",
			s.LeadNs, s.PrerollNs, s.PrerollReportedBy, s.DeliveryBoundNs, s.MarginNs)
		if s.ClockErrorBoundKnown {
			_, _ = fmt.Fprintf(stdout, "  clock bound:    %d ns, included in the lead\n", s.ClockErrorBoundNs)
		} else {
			_, _ = fmt.Fprintf(stdout, "  clock bound:    UNKNOWN; the lead carries no allowance for clock uncertainty (unknown is not zero)\n")
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "showmeshctl %s: NOT ALIGNED. %s\n", cmdLabel, resp.UnalignedReason)
	}

	worst := exitOK
	for _, phase := range []struct {
		label   string
		results []audioSessionCommandResult
	}{{"prepare", resp.Prepares}, {"start", resp.Starts}} {
		for _, result := range phase.results {
			_, _ = fmt.Fprintf(stdout, "  %-7s %-24s %s", phase.label, result.NodeID, result.Outcome)
			if result.Reason != "" {
				_, _ = fmt.Fprintf(stdout, " (%s)", strings.TrimSpace(result.Reason))
			}
			_, _ = fmt.Fprintln(stdout)
			if code := exitCodeForAudioSessionCommandResult(result); code > worst {
				worst = code
			}
		}
	}
	return worst
}

// exitCodeForAlignedAudioStart reports the worst per-node outcome. An
// unaligned run is NOT itself a failure exit: every node still started, on
// arrival, which is the documented behaviour for a node without a locked
// clock. The printed and JSON output both say so; the exit code reports
// what happened to the commands.
func exitCodeForAlignedAudioStart(resp alignedAudioStartResponse) int {
	worst := exitOK
	for _, result := range append(append([]audioSessionCommandResult{}, resp.Prepares...), resp.Starts...) {
		if code := exitCodeForAudioSessionCommandResult(result); code > worst {
			worst = code
		}
	}
	return worst
}
