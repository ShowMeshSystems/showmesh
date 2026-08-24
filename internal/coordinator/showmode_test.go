package coordinator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeShowModeSource is a showModeProvider a test can move.
type fakeShowModeSource struct {
	mu sync.Mutex
	v  showModeValue
	n  int
}

func (f *fakeShowModeSource) Current(context.Context) showModeValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return f.v
}

func (f *fakeShowModeSource) set(v showModeValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.v = v
}

func (f *fakeShowModeSource) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// fakeShowModeFootprint records what the mode asked the Resolume WebSocket
// switch to do.
type fakeShowModeFootprint struct {
	mu      sync.Mutex
	applied []bool
}

func (f *fakeShowModeFootprint) SetWebSocketEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, enabled)
}

func (f *fakeShowModeFootprint) history() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.applied...)
}

type fakeShowModePublisher struct {
	mu       sync.Mutex
	messages []mqttproto.ShowModeMessage
	topics   []string
	retained []bool
	qos      []byte
	err      error
}

func (f *fakeShowModePublisher) Publish(_ context.Context, topic string, qos byte, retain bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	msg, err := mqttproto.DecodeShowModeMessage(payload)
	if err != nil {
		return err
	}
	f.messages = append(f.messages, msg)
	f.topics = append(f.topics, topic)
	f.retained = append(f.retained, retain)
	f.qos = append(f.qos, qos)
	return nil
}

func (f *fakeShowModePublisher) published() []mqttproto.ShowModeMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mqttproto.ShowModeMessage(nil), f.messages...)
}

func fixedNow() func() time.Time {
	t := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// The first pass runs immediately rather than after one tick, so a
// coordinator that starts in show mode is never briefly in program.
func TestRunShowModeAppliesOnTheFirstPassBeforeAnyTick(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeShow, Revision: 3}}
	fp := &fakeShowModeFootprint{}
	pub := &fakeShowModePublisher{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// An interval long enough that nothing but the first pass can run.
		runShowMode(ctx, src, fp, pub, fixedNow(), discardLogger(), time.Hour)
	}()

	waitFor(t, func() bool { return len(fp.history()) > 0 })
	if got := fp.history(); got[0] != false {
		t.Fatalf("show mode did not close the WebSocket on the first pass: %v", got)
	}
	waitFor(t, func() bool { return len(pub.published()) > 0 })

	cancel()
	<-done
}

// ADR-036 decision 2's argument applied here: BOTH directions must be live.
// Entering show closes the Resolume WebSocket and returning to program
// reopens it, with no coordinator restart in either direction.
func TestRunShowModeDrivesTheResolumeWebSocketInBothDirections(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeProgram, Revision: 1}}
	fp := &fakeShowModeFootprint{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runShowMode(ctx, src, fp, nil, fixedNow(), discardLogger(), time.Millisecond)
	}()

	waitFor(t, func() bool { return len(fp.history()) == 1 })
	if fp.history()[0] != true {
		t.Fatalf("program mode did not hold the WebSocket open: %v", fp.history())
	}

	src.set(showModeValue{Mode: config.ShowModeShow, Revision: 2})
	waitFor(t, func() bool { return len(fp.history()) == 2 })
	if fp.history()[1] != false {
		t.Fatalf("entering show mode did not close the WebSocket: %v", fp.history())
	}

	src.set(showModeValue{Mode: config.ShowModeProgram, Revision: 3})
	waitFor(t, func() bool { return len(fp.history()) == 3 })
	if fp.history()[2] != true {
		t.Fatalf("returning to program mode did not reopen the WebSocket: %v", fp.history())
	}

	cancel()
	<-done
}

// The switch is written on a CHANGE, not on every tick: a supervisor that
// saw the flag rewritten every five seconds would be indistinguishable from
// one being told to act.
func TestRunShowModeDoesNotRewriteTheFootprintOnEveryTick(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeShow, Revision: 1}}
	fp := &fakeShowModeFootprint{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runShowMode(ctx, src, fp, nil, fixedNow(), discardLogger(), time.Millisecond)

	waitFor(t, func() bool { return src.calls() > 20 })
	if got := len(fp.history()); got != 1 {
		t.Fatalf("footprint written %d times across %d passes, want 1", got, src.calls())
	}
}

// The mode reaches nodes as ONE retained, installation-wide message, never
// a per-node command re-dispatch.
func TestRunShowModePublishesOneRetainedInstallationWideMessage(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeShow, Revision: 7}}
	pub := &fakeShowModePublisher{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runShowMode(ctx, src, nil, pub, fixedNow(), discardLogger(), time.Millisecond)

	waitFor(t, func() bool { return len(pub.published()) > 0 })

	pub.mu.Lock()
	topic, retain, qos := pub.topics[0], pub.retained[0], pub.qos[0]
	pub.mu.Unlock()

	if topic != mqttproto.ShowModeTopic() {
		t.Errorf("topic = %q, want %q", topic, mqttproto.ShowModeTopic())
	}
	if !retain || qos != 1 {
		t.Errorf("retain=%v qos=%d, want retained QoS 1", retain, qos)
	}
	m := pub.published()[0]
	if m.Mode != config.ShowModeShow || m.Revision != 7 {
		t.Errorf("published %+v, want mode show at revision 7", m)
	}
}

// A publish failure is logged and retried on the next tick. Nothing waits
// on it, and the footprint half keeps working: ADR-033 decision 4 forbids
// the mode's own delivery from degrading anything.
func TestRunShowModeSurvivesAPublishFailure(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeShow, Revision: 1}}
	fp := &fakeShowModeFootprint{}
	pub := &fakeShowModePublisher{err: context.DeadlineExceeded}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runShowMode(ctx, src, fp, pub, fixedNow(), discardLogger(), time.Millisecond)

	waitFor(t, func() bool { return src.calls() > 5 })
	if got := fp.history(); len(got) != 1 || got[0] != false {
		t.Fatalf("footprint history = %v, want the mode applied despite the publish failure", got)
	}
}

// A nil footprint (no Resolume instance configured) and a nil publisher (no
// broker) each disable only their own half.
func TestRunShowModeToleratesMissingConsumers(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeProgram}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runShowMode(ctx, src, nil, nil, fixedNow(), discardLogger(), time.Millisecond)
	waitFor(t, func() bool { return src.calls() > 3 })
}

func TestRunShowModeReturnsWhenItsContextIsCancelled(t *testing.T) {
	src := &fakeShowModeSource{v: showModeValue{Mode: config.ShowModeProgram}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runShowMode(ctx, src, nil, nil, fixedNow(), discardLogger(), time.Millisecond)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runShowMode did not return after its context was cancelled")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was never satisfied")
}
