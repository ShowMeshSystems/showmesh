package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// resolumeProductHandler serves a fixed, well-formed GET /api/v1/product
// response — the exact shape resolume.Client.Product decodes (see
// internal/coordinator/collector/resolume/client.go) — and 404s everything
// else, since this seam's own Collector never calls anything but /product
// (see resolume.Collector.Poll's own doc comment: composition semantics are
// seam D-2, out of scope here).
func resolumeProductHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/product" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Arena","major":7,"minor":23,"micro":2,"revision":51094}`))
	}
}

// waitForObservation polls st for a resolume.reachable=true observation
// under resourceID, or fails the test after d — the same bounded-retry
// shape internal/coordinator/api/stream_test.go's own goroutine-baseline
// and subscriber-count waits already use in this codebase, applied here
// because this test drives a real collector.Runner goroutine on its own
// timer rather than calling Poll synchronously (see this test's own
// comment for why that is the point).
func waitForObservation(t *testing.T, st *store.Store, resourceID string, sig observation.SignalID, d time.Duration) []observation.Observation {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(d)
	var last []observation.Observation
	for time.Now().Before(deadline) {
		obs, err := st.ListObservations(ctx, store.ObservationFilter{
			ResourceKind: observation.ResourceResolume, ResourceID: resourceID, Signal: sig,
		})
		if err != nil {
			t.Fatalf("list observations: %v", err)
		}
		if len(obs) > 0 {
			return obs
		}
		last = obs
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("signal %q for resolume/%s did not appear within %s; last read: %v", sig, resourceID, d, last)
	return last
}

// TestResolumeWiringSurfacesReachableObservation is this task's own named
// deliverable: with SHOWMESH_RESOLUME_URL pointed at an httptest.Server
// serving a real /api/v1/product response, driven through the actual
// coordinator wiring (newResolumeWiring, the shared collector.Runner, and
// *fppSink — the same three seams coordinator.go's Run wires together, not
// a hand-built substitute for any of them), the coordinator's observations
// surface (here, *store.Store — what api.Dependencies.Observations reads
// from via storeObservationLister, see apiwiring.go) reports
// resolume.reachable = true for the configured Resolume id.
//
// This is the wiring-level half; internal/coordinator/collector/resolume's
// own test suite already covers Collector.Poll's decoding and error
// handling in isolation. What only a test at this seam can prove is that
// newResolumeWiring's Add call actually lands the collector on a Runner
// that runs it, and that the generic *fppSink this file's own doc comment
// says is "reused across collector sources" really does persist a
// Resolume observation despite its FPP-suggesting name — an assumption
// worth checking with a real *store.Store rather than trusting the doc
// comment.
//
// coordinator.go's top-level Run is not called here: it loads config from
// the real process environment, installs OS signal handlers, and blocks
// until one arrives, none of which this test wants. Driving
// newResolumeWiring + collector.Runner + *fppSink + *store.Store directly
// is as far as this package's existing wiring-test harness
// (apiwiring_test.go) already reaches for the FPP collector's own
// equivalent (TestFPPSinkRealPortFixturesPruneGhostRowsOnEmptyDelivery) —
// this test follows that same ceiling rather than contorting a new harness
// to invoke Run itself.
func TestResolumeWiringSurfacesReachableObservation(t *testing.T) {
	srv := httptest.NewServer(resolumeProductHandler())
	defer srv.Close()

	// Every other field left at Go zero values: this test exercises
	// newResolumeWiring in isolation, not full config.Validate() (which
	// requires an HTTP addr, MQTT broker, and log level this test has no
	// use for) — config's own test suite already covers Validate().
	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-test"}
	if err := config.ValidateResolumeIDAgainstFPPEndpoints(cfg.ResolumeID, cfg.FPPEndpoints); err != nil {
		t.Fatalf("ValidateResolumeIDAgainstFPPEndpoints: %v", err)
	}

	st := openTestStore(t)
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())

	wire, err := newResolumeWiring(context.Background(), cfg, runner, &resolume.CompositionStore{}, testLogger(), nil, nil)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if wire.watcher == nil {
		t.Fatalf("wire.watcher = nil, want a constructed *resolume.Watcher when ResolumeURL is set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// runner.loop polls immediately on Run's own start (collector.go's own
	// doc comment: "a freshly-started collector's evidence appears without
	// waiting a full interval"), so no sleep is needed before the poll
	// this test waits on — only before the OBSERVATION becomes visible in
	// the store, which is what waitForObservation's retry loop is for.
	go runner.Run(ctx)
	// The watcher's own WebSocket connection is exercised by
	// internal/coordinator/collector/resolume's own test suite
	// (watch_test.go); this test's srv serves REST only, so the watcher
	// would sit in its dial-error backoff loop harmlessly for the
	// duration of this test — started anyway so this test proves what
	// coordinator.go's real Run does: both goroutines alive together,
	// neither blocking the other. There used to be a third goroutine here
	// (wire.adapter.Run) that owned the only GET /composition read this
	// seam performed; ADR-032 decision 2 forbids that call outright — it
	// is known to crash the target Arena build — so the adapter, and this
	// test's own exercise of it, are gone along with it.
	go wire.watcher.Run(ctx)

	obs := waitForObservation(t, st, "resolume-test", "resolume.reachable", 5*time.Second)
	if len(obs) != 1 {
		t.Fatalf("resolume.reachable observations = %d, want exactly 1", len(obs))
	}
	if state := obs[0].StateAt(time.Now()); state != observation.StateCurrent {
		t.Errorf("resolume.reachable state = %q, want %q", state, observation.StateCurrent)
	}
	if v, ok := obs[0].Value.(bool); !ok || !v {
		t.Errorf("resolume.reachable value = %#v, want true", obs[0].Value)
	}

	// The Track D seam D-1 rule this whole file exists to guard: a
	// parameter id must never reach anything this test can see, and the
	// most likely place one would leak by accident is exactly here — a
	// stray %v of a resolume.ParameterID landing in an observation's
	// Value or Reason. Neither collector signal this seam produces
	// (resolume.reachable, resolume.product) is parameter-derived, but
	// this assertion is cheap insurance against that ever changing
	// silently.
	for _, o := range obs {
		if s, ok := o.Value.(string); ok && strings.Contains(strings.ToLower(s), "parameterid") {
			t.Errorf("observation value looks like it leaked a ParameterID: %q", s)
		}
	}
}

// TestResolumeWiringDisabledWhenURLUnset proves the feature-flag half:
// with ResolumeURL empty, newResolumeWiring returns a nil watcher and
// registers nothing on runner — no goroutine, no observation, ever, for a
// Resolume instance nothing configured.
func TestResolumeWiringDisabledWhenURLUnset(t *testing.T) {
	st := openTestStore(t)
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())

	wire, err := newResolumeWiring(context.Background(), config.Config{}, runner, &resolume.CompositionStore{}, testLogger(), nil, nil)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if wire.watcher != nil {
		t.Errorf("wire.watcher = %v, want nil when ResolumeURL is unset", wire.watcher)
	}

	statuses, err := wire.status.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != "not_configured" {
		t.Errorf("CollectorStatuses = %+v, want exactly one not_configured entry", statuses)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go runner.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()

	obs, err := st.ListObservations(context.Background(), store.ObservationFilter{ResourceKind: observation.ResourceResolume})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("observations = %v, want none for resource kind resolume when the collector was never configured", obs)
	}
}

// TestResolumeInstanceListerSynthesizesNotYetPolledBeforeFirstPoll is
// finding 5's regression guard (owner review, 2026-08-16): before the
// first successful poll for a freshly configured Resolume instance (or
// immediately after a coordinator restart), GET /resolume/instances must
// report a stated not_collected row per static signal, not an empty
// observations array — mirroring apiwiring_test.go's identical FPP-side
// case for notYetPolledObservations (the Step 3 review finding both
// repeat: an empty array renders as blank, and blank reads as fine).
func TestResolumeInstanceListerSynthesizesNotYetPolledBeforeFirstPoll(t *testing.T) {
	st := openTestStore(t)
	lister := resolumeInstanceLister{st: st, instanceID: "resolume-test"}

	views, err := lister.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	if len(views[0].Observations) != len(resolume.AllSignals) {
		t.Fatalf("len(Observations) = %d, want %d (one not_collected row per resolume.AllSignals entry)",
			len(views[0].Observations), len(resolume.AllSignals))
	}
	for _, o := range views[0].Observations {
		if o.Absence != observation.StateNotCollected {
			t.Errorf("signal %q Absence = %q, want %q", o.Signal, o.Absence, observation.StateNotCollected)
		}
	}
}

// --- Track D seam D-2/B: storeCompositionConfigReader and
// newResolumeCompositionWiring -----------------------------------------------
//
// These tests write directly to *store.Store using the SAME
// resolumeCompositionConfigKind/resolumeCompositionObjectID constants and
// the SAME "{"composition": ...}" envelope shape
// internal/coordinator/api/resolumecomposition.go's own upload handler
// writes (that package's private resolumeCompositionStoredPayload), rather
// than driving a full authenticated HTTP upload through api.New — building
// that harness (principal bootstrap, session or token issuance, the
// config:write scope, CSRF) belongs to api's own test suite (Step 6/7's
// area), which this seam does not own and did not touch. What these tests
// DO prove is the thing this file's own doc comments flag as the real risk
// of duplicating those two constants and this one envelope shape by value
// instead of by import: that storeCompositionConfigReader reads back
// exactly the shape that shape implies, and that a mismatch would be
// caught here rather than only in production.

// writeTestCompositionRevision writes one resolume.composition config
// revision directly via st's generic config_objects/config_revisions
// methods (store/config.go) and activates it, mirroring
// api/resolumecomposition.go's own handlePostResolumeCompositionUpload
// closure (CreateConfigRevision then ActivateConfigRevision, no separate
// CreateConfigObject call — see ActivateConfigRevision's own doc comment
// for why that upserts the pointer row itself).
func writeTestCompositionRevision(t *testing.T, st *store.Store, revision int64, comp *resolumecomp.Composition) {
	t.Helper()
	ctx := context.Background()

	compJSON, err := json.Marshal(comp)
	if err != nil {
		t.Fatalf("marshaling test composition: %v", err)
	}
	envelope := struct {
		SourceFilename string          `json:"sourceFilename"`
		ContentHash    string          `json:"contentHash"`
		SizeBytes      int64           `json:"sizeBytes"`
		Composition    json.RawMessage `json:"composition"`
	}{
		SourceFilename: "test.avc",
		ContentHash:    "sha256:test",
		SizeBytes:      int64(len(compJSON)),
		Composition:    compJSON,
	}
	payloadJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshaling test envelope: %v", err)
	}

	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind:        resolumeCompositionConfigKind,
		ObjectID:    resolumeCompositionObjectID,
		Revision:    revision,
		PayloadJSON: string(payloadJSON),
	}); err != nil {
		t.Fatalf("CreateConfigRevision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, resolumeCompositionConfigKind, resolumeCompositionObjectID, revision); err != nil {
		t.Fatalf("ActivateConfigRevision: %v", err)
	}
}

func TestStoreCompositionConfigReaderNoRevisionActivated(t *testing.T) {
	st := openTestStore(t)
	reader := storeCompositionConfigReader{st: st}

	revision, compJSON, ok, err := reader.CurrentCompositionRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentCompositionRevision: %v", err)
	}
	if ok {
		t.Errorf("ok = true against an empty store, want false")
	}
	if revision != 0 {
		t.Errorf("revision = %d, want 0", revision)
	}
	if compJSON != nil {
		t.Errorf("compositionJSON = %v, want nil", compJSON)
	}
}

// TestStoreCompositionConfigReaderReadsActiveRevisionByTheSameConstants is
// this file's own answer to its "mirrors by value, not by import" risk
// comment: it writes a revision using EXACTLY the kind/id constants and
// envelope shape defined here, and confirms the reader reads it back —
// which is only reassuring because those constants and that shape are
// copied from api/resolumecomposition.go's own definitions rather than
// invented independently. A divergence between the two files would not be
// caught by this test (it would still pass, testing only internal
// self-consistency); it would be caught by
// TestBuildTrackedCompositionCountsAndWrittenBy-style fixtures failing to
// appear on a real upload, which is exactly why this comment says so
// rather than claiming more than this test actually proves.
func TestStoreCompositionConfigReaderReadsActiveRevisionByTheSameConstants(t *testing.T) {
	st := openTestStore(t)
	comp := &resolumecomp.Composition{
		Name: "Reader Test Show",
		Layers: []resolumecomp.Layer{
			{ID: "3000000000001", Index: 0},
		},
	}
	writeTestCompositionRevision(t, st, 1, comp)

	reader := storeCompositionConfigReader{st: st}
	revision, compJSON, ok, err := reader.CurrentCompositionRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentCompositionRevision: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true after writing and activating a revision")
	}
	if revision != 1 {
		t.Errorf("revision = %d, want 1", revision)
	}

	var got resolumecomp.Composition
	if err := json.Unmarshal(compJSON, &got); err != nil {
		t.Fatalf("unmarshaling returned compositionJSON: %v", err)
	}
	if got.Name != "Reader Test Show" {
		t.Errorf("decoded composition Name = %q, want %q", got.Name, "Reader Test Show")
	}
	if len(got.Layers) != 1 || got.Layers[0].ID != "3000000000001" {
		t.Errorf("decoded composition Layers = %+v, want one layer with id 3000000000001", got.Layers)
	}
}

// TestNewResolumeCompositionWiringNoUploadYet is TRACK-D-D2-SPEC.md §9's
// D-2/B row's own acceptance criterion, exercised at the coordinator
// wiring layer rather than only inside package resolume:
// newResolumeCompositionWiring against a store nothing has ever been
// uploaded to must produce a store whose Current() reports
// resolume.ErrCompositionNotUploaded, never a zero-valued
// *resolume.TrackedComposition a caller could misread as an uploaded
// composition with nothing in it.
func TestNewResolumeCompositionWiringNoUploadYet(t *testing.T) {
	st := openTestStore(t)

	wire := newResolumeCompositionWiring(context.Background(), st, testLogger())

	_, err := wire.store.Current()
	if !errors.Is(err, resolume.ErrCompositionNotUploaded) {
		t.Errorf("Current() error = %v, want it to wrap resolume.ErrCompositionNotUploaded", err)
	}
}

// TestNewResolumeCompositionWiringLoadsAlreadyActiveRevisionAtStartup
// proves the synchronous startup load newResolumeCompositionWiring's own
// doc comment promises: a composition already active in the store (as if
// uploaded before this process last started) is reflected in the returned
// wiring's store immediately, with no need to wait out
// resolumeCompositionRefreshInterval's first tick.
func TestNewResolumeCompositionWiringLoadsAlreadyActiveRevisionAtStartup(t *testing.T) {
	st := openTestStore(t)
	comp := &resolumecomp.Composition{
		Name: "Startup Load Show",
		Decks: []resolumecomp.Deck{
			{ID: "2000000000001", Name: "Main"},
		},
		Clips: []resolumecomp.Clip{
			{ID: "6000000000001", DeckID: "2000000000001", Name: "Clip One"},
		},
	}
	writeTestCompositionRevision(t, st, 1, comp)

	wire := newResolumeCompositionWiring(context.Background(), st, testLogger())

	tc, err := wire.store.Current()
	if err != nil {
		t.Fatalf("Current() after startup load: %v", err)
	}
	if tc.Name() != "Startup Load Show" {
		t.Errorf("Name() = %q, want %q", tc.Name(), "Startup Load Show")
	}
	if got, want := len(tc.Clips()), 1; got != want {
		t.Fatalf("Clips() len = %d, want %d", got, want)
	}
	if tc.Clips()[0].DeckID != resolume.ObjectID(2000000000001) {
		t.Errorf("Clips()[0].DeckID = %v, want 2000000000001", tc.Clips()[0].DeckID)
	}
	if got, want := wire.store.LoadedRevision(), int64(1); got != want {
		t.Errorf("LoadedRevision() = %d, want %d", got, want)
	}
}

// TestResolumeCompositionWiringRunPicksUpANewUploadWithoutRestart is this
// seam's own headline requirement, checked end to end at this layer: a
// composition uploaded (here, written directly to the store, matching
// this file's own established pattern) AFTER newResolumeCompositionWiring
// already returned reaches wire.store.Current() once Run's own periodic
// refresh ticks — with no restart, and with no direct call from this test
// into Refresh itself, which is what makes this a test of Run's loop
// rather than of Refresh in isolation (idmap_test.go already covers
// Refresh directly). resolumeCompositionRefreshInterval is not overridden
// for this test: it is a small (5s) package-level constant already, and
// keeping it real here — rather than adding a test-only seam to shrink it
// — is what actually exercises the ticker Run constructs, not a
// substitute for it.
func TestResolumeCompositionWiringRunPicksUpANewUploadWithoutRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a real resolumeCompositionRefreshInterval tick; skipped in -short")
	}

	st := openTestStore(t)
	wire := newResolumeCompositionWiring(context.Background(), st, testLogger())

	if _, err := wire.store.Current(); !errors.Is(err, resolume.ErrCompositionNotUploaded) {
		t.Fatalf("Current() before any upload = %v, want resolume.ErrCompositionNotUploaded", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go wire.Run(ctx)

	// Simulate an upload landing AFTER this coordinator process already
	// started and already ran its one synchronous load above — exactly
	// the scenario TRACK-D-D2-SPEC.md §9's D-2/B row names.
	comp := &resolumecomp.Composition{Name: "Uploaded After Startup"}
	writeTestCompositionRevision(t, st, 1, comp)

	deadline := time.Now().Add(resolumeCompositionRefreshInterval + 5*time.Second)
	for time.Now().Before(deadline) {
		tc, err := wire.store.Current()
		if err == nil && tc.Name() == "Uploaded After Startup" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wire.store.Current() never reflected the post-startup upload within %s", resolumeCompositionRefreshInterval+5*time.Second)
}
