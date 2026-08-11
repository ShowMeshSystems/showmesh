package api

import (
	"context"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
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

type fakeFPPLister struct {
	views []FPPInstanceView
	err   error
}

func (f *fakeFPPLister) ListInstances(context.Context) ([]FPPInstanceView, error) {
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
