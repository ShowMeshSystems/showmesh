package preview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type fakeGateway struct {
	mu         sync.Mutex
	token      string
	sources    []Source
	states     []StreamState
	puts       [][]Stream
	offers     []string
	snapshots  []string
	down       bool
	refuseRTSP bool
	discovery  *DiscoveryStatus
	redirect   string
	slow       time.Duration
	srv        *httptest.Server
}

func newFakeGateway(t *testing.T, token string) *fakeGateway {
	t.Helper()
	g := &fakeGateway{token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sources", func(w http.ResponseWriter, r *http.Request) { g.reply(w, r, g.sources) })
	mux.HandleFunc("PUT /v1/streams", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(w, r) {
			return
		}
		var set []Stream
		_ = json.NewDecoder(r.Body).Decode(&set)
		g.mu.Lock()
		g.puts = append(g.puts, set)
		refuse := g.refuseRTSP
		g.mu.Unlock()
		if refuse {
			for _, st := range set {
				if st.Kind == "rtsp" {
					http.Error(w, "bad stream "+st.URL, http.StatusBadRequest)
					return
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/streams", func(w http.ResponseWriter, r *http.Request) { g.reply(w, r, g.states) })
	mux.HandleFunc("GET /v1/discovery", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		d := g.discovery
		g.mu.Unlock()
		if d == nil {
			http.NotFound(w, r)
			return
		}
		g.reply(w, r, d)
	})
	mux.HandleFunc("POST /v1/streams/{key}/webrtc", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(w, r) {
			return
		}
		time.Sleep(g.slow)
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.offers = append(g.offers, r.PathValue("key")+"|"+string(b))
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/sdp")
		_, _ = w.Write([]byte("v=0 answer"))
	})
	mux.HandleFunc("GET /v1/streams/{key}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !g.authorized(w, r) {
			return
		}
		time.Sleep(g.slow)
		if r.PathValue("key") == "camera:nofeed" {
			http.Error(w, "no frame yet", http.StatusNotFound)
			return
		}
		g.mu.Lock()
		g.snapshots = append(g.snapshots, r.PathValue("key"))
		g.mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8frame"))
	})
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		down, redirect := g.down, g.redirect
		g.mu.Unlock()
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
			return
		}
		if down {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+g.token {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return false
	}
	return true
}

func (g *fakeGateway) reply(w http.ResponseWriter, r *http.Request, v any) {
	if !g.authorized(w, r) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_ = json.NewEncoder(w).Encode(v)
}

func (g *fakeGateway) lastPut() []Stream {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.puts) == 0 {
		return nil
	}
	return g.puts[len(g.puts)-1]
}

type staticSettings struct {
	mu sync.Mutex
	s  Settings
}

func (s *staticSettings) Load(context.Context) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s, nil
}

func (s *staticSettings) set(f func(*Settings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.s)
}

func newTestService(src SettingsSource) *Service {
	return NewService(src, nil, func() time.Time { return testNow }, nil)
}

func tileByKey(t *testing.T, ov Overview, key string) Tile {
	t.Helper()
	for _, tile := range ov.Tiles {
		if tile.Key == key {
			return tile
		}
	}
	t.Fatalf("no tile %q in %+v", key, ov.Tiles)
	return Tile{}
}

