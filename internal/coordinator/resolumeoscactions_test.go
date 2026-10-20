package coordinator

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/oscfade"
	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// oscMsg is one decoded OSC message with a single int or float argument.
type oscMsg struct {
	addr string
	tag  byte
	i    int32
	f    float32
}

func decodeOSC(t *testing.T, b []byte) oscMsg {
	t.Helper()
	readStr := func() string {
		end := bytes.IndexByte(b, 0)
		if end < 0 {
			t.Fatalf("OSC packet %q has an unterminated string", b)
		}
		s := string(b[:end])
		b = b[(end/4+1)*4:]
		return s
	}
	m := oscMsg{addr: readStr()}
	tags := readStr()
	if len(tags) != 2 || tags[0] != ',' || len(b) != 4 {
		t.Fatalf("OSC message %q has tags %q and %d argument bytes, want one 4-byte argument", m.addr, tags, len(b))
	}
	m.tag = tags[1]
	switch m.tag {
	case 'i':
		m.i = int32(binary.BigEndian.Uint32(b))
	case 'f':
		m.f = math.Float32frombits(binary.BigEndian.Uint32(b))
	default:
		t.Fatalf("OSC message %q has type tag %q", m.addr, m.tag)
	}
	return m
}

// oscListener is a local UDP socket standing in for Arena's OSC input.
type oscListener struct {
	conn net.PacketConn
}

func newOSCListener(t *testing.T) *oscListener {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &oscListener{conn: conn}
}

func (l *oscListener) port() int { return l.conn.LocalAddr().(*net.UDPAddr).Port }

// read returns the next n messages, failing after a short wait.
func (l *oscListener) read(t *testing.T, n int) []oscMsg {
	t.Helper()
	out := make([]oscMsg, 0, n)
	buf := make([]byte, 2048)
	for len(out) < n {
		_ = l.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		k, _, err := l.conn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("read OSC message %d of %d: %v (got %+v)", len(out)+1, n, err, out)
		}
		out = append(out, decodeOSC(t, append([]byte(nil), buf[:k]...)))
	}
	return out
}

// drain discards every message already waiting.
func (l *oscListener) drain() {
	buf := make([]byte, 2048)
	for {
		_ = l.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, _, err := l.conn.ReadFrom(buf); err != nil {
			return
		}
	}
}

func newTestFaderFor(t *testing.T, port int) *oscfade.Fader {
	t.Helper()
	f := oscfade.New("127.0.0.1", port)
	t.Cleanup(f.Close)
	return f
}

// staticCompositionReader serves one composition revision to a CompositionStore.
type staticCompositionReader struct{ json []byte }

func (r staticCompositionReader) CurrentCompositionRevision(context.Context) (int64, []byte, bool, error) {
	return 1, r.json, true, nil
}

// oscTestCompositionStore holds two decks, two layers, a column and a clip on the
// second deck, and one persistent clip.
func oscTestCompositionStore(t *testing.T) *resolume.CompositionStore {
	t.Helper()
	comp := resolumecomp.Composition{
		Decks:           []resolumecomp.Deck{{ID: "1001", Name: "Main"}, {ID: "1002", Name: "Second"}},
		Layers:          []resolumecomp.Layer{{ID: "2001", Index: 0, Name: "Layer A"}, {ID: "2002", Index: 1, Name: "Layer B"}},
		Columns:         []resolumecomp.Column{{ID: "4001", DeckID: "1002", Index: 2}},
		Clips:           []resolumecomp.Clip{{ID: "3001", DeckID: "1002", LayerIndex: 1, ColumnIndex: 2, Name: "Snow"}},
		PersistentClips: []resolumecomp.Clip{{ID: "5001", LayerIndex: 0, Name: "Logo"}},
	}
	raw, err := json.Marshal(comp)
	if err != nil {
		t.Fatal(err)
	}
	cs := &resolume.CompositionStore{}
	if err := cs.Refresh(context.Background(), staticCompositionReader{json: raw}); err != nil {
		t.Fatalf("load composition: %v", err)
	}
	return cs
}

