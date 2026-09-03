package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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

type currentRunsAudioListerFake struct {
	observations map[string][]observation.Observation
}

func (f currentRunsAudioListerFake) NodeAudioObservations(nodeID string) []observation.Observation {
	return f.observations[nodeID]
}

func newCurrentRunsProductionReader(t *testing.T, observations []observation.Observation) CurrentRunsReader {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	putShowForTest(t, st, "halloween", "Halloween")
	putActiveShowForTest(t, st, "halloween")
	playlist, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: "halloween", Name: "Background", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		ShowmeshAudio: &config.ShowPlaylistShowmeshAudio{Repeat: config.ShowPlaylistShowmeshAudioRepeatAll},
		Entries:       []config.ShowPlaylistEntry{{ID: "cue-1", Cue: "cue-1"}},
	})
	if err != nil {
		t.Fatalf("encode audio playlist: %v", err)
	}
	putConfigForTest(t, st, config.ShowPlaylistConfigKind, "background", playlist)
	return NewCurrentRunsReader(Dependencies{
		Config: st,
		Nodes:  &fakeNodeLister{views: []inventory.NodeView{{NodeID: "audio-01"}}},
		Audio:  currentRunsAudioListerFake{observations: map[string][]observation.Observation{"audio-01": observations}},
	})
}

func currentRunsAudioObservation(t *testing.T, sessionID string, signal observation.SignalID, value any, at time.Time, collectedAt ...time.Time) observation.Observation {
	t.Helper()
	collected := at
	if len(collectedAt) != 0 {
		collected = collectedAt[0]
	}
	res := observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID}
	o, err := observation.Measured(res, signal, value, at,
		observation.WithSource("nodeaudio/audio-01/"+sessionID),
		observation.WithCollectedAt(collected), observation.WithValidFor(time.Minute))
	if err != nil {
		t.Fatalf("build audio observation %s: %v", signal, err)
	}
	return o
}

func currentRunsAudioAbsence(t *testing.T, sessionID string, signal observation.SignalID, reason string, collected time.Time) observation.Observation {
	t.Helper()
	res := observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID}
	o, err := observation.NotCollected(res, signal, reason,
		observation.WithSource("nodeaudio/audio-01/"+sessionID), observation.WithCollectedAt(collected))
	if err != nil {
		t.Fatalf("build absent audio observation %s: %v", signal, err)
	}
	return o
}

func currentRunsAudioRun(t *testing.T, snap currentrun.Snapshot, id string) currentrun.Run {
	t.Helper()
	for _, run := range snap.Runs {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("run %q not found in %#v", id, snap.Runs)
	return currentrun.Run{}
}

// --- FPP runs: schemaV29's marker outranks Reconcile (owner ruling 2026-09-02) ---

// newFPPCurrentRunsProductionReader wires a real store through appendFPPRuns
// exactly as the production route does: FPPObservations and
// FPPReconciliation both real, mirroring failToBlackComposedSetup's own
// FPP-side wiring one file over.
func newFPPCurrentRunsProductionReader(t *testing.T) (currentRunsReader, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	putShowForTest(t, st, "halloween", "Halloween")
	putActiveShowForTest(t, st, "halloween")
	putAudioOnlyCueForTest(t, st, "cue-1", "halloween")
	putPlaylistForTest(t, st, "playlist-1", config.ShowPlaylistPayload{
		Show: "halloween", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "cue-1",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	})
	deps := Dependencies{
		Config: st, FPP: &fakeFPPLister{}, FPPObservations: st,
		FPPReconciliation: StoreFPPReconciliation{Store: st},
		Nodes:             &fakeNodeLister{}, Audio: currentRunsAudioListerFake{},
	}.withDefaults()
	return currentRunsReader{deps: deps}, st
}

// TestCurrentRunsFPPRunReportsEvidenceBrokenReconciliationState is #301's
// own primary Show Night surface (cue-deactivate-on-jump proposal §0a):
// once schemaV29's marker is set, GET /current-runs's per-run
// reconciliation collapses to "evidence-broken", outranking whatever
// fppreconcile.Reconcile would otherwise report for the same
// (now-possibly-stale) row — the identical precedence rule
// cueactivate.Decide applies for dispatch, restated here for display.
func TestCurrentRunsFPPRunReportsEvidenceBrokenReconciliationState(t *testing.T) {
	reader, st := newFPPCurrentRunsProductionReader(t)
	ctx := context.Background()

	entryKey, err := config.DerivePlaylistEntryKey(config.ShowPlaylistPayload{
		Show: "halloween", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "cue-1",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}, "entry-1")
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	if err := st.PutFPPPlaylistEntryObservation(ctx, store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "inst-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		PlaylistName: "Main", PlaylistHash: hash64ForTest("a1"),
		Section: "mainPlaylist", Position: 0, EntryKey: entryKey,
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	before, err := reader.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatalf("snapshot before marking evidence broken: %v", err)
	}
	if run := currentRunsAudioRun(t, before, "fpp:inst-1"); run.Reconciliation.State != "resolved" {
		t.Fatalf("precondition failed: reconciliation.state = %q, want resolved before marking evidence broken", run.Reconciliation.State)
	}

	brokenAt := testNow.Add(time.Second)
	if err := st.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, "inst-1", brokenAt); err != nil {
		t.Fatalf("mark evidence broken: %v", err)
	}

	after, err := reader.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatalf("snapshot after marking evidence broken: %v", err)
	}
	run := currentRunsAudioRun(t, after, "fpp:inst-1")
	if run.Reconciliation.State != "evidence-broken" {
		t.Fatalf("reconciliation.state = %q, want evidence-broken", run.Reconciliation.State)
	}
	if run.Reconciliation.Reason == "" {
		t.Error("reconciliation.reason is empty, want it to name the discontinuity")
	}
}

