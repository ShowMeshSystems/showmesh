//go:build cgo

package gstengine

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

const slowCardSampleRate = 44100

// slowCardUnderrun is how far behind its own schedule a [slowCard] may
// find itself before it counts as having run dry.
const slowCardUnderrun = 30 * time.Millisecond

// steppableClock is a [ClockReader] over the host's own clock that a
// test can step forward, standing in for the node's media clock, which
// the sink's own drain rate does not follow.
type steppableClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *steppableClock) Now() (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset), nil
}

func (c *steppableClock) Close() error { return nil }

func (c *steppableClock) stepForward(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

// slowCard drains a real appsink the way a sound card drains its sink:
// at its own fixed rate, a little slower than the pipeline clock, never
// waiting on that clock and never taking audio faster than it plays it.
type slowCard struct {
	sink gstapp.AppSink
	// slowdown is the fraction by which this card runs slower than the
	// pipeline clock.
	slowdown float64

	mu sync.Mutex
	// audibleAt is when the first sample above silenceThreshold was
	// played since the last arm call; zero until then.
	audibleAt time.Time
	// stalledUntil is when this card next takes audio again; see stall.
	stalledUntil time.Time

	stop chan struct{}
	done chan struct{}
}

// newSlowCard builds a mono S16LE appsink that neither waits on the clock
// nor queues more than a couple of buffers, and installs it as the
// engine's sink.
func newSlowCard(t *testing.T, slowdown float64) *slowCard {
	t.Helper()
	gst.Init()
	el := gst.ElementFactoryMake("appsink", "slow-card")
	if el == nil {
		t.Skip("skipping: could not construct appsink")
	}
	sink, ok := el.(gstapp.AppSink)
	if !ok {
		t.Fatalf("appsink element does not implement gstapp.AppSink")
	}
	sink.SetObjectProperty("caps", gst.CapsFromString(
		fmt.Sprintf("audio/x-raw,format=S16LE,rate=%d,channels=1,layout=interleaved", slowCardSampleRate)))
	sink.SetObjectProperty("sync", false)
	sink.SetObjectProperty("emit-signals", false)
	sink.SetObjectProperty("max-buffers", uint32(2))
	sink.SetObjectProperty("drop", false)
	useSinkElement(t, sink)
	return &slowCard{sink: sink, slowdown: slowdown, stop: make(chan struct{}), done: make(chan struct{})}
}

// run plays every buffer for its own duration stretched by slowdown. A
// buffer that arrives late starts when it arrives: a card that ran dry
// does not catch up afterwards.
func (c *slowCard) run() {
	defer close(c.done)
	var next time.Time
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		c.mu.Lock()
		stalledUntil := c.stalledUntil
		c.mu.Unlock()
		time.Sleep(time.Until(stalledUntil))
		sample := c.sink.TryPullSample(gst.ClockTime(200 * time.Millisecond))
		if sample == nil {
			continue
		}
		buf := sample.GetBuffer()
		if buf == nil {
			continue
		}
		info, ok := buf.Map(gst.MapRead)
		if !ok {
			continue
		}
		data := info.Int16Data(binary.LittleEndian)
		info.Unmap()

		// A card plays at a steady rate; only one that actually ran dry
		// restarts from now.
		start := next
		if now := time.Now(); now.Sub(next) > slowCardUnderrun {
			start = now
		}
		onset := -1
		for i, v := range data {
			if v > silenceThreshold || v < -silenceThreshold {
				onset = i
				break
			}
		}
		if onset >= 0 {
			at := start.Add(c.playTime(onset))
			c.mu.Lock()
			if c.audibleAt.IsZero() {
				c.audibleAt = at
			}
			c.mu.Unlock()
		}
		next = start.Add(c.playTime(len(data)))
		time.Sleep(time.Until(next))
	}
}

// playTime is how long this card takes to play frames samples.
func (c *slowCard) playTime(frames int) time.Duration {
	nominal := time.Duration(frames) * time.Second / slowCardSampleRate
	return time.Duration(float64(nominal) / (1 - c.slowdown))
}

// stall stops this card taking any audio for d, as a graph that skips
// cycles does.
func (c *slowCard) stall(d time.Duration) {
	c.mu.Lock()
	c.stalledUntil = time.Now().Add(d)
	c.mu.Unlock()
	time.Sleep(d)
}

func (c *slowCard) arm() {
	c.mu.Lock()
	c.audibleAt = time.Time{}
	c.mu.Unlock()
}

func (c *slowCard) firstAudible() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.audibleAt
}

func (c *slowCard) Stop() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	<-c.done
}

// startToAudible loads and starts media on e and reports how long after
// Start returned its first audible sample was played by card.
func startToAudible(t *testing.T, e *Engine, card *slowCard, handle agentaudio.EngineHandle, path string) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := e.Load(ctx, handle, mediaRef(path), 2*time.Second); err != nil {
		t.Fatalf("Load(%s): %v", handle, err)
	}
	card.arm()
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start(%s): %v", handle, err)
	}
	started := time.Now()
	deadline := started.Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if at := card.firstAudible(); !at.IsZero() {
			if err := e.Release(ctx, handle); err != nil {
				t.Fatalf("Release(%s): %v", handle, err)
			}
			return at.Sub(started)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no audible sample from %s reached the card within 15s of Start", handle)
	return 0
}

