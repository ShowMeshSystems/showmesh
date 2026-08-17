package api

import "testing"

// This file enforces the strict nesting renderdispatch.go's own top
// comment documents: agent renderConfirmDeadline (internal/agent/
// renderops.go, 5s, duplicated here as a literal since this package must
// not import internal/agent) < renderCommandConfirmDeadline <
// renderHandlerWriteDeadline() < minRenderCommandClientTimeout
// (cmd/showmeshctl/cmd_render_command.go, duplicated here for the same
// reason). A previous track shipped exactly this defect twice, the second
// time with a test that accepted equality where strict inequality was
// required — every comparison below is "<", never "<=".
const (
	agentRenderConfirmDeadlineLiteral      = 5  // seconds, internal/agent/renderops.go's renderConfirmDeadline
	cliMinRenderCommandClientTimeoutSecond = 35 // seconds, cmd/showmeshctl/cmd_render_command.go's minRenderCommandClientTimeout
)

func TestRenderTimeoutsNestStrictly(t *testing.T) {
	agent := agentRenderConfirmDeadlineLiteral
	confirm := int(renderCommandConfirmDeadline.Seconds())
	handler := int(renderHandlerWriteDeadline().Seconds())
	cli := cliMinRenderCommandClientTimeoutSecond

	if agent >= confirm {
		t.Fatalf("agent renderConfirmDeadline (%ds) must be strictly less than the coordinator's renderCommandConfirmDeadline (%ds)", agent, confirm)
	}
	if confirm >= handler {
		t.Fatalf("renderCommandConfirmDeadline (%ds) must be strictly less than renderHandlerWriteDeadline() (%ds)", confirm, handler)
	}
	if handler >= cli {
		t.Fatalf("renderHandlerWriteDeadline() (%ds) must be strictly less than the CLI's minRenderCommandClientTimeout (%ds)", handler, cli)
	}
}

// TestRenderHandlerWriteDeadlineMarginIsPositive is a narrower check on
// the one piece of arithmetic renderHandlerWriteDeadline performs: the
// margin it adds must be strictly positive, or the deadline above could
// degenerate to equality with renderCommandConfirmDeadline for some future
// edit that zeroes the margin without anyone noticing this test still
// passes (a zero margin would still satisfy "confirm < handler" today only
// by coincidence of the current 15s/10s values — this test pins the
// margin's own sign directly rather than relying on that coincidence).
func TestRenderHandlerWriteDeadlineMarginIsPositive(t *testing.T) {
	if renderHandlerWriteDeadlineMargin <= 0 {
		t.Fatalf("renderHandlerWriteDeadlineMargin = %s, want > 0", renderHandlerWriteDeadlineMargin)
	}
}
