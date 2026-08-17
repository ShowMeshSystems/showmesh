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
// this seam's own sharper exit code: OutcomeState "failed" is DIRECT
// evidence the pipeline is down, distinct from every other unconfirmed
// cause, which stays on exitCommandUnconfirmed.
func TestReportRenderCommandResultUnconfirmedFailedExitsRenderPipelineDown(t *testing.T) {
	var buf bytes.Buffer
	code := reportRenderCommandResult(&buf, "render apply", renderCommandResult{
		Outcome: "unconfirmed", OutcomeState: "failed", OutcomeReason: "pipeline crashed on startup",
	})
	if code != exitRenderPipelineDown {
		t.Fatalf("exit = %d, want exitRenderPipelineDown (23)", code)
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

func TestReportRenderCommandResultReplayNotesOnStdout(t *testing.T) {
	var buf bytes.Buffer
	reportRenderCommandResult(&buf, "render clear", renderCommandResult{
		CommandID: "cmd-1", Replay: true, Outcome: "confirmed",
	})
	if !bytes.Contains(buf.Bytes(), []byte("this idempotency key was already used")) {
		t.Fatalf("stdout = %q, want a replay note", buf.String())
	}
}