func dispatchOSC(t *testing.T, d api.ResolumeActionDispatcher, action string, params map[string]any) api.ResolumeActionResult {
	t.Helper()
	res, err := d.Dispatch(context.Background(), action, params, time.Now())
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return res
}

func requireUnconfirmableSent(t *testing.T, action string, res api.ResolumeActionResult) {
	t.Helper()
	if res.Outcome != api.ResolumeOutcomeUnconfirmable || !res.Dispatched || res.DispatchedAt == nil {
		t.Fatalf("%s: result = %+v, want unconfirmable and dispatched", action, res)
	}
	if !strings.Contains(res.Reason, "Resolume does not answer OSC") {
		t.Errorf("%s: reason = %q, want it to say Resolume does not answer OSC", action, res.Reason)
	}
}

func TestResolumeOSCActionsSendArenaAddresses(t *testing.T) {
	l := newOSCListener(t)
	osc := newTestFaderFor(t, l.port())
	d := newResolumeOSCActionDispatcher(oscTestCompositionStore(t), osc)

	tests := []struct {
		action string
		params map[string]any
		want   []oscMsg
	}{
		{"launchClip", map[string]any{"clip": "Snow", "deck": "Second"}, []oscMsg{
			{addr: "/composition/decks/2/select", tag: 'i', i: 1},
			{addr: "/composition/layers/2/clips/3/connect", tag: 'i', i: 1},
		}},
		{"selectDeck", map[string]any{"deck": "Second"}, []oscMsg{{addr: "/composition/decks/2/select", tag: 'i', i: 1}}},
		{"launchColumn", map[string]any{"column": "Column 3", "deck": "Second"}, []oscMsg{
			{addr: "/composition/decks/2/select", tag: 'i', i: 1},
			{addr: "/composition/columns/3/connect", tag: 'i', i: 1},
		}},
		{"clearLayer", map[string]any{"layer": "Layer B"}, []oscMsg{{addr: "/composition/layers/2/clear", tag: 'i', i: 1}}},
		{"setLayerBypass", map[string]any{"layer": "Layer A", "bypassed": true}, []oscMsg{{addr: "/composition/layers/1/bypassed", tag: 'i', i: 1}}},
		{"setLayerBypass", map[string]any{"layer": "Layer A", "bypassed": false}, []oscMsg{{addr: "/composition/layers/1/bypassed", tag: 'i', i: 0}}},
		{"setLayerMaster", map[string]any{"layer": "Layer B", "master": 0.25}, []oscMsg{{addr: "/composition/layers/2/master", tag: 'f', f: 0.25}}},
		{"fadeLayer", map[string]any{"layer": "Layer B", "targetPercent": 60.0}, []oscMsg{{addr: "/composition/layers/2/master", tag: 'f', f: 0.6}}},
		{"blackout", map[string]any{}, []oscMsg{
			{addr: "/composition/layers/1/clear", tag: 'i', i: 1},
			{addr: "/composition/layers/2/clear", tag: 'i', i: 1},
		}},
	}
	for _, tt := range tests {
		res := dispatchOSC(t, d, tt.action, tt.params)
		requireUnconfirmableSent(t, tt.action, res)
		got := l.read(t, len(tt.want))
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("%s message %d = %+v, want %+v", tt.action, i, got[i], tt.want[i])
			}
		}
	}
}

