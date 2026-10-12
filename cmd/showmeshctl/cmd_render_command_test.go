package main

import (
	"bytes"
	"testing"
	"time"
)

func TestEffectiveRenderCommandTimeoutRaisesTooSmallBudget(t *testing.T) {
	if got := effectiveRenderCommandTimeout(5 * time.Second); got != minRenderCommandClientTimeout {
		t.Fatalf("effectiveRenderCommandTimeout(5s) = %s, want the %s floor", got, minRenderCommandClientTimeout)
	}
}

func TestEffectiveRenderCommandTimeoutHonorsLargerExplicitBudget(t *testing.T) {
	want := 60 * time.Second
	if got := effectiveRenderCommandTimeout(want); got != want {
		t.Fatalf("effectiveRenderCommandTimeout(60s) = %s, want 60s unchanged", got)
	}
}

func TestReportRenderCommandResultConfirmedExitsOK(t *testing.T) {
	var buf bytes.Buffer
	code := reportRenderCommandResult(&buf, "render apply", renderCommandResult{
		Action: "render.surface.apply", NodeID: "media-01", SurfaceID: "wall-1",
		Outcome: "confirmed", OutcomeReason: "surface.pipeline.state = \"running\"",
	})
	if code != exitOK {
		t.Fatalf("exit = %d, want exitOK", code)
	}
}

// TestReportRenderCommandResultUnconfirmedFailedExitsRenderPipelineDown is
// this seam's own sharper exit code: PipelineFailed is DIRECT evidence the
// pipeline is down, distinct from every other unconfirmed cause, which
// stays on exitCommandUnconfirmed.
//
// Finding 15: this fixture used to set OutcomeState: "failed" directly —
// a value no real coordinator response can ever carry, since OutcomeState
// only ever holds pkg/observation's six-value evidence-state vocabulary
// (see internal/coordinator/api/renderdispatch.go's
// evaluateRenderSurfaceState, which never returns "failed" as a state).
// That made this test pass whether or not exitRenderPipelineDown's own
// mapping logic was reachable by anything real. OutcomeState here is
// "current" — the actual state a fresh-but-wrong-value pipeline reading
// carries — and PipelineFailed:true is what a real coordinator response
// sets in exactly that case; TestRenderRestartUnconfirmedWithFailedPipelineExits23
// (test/integration/render_pipeline_failed_test.go) proves the server side
// of that claim against the real dispatch/confirm handler, not a fake.
func TestReportRenderCommandResultUnconfirmedFailedExitsRenderPipelineDown(t *testing.T) {
	var buf bytes.Buffer
	code := reportRenderCommandResult(&buf, "render apply", renderCommandResult{
		Outcome: "unconfirmed", OutcomeState: "current", OutcomeReason: "surface.pipeline.state = failed, wanted \"running\"",
		PipelineFailed: true,
	})
	if code != exitRenderPipelineDown {
		t.Fatalf("exit = %d, want exitRenderPipelineDown (23)", code)
	}
}

// A command the node failed prints the node's own reason and never exits 0.
func TestReportRenderCommandResultNodeFailurePrintsTheNodeReason(t *testing.T) {
	const reason = "render.surface.apply: starting frame writer: fseq: requested channel range is not covered by this file"
	var buf bytes.Buffer
	code := reportRenderCommandResult(&buf, "render apply", renderCommandResult{
		Action: "render.surface.apply", NodeID: "media-01", SurfaceID: "wall-1", CommandID: "c1",
		Outcome: "unconfirmed", OutcomeState: "current", OutcomeReason: reason,
	})
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit = %d, want exitCommandUnconfirmed (9)", code)
	}
	if want := "unconfirmed: render.surface.apply on media-01/wall-1: " + reason + " (command c1)\n"; buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

func TestReportRenderCommandResultUnconfirmedOtherwiseExitsCommandUnconfirmed(t *testing.T) {
	var buf bytes.Buffer
	code := reportRenderCommandResult(&buf, "render apply", renderCommandResult{
		Outcome: "unconfirmed", OutcomeState: "not_collected", OutcomeReason: "no evidence yet",
	})
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit = %d, want exitCommandUnconfirmed (9), not exitRenderPipelineDown", code)
	}
}

// TestReportRenderCommandResultUnconfirmedOutcomeStateFailedAloneNeverTriggersPipelineDown
// proves the inverse of the fix: OutcomeState alone, even if a caller
// somehow set it to the literal string "failed" (impossible from a real
// coordinator, but this must not become load-bearing again by accident),
// never triggers exitRenderPipelineDown without PipelineFailed also true —
// the exit code must be driven by the structured field, never a string
// comparison against OutcomeState.
func TestReportRenderCommandResultUnconfirmedOutcomeStateFailedAloneNeverTriggersPipelineDown(t *testing.T) {
	var buf bytes.Buffer
	code := reportRenderCommandResult(&buf, "render apply", renderCommandResult{
		Outcome: "unconfirmed", OutcomeState: "failed", OutcomeReason: "not a real coordinator value",
	})
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit = %d, want exitCommandUnconfirmed (9): OutcomeState alone must never select exitRenderPipelineDown", code)
	}
}

func TestReportRenderCommandResultReplayNotesOnStdout(t *testing.T) {
	var buf bytes.Buffer
	reportRenderCommandResult(&buf, "render clear", renderCommandResult{
		CommandID: "cmd-1", Replay: true, Outcome: "confirmed",
	})
	if !bytes.Contains(buf.Bytes(), []byte("this idempotency key was already used")) {
		t.Fatalf("stdout = %q, want a replay note", buf.String())
	}
}