// newSlowCardEngine builds a one-channel engine on clock whose sink is a
// [slowCard] running slowdown slower than that clock.
func newSlowCardEngine(t *testing.T, slowdown float64, clock ClockReader) (*Engine, *slowCard) {
	t.Helper()
	card := newSlowCard(t, slowdown)
	// Draining before the engine exists: a card nobody drains would stall
	// the pipeline for the whole of New's own settle window.
	go card.run()
	e, err := New(Config{
		SinkFactory:     "fakesink", // overridden by useSinkElement; must still name a real factory
		ProgramChannels: []int{1},
		ChannelCount:    1,
		SampleRate:      slowCardSampleRate,
		Resolve:         resolveByRuntimeFilename,
		Clock:           clock,
		ClockKind:       ClockKindRealtime,
	})
	t.Cleanup(func() {
		card.Stop()
		if e != nil {
			_ = e.Close()
		}
	})
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	return e, card
}

// requireStartNotDelayed starts a session on the fresh engine, runs
// fallBehind, which must leave the mixers behind the clock by behind, and
// starts another, which must be heard as soon after Start as the first.
func requireStartNotDelayed(t *testing.T, e *Engine, card *slowCard, behind time.Duration, fallBehind func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tone.wav")
	generateWAV(t, path, 2)

	before := startToAudible(t, e, card, "before", path)
	fallBehind()
	after := startToAudible(t, e, card, "after", path)
	t.Logf("start to first audible sample: %s on the fresh engine, %s once the mixers were %s behind the clock", before, after, behind)

	if growth := after - before; growth > behind/3 {
		t.Fatalf("a start was heard %s later once the mixers were %s behind the clock (%s fresh, %s after): a start must not inherit that delay",
			growth, behind, before, after)
	}
}

// TestStartIsNotDelayedByASinkSlowerThanTheClock runs the engine into a
// sink 10% slower than the pipeline clock, so the mixers fall steadily
// further behind it the longer the engine stays up.
func TestStartIsNotDelayedByASinkSlowerThanTheClock(t *testing.T) {
	const slowdown = 0.1
	const uptime = 8 * time.Second
	e, card := newSlowCardEngine(t, slowdown, &steppableClock{})
	requireStartNotDelayed(t, e, card, time.Duration(float64(uptime)*slowdown), func() { time.Sleep(uptime) })
}

// TestStartIsNotDelayedByASinkThatStoppedDraining covers a sink that
// took no audio for a while and then carried on at the clock's own rate.
func TestStartIsNotDelayedByASinkThatStoppedDraining(t *testing.T) {
	const gap = time.Second
	e, card := newSlowCardEngine(t, 0, &steppableClock{})
	requireStartNotDelayed(t, e, card, gap, func() {
		card.stall(gap)
		time.Sleep(500 * time.Millisecond)
	})
}

// TestStartIsNotDelayedByAForwardClockStep covers the media clock
// stepping forward under an engine whose sink keeps its own pace.
func TestStartIsNotDelayedByAForwardClockStep(t *testing.T) {
	const step = time.Second
	clock := &steppableClock{}
	e, card := newSlowCardEngine(t, 0, clock)
	requireStartNotDelayed(t, e, card, step, func() {
		clock.stepForward(step)
		time.Sleep(500 * time.Millisecond)
	})
}

// TestStartOnAnEngineKeepingPaceIsAnchoredToTheClock proves the common
// case is untouched: while the mixers keep pace with the clock, a start is
// placed at the clock's own running time, read as Start runs.
func TestStartOnAnEngineKeepingPaceIsAnchoredToTheClock(t *testing.T) {
	e := newTestEngine(t)
	path := filepath.Join(t.TempDir(), "tone.wav")
	generateWAV(t, path, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	checked := 0
	for i := 0; i < 10; i++ {
		handle := agentaudio.EngineHandle(fmt.Sprintf("paced-%d", i))
		_, lagAtLoad := e.mixerLag()
		if _, err := e.Load(ctx, handle, mediaRef(path), 2*time.Second); err != nil {
			t.Fatalf("Load(%s): %v", handle, err)
		}
		before, lagBefore := e.mixerLag()
		if _, err := e.Start(ctx, handle, 0); err != nil {
			t.Fatalf("Start(%s): %v", handle, err)
		}
		after, lagAfter := e.mixerLag()
		b, err := e.branchFor(handle)
		if err != nil {
			t.Fatalf("branchFor(%s): %v", handle, err)
		}
		offset := time.Duration(b.deinterleaveSrcPads[0].GetOffset())
		if err := e.Release(ctx, handle); err != nil {
			t.Fatalf("Release(%s): %v", handle, err)
		}
		// A host too starved to keep this pipeline in pace says nothing
		// about one that does.
		if lagAtLoad != 0 || lagBefore != 0 || lagAfter != 0 {
			continue
		}
		checked++
		if offset < before || offset > after {
			t.Fatalf("start %d was anchored at running time %s, outside the clock's own %s to %s across Start: a mixer keeping pace must leave the anchor on the clock",
				i, offset, before, after)
		}
	}
	if checked == 0 {
		t.Skip("skipping: this host never kept the pipeline in pace across a whole start")
	}
	t.Logf("%d of 10 starts ran with the mixers keeping pace; every one was anchored to the clock", checked)
}
