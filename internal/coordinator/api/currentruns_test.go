package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
)

type currentRunsReaderFake struct {
	snapshot currentrun.Snapshot
	err      error
}

func (f currentRunsReaderFake) Snapshot(context.Context, time.Time) (currentrun.Snapshot, error) {
	return f.snapshot, f.err
}

func TestCurrentRunsRouteReturnsConcurrentRunnerSetAndExplicitNullNext(t *testing.T) {
	show := "halloween"
	generation := int64(7)
	api := New(Dependencies{CurrentRuns: currentRunsReaderFake{snapshot: currentrun.Snapshot{
		Active: currentrun.ActiveContext{Configured: true, Show: show, Generation: generation},
		Runs: []currentrun.Run{
			{ID: "showmesh-audio:node-a:s-1", Runner: currentrun.RunnerShowmeshAudio, Show: show, Generation: generation,
				Status: "playing", StatusReason: "audio session is playing",
				Playback:  currentrun.Playback{State: "playing", ItemID: "cue-1", Evidence: []currentrun.Evidence{{Signal: "audio_session.state", Value: "playing", State: "current"}}},
				Freshness: currentrun.Freshness{State: "current"}, Reconciliation: currentrun.Reconciliation{State: "resolved", Reason: "bound"},
				Activation: currentrun.Activation{Show: show, Generation: generation, PlaylistID: "music", Revision: 3, Runner: currentrun.RunnerShowmeshAudio},
				Targets:    []currentrun.Target{{Kind: "node", ID: "node-a", Evidence: []currentrun.Evidence{{Signal: "audio_session.state", Value: "playing", State: "current"}}}},
				Next:       &currentrun.Next{ItemID: "cue-2", ItemIndex: 1, Source: "showmesh-audio runner"}},
			{ID: "fpp:player-1", Runner: currentrun.RunnerFPP, Show: show, Generation: generation,
				Status: "idle", StatusReason: "FPP reported no current playlist entry",
				Playback: currentrun.Playback{State: "idle"}, Freshness: currentrun.Freshness{State: "not_collected"},
				Reconciliation: currentrun.Reconciliation{State: "unbound", Reason: "no matching FPP playlist"},
				Activation:     currentrun.Activation{Runner: currentrun.RunnerFPP}, Targets: []currentrun.Target{{Kind: "fpp", ID: "player-1"}}},
		},
	}}}, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/current-runs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var got v1.CurrentRunsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v\n%s", err, body)
	}
	if !got.ActiveShow.Configured || got.ActiveShow.Show == nil || *got.ActiveShow.Show != show || got.ActiveShow.Generation == nil || *got.ActiveShow.Generation != generation {
		t.Fatalf("active show context = %#v", got.ActiveShow)
	}
	if len(got.Runs) != 2 || got.Runs[0].Runner != currentrun.RunnerFPP || got.Runs[1].Runner != currentrun.RunnerShowmeshAudio {
		t.Fatalf("runs = %#v, want deterministic FPP then audio order", got.Runs)
	}
	if got.Runs[1].Next == nil || got.Runs[1].Next.ItemID != "cue-2" {
		t.Fatalf("authoritative next missing: %#v", got.Runs[1].Next)
	}
	if got.Runs[0].Next != nil {
		t.Fatalf("FPP next should be explicit null when unavailable: %#v", got.Runs[0].Next)
	}
	if len(got.Runs[1].Targets) != 1 || len(got.Runs[1].Targets[0].Evidence) != 1 || len(got.Runs[1].Playback.Evidence) != 1 {
		t.Fatalf("target/playback evidence not preserved: %#v", got.Runs[1])
	}
	if string(body) == "" || !containsJSONNull(body, "next") {
		t.Fatalf("response must emit next:null for unavailable authoritative next: %s", body)
	}
}

func TestCurrentRunsStreamEmitsFullFrameOnSubstantiveChange(t *testing.T) {
	r := currentRunsReaderFake{snapshot: currentrun.Snapshot{Runs: []currentrun.Run{{
		ID: "showmesh-audio:node-a:s-1", Runner: currentrun.RunnerShowmeshAudio,
		Status: "playing", Playback: currentrun.Playback{State: "playing"},
	}}}}
	api := newStreamTestAPI(Dependencies{
		CurrentRuns: r, Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})
	id, sub := api.Hub.subscribe(false, nil)
	defer api.Hub.unsubscribe(id)
	// Direct rendering keeps this test focused on the full-frame event shape
	// and avoids making a second stream server just to inspect one frame.
	api.Hub.render(context.Background())
	select {
	case <-sub.frames:
	default:
		t.Fatal("initial non-empty current-runs frame was not queued")
	}
	api.Hub.mu.Lock()
	if len(api.Hub.lastRendered["current-runs"]) == 0 {
		api.Hub.mu.Unlock()
		t.Fatal("current-runs baseline was not recorded")
	}
	api.Hub.mu.Unlock()

	// A value change must produce exactly one full-frame pending event; the
	// stream's reconnect rule still tells clients to refetch the REST route.
	r.snapshot.Runs[0].Status = "stopped"
	api.Hub.deps.CurrentRuns = r
	api.Hub.render(context.Background())
	select {
	case frame := <-sub.frames:
		if frame.event != "currentRuns.changed" || frame.currentRuns == nil || len(frame.currentRuns.Runs) != 1 || frame.currentRuns.Runs[0].Status != "stopped" {
			t.Fatalf("frame = %#v, want full currentRuns.changed replacement", frame)
		}
	default:
		t.Fatal("substantive current-runs change did not queue a full-frame event")
	}
}

func containsJSONNull(body []byte, field string) bool {
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) != nil {
		return false
	}
	runs, ok := decoded["runs"].([]any)
	if !ok || len(runs) == 0 {
		return false
	}
	run, ok := runs[0].(map[string]any)
	if !ok {
		return false
	}
	_, present := run[field]
	return present && run[field] == nil
}
