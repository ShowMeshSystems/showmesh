package previewgateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

var (
	ndiA = StreamSpec{Key: "ndi:A", Kind: KindNDI, NDIName: "A (x)"}
	camB = StreamSpec{Key: "camera:b", Kind: KindRTSP, URL: "rtsp://u:p@h:554/s"}
)

func newTestManager(e *fakeEngine, s *fakeSources) *Manager {
	return NewManager(e, s, "/usr/local/bin/gw", quietLog())
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
	if len(e.puts) != 3 || e.streams[streamID("camera:b")] != changed.URL {
		t.Fatalf("changed stream not replaced: %v %v", e.puts, e.streams)
	}

	m.Set(ctx, []StreamSpec{ndiA})
	if len(e.deletes) != 1 || e.deletes[0] != streamID("camera:b") {
		t.Fatalf("removed stream not deleted: %v", e.deletes)
	}
	if _, _, ok := m.Lookup("camera:b"); ok {
		t.Fatal("removed key still known")
	}
}

func TestNDISourceIsOneExecToken(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	m.Set(context.Background(), []StreamSpec{{Key: "k", Kind: KindNDI, NDIName: "A (x) y"}})
	src := e.streams[streamID("k")]
	want := "exec:/usr/local/bin/gw ndi-pipeline " + EncodeNDIName("A (x) y")
	if src != want || strings.Count(src, " ") != 2 {
		t.Fatalf("source = %q", src)
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

	e.info[streamID("ndi:A")] = Go2RTCInfo{Producers: []map[string]any{{"recv": float64(1200)}}, Consumers: []map[string]any{{}}}
	if s := byKey()["ndi:A"]; s.State != StateLive || s.Message != "" {
		t.Fatalf("live: %+v", s)
	}

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
		io.WriteString(w, "Device found:\n\t\tndi-name = A (x)\n\t\turl-address = 10.0.0.5:5961\n\tgst-launch-1.0 x\n")
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
