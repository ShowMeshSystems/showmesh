package pipeline

import (
	"bytes"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// constFrameSource draws value on every channel of every frame, so a test
// can tell which of two sequences reached the pipeline.
type constFrameSource struct {
	value      byte
	frameCount int
	stepTimeMS byte
	channels   int
}

func (c *constFrameSource) FrameCount() int  { return c.frameCount }
func (c *constFrameSource) StepTimeMS() byte { return c.stepTimeMS }
func (c *constFrameSource) ChannelRange(frame, start, count int, dst []byte) error {
	if start+count > c.channels {
		return &errUncovered{}
	}
	for i := range dst {
		dst[i] = c.value
	}
	return nil
}

type errUncovered struct{}

func (*errUncovered) Error() string { return "channel range not covered" }

const queueTestChannels = 8

type queueHarness struct {
	t       *testing.T
	sup     *Supervisor
	fp      *fakeProcess
	tl      *fakeTimelineSource
	hold    *fakeHoldBlack
	fw      *FrameWriter
	tick    time.Time
	swapped []*QueuedSequence
	dropped []*QueuedSequence
}

func newQueueHarness(t *testing.T) *queueHarness {
	t.Helper()
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)
	h := &queueHarness{t: t, sup: sup, fp: fp, tl: &fakeTimelineSource{}, hold: newFakeHoldBlack(false), tick: time.Now()}
	held := &constFrameSource{value: 0x11, frameCount: 100, stepTimeMS: 25, channels: queueTestChannels}
	fw, err := NewFrameWriter(sup, surfaceID, held, h.tl, "wake.fseq", 0, queueTestChannels, queueTestChannels, 1, IdleOutputHold, nil, h.hold, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	fw.SetQueueHandlers(
		func(q *QueuedSequence) { h.swapped = append(h.swapped, q) },
		func(q *QueuedSequence) { h.dropped = append(h.dropped, q) },
	)
	h.fw = fw
	return h
}

// frame drives one tick with the timeline naming filename and returns the
// bytes that tick wrote and the drawing state it reported.
func (h *queueHarness) frame(state multisync.State, filename string) ([]byte, string) {
	h.t.Helper()
	h.tl.setWithFilename(state, 100, filename)
	h.tick = h.tick.Add(25 * time.Millisecond)
	h.fw.writeOneFrame(h.tick)
	out := h.fp.stdinSnapshot()
	snap, ok := h.sup.Snapshot("surface-1")
	if !ok {
		h.t.Fatalf("no snapshot")
	}
	return out[len(out)-queueTestChannels:], snap.Drawing
}

func (h *queueHarness) queueKpop() *QueuedSequence {
	h.t.Helper()
	q, displaced, err := h.fw.Queue("kpop.fseq", &constFrameSource{value: 0x22, frameCount: 100, stepTimeMS: 50, channels: queueTestChannels})
	if err != nil {
		h.t.Fatalf("Queue: %v", err)
	}
	if displaced != nil {
		h.t.Fatalf("Queue displaced %q, want nothing", displaced.Filename)
	}
	return q
}

func filled(b byte) []byte { return bytes.Repeat([]byte{b}, queueTestChannels) }

func TestFrameWriterSwitchesToQueuedSequenceWithNoBlackFrame(t *testing.T) {
	h := newQueueHarness(t)
	q := h.queueKpop()

	if got, drawing := h.frame(multisync.StatePlaying, "wake.fseq"); !bytes.Equal(got, filled(0x11)) || drawing != DrawingContent {
		t.Fatalf("before the change: wrote %v drawing %q, want the held sequence", got, drawing)
	}
	got, drawing := h.frame(multisync.StatePlaying, "kpop.fseq")
	if !bytes.Equal(got, filled(0x22)) || drawing != DrawingContent {
		t.Fatalf("first tick of the queued sequence wrote %v drawing %q, want its content with no black frame", got, drawing)
	}
	if len(h.swapped) != 1 || h.swapped[0] != q {
		t.Fatalf("switch handler ran %d times, want once with the queued sequence", len(h.swapped))
	}
	if h.fw.TakeQueued() != nil {
		t.Fatalf("the queue still holds a sequence after the writer switched to it")
	}
	if h.fw.stepTime != 50*time.Millisecond {
		t.Fatalf("step time after the switch = %v, want the queued file's 50ms", h.fw.stepTime)
	}
	if got, drawing := h.frame(multisync.StatePlaying, "wake.fseq"); !bytes.Equal(got, filled(0)) || drawing != DrawingStale {
		t.Fatalf("the previous sequence after the switch wrote %v drawing %q, want black", got, drawing)
	}
}

func TestFrameWriterStillDrawsBlackForASequenceNobodyQueued(t *testing.T) {
	h := newQueueHarness(t)
	h.queueKpop()

	got, drawing := h.frame(multisync.StatePlaying, "halloween.fseq")
	if !bytes.Equal(got, filled(0)) || drawing != DrawingStale {
		t.Fatalf("an unqueued sequence wrote %v drawing %q, want black", got, drawing)
	}
	if len(h.swapped) != 0 || len(h.dropped) != 0 {
		t.Fatalf("an unqueued sequence switched %d / dropped %d, want neither", len(h.swapped), len(h.dropped))
	}
	if got, _ := h.frame(multisync.StatePlaying, "kpop.fseq"); !bytes.Equal(got, filled(0x22)) {
		t.Fatalf("the queued sequence after an unrelated one wrote %v, want its content", got)
	}
}

func TestFrameWriterNeverDrawsAQueuedSequenceDuringOrAfterABlackout(t *testing.T) {
	h := newQueueHarness(t)
	q := h.queueKpop()

	h.hold.held.Store(true)
	for i := 0; i < 3; i++ {
		got, drawing := h.frame(multisync.StatePlaying, "kpop.fseq")
		if !bytes.Equal(got, filled(0)) || drawing != DrawingBlackout {
			t.Fatalf("held black with the queued sequence playing wrote %v drawing %q, want black", got, drawing)
		}
	}
	if len(h.swapped) != 0 {
		t.Fatalf("the writer switched to the queued sequence while held black")
	}
	if len(h.dropped) != 1 || h.dropped[0] != q {
		t.Fatalf("drop handler ran %d times, want once with the queued sequence", len(h.dropped))
	}

	h.hold.held.Store(false)
	got, drawing := h.frame(multisync.StatePlaying, "kpop.fseq")
	if !bytes.Equal(got, filled(0)) || drawing != DrawingStale {
		t.Fatalf("after the blackout lifted the dropped sequence wrote %v drawing %q, want black until it is queued again", got, drawing)
	}
}

func TestFrameWriterDoesNotSwitchWhileTheTimelineIsIdle(t *testing.T) {
	h := newQueueHarness(t)
	h.queueKpop()

	for _, state := range []multisync.State{multisync.StateOpened, multisync.StateStopped, multisync.StateUnknown} {
		if _, drawing := h.frame(state, "kpop.fseq"); drawing != DrawingIdle {
			t.Fatalf("%s drew %q, want idle", state, drawing)
		}
	}
	if len(h.swapped) != 0 || len(h.dropped) != 0 {
		t.Fatalf("idle ticks switched %d / dropped %d, want neither", len(h.swapped), len(h.dropped))
	}
	if got, _ := h.frame(multisync.StatePlaying, "kpop.fseq"); !bytes.Equal(got, filled(0x22)) {
		t.Fatalf("the queued sequence once playing wrote %v, want its content", got)
	}
}

func TestFrameWriterQueueRefusesAFileThatDoesNotCoverTheSurface(t *testing.T) {
	h := newQueueHarness(t)
	if _, _, err := h.fw.Queue("narrow.fseq", &constFrameSource{value: 0x33, frameCount: 100, stepTimeMS: 25, channels: queueTestChannels - 1}); err == nil {
		t.Fatalf("Queue accepted a file that does not cover the surface's channels")
	}
	if _, _, err := h.fw.Queue("", &constFrameSource{value: 0x33, frameCount: 100, stepTimeMS: 25, channels: queueTestChannels}); err == nil {
		t.Fatalf("Queue accepted an empty filename")
	}
	if h.fw.TakeQueued() != nil {
		t.Fatalf("a refused Queue left something queued")
	}
}

func TestFrameWriterQueueHandsBackTheSequenceItDisplaces(t *testing.T) {
	h := newQueueHarness(t)
	first := h.queueKpop()
	_, displaced, err := h.fw.Queue("other.fseq", &constFrameSource{value: 0x44, frameCount: 100, stepTimeMS: 25, channels: queueTestChannels})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if displaced != first {
		t.Fatalf("displaced = %v, want the first queued sequence", displaced)
	}
	if got, drawing := h.frame(multisync.StatePlaying, "kpop.fseq"); !bytes.Equal(got, filled(0)) || drawing != DrawingStale {
		t.Fatalf("the displaced sequence wrote %v drawing %q, want black", got, drawing)
	}
}

func TestFrameWriterDropsAQueuedSequenceTheSwitchGuardRefuses(t *testing.T) {
	h := newQueueHarness(t)
	q := h.queueKpop()
	h.fw.SetSwitchGuard(func(*QueuedSequence) bool { return false })

	got, drawing := h.frame(multisync.StatePlaying, "kpop.fseq")
	if !bytes.Equal(got, filled(0)) || drawing != DrawingStale {
		t.Fatalf("a refused switch wrote %v drawing %q, want black", got, drawing)
	}
	if len(h.swapped) != 0 || len(h.dropped) != 1 || h.dropped[0] != q {
		t.Fatalf("switched %d, dropped %d, want the queued sequence dropped and never switched to", len(h.swapped), len(h.dropped))
	}
	if h.fw.stepTime != 25*time.Millisecond {
		t.Fatalf("step time = %v, want the held sequence's 25ms", h.fw.stepTime)
	}
}