func TestReconcileSendsTheFullDesiredSetAndAuthenticates(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "NODE-01 (matrix-main)"}, {Name: "ARENA (Composition)"}}
	src := &staticSettings{s: Settings{
		GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{
			{ID: "driveway", Name: "Driveway", Enabled: true, URL: "rtsp://192.168.1.20:37379/a"},
			{ID: "porch", Name: "Porch", Enabled: false, URL: "rtsp://192.168.1.21/b"},
		},
	}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())

	got := gw.lastPut()
	want := []Stream{
		{Key: "camera:driveway", Kind: "rtsp", URL: "rtsp://192.168.1.20:37379/a"},
		{Key: "ndi:ARENA (Composition)", Kind: "ndi", NDIName: "ARENA (Composition)"},
		{Key: "ndi:NODE-01 (matrix-main)", Kind: "ndi", NDIName: "NODE-01 (matrix-main)"},
	}
	if len(got) != len(want) {
		t.Fatalf("desired set = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("desired[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	ov, err := svc.Overview(context.Background())
	if err != nil || ov.Gateway.State != GatewayReachable {
		t.Fatalf("overview = %+v, %v", ov.Gateway, err)
	}
}

func TestReconcileFollowsConfigurationChanges(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok"}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	if len(gw.lastPut()) != 0 {
		t.Fatalf("first set = %+v, want empty", gw.lastPut())
	}
	src.set(func(s *Settings) { s.Cameras = []Camera{{ID: "a", Name: "A", Enabled: true, URL: "rtsp://h/a"}} })
	gw.sources = []Source{{Name: "X (y)"}}
	svc.Reconcile(context.Background())
	if len(gw.lastPut()) != 2 {
		t.Fatalf("second set = %+v, want a camera and a source", gw.lastPut())
	}
	src.set(func(s *Settings) { s.Cameras[0].Enabled = false })
	svc.Reconcile(context.Background())
	if len(gw.lastPut()) != 1 {
		t.Fatalf("a disabled camera must leave the set: %+v", gw.lastPut())
	}
}

func TestRunReconcilesOnNotify(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok"}}
	svc := newTestService(src)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, func() bool { gw.mu.Lock(); defer gw.mu.Unlock(); return len(gw.puts) >= 1 })
	src.set(func(s *Settings) { s.Cameras = []Camera{{ID: "a", Name: "A", Enabled: true, URL: "rtsp://h/a"}} })
	svc.Notify()
	waitFor(t, func() bool { return len(gw.lastPut()) == 1 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNotConfiguredHasNoTilesAndRefusesProxying(t *testing.T) {
	svc := newTestService(&staticSettings{})
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())
	if ov.Gateway.State != GatewayNotConfigured || len(ov.Tiles) != 0 || ov.Tiles == nil {
		t.Fatalf("overview = %+v", ov)
	}
	var unavailable *UnavailableError
	if _, err := svc.Offer(context.Background(), "camera:a", []byte("o")); !errors.As(err, &unavailable) {
		t.Fatalf("offer err = %v, want unavailable", err)
	}
	if _, err := svc.Snapshot(context.Background(), "camera:a"); !errors.As(err, &unavailable) {
		t.Fatalf("snapshot err = %v, want unavailable", err)
	}
}

func TestUnreachableGatewayKeepsCameraTilesAndSaysWhatToDo(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "NODE-01 (matrix-main)"}}
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "driveway", Name: "Driveway", Enabled: true, URL: "rtsp://h/a"}}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	gw.mu.Lock()
	gw.down = true
	gw.mu.Unlock()
	svc.Reconcile(context.Background())

	ov, _ := svc.Overview(context.Background())
	if ov.Gateway.State != GatewayUnreachable || !strings.Contains(ov.Gateway.Message, "cannot be reached") {
		t.Fatalf("gateway = %+v", ov.Gateway)
	}
	cam := tileByKey(t, ov, "camera:driveway")
	ndi := tileByKey(t, ov, "ndi:NODE-01 (matrix-main)")
	if cam.State != TileError || ndi.State != TileError || cam.Message == "" {
		t.Fatalf("tiles = %+v / %+v", cam, ndi)
	}
	if _, err := svc.Snapshot(context.Background(), "camera:driveway"); err == nil || !strings.Contains(err.Error(), "cannot be reached") {
		t.Fatalf("snapshot err = %v", err)
	}
}

func TestWrongTokenIsReportedAsUnreachable(t *testing.T) {
	gw := newFakeGateway(t, "right")
	svc := newTestService(&staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "wrong"}})
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())
	if ov.Gateway.State != GatewayUnreachable {
		t.Fatalf("gateway = %+v", ov.Gateway)
	}
}