// TestCurrentRunsFPPRunReportsOperatorInstructionForMismatch is the
// GET /current-runs half of the mismatch-notice ruling: a playlist edited
// mid-show without an FPP restart reconciles to stale-import (one of
// H0.2's four mismatch outcomes), and this route's reconciliation must
// carry the same operatorInstruction the reconciliation route does. A
// notice only -- dispatch is untouched by this test.
func TestCurrentRunsFPPRunReportsOperatorInstructionForMismatch(t *testing.T) {
	reader, st := newFPPCurrentRunsProductionReader(t)
	ctx := context.Background()

	// The playlist was edited mid-show without an FPP restart: a hash that
	// does not match the bound hash64ForTest("a1") observed for the entry.
	staleHash := hash64ForTest("edited")
	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: staleHash, Section: "mainPlaylist", Position: 0,
	})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	if err := st.PutFPPPlaylistEntryObservation(ctx, store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "inst-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		PlaylistName: "Main", PlaylistHash: staleHash,
		Section: "mainPlaylist", Position: 0, EntryKey: entryKey,
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	snap, err := reader.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	run := currentRunsAudioRun(t, snap, "fpp:inst-1")
	if run.Reconciliation.State != "stale-import" {
		t.Fatalf("reconciliation.state = %q, want stale-import", run.Reconciliation.State)
	}
	const wantInstruction = "Restart FPP, or re-import the playlist so the coordinator's binding and FPP agree."
	if run.Reconciliation.OperatorInstruction != wantInstruction {
		t.Fatalf("reconciliation.operatorInstruction = %q, want %q", run.Reconciliation.OperatorInstruction, wantInstruction)
	}

	// Assert the wire shape too, field name and all, not just the internal
	// currentrun.Reconciliation struct.
	v1run := mapCurrentRun(run)
	if v1run.Reconciliation.OperatorInstruction != wantInstruction {
		t.Fatalf("v1.CurrentReconciliation.OperatorInstruction = %q, want %q", v1run.Reconciliation.OperatorInstruction, wantInstruction)
	}
}

// TestCurrentRunsFPPRunOmitsOperatorInstructionWhenResolved proves the
// additive field stays absent for a resolved (non-mismatched) run, on the
// wire, field name and all.
func TestCurrentRunsFPPRunOmitsOperatorInstructionWhenResolved(t *testing.T) {
	reader, st := newFPPCurrentRunsProductionReader(t)
	ctx := context.Background()

	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash64ForTest("a1"), Section: "mainPlaylist", Position: 0,
	})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	if err := st.PutFPPPlaylistEntryObservation(ctx, store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "inst-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		PlaylistName: "Main", PlaylistHash: hash64ForTest("a1"),
		Section: "mainPlaylist", Position: 0, EntryKey: entryKey,
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	snap, err := reader.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	run := currentRunsAudioRun(t, snap, "fpp:inst-1")
	if run.Reconciliation.State != "resolved" {
		t.Fatalf("reconciliation.state = %q, want resolved", run.Reconciliation.State)
	}
	if run.Reconciliation.OperatorInstruction != "" {
		t.Fatalf("reconciliation.operatorInstruction = %q, want empty for a resolved run", run.Reconciliation.OperatorInstruction)
	}

	raw, err := json.Marshal(mapCurrentRun(run).Reconciliation)
	if err != nil {
		t.Fatalf("marshal reconciliation: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal reconciliation: %v", err)
	}
	if _, present := decoded["operatorInstruction"]; present {
		t.Fatalf("operatorInstruction present in serialized JSON for a resolved run: %s", raw)
	}
}

