package previewgateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	ndiA = StreamSpec{Key: "ndi:A", Kind: KindNDI, NDIName: "A (x)"}
	camB = StreamSpec{Key: "camera:b", Kind: KindRTSP, URL: "rtsp://u:p@h:554/s"}
)

func newTestManager(e *fakeEngine, s *fakeSources) *Manager {
	return NewManager(e, s, "http://127.0.0.1:9/ndi/sec", quietLog())
}

func TestSetStartsOnlyChangedStreams(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	ctx := context.Background()

	m.Set(ctx, []StreamSpec{ndiA, camB})
	if len(e.puts) != 2 {
		t.Fatalf("puts = %v", e.puts)
	}
	m.Set(ctx, []StreamSpec{ndiA, camB})
	if len(e.puts) != 2 || len(e.deletes) != 0 {
		t.Fatalf("an unchanged set touched the engine: puts=%v deletes=%v", e.puts, e.deletes)
	}

	changed := camB
	changed.URL = "rtsp://u:p@h:554/other"
	m.Set(ctx, []StreamSpec{ndiA, changed})
	if len(e.puts) != 3 || len(e.deletes) != 1 || e.streams[streamID("camera:b")] != changed.URL {
		t.Fatalf("changed stream not replaced: %v %v", e.puts, e.streams)
	}

	m.Set(ctx, []StreamSpec{ndiA})
	if len(e.deletes) != 2 || e.deletes[1] != streamID("camera:b") {
		t.Fatalf("removed stream not deleted: %v", e.deletes)
	}
	if _, _, ok := m.Lookup("camera:b"); ok {
		t.Fatal("removed key still known")
	}
}

func TestNDISourceIsALoopbackFeedNotACommand(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	m.Set(context.Background(), []StreamSpec{{Key: "k", Kind: KindNDI, NDIName: "A (x) y"}})
	src := e.streams[streamID("k")]
	want := "http://127.0.0.1:9/ndi/sec/" + streamID("k")
	if src != want {
		t.Fatalf("source = %q", src)
	}
	if name, _, ok := m.NDISource(streamID("k")); !ok || name != "A (x) y" {
		t.Fatalf("NDISource = %q, %v", name, ok)
	}
	if _, _, ok := m.NDISource(streamID("other")); ok {
		t.Fatal("unknown id resolved")
	}
}

func TestFailedPutIsRetriedAndResynced(t *testing.T) {
	e := newFakeEngine()
	e.failPut = true
	m := newTestManager(e, &fakeSources{ok: true})
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{camB})
	st := m.Streams(ctx)
	if st[0].State != StateError {
		t.Fatalf("state = %q", st[0].State)
	}
	e.failPut = false
	m.Retry(ctx)
	if len(e.puts) != 1 {
		t.Fatalf("retry did not apply: %v", e.puts)
	}
	m.Resync(ctx)
	if len(e.puts) != 2 {
		t.Fatalf("resync did not re-apply: %v", e.puts)
	}
}