func TestTileStatesComeFromTheGateway(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "A (one)"}, {Name: "B (two)"}}
	gw.states = []StreamState{
		{Key: "ndi:A (one)", State: "live"},
		{Key: "camera:cam", State: "not_found", Message: "rtsp: dial tcp refused"},
		{Key: "camera:bad", State: "error", Message: ""},
	}
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok", Cameras: []Camera{
		{ID: "cam", Name: "Cam", Enabled: true, URL: "rtsp://h/1"},
		{ID: "bad", Name: "Bad", Enabled: true, URL: "rtsp://h/2"},
		{ID: "off", Name: "Off", Enabled: false, URL: "rtsp://h/3"},
	}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())

	if got := tileByKey(t, ov, "ndi:A (one)"); got.State != TileLive || got.Message != "" {
		t.Fatalf("live tile = %+v", got)
	}
	if got := tileByKey(t, ov, "ndi:B (two)"); got.State != TileConnecting {
		t.Fatalf("a stream the gateway has not reported yet is connecting: %+v", got)
	}
	cam := tileByKey(t, ov, "camera:cam")
	if cam.State != TileNotFound || strings.Contains(cam.Message, "dial") || cam.Message == "" {
		t.Fatalf("not_found camera = %+v (the gateway's own text must not reach the operator)", cam)
	}
	if got := tileByKey(t, ov, "camera:bad"); got.State != TileError || got.Message == "" {
		t.Fatalf("error tile = %+v", got)
	}
	if got := tileByKey(t, ov, "camera:off"); got.State != TileDisabled || got.Message == "" {
		t.Fatalf("disabled tile = %+v", got)
	}
}

func TestTilesFollowLayoutThenStableOrderAndMarkHidden(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "ZED (z)"}, {Name: "ARENA (Composition)"}, {Name: "NODE-01 (matrix-main)"}}
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "driveway", Name: "Driveway", Enabled: true, URL: "rtsp://h/1"}},
		Layout: Layout{
			Order:  []string{"ndi:NODE-01 (matrix-main)", "camera:driveway", "camera:removed"},
			Hidden: []string{"ndi:ARENA (Composition)"},
		}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())

	var keys []string
	for _, tile := range ov.Tiles {
		keys = append(keys, tile.Key)
	}
	want := []string{"ndi:NODE-01 (matrix-main)", "camera:driveway", "ndi:ARENA (Composition)", "ndi:ZED (z)"}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v", keys, want)
	}
	if !tileByKey(t, ov, "ndi:ARENA (Composition)").Hidden || tileByKey(t, ov, "camera:driveway").Hidden {
		t.Fatalf("hidden flags wrong: %+v", ov.Tiles)
	}
}

func TestANamedSourceThatIsNoLongerAnnouncedStaysAsNotFound(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Layout: Layout{Order: []string{"ndi:ARENA (Composition)"}}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())
	got := tileByKey(t, ov, "ndi:ARENA (Composition)")
	if got.State != TileNotFound || got.Message == "" {
		t.Fatalf("tile = %+v", got)
	}
}

func TestNDITilesAreLabelledWithNodeAndSurface(t *testing.T) {
	surfaces := []Surface{
		{NodeID: "node-01", SurfaceID: "matrix-main", SourceName: "matrix-main"},
		{NodeID: "node-02", SurfaceID: "matrix-left", SourceName: "matrix-left"},
		{NodeID: "node-03", SurfaceID: "wall-a", SourceName: "wall"},
		{NodeID: "node-04", SurfaceID: "wall-b", SourceName: "Wall"},
	}
	nodes := []Node{{ID: "node-03"}, {ID: "node-04", Names: []string{"Showhost4"}}}
	cases := []struct {
		name, label, node, surface string
	}{
		{"NODE-01 (matrix-main)", "node-01 · matrix-main", "node-01", "matrix-main"},
		{"whatever (MATRIX-LEFT)", "node-02 · matrix-left", "node-02", "matrix-left"},
		{"NODE-03 (wall)", "node-03 · wall-a", "node-03", "wall-a"},
		{"showhost4 (wall)", "node-04 · wall-b", "node-04", "wall-b"},
		{"OTHER (wall)", "OTHER (wall)", "", ""},
		{"ARENA (Composition)", "ARENA (Composition)", "", ""},
		{"no parentheses", "no parentheses", "", ""},
	}
	for _, tc := range cases {
		label, node, surface := labelNDI(tc.name, surfaces, nodes)
		if label != tc.label || node != tc.node || surface != tc.surface {
			t.Errorf("labelNDI(%q) = %q %q %q, want %q %q %q", tc.name, label, node, surface, tc.label, tc.node, tc.surface)
		}
	}
}

