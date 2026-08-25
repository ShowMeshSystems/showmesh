package pipeline

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// TestDiagnosticFrameWriterDrawsWithNoSourceAndNoTimeline is the owner's
// ruling at this package's level: a writer built with neither a sequence nor a
// timeline still writes the diagnostic pattern every tick.
func TestDiagnosticFrameWriterDrawsWithNoSourceAndNoTimeline(t *testing.T) {
	const surfaceID = "surface-diagnostic"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	fw, err := NewDiagnosticFrameWriter(sup, surfaceID, 64, 4, 3, 40, testLogger{})
	if err != nil {
		t.Fatalf("NewDiagnosticFrameWriter: %v", err)
	}
	// The sentinels, not nil: the frame loop dereferences both every tick,
	// and a writer that depends on neither must say so with a value rather
	// than with an absence the hot path has to branch on.
	if _, ok := fw.source.(emptyFrameSource); !ok {
		t.Fatalf("the diagnostic writer's source is %T, want emptyFrameSource", fw.source)
	}
	if _, ok := fw.timeline.(unknownTimeline); !ok {
		t.Fatalf("the diagnostic writer's timeline is %T, want unknownTimeline", fw.timeline)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	defer func() {
		cancel()
		fw.Stop()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := fp.stdinSnapshot()
		if len(got) >= 64*4*3 {
			if !bytes.Contains(got, []byte{diagnosticBarFill, diagnosticBarFill, diagnosticBarFill}) {
				t.Fatal("no diagnostic bar reached the pipeline's stdin")
			}
			if !bytes.Contains(got, []byte{diagnosticBackgroundFill, diagnosticBackgroundFill, diagnosticBackgroundFill}) {
				t.Fatal("no diagnostic background reached the pipeline's stdin")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d bytes reached the pipeline's stdin within the deadline", len(got))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDiagnosticSentinelsAreLoudNotAbsent covers the two stand-ins the
// diagnostic writer holds in place of a real timeline and sequence. Both
// exist so the frame loop never branches on nil: an unknown timeline is
// permanently idle, and an extraction from no sequence at all is an error
// the failure path can report, not a panic and not a silent black frame.
func TestDiagnosticSentinelsAreLoudNotAbsent(t *testing.T) {
	if got := (unknownTimeline{}).Snapshot().State; got != multisync.StateUnknown {
		t.Fatalf("unknownTimeline reports %q, want %q", got, multisync.StateUnknown)
	}
	if !idleContentStates[(unknownTimeline{}).Snapshot().State] {
		t.Fatal("unknownTimeline's state is not an idle state, so a diagnostic writer could leave the idle path")
	}

	src := emptyFrameSource{}
	if got := src.FrameCount(); got != 0 {
		t.Fatalf("emptyFrameSource reports %d frames, want 0", got)
	}
	if err := src.ChannelRange(0, 0, 4, make([]byte, 4)); err == nil {
		t.Fatal("emptyFrameSource extracted a channel range from a sequence it does not have")
	}
}

func TestNewDiagnosticFrameWriterRejectsInvalidGeometryAndRate(t *testing.T) {
	sup, _ := newTestFrameWriterSupervisor(t, "surface-diagnostic")
	cases := map[string][4]int{
		"zero width":         {0, 4, 3, 40},
		"zero height":        {64, 0, 3, 40},
		"zero bytes":         {64, 4, 0, 40},
		"zero frame rate":    {64, 4, 3, 0},
		"negative frameRate": {64, 4, 3, -1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewDiagnosticFrameWriter(sup, "surface-diagnostic", c[0], c[1], c[2], c[3], testLogger{}); err == nil {
				t.Fatal("accepted an invalid diagnostic writer configuration")
			}
		})
	}
}