func TestCurrentRunsProductionSeparatesConcurrentBackgroundAndAnnouncement(t *testing.T) {
	old := testNow.Add(-2 * time.Second)
	observations := []observation.Observation{
		currentRunsAudioObservation(t, "background", "audio_session.source_role", "background", old),
		currentRunsAudioObservation(t, "background", "audio_session.playlist.revision", int64(1), old),
		currentRunsAudioObservation(t, "background", "audio_session.state", "playing", old),
		currentRunsAudioObservation(t, "announcement", "audio_session.source_role", "announcement", old),
		currentRunsAudioObservation(t, "announcement", "audio_session.state", "playing", old),
	}

	snapshot, err := newCurrentRunsProductionReader(t, observations).Snapshot(context.Background(), testNow)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Runs) != 2 {
		t.Fatalf("runs = %#v, want distinct background and announcement sessions", snapshot.Runs)
	}
	background := currentRunsAudioRun(t, snapshot, "showmesh-audio:audio-01:background")
	if background.Show != "halloween" || background.PlaylistID != "background" || background.PlaylistRevision != 1 || background.Reconciliation.State != "resolved" {
		t.Fatalf("background run = %#v, want resolved against matching playlist identity", background)
	}
	announcement := currentRunsAudioRun(t, snapshot, "showmesh-audio:audio-01:announcement")
	if announcement.Status != "playing" || announcement.Show != "" || announcement.PlaylistID != "" || announcement.Reconciliation.State == "resolved" {
		t.Fatalf("announcement run = %#v, must remain a distinct non-program run", announcement)
	}
	if !hasCurrentRunsAudioEvidence(announcement.Playback.Evidence, "audio_session.source_role", "announcement") {
		t.Fatalf("announcement source role evidence was not preserved: %#v", announcement.Playback.Evidence)
	}
}

func TestCurrentRunsProductionDoesNotResolveBackgroundWithoutCurrentPlaylistRevision(t *testing.T) {
	at := testNow.Add(-2 * time.Second)
	observations := []observation.Observation{
		currentRunsAudioObservation(t, "background", "audio_session.source_role", "background", at),
		currentRunsAudioObservation(t, "background", "audio_session.state", "playing", at),
		currentRunsAudioAbsence(t, "background", "audio_session.playlist.revision", "session has no pinned playlist", testNow),
	}
	snapshot, err := newCurrentRunsProductionReader(t, observations).Snapshot(context.Background(), testNow)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	run := currentRunsAudioRun(t, snapshot, "showmesh-audio:audio-01:background")
	if run.Status != "playing" {
		t.Fatalf("status = %q, want the observed playback state preserved", run.Status)
	}
	if run.Show != "" || run.PlaylistID != "" || run.PlaylistRevision != 0 || run.Reconciliation.State == "resolved" {
		t.Fatalf("run = %#v, must not resolve without playlist identity evidence", run)
	}
	if run.Reconciliation.State != "unknown" {
		t.Fatalf("reconciliation = %#v, want explicit unknown for missing identity evidence", run.Reconciliation)
	}
}

func TestCurrentRunsProductionSelectsLatestAudioEvidenceIndependentOfInputOrder(t *testing.T) {
	old, latest := testNow.Add(-20*time.Second), testNow.Add(-2*time.Second)
	forward := []observation.Observation{
		currentRunsAudioObservation(t, "background", "audio_session.source_role", "background", old),
		currentRunsAudioObservation(t, "background", "audio_session.playlist.revision", int64(0), old),
		currentRunsAudioObservation(t, "background", "audio_session.playlist.item_id", "old-item", old),
		currentRunsAudioObservation(t, "background", "audio_session.state", "stopped", old),
		currentRunsAudioObservation(t, "background", "audio_session.source_role", "background", latest),
		currentRunsAudioObservation(t, "background", "audio_session.playlist.revision", int64(1), latest),
		currentRunsAudioObservation(t, "background", "audio_session.playlist.item_id", "cue-1", latest),
		currentRunsAudioObservation(t, "background", "audio_session.state", "playing", latest),
	}
	reverse := append([]observation.Observation(nil), forward...)
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}

	first, err := newCurrentRunsProductionReader(t, forward).Snapshot(context.Background(), testNow)
	if err != nil {
		t.Fatalf("forward Snapshot: %v", err)
	}
	second, err := newCurrentRunsProductionReader(t, reverse).Snapshot(context.Background(), testNow)
	if err != nil {
		t.Fatalf("reverse Snapshot: %v", err)
	}
	a, b := currentRunsAudioRun(t, first, "showmesh-audio:audio-01:background"), currentRunsAudioRun(t, second, "showmesh-audio:audio-01:background")
	if a.Status != "playing" || a.Playback.ItemID != "cue-1" || a.PlaylistRevision != 1 || a.Reconciliation.State != "resolved" {
		t.Fatalf("latest forward projection = %#v", a)
	}
	if a.Status != b.Status || a.Playback.ItemID != b.Playback.ItemID || a.PlaylistRevision != b.PlaylistRevision || a.Reconciliation != b.Reconciliation {
		t.Fatalf("input order changed projection: forward=%#v reverse=%#v", a, b)
	}
}

func hasCurrentRunsAudioEvidence(evidence []currentrun.Evidence, signal, value string) bool {
	for _, e := range evidence {
		if e.Signal == signal && e.Value == value {
			return true
		}
	}
	return false
}