func TestOfferAndSnapshotAreProxiedWithTheTilesKey(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "NODE-01 (matrix-main)"}}
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "nofeed", Name: "No feed", Enabled: true, URL: "rtsp://h/1"}}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	ctx := context.Background()

	answer, err := svc.Offer(ctx, "ndi:NODE-01 (matrix-main)", []byte("v=0 offer"))
	if err != nil || string(answer) != "v=0 answer" {
		t.Fatalf("offer = %q, %v", answer, err)
	}
	if len(gw.offers) != 1 || gw.offers[0] != "ndi:NODE-01 (matrix-main)|v=0 offer" {
		t.Fatalf("gateway saw %v", gw.offers)
	}
	frame, err := svc.Snapshot(ctx, "ndi:NODE-01 (matrix-main)")
	if err != nil || !strings.HasPrefix(string(frame), "\xff\xd8") {
		t.Fatalf("snapshot = %q, %v", frame, err)
	}
	if _, err := svc.Snapshot(ctx, "ndi:NOT ANNOUNCED (x)"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key err = %v", err)
	}
	if _, err := svc.Offer(ctx, "camera:missing", []byte("o")); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key err = %v", err)
	}
	var unavailable *UnavailableError
	if _, err := svc.Snapshot(ctx, "camera:nofeed"); !errors.As(err, &unavailable) || strings.Contains(err.Error(), "no frame yet") {
		t.Fatalf("a gateway refusal must become an operator message: %v", err)
	}
}

func TestRefusedCameraAddressDoesNotMakeTheWholeWallUnreachable(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.refuseRTSP = true
	gw.sources = []Source{{Name: "NODE-01 (matrix-main)"}}
	gw.states = []StreamState{{Key: "ndi:NODE-01 (matrix-main)", State: TileLive}}
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "driveway", Name: "Driveway", Enabled: true, URL: "rtsp://secret-host/a"}}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())

	ov, _ := svc.Overview(context.Background())
	if ov.Gateway.State != GatewayReachable {
		t.Fatalf("gateway = %+v, want reachable", ov.Gateway)
	}
	if ndi := tileByKey(t, ov, "ndi:NODE-01 (matrix-main)"); ndi.State != TileLive {
		t.Fatalf("ndi tile = %+v, want live", ndi)
	}
	cam := tileByKey(t, ov, "camera:driveway")
	if cam.State != TileError || cam.Message != msgCameraRefused {
		t.Fatalf("camera tile = %+v", cam)
	}
	if last := gw.lastPut(); len(last) != 1 || last[0].Kind != "ndi" {
		t.Fatalf("last stream set = %+v, want the NDI stream only", last)
	}
}

func TestGatewayTileErrorTextIsNotRelayed(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.states = []StreamState{{Key: "camera:driveway", State: TileError, Message: "dial rtsp://u:p@10.0.0.9/a failed"}}
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "driveway", Name: "Driveway", Enabled: true, URL: "rtsp://10.0.0.9/a"}}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())
	if got := tileByKey(t, ov, "camera:driveway"); got.Message != msgTileError {
		t.Fatalf("message = %q, want %q", got.Message, msgTileError)
	}
}

func TestGatewayRedirectsAreNotFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect target was fetched: %s %s", r.Method, r.URL)
	}))
	t.Cleanup(target.Close)
	gw := newFakeGateway(t, "tok")
	gw.redirect = target.URL + "/x"
	svc := newTestService(&staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok"}})
	svc.Reconcile(context.Background())
	ov, _ := svc.Overview(context.Background())
	if ov.Gateway.State != GatewayUnreachable {
		t.Fatalf("gateway = %+v", ov.Gateway)
	}
}

