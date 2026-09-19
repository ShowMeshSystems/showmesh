package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// mutableWeatherDelayStore is a minimal, directly settable
// [WeatherDelayStore] for a test that needs to drive a genuine state
// transition rather than only read a fixed fixture.
type mutableWeatherDelayStore struct {
	mu  sync.Mutex
	rec store.WeatherDelayStateRecord
}

func (s *mutableWeatherDelayStore) GetWeatherDelayState(context.Context) (store.WeatherDelayStateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec, nil
}

func (s *mutableWeatherDelayStore) SetWeatherDelayState(_ context.Context, rec store.WeatherDelayStateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec = rec
	return nil
}

// TestOpenAPIWeatherDelayChangedEventMatchesRealFrame is
// [TestOpenAPIStreamEventSchemasMatchRealFrames]'s own sibling for
// weatherDelay.changed: a real frame, obtained over a live stream
// connection from a genuine start transition, validated against
// WeatherDelayChangedEvent.
func TestOpenAPIWeatherDelayChangedEventMatchesRealFrame(t *testing.T) {
	c := newOpenAPICompiler(t)

	wd := &mutableWeatherDelayStore{}
	testAPI := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{}, WeatherDelay: wd,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go testAPI.Hub.Run(ctx)

	srv := httptest.NewServer(testAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start", event)
	}

	if err := wd.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{
		Active: true, Kind: "delay", StartedAt: testNow, StartedBy: "op-1", Revision: 1,
	}); err != nil {
		t.Fatalf("set weather delay state: %v", err)
	}
	testAPI.Hub.Notify()

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "weatherDelay.changed" {
		t.Fatalf("event = %q, want weatherDelay.changed", event)
	}
	assertMatchesSchema(t, c, "WeatherDelayChangedEvent", []byte(data))
}