func TestResolumeOSCFadeLayerStartsFromLastSentThenFromPercentThenFull(t *testing.T) {
	l := newOSCListener(t)
	d := newResolumeOSCActionDispatcher(oscTestCompositionStore(t), newTestFaderFor(t, l.port()))

	// Never sent: fromPercent is the start.
	dispatchOSC(t, d, "fadeLayer", map[string]any{"layer": "Layer A", "targetPercent": 0.0, "fromPercent": 40.0, "durationMs": 80.0})
	msgs := l.read(t, 2)
	if !near(msgs[0].f, 0.4) {
		t.Errorf("first fade started at %v, want fromPercent 0.4", msgs[0].f)
	}
	waitForLastSent(t, d.osc, 0, 0)
	l.drain()

	// Sent before: the last sent level wins over fromPercent.
	dispatchOSC(t, d, "setLayerMaster", map[string]any{"layer": "Layer A", "master": 0.7})
	l.read(t, 1)
	dispatchOSC(t, d, "fadeLayer", map[string]any{"layer": "Layer A", "targetPercent": 10.0, "fromPercent": 40.0, "durationMs": 80.0})
	if got := l.read(t, 1)[0].f; !near(got, 0.7) {
		t.Errorf("second fade started at %v, want the last sent 0.7", got)
	}

	// Neither: full.
	dispatchOSC(t, d, "fadeLayer", map[string]any{"layer": "Layer B", "targetPercent": 0.0, "durationMs": 80.0})
	for {
		m := l.read(t, 1)[0]
		if m.addr == "/composition/layers/2/master" {
			if !near(m.f, 1) {
				t.Errorf("third fade started at %v, want 1", m.f)
			}
			break
		}
	}
}

// near allows for the ramp's real clock moving before its first message.
func near(got, want float32) bool { return math.Abs(float64(got-want)) < 0.01 }

func waitForLastSent(t *testing.T, f interface{ LastSent(int) (float64, bool) }, layer int, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := f.LastSent(layer); ok && v == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("layer %d never reached %v", layer, want)
}

func TestResolumeOSCRefusesWhatIsNotInTheComposition(t *testing.T) {
	l := newOSCListener(t)
	d := newResolumeOSCActionDispatcher(oscTestCompositionStore(t), newTestFaderFor(t, l.port()))

	for _, tt := range []struct {
		action string
		params map[string]any
	}{
		{"launchClip", map[string]any{"clip": "Missing", "deck": "Second"}},
		{"launchClip", map[string]any{"clip": "Logo", "persistent": true}},
		{"clearLayer", map[string]any{"layer": "Layer Z"}},
		{"selectDeck", map[string]any{"deck": "Nope"}},
		{"launchColumn", map[string]any{"column": "Column 9", "deck": "Second"}},
		{"fadeLayer", map[string]any{"layer": "Layer Z", "targetPercent": 0.0}},
	} {
		res := dispatchOSC(t, d, tt.action, tt.params)
		if res.Outcome != api.ResolumeOutcomeRefused || res.Dispatched || res.Reason == "" {
			t.Errorf("%s %v: result = %+v, want refused with a reason", tt.action, tt.params, res)
		}
	}
	_ = l.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, _, err := l.conn.ReadFrom(make([]byte, 512)); err == nil {
		t.Fatalf("a refused action sent a %d-byte OSC message", n)
	}
}

func TestResolumeOSCBlackoutWithoutACompositionDisconnectsAll(t *testing.T) {
	l := newOSCListener(t)
	d := newResolumeOSCActionDispatcher(&resolume.CompositionStore{}, newTestFaderFor(t, l.port()))
	requireUnconfirmableSent(t, "blackout", dispatchOSC(t, d, "blackout", map[string]any{}))
	if got := l.read(t, 1)[0]; got != (oscMsg{addr: "/composition/disconnectall", tag: 'i', i: 1}) {
		t.Fatalf("blackout sent %+v, want /composition/disconnectall 1", got)
	}
	if res := dispatchOSC(t, d, "clearLayer", map[string]any{"layer": "Layer A"}); res.Outcome != api.ResolumeOutcomeRefused {
		t.Fatalf("clearLayer with no composition = %+v, want refused", res)
	}
}