func TestUnreachableGatewayIsLoggedOnlyWhenItBecomesUnreachable(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	gw := newFakeGateway(t, "tok")
	gw.down = true
	svc := NewService(&staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok"}}, nil, func() time.Time { return testNow }, logger)
	for range 3 {
		svc.Reconcile(context.Background())
	}
	if n := strings.Count(buf.String(), "gateway unreachable"); n != 1 {
		t.Fatalf("logged %d times, want 1:\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "down") && strings.Contains(buf.String(), "502: down") {
		t.Fatalf("gateway reply text was logged:\n%s", buf.String())
	}
}

func slowCallService(gw *fakeGateway, snapshot, webrtc time.Duration) *Service {
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok"}}
	return NewService(src, func(url, token string) *Client {
		c := NewClient(url, token, nil)
		c.snapshotTimeout, c.webrtcTimeout = snapshot, webrtc
		return c
	}, func() time.Time { return testNow }, nil)
}

func TestSlowSnapshotWaitsLongerThanControlCalls(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "NODE-01 (matrix-main)"}}
	gw.slow = 150 * time.Millisecond
	svc := slowCallService(gw, 2*time.Second, 2*time.Second)
	svc.Reconcile(context.Background())
	key := "ndi:NODE-01 (matrix-main)"
	if frame, err := svc.Snapshot(context.Background(), key); err != nil || !strings.HasPrefix(string(frame), "\xff\xd8") {
		t.Fatalf("slow snapshot = %q, %v", frame, err)
	}
	if answer, err := svc.Offer(context.Background(), key, []byte("o")); err != nil || string(answer) != "v=0 answer" {
		t.Fatalf("slow offer = %q, %v", answer, err)
	}
}

func TestSlowSnapshotAndOfferTimeOutWithoutMarkingTheGatewayUnreachable(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.sources = []Source{{Name: "NODE-01 (matrix-main)"}}
	svc := slowCallService(gw, 50*time.Millisecond, 50*time.Millisecond)
	svc.Reconcile(context.Background())
	gw.mu.Lock()
	gw.slow = 300 * time.Millisecond
	gw.mu.Unlock()
	key := "ndi:NODE-01 (matrix-main)"
	calls := map[string]func() error{
		"snapshot": func() error { _, err := svc.Snapshot(context.Background(), key); return err },
		"offer":    func() error { _, err := svc.Offer(context.Background(), key, []byte("o")); return err },
	}
	for name, call := range calls {
		var unavailable *UnavailableError
		if err := call(); !errors.As(err, &unavailable) || unavailable.Message != msgSlowStart {
			t.Fatalf("%s timeout err = %v", name, err)
		}
	}
	ov, err := svc.Overview(context.Background())
	if err != nil || ov.Gateway.State != GatewayReachable {
		t.Fatalf("gateway state = %q, %v", ov.Gateway.State, err)
	}
}

func TestOverviewReportsNDIDiscoveryOnlyWhenTheGatewaySaysSo(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	svc := newTestService(&staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok"}})
	discovery := func() *NDIDiscovery {
		t.Helper()
		svc.Reconcile(context.Background())
		ov, err := svc.Overview(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return ov.Gateway.NDIDiscovery
	}

	if d := discovery(); d != nil {
		t.Fatalf("an older gateway without the route reported %+v", d)
	}
	gw.mu.Lock()
	gw.discovery = &DiscoveryStatus{Message: "The NDI runtime is named libndi.so. Rename or link it to libndi.so.6."}
	gw.mu.Unlock()
	if d := discovery(); d == nil || d.State != NDIDiscoveryNotRunning || d.Message != "The NDI runtime is named libndi.so. Rename or link it to libndi.so.6." {
		t.Fatalf("not running: %+v", d)
	}
	gw.mu.Lock()
	gw.discovery = &DiscoveryStatus{}
	gw.mu.Unlock()
	if d := discovery(); d == nil || d.State != NDIDiscoveryNotRunning || d.Message != msgNoDiscovery {
		t.Fatalf("not running without a reason: %+v", d)
	}
	gw.mu.Lock()
	gw.discovery = &DiscoveryStatus{Running: true}
	gw.mu.Unlock()
	if d := discovery(); d == nil || d.State != NDIDiscoveryRunning || d.Message != "" {
		t.Fatalf("running: %+v", d)
	}
	gw.mu.Lock()
	gw.down = true
	gw.mu.Unlock()
	if d := discovery(); d != nil {
		t.Fatalf("an unreachable gateway reported %+v", d)
	}
}
