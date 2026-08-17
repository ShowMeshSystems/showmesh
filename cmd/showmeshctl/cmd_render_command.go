package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// This file is Track B seam B2b-front's own CLI dispatch core, mirroring
// cmd_fpp_command.go's shared plumbing (dispatchFPPCommand/
// effectiveFPPCommandTimeout/newIdempotencyKey) for the three render
// operations coordinator seam renderdispatch.go added:
// POST /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/apply|clear|restart.

// minRenderCommandClientTimeout is every "render apply/clear/restart"
// subcommand's own minimum request budget, overriding --timeout's global
// default (10s) when it is smaller — the identical reasoning
// minFPPCommandClientTimeout's own doc comment gives, applied to this
// seam's own numbers: the coordinator's renderHandlerWriteDeadline()
// (internal/coordinator/api/renderdispatch.go) is 25s
// (renderCommandConfirmDeadline 15s + a 10s write-deadline margin), so a
// client budget below that could only ever abort a healthy, still-working
// confirmation wait and misreport it as failed. 35s = that 25s plus a 10s
// round-trip margin — the SAME independently-chosen value
// minFPPCommandClientTimeout uses, reconciled against the real server
// value the only way two independent literals safely can be: this
// package's own renderdispatch_timeouts_test.go equivalent
// (internal/coordinator/api/renderdispatch_timeouts_test.go) fails loudly
// if the coordinator's own deadline is ever raised past what this literal
// still covers.
const minRenderCommandClientTimeout = 35 * time.Second

// effectiveRenderCommandTimeout is effectiveFPPCommandTimeout's identical
// floor-and-raise rule, applied to minRenderCommandClientTimeout.
func effectiveRenderCommandTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minRenderCommandClientTimeout {
		return minRenderCommandClientTimeout
	}
	return flagTimeout
}

func newRenderIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", newCLIError(exitAPIError, "generating an idempotency key: %v", err)
	}
	return hex.EncodeToString(buf), nil
}

// renderCommandRequest is the body of every render dispatch endpoint;
// sequenceId is only meaningful (and only required) for apply.
type renderCommandRequest struct {
	SequenceID     string `json:"sequenceId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// renderCommandResult mirrors internal/coordinator/api/v1.RenderCommandResult
// field for field — this program's own independent transcription, per
// this program's standing "no shared import with the coordinator" rule
// (importgraph_test.go's forbiddenImports).
type renderCommandResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	NodeID         string `json:"nodeId"`
	SurfaceID      string `json:"surfaceId"`
	Replay         bool   `json:"replay"`

	Outcome       string `json:"outcome"`
	OutcomeState  string `json:"outcomeState"`
	OutcomeReason string `json:"outcomeReason"`

	DispatchedAt string  `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`

	IdleOutput string `json:"idleOutput"`
}

type renderCommandResponse struct {
	ServerTime time.Time           `json:"serverTime"`
	Command    renderCommandResult `json:"command"`
}

// renderPipelineFailedState is [collector/noderender]'s
// surface.pipeline.state "failed" value, independently reproduced here
// (this program imports no coordinator package) exactly like
// fppStatusValueIdle's own reasoning in the fpp CLI files.
const renderPipelineFailedState = "failed"

// dispatchRenderCommand is the request/response core shared by "render
// apply", "render clear", and "render restart": build a client on this
// command's own timeout floor, mint a fresh idempotency key, POST to
// /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/{verb}, and report
// the outcome honestly (ADR-003).
func dispatchRenderCommand(stdout, stderr io.Writer, clock func() time.Time, g *globalFlags, cmdLabel, nodeID, surfaceID, verb, sequenceID string) int {
	timeout := effectiveRenderCommandTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl %s: --timeout %s is below this command's own minimum request budget of %s; using %s "+
				"instead. The coordinator holds an unresolved command's response open for its full confirmation "+
				"deadline before answering, so a shorter client budget can only ever produce a false "+
				"transport-timeout failure for a healthy, still-working conversation.\n",
			cmdLabel, g.timeout, minRenderCommandClientTimeout, timeout)
	}
	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key, err := newRenderIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	path := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/render/surfaces/" + url.PathEscape(surfaceID) + "/" + verb
	var resp renderCommandResponse
	reqErr := c.postJSON(ctx, path, renderCommandRequest{SequenceID: sequenceID, IdempotencyKey: key}, &resp)
	if reqErr != nil {
		return reportError(stderr, cmdLabel, reqErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForRenderCommandResult(resp.Command)
	}
	return reportRenderCommandResult(stdout, cmdLabel, resp.Command)
}

// reportRenderCommandResult prints result's outcome honestly to stdout and
// returns the exit code it maps to, mirroring reportFPPCommandResult's own
// honesty rule: exitOK only for "confirmed", never inferred from a bare
// 200. "unconfirmed" splits further than the FPP case: OutcomeState
// "failed" — direct evidence the pipeline is down, not merely absent —
// gets its own [exitRenderPipelineDown] rather than sharing
// [exitCommandUnconfirmed] with every other unconfirmed cause (a deadline
// that simply elapsed, or stale/unknown evidence).
func reportRenderCommandResult(stdout io.Writer, cmdLabel string, result renderCommandResult) int {
	if result.Replay {
		_, _ = fmt.Fprintf(stdout, "showmeshctl %s: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", cmdLabel, result.CommandID)
	}
	switch result.Outcome {
	case "confirmed":
		_, _ = fmt.Fprintf(stdout, "confirmed: %s on %s/%s (command %s): %s\n",
			result.Action, result.NodeID, result.SurfaceID, result.CommandID, result.OutcomeReason)
		if result.IdleOutput != "" {
			_, _ = fmt.Fprintf(stdout, "  idleOutput: %s (resolved from render.settings at dispatch time)\n", result.IdleOutput)
		}
		return exitOK
	case "unconfirmed":
		_, _ = fmt.Fprintf(stdout, "unconfirmed: %s on %s/%s: %s (command %s)\n",
			result.Action, result.NodeID, result.SurfaceID, result.OutcomeReason, result.CommandID)
		if result.OutcomeState == renderPipelineFailedState {
			return exitRenderPipelineDown
		}
		return exitCommandUnconfirmed
	default:
		_, _ = fmt.Fprintf(stdout, "pending: %s on %s/%s: command %s has not yet resolved (state %s)\n",
			result.Action, result.NodeID, result.SurfaceID, result.CommandID, result.OutcomeState)
		return exitCommandUnconfirmed
	}
}

// exitCodeForRenderCommandResult is reportRenderCommandResult's exit-code
// mapping alone, for --output json's caller — matching
// exitCodeForCommandResult's identical split.
func exitCodeForRenderCommandResult(result renderCommandResult) int {
	switch result.Outcome {
	case "confirmed":
		return exitOK
	case "unconfirmed":
		if result.OutcomeState == renderPipelineFailedState {
			return exitRenderPipelineDown
		}
		return exitCommandUnconfirmed
	default:
		return exitCommandUnconfirmed
	}
}