func TestStreamStates(t *testing.T) {
	e := newFakeEngine()
	src := &fakeSources{ok: true, names: map[string]bool{"A (x)": true}}
	m := newTestManager(e, src)
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{ndiA, camB})
	byKey := func() map[string]StreamStatus {
		out := map[string]StreamStatus{}
		for _, s := range m.Streams(ctx) {
			out[s.Key] = s
		}
		return out
	}

	got := byKey()
	if got["ndi:A"].State != StateIdle || got["camera:b"].State != StateIdle {
		t.Fatalf("unwatched streams: %+v", got)
	}

	e.info[streamID("ndi:A")] = Go2RTCInfo{Consumers: []map[string]any{{}}}
	if s := byKey()["ndi:A"]; s.State != StateConnecting || s.Consumers != 1 {
		t.Fatalf("watched, no media: %+v", s)
	}

	e.info[streamID("ndi:A")] = Go2RTCInfo{Producers: []map[string]any{{"id": float64(1), "bytes_recv": float64(1200)}}, Consumers: []map[string]any{{}}}
	if s := byKey()["ndi:A"]; s.State != StateLive || s.Message != "" {
		t.Fatalf("live: %+v", s)
	}

	now0 := time.Now()
	m.now = func() time.Time { return now0.Add(stallAfter + time.Second) }
	if s := byKey()["ndi:A"]; s.State != StateConnecting {
		t.Fatalf("a source that stopped sending must not stay live: %+v", s)
	}
	e.info[streamID("ndi:A")] = Go2RTCInfo{Producers: []map[string]any{{"id": float64(1), "bytes_recv": float64(5000)}}}
	if s := byKey()["ndi:A"]; s.State != StateLive {
		t.Fatalf("growing bytes: %+v", s)
	}
	m.now = time.Now

	delete(e.info, streamID("ndi:A"))
	src.names = map[string]bool{}
	if s := byKey()["ndi:A"]; s.State != StateNotFound || s.Message == "" {
		t.Fatalf("unannounced: %+v", s)
	}

	src.ok = false
	if s := byKey()["ndi:A"]; s.State != StateError {
		t.Fatalf("no NDI runtime: %+v", s)
	}
	if s := byKey()["camera:b"]; s.State != StateIdle {
		t.Fatalf("a camera must not depend on NDI: %+v", s)
	}

	now := time.Now()
	m.now = func() time.Time { return now }
	m.NoteResult("camera:b", false)
	if s := byKey()["camera:b"]; s.State != StateNotFound {
		t.Fatalf("refused camera: %+v", s)
	}
	m.now = func() time.Time { return now.Add(failureMemory + time.Second) }
	if s := byKey()["camera:b"]; s.State != StateIdle {
		t.Fatalf("failure should expire: %+v", s)
	}
	m.NoteResult("camera:b", false)
	m.NoteResult("camera:b", true)
	if s := byKey()["camera:b"]; s.State != StateIdle {
		t.Fatalf("success should clear: %+v", s)
	}

	e.listErr = context.DeadlineExceeded
	for _, s := range m.Streams(ctx) {
		if s.State != StateError {
			t.Fatalf("engine down: %+v", s)
		}
	}
}

func TestOperatorMessagesAvoidInternalsWords(t *testing.T) {
	banned := []string{"interlock", "revision", "epoch", "evidence", "boundary", "armed", "ADR"}
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: false})
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{ndiA})
	e.listErr = context.DeadlineExceeded
	msgs := []string{m.Streams(ctx)[0].Message}
	e.listErr = nil
	msgs = append(msgs, m.Streams(ctx)[0].Message)
	for _, msg := range msgs {
		for _, b := range banned {
			if strings.Contains(msg, b) {
				t.Errorf("%q contains %q", msg, b)
			}
		}
	}
}

func feed(p *monitorParser, text string) []monitorEvent {
	var evs []monitorEvent
	for _, l := range strings.Split(text, "\n") {
		if ev := p.Feed(l); ev != nil {
			evs = append(evs, *ev)
		}
	}
	if ev := p.Flush(); ev != nil {
		evs = append(evs, *ev)
	}
	return evs
}

func TestMonitorParser(t *testing.T) {
	out := `Probing devices...

Monitoring devices, waiting for devices to be removed or new devices to be added...

Device found:

	name  : NODE-01 (matrix-main)
	class : Source/Audio/Video/Network
	caps  : application/x-ndi
	properties:
		ndi-name = NODE-01 (matrix-main)
		url-address = 192.168.130.22:5961
	gst-launch-1.0 ndisrc ndi-name="NODE-01\ \(matrix-main\)" url-address=192.168.130.22:5961 ! ...

Device found:

	name  : ARENA (Composition)
	properties:
		ndi-name = "ARENA (Composition)"
		url-address = "192.168.130.30:5962"

Device removed:

	name  : NODE-01 (matrix-main)
	properties:
		ndi-name = NODE-01 (matrix-main)
		url-address = 192.168.130.22:5961
`
	evs := feed(&monitorParser{}, out)
	if len(evs) != 3 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].removed || evs[0].src.Name != "NODE-01 (matrix-main)" || evs[0].src.Address != "192.168.130.22:5961" {
		t.Fatalf("first: %+v", evs[0])
	}
	if evs[1].src.Name != "ARENA (Composition)" || evs[1].src.Address != "192.168.130.30:5962" {
		t.Fatalf("second: %+v", evs[1])
	}
	if !evs[2].removed || evs[2].src.Name != "NODE-01 (matrix-main)" {
		t.Fatalf("third: %+v", evs[2])
	}
	if len(feed(&monitorParser{}, "Probing devices...\n")) != 0 {
		t.Fatal("expected none")
	}
}

