package preview

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingSettings struct {
	staticSettings
	loads atomic.Int32
	fail  atomic.Bool
}

func (c *countingSettings) Load(ctx context.Context) (Settings, error) {
	c.loads.Add(1)
	if c.fail.Load() {
		return Settings{}, errors.New("store unavailable")
	}
	return c.staticSettings.Load(ctx)
}

func tileKeys(ov Overview) string {
	keys := make([]string, 0, len(ov.Tiles))
	for _, t := range ov.Tiles {
		keys = append(keys, t.Key)
	}
	return strings.Join(keys, "|")
}

func TestChangingTheGatewayAddressDropsTheOldObservation(t *testing.T) {
	a := newFakeGateway(t, "tok")
	a.sources = []Source{{Name: "A-HOST (only-on-a)"}}
	b := newFakeGateway(t, "tok")
	src := &countingSettings{staticSettings: staticSettings{s: Settings{GatewayURL: a.srv.URL, Token: "tok"}}}
	svc := newTestService(src)
	ctx := context.Background()
	svc.Reconcile(ctx)
	if ov, _ := svc.Overview(ctx); ov.Gateway.State != GatewayReachable || len(ov.Tiles) != 1 {
		t.Fatalf("overview for A = %+v", ov)
	}

	src.set(func(s *Settings) { s.GatewayURL = b.srv.URL })
	svc.Notify()
	ov, _ := svc.Overview(ctx)
	if ov.Gateway.State != GatewayConnecting || ov.Gateway.Message == "" || len(ov.Tiles) != 0 {
		t.Fatalf("before B answers: %+v", ov)
	}
	var unavailable *UnavailableError
	if _, err := svc.Snapshot(ctx, "ndi:A-HOST (only-on-a)"); !errors.As(err, &unavailable) || unavailable.Message != msgConnecting {
		t.Fatalf("a proxy call while connecting must refuse with the connecting message: %v", err)
	}
	if len(a.snapshots) != 0 || len(b.snapshots) != 0 {
		t.Fatal("no gateway should have been asked for a frame")
	}

	svc.Reconcile(ctx)
	if ov, _ := svc.Overview(ctx); ov.Gateway.State != GatewayReachable || len(ov.Tiles) != 0 {
		t.Fatalf("after B answers: %+v", ov)
	}
}

func TestChangingTheTokenCountsAsAnotherGateway(t *testing.T) {
	gw := newFakeGateway(t, "new")
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "old"}}
	svc := newTestService(src)
	ctx := context.Background()
	svc.Reconcile(ctx)
	if ov, _ := svc.Overview(ctx); ov.Gateway.State != GatewayUnreachable {
		t.Fatalf("wrong token: %+v", ov)
	}
	src.set(func(s *Settings) { s.Token = "new" })
	svc.Notify()
	if ov, _ := svc.Overview(ctx); ov.Gateway.State != GatewayConnecting {
		t.Fatalf("a new token must not inherit the old state: %+v", ov)
	}
}

func TestFirstAddressIsConnectingNotUnreachable(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &staticSettings{}
	svc := newTestService(src)
	ctx := context.Background()
	svc.Reconcile(ctx)
	src.set(func(s *Settings) { s.GatewayURL, s.Token = gw.srv.URL, "tok" })
	svc.Notify()
	ov, _ := svc.Overview(ctx)
	if ov.Gateway.State != GatewayConnecting {
		t.Fatalf("state = %q, want connecting", ov.Gateway.State)
	}
}

func TestConnectingTilesAreNotErrors(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "a", Name: "A", Enabled: true, URL: "rtsp://h/a"}}}}
	svc := newTestService(src)
	ov, _ := svc.Overview(context.Background())
	if ov.Gateway.State != GatewayConnecting || len(ov.Tiles) != 1 || ov.Tiles[0].State != TileConnecting || ov.Tiles[0].Message != "" {
		t.Fatalf("overview = %+v", ov)
	}
}

func TestANewSourceIsAppendedAtTheEnd(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok", Cameras: []Camera{
		{ID: "b", Name: "B", Enabled: true, URL: "rtsp://h/b"},
		{ID: "c", Name: "C", Enabled: true, URL: "rtsp://h/c"},
	}}}
	svc := newTestService(src)
	ctx := context.Background()
	svc.Reconcile(ctx)
	ov, _ := svc.Overview(ctx)
	if got := tileKeys(ov); got != "camera:b|camera:c" {
		t.Fatalf("startup order = %s", got)
	}

	src.set(func(s *Settings) {
		s.Cameras = append(s.Cameras, Camera{ID: "a", Name: "A", Enabled: true, URL: "rtsp://h/a"})
	})
	svc.Notify()
	ov, _ = svc.Overview(ctx)
	if got := tileKeys(ov); got != "camera:b|camera:c|camera:a" {
		t.Fatalf("before the next pass = %s", got)
	}
	svc.Reconcile(ctx)
	gw.mu.Lock()
	gw.sources = []Source{{Name: "AAA (first)"}}
	gw.mu.Unlock()
	svc.Reconcile(ctx)
	ov, _ = svc.Overview(ctx)
	if got := tileKeys(ov); got != "camera:b|camera:c|camera:a|ndi:AAA (first)" {
		t.Fatalf("after later passes = %s", got)
	}
}

func TestRequestsUseCachedSettingsAndSurviveAStoreFailure(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	src := &countingSettings{staticSettings: staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "a", Name: "A", Enabled: true, URL: "rtsp://h/a"}}}}}
	svc := newTestService(src)
	ctx := context.Background()
	svc.Reconcile(ctx)
	loads := src.loads.Load()
	for range 5 {
		if _, err := svc.Overview(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Snapshot(ctx, "camera:a"); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.loads.Load(); got != loads {
		t.Fatalf("requests loaded settings %d more times", got-loads)
	}

	src.fail.Store(true)
	svc.Notify()
	ov, err := svc.Overview(ctx)
	if err != nil || len(ov.Tiles) != 1 {
		t.Fatalf("a failed reload must keep the last good settings: %+v, %v", ov, err)
	}
}

func TestConcurrentSnapshotsOfOneTileShareOneGatewayCall(t *testing.T) {
	gw := newFakeGateway(t, "tok")
	gw.slow = 150 * time.Millisecond
	src := &staticSettings{s: Settings{GatewayURL: gw.srv.URL, Token: "tok",
		Cameras: []Camera{{ID: "a", Name: "A", Enabled: true, URL: "rtsp://h/a"}}}}
	svc := newTestService(src)
	svc.Reconcile(context.Background())

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Snapshot(context.Background(), "camera:a"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.snapshots) != 1 {
		t.Fatalf("gateway served %d snapshot calls, want 1", len(gw.snapshots))
	}
}