// countingArena counts every HTTP request, including WebSocket upgrades.
func countingArena(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	product := resolumeProductHandler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		product(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func newZeroHTTPTestManager(t *testing.T) *resolumeManager {
	t.Helper()
	st := openTestStore(t)
	identitySvc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(testLogger()))
	runner := collector.NewRunner(&fppSink{st: st, logger: testLogger()}, testLogger())
	cfg := config.Config{ResolumePollInterval: 50 * time.Millisecond}
	mgr := newResolumeManager(cfg, runner, oscTestCompositionStore(t), st, identitySvc, testLogger(), func() {})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go runner.Run(ctx)
	return mgr
}

func TestResolumeOSCControlNeverContactsArenasWebServer(t *testing.T) {
	srv, requests := countingArena(t)
	l := newOSCListener(t)
	mgr := newZeroHTTPTestManager(t)
	ctx := context.Background()

	mgr.reconcile(ctx, []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL, OSCPort: l.port(), Control: config.ResolumeControlOSC}})
	mgr.SetWebSocketEnabled(true)

	for _, a := range []struct {
		action string
		params map[string]any
	}{
		{"launchClip", map[string]any{"clip": "Snow", "deck": "Second"}},
		{"selectDeck", map[string]any{"deck": "Main"}},
		{"launchColumn", map[string]any{"column": "Column 3", "deck": "Second"}},
		{"clearLayer", map[string]any{"layer": "Layer A"}},
		{"setLayerBypass", map[string]any{"layer": "Layer A", "bypassed": true}},
		{"setLayerMaster", map[string]any{"layer": "Layer A", "master": 0.5}},
		{"fadeLayer", map[string]any{"layer": "Layer B", "targetPercent": 0.0}},
		{"blackout", map[string]any{}},
	} {
		res, err := mgr.Dispatch(ctx, a.action, a.params, time.Now())
		if err != nil || res.Outcome != api.ResolumeOutcomeUnconfirmable {
			t.Fatalf("%s: result %+v, err %v, want unconfirmable", a.action, res, err)
		}
	}
	if _, err := mgr.Restore(ctx, "operator"); err == nil {
		t.Errorf("Restore succeeded over OSC control, want it refused")
	}
	if mgr.Configured() {
		t.Errorf("Configured() = true, want false so recovery reads as unavailable")
	}

	// Several poll intervals and one WebSocket supervisor tick.
	time.Sleep(resolumeWebSocketSupervisorInterval + 500*time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("Arena's web server got %d requests with control osc, want 0", got)
	}

	views, err := mgr.ListInstances(ctx)
	if err != nil || len(views) != 1 || !views[0].ControlledOverOSC {
		t.Fatalf("ListInstances = %+v, %v, want one OSC-controlled view", views, err)
	}
	for _, o := range views[0].Observations {
		if o.StateAt(time.Now()) != observation.StateNotCollected || o.Reason != resolumeOSCNotReadReason {
			t.Errorf("observation %s = %+v, want not collected because of OSC control", o.Signal, o)
		}
	}
	statuses, _ := mgr.CollectorStatuses(ctx)
	if len(statuses) != 1 || statuses[0].State != string(api.CollectorNotConfigured) || statuses[0].Reason == nil || *statuses[0].Reason != resolumeOSCNotReadReason {
		t.Fatalf("CollectorStatuses = %+v, want not_configured because of OSC control", statuses)
	}
}

func TestResolumeControlAbsentStillPollsArenaOverREST(t *testing.T) {
	srv, requests := countingArena(t)
	mgr := newZeroHTTPTestManager(t)
	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL}})

	waitForObservation(t, mgr.st, "arena-1", "resolume.reachable", 5*time.Second)
	if requests.Load() == 0 {
		t.Fatalf("Arena's web server got no requests with control absent, want the REST collector polling it")
	}
	if !mgr.Configured() {
		t.Fatalf("Configured() = false with control absent, want true")
	}
	views, _ := mgr.ListInstances(context.Background())
	if len(views) != 1 || views[0].ControlledOverOSC {
		t.Fatalf("ListInstances = %+v, want one REST view", views)
	}
}
