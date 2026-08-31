package api

import (
	"context"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file's fakes are the test-side implementations of every interface
// this package declares in interfaces.go. Per the contract's standing rule
// (section 1), none of this package's tests construct a v1 wire struct by
// hand and marshal it: every test drives these fakes through the real
// mapping and handler code, and asserts on the resulting JSON bytes. A
// fake that returned pre-built v1 types would prove nothing about the
// mapping layer this package exists to hold accountable.

// fakeNodeLister is mutex-protected because stream_test.go's hub tests
// mutate it (to simulate a node change arriving) from the test goroutine
// while [Hub.Run] concurrently reads it via [Hub.render] from its own
// goroutine — exactly the concurrent-access shape a real store/inventory
// implementation has under real traffic, and exactly what `go test -race`
// is being asked to prove clean per contract section 6.4's "leaks no
// goroutines" / clean-shutdown requirement.
type fakeNodeLister struct {
	mu    sync.Mutex
	views []inventory.NodeView
	err   error
}

func (f *fakeNodeLister) Snapshot(context.Context, time.Time) ([]inventory.NodeView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.views, f.err
}

func (f *fakeNodeLister) setViews(views []inventory.NodeView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views = views
}

// fakeNodeAudioLister is [NodeAudioLister]'s test double, for a test that
// needs to drive audioNodeEngineConfirmedUsableNow against controlled
// node.audio.* observations without a real collector.
type fakeNodeAudioLister struct {
	mu     sync.Mutex
	byNode map[string][]observation.Observation
}

func (f *fakeNodeAudioLister) NodeAudioObservations(nodeID string) []observation.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byNode[nodeID]
}

func (f *fakeNodeAudioLister) setObservations(nodeID string, obs []observation.Observation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byNode == nil {
		f.byNode = map[string][]observation.Observation{}
	}
	f.byNode[nodeID] = obs
}

type fakeFPPLister struct {
	views []FPPInstanceView
	err   error
}

func (f *fakeFPPLister) ListInstances(context.Context) ([]FPPInstanceView, error) {
	return f.views, f.err
}

// fakeResolumeLister is [ResolumeLister]'s own fake, mirroring
// [fakeFPPLister]'s shape exactly.
type fakeResolumeLister struct {
	views []ResolumeInstanceView
	err   error
}

func (f *fakeResolumeLister) ListInstances(context.Context) ([]ResolumeInstanceView, error) {
	return f.views, f.err
}

type fakeObservationLister struct {
	obs []observation.Observation
	err error
	// gotFilter records the last filter passed in, for tests asserting the
	// query parameters were parsed and forwarded correctly.
	gotFilter ObservationFilter
}

func (f *fakeObservationLister) ListObservations(_ context.Context, filter ObservationFilter) ([]observation.Observation, error) {
	f.gotFilter = filter
	return f.obs, f.err
}

type fakeEventReader struct {
	records []EventRecord
	gap     bool
	latest  uint64
	oldest  uint64
	hasOld  bool

	listErr   error
	latestErr error
	oldestErr error

	// gotSince/gotLimit record the last ListEvents call's arguments.
	gotSince uint64
	gotLimit int
}

func (f *fakeEventReader) ListEvents(_ context.Context, since uint64, limit int) ([]EventRecord, bool, error) {
	f.gotSince, f.gotLimit = since, limit
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	return f.records, f.gap, nil
}

func (f *fakeEventReader) LatestEventSeq(context.Context) (uint64, error) {
	if f.latestErr != nil {
		return 0, f.latestErr
	}
	return f.latest, nil
}

func (f *fakeEventReader) OldestEventSeq(context.Context) (uint64, bool, error) {
	if f.oldestErr != nil {
		return 0, false, f.oldestErr
	}
	return f.oldest, f.hasOld, nil
}

type fakeCollectorStatusLister struct {
	statuses []CollectorState
	err      error
}

func (f *fakeCollectorStatusLister) CollectorStatuses(context.Context) ([]CollectorState, error) {
	return f.statuses, f.err
}

