package pipeline

import (
	"bytes"
	"context"
	"testing"
	"time"
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
	if fw.source != nil || fw.timeline != nil {
		t.Fatal("the diagnostic writer holds a source or a timeline; it must depend on neither")
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