func TestDiscoveryTracksAppearAndRemove(t *testing.T) {
	d := NewDiscovery(quietLog())
	p := &monitorParser{}
	for _, l := range []string{
		"Device found:", "\tproperties:", "\t\tndi-name = A (x)", "\t\turl-address = 10.0.0.5:5961", "\tgst-launch-1.0 ndisrc",
		"Device found:", "\t\tndi-name = bad\"name", "\t\turl-address = 10.0.0.6:5961", "\tgst-launch-1.0 ndisrc",
	} {
		if ev := p.Feed(l); ev != nil {
			d.apply(*ev)
		}
	}
	got := d.Sources()
	if len(got) != 1 || got[0].Name != "A (x)" {
		t.Fatalf("an unusable name must be skipped: %+v", got)
	}
	d.apply(monitorEvent{removed: true, src: Source{Name: "A (x)"}})
	if len(d.Sources()) != 0 {
		t.Fatal("removed source still listed")
	}
}

func TestDiscoveryLoopReportsUnavailableRuntime(t *testing.T) {
	d := NewDiscovery(quietLog())
	d.Available = func(context.Context) error { return errors.New("no plugin") }
	d.Monitor = func(context.Context, io.Writer) error { t.Error("monitor must not run"); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	go d.Loop(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if _, ok := d.Announced(); ok {
		t.Fatal("discovery must report itself unavailable")
	}
}

func TestDiscoveryLoopFeedsMonitorOutput(t *testing.T) {
	d := NewDiscovery(quietLog())
	d.Available = func(context.Context) error { return nil }
	d.Monitor = func(ctx context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, "Device found:\n\t\tndi-name = A (x)\n\t\turl-address = 10.0.0.5:5961\n\tgst-launch-1.0 x\n")
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Loop(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if names, ok := d.Announced(); ok && names["A (x)"] {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("source never appeared")
}

type blockingEngine struct {
	*fakeEngine
	enter   chan struct{}
	release chan struct{}
}

func (b *blockingEngine) Put(ctx context.Context, id, src string) error {
	b.enter <- struct{}{}
	<-b.release
	return b.fakeEngine.Put(ctx, id, src)
}

func TestSlowEngineDoesNotBlockLookupsOrStatus(t *testing.T) {
	e := &blockingEngine{fakeEngine: newFakeEngine(), enter: make(chan struct{}, 1), release: make(chan struct{})}
	m := NewManager(e, &fakeSources{ok: true}, "http://127.0.0.1:9/ndi/sec", quietLog())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.Set(context.Background(), []StreamSpec{camB}) }()
	<-e.enter
	done := make(chan struct{})
	go func() {
		m.Lookup("camera:b")
		m.NDISource("x")
		m.NoteResult("camera:b", true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lookups wait for the engine call")
	}
	close(e.release)
	wg.Wait()
}

func TestChangedSpecUnderTheSameKeyIsReplaced(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{ndiA})
	renamed := ndiA
	renamed.NDIName = "B (y)"
	m.Set(ctx, []StreamSpec{renamed})
	id := streamID(ndiA.Key)
	if len(e.deletes) != 1 || e.deletes[0] != id || len(e.puts) != 2 {
		t.Fatalf("a changed ndiName must be deleted then put: deletes=%v puts=%v", e.deletes, e.puts)
	}
	if name, _, _ := m.NDISource(id); name != "B (y)" {
		t.Fatalf("feed would still serve %q", name)
	}
}

func TestUnavailableDiscoveryStatesWhyAndWhatToDo(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: false, msg: msgNoAvahi})
	m.Set(context.Background(), []StreamSpec{ndiA})
	st := m.Streams(context.Background())
	if st[0].State != StateError || st[0].Message != msgNoAvahi {
		t.Fatalf("%+v", st[0])
	}
}

func TestFailedDeleteIsRetriedUntilTheEngineAcceptsIt(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{camB})
	id := streamID(camB.Key)
	e.failDelete = true
	m.Set(ctx, nil)
	m.Retry(ctx)
	if _, held := e.streams[id]; !held {
		t.Fatal("the engine dropped the stream while it was refusing deletes")
	}
	e.failDelete = false
	m.Retry(ctx)
	if _, held := e.streams[id]; held {
		t.Fatal("the stream the gateway no longer wants is still in the engine")
	}
	before := len(e.deletes)
	m.Retry(ctx)
	if len(e.deletes) != before {
		t.Fatal("a finished delete was sent again")
	}
}