// fakeCommandStore is Step 9 wave 2's own fake for [CommandStore], used
// only by tests that need GET /macro-runs/{runId} to resolve a step's
// commandId into command detail (macroruns_test.go) — every existing
// caller of this package's CommandStore-dependent write routes already has
// its own richer fake (fppcommand_dispatch_test.go), so this one carries
// only what GetCommand needs.
type fakeCommandStore struct {
	mu       sync.Mutex
	commands map[string]store.CommandRecord
}

func newFakeCommandStore() *fakeCommandStore {
	return &fakeCommandStore{commands: make(map[string]store.CommandRecord)}
}

func (f *fakeCommandStore) InsertCommand(context.Context, store.CommandRecord) (store.CommandRecord, error) {
	return store.CommandRecord{}, errCommandStoreNotConfigured
}

func (f *fakeCommandStore) SetDesiredState(context.Context, store.DesiredStateRecord) (store.DesiredStateRecord, error) {
	return store.DesiredStateRecord{}, errCommandStoreNotConfigured
}

func (f *fakeCommandStore) UpdateCommandOutcome(context.Context, string, store.CommandOutcomeUpdate) error {
	return errCommandStoreNotConfigured
}

func (f *fakeCommandStore) ListUnresolvedCommands(context.Context) ([]store.CommandRecord, error) {
	return nil, nil
}

func (f *fakeCommandStore) GetCommandByIdempotencyKey(context.Context, string) (store.CommandRecord, error) {
	return store.CommandRecord{}, store.ErrCommandNotFound
}

func (f *fakeCommandStore) GetCommand(_ context.Context, id string) (store.CommandRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.commands[id]
	if !ok {
		return store.CommandRecord{}, store.ErrCommandNotFound
	}
	return rec, nil
}

func (f *fakeCommandStore) GetLatestCommandByTargetAction(_ context.Context, targetKind, targetID, action string) (store.CommandRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var (
		best  store.CommandRecord
		found bool
	)
	for _, rec := range f.commands {
		if rec.TargetKind != targetKind || rec.TargetID != targetID || rec.Action != action {
			continue
		}
		if !found || rec.CreatedAt.After(best.CreatedAt) {
			best, found = rec, true
		}
	}
	if !found {
		return store.CommandRecord{}, store.ErrCommandNotFound
	}
	return best, nil
}

func (f *fakeCommandStore) setCommand(rec store.CommandRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands[rec.ID] = rec
}

// fakeMacroRunner is Step 9 wave 2's own fake for [MacroRunner]. Every
// test in showconfig_test.go and macroruns_test.go that exercises the run
// routes drives this fake through the real route/mapping code, matching
// this file's own standing rule (top doc comment): a fake never returns a
// pre-built v1 wire struct.
type fakeMacroRunner struct {
	mu sync.Mutex

	submitResult MacroRunResult
	submitProb   *v1.Problem
	submitErr    error
	// gotSubmit records the last SubmitRun call's request, for tests
	// asserting what this package actually sent to the executor (the
	// issuer it resolved, the trigger and priorFailures it parsed).
	gotSubmit MacroSubmitRequest

	getResult MacroRunResult
	getErr    error
	gotGetID  string

	listResult []store.MacroRunRecord
	listErr    error
	gotFilter  MacroRunFilter

	snapshotResult []store.MacroRunRecord
	snapshotErr    error
}

func (f *fakeMacroRunner) SubmitRun(_ context.Context, req MacroSubmitRequest) (MacroRunResult, *v1.Problem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotSubmit = req
	return f.submitResult, f.submitProb, f.submitErr
}

func (f *fakeMacroRunner) GetRun(_ context.Context, runID string) (MacroRunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotGetID = runID
	return f.getResult, f.getErr
}

func (f *fakeMacroRunner) ListRuns(_ context.Context, filter MacroRunFilter) ([]store.MacroRunRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotFilter = filter
	return f.listResult, f.listErr
}

func (f *fakeMacroRunner) SnapshotRuns(context.Context) ([]store.MacroRunRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotResult, f.snapshotErr
}

func (f *fakeMacroRunner) setSnapshot(runs []store.MacroRunRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotResult = runs
}