func TestReAddedStreamIsNotDeletedByAnEarlierFailedDelete(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{camB})
	e.failDelete = true
	m.Set(ctx, nil)
	e.failDelete = false
	m.Set(ctx, []StreamSpec{camB})
	m.Retry(ctx)
	if _, held := e.streams[streamID(camB.Key)]; !held {
		t.Fatal("a pending delete removed the stream that was added back")
	}
}

type countingHandler struct {
	mu    sync.Mutex
	warns int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Level >= slog.LevelWarn {
		h.warns++
	}
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func TestPendingStreamsLogOncePerChangeNotPerStreamPerPass(t *testing.T) {
	h := &countingHandler{}
	e := newFakeEngine()
	e.failPut = true
	m := NewManager(e, &fakeSources{ok: true}, "http://127.0.0.1:9/ndi/sec", slog.New(h))
	ctx := context.Background()
	specs := []StreamSpec{camB, {Key: "camera:c", Kind: KindRTSP, URL: "rtsp://h/c"}, {Key: "camera:d", Kind: KindRTSP, URL: "rtsp://h/d"}}
	m.Set(ctx, specs)
	for range 5 {
		m.Retry(ctx)
	}
	if h.warns != 1 {
		t.Fatalf("%d warnings for three stuck streams over six passes, want 1", h.warns)
	}
	e.failPut = false
	m.Retry(ctx)
	e.failPut = true
	m.Resync(ctx)
	if h.warns != 2 {
		t.Fatalf("%d warnings after the engine recovered and failed again, want 2", h.warns)
	}
}

func TestNDIStreamWithAnAddressNeedsNoAnnouncement(t *testing.T) {
	e := newFakeEngine()
	src := &fakeSources{ok: false, names: map[string]bool{}, msg: msgNoAvahi}
	m := newTestManager(e, src)
	spec := StreamSpec{Key: "ndi:A (x)", Kind: KindNDI, NDIName: "A (x)", NDIAddress: "10.0.20.15:5961"}
	m.Set(context.Background(), []StreamSpec{spec})
	if name, addr, ok := m.NDISource(streamID(spec.Key)); !ok || name != "A (x)" || addr != "10.0.20.15:5961" {
		t.Fatalf("NDISource = %q %q %v", name, addr, ok)
	}
	for _, st := range m.Streams(context.Background()) {
		if st.Key == spec.Key && (st.State == StateNotFound || st.State == StateError) {
			t.Fatalf("a source reached by address was judged by discovery: %+v", st)
		}
	}
	moved := spec
	moved.NDIAddress = "10.0.20.16:5961"
	m.Set(context.Background(), []StreamSpec{moved})
	if len(e.deletes) != 1 {
		t.Fatalf("a changed address must replace the stream: deletes=%v", e.deletes)
	}
}
