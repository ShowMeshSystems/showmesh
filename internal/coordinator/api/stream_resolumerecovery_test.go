package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track D seam D-3a's own addition to the change-stream test
// suite (build contract §1.7), added after review: seam E's TestStreamResolumeChangedDeliveredOnNotify/
// TestStreamResolumeChangedNotResentWhenNothingChanged (stream_test.go) are
// the pattern this file follows for the resolumerecovery:default resource.

// mutableResolumeRecoveryConfigStore is a minimal, in-memory ConfigStore
// fake carrying exactly one resolume.recovery revision — controllable by a
// test via setEnabled — mirroring mutableResolumeLister's own "just enough
// to drive the hub" posture (stream_test.go). Every other kind/id this
// interface could be asked about answers "not found"/"empty", since
// nothing in this seam's render path asks about any other kind.
type mutableResolumeRecoveryConfigStore struct {
	mu       sync.Mutex
	hasValue bool
	enabled  bool
}

func (s *mutableResolumeRecoveryConfigStore) setEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasValue = true
	s.enabled = enabled
}

func (s *mutableResolumeRecoveryConfigStore) GetConfigObject(_ context.Context, kind, id string) (store.ConfigObjectRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind != config.ResolumeRecoveryConfigKind || !s.hasValue {
		return store.ConfigObjectRecord{}, store.ErrConfigObjectNotFound
	}
	return store.ConfigObjectRecord{Kind: kind, ID: id, CurrentRevision: 1}, nil
}

func (s *mutableResolumeRecoveryConfigStore) GetConfigRevision(_ context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := config.EncodeResolumeRecoveryPayload(s.enabled)
	if err != nil {
		return store.ConfigRevisionRecord{}, err
	}
	return store.ConfigRevisionRecord{Kind: kind, ObjectID: id, Revision: revision, PayloadJSON: payload}, nil
}

func (s *mutableResolumeRecoveryConfigStore) ListConfigRevisions(context.Context, string, string) ([]store.ConfigRevisionRecord, error) {
	return nil, nil
}

func (s *mutableResolumeRecoveryConfigStore) ListConfigObjects(context.Context, string) ([]store.ConfigObjectRecord, error) {
	return nil, nil
}

var _ ConfigStore = (*mutableResolumeRecoveryConfigStore)(nil)

// mutableResolumeRecoveryProvider is a minimal ResolumeRecoveryProvider
// fake, controllable by a test via setRecord/setLastReport.
type mutableResolumeRecoveryProvider struct {
	mu     sync.Mutex
	record []ResolumeRecoveryRecordEntryView
	last   *ResolumeRecoveryRestoreReportView
}

func (p *mutableResolumeRecoveryProvider) setRecord(r []ResolumeRecoveryRecordEntryView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record = r
}

func (p *mutableResolumeRecoveryProvider) setLastReport(r *ResolumeRecoveryRestoreReportView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = r
}

func (p *mutableResolumeRecoveryProvider) Record() []ResolumeRecoveryRecordEntryView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.record
}

func (p *mutableResolumeRecoveryProvider) LastReport() *ResolumeRecoveryRestoreReportView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func (p *mutableResolumeRecoveryProvider) Restore(context.Context, string) (ResolumeRecoveryRestoreReportView, error) {
	return ResolumeRecoveryRestoreReportView{}, errResolumeRecoveryNotConfigured
}

var _ ResolumeRecoveryProvider = (*mutableResolumeRecoveryProvider)(nil)

// TestStreamResolumeRecoveryChangedOnToggleFlipAndRestore is build contract
// §1.7: the resolumerecovery:default resource moves on the stream both
// when the auto-restore toggle flips and when a restore finishes — the
// property named in the spec's own §10.6 property 1 ("an external process
// subscribes and learns... without polling"), applied here to the restore
// report itself rather than only resolume.reachable.
//
// Breaking: removed the render block's own `if h.updateRendered(key, proj)`
// call (always constructing pending regardless of the diff) — confirmed
// this test still passed (a positive-only test cannot catch a diff that
// fires too often), which is exactly why
// TestStreamResolumeRecoveryChangedNotResentWhenNothingChanged exists
// below; the mutation that actually turns THIS test red is removing the
// pendingFrame append (or the render block) entirely — confirmed and
// restored (see this file's own report).
func TestStreamResolumeRecoveryChangedOnToggleFlipAndRestore(t *testing.T) {
	cs := &mutableResolumeRecoveryConfigStore{}
	cs.setEnabled(true)
	rec := &mutableResolumeRecoveryProvider{}

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Config: cs, ResolumeRecovery: rec, ResolumeRecoverySettleSeconds: 8,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
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

	// Baseline render: the resource is new, so this always produces one
	// frame regardless of the diff — establishes the "before" state the
	// next two steps each change exactly one fact away from.
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "resolumeRecovery.changed" {
		t.Fatalf("baseline Notify: event = %q, want resolumeRecovery.changed", event)
	}

	// Step 1: the toggle flips true -> false. Must move the resource.
	cs.setEnabled(false)
	api.Hub.Notify()
	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "resolumeRecovery.changed" {
		t.Fatalf("after toggle flip: event = %q, want resolumeRecovery.changed", event)
	}
	var togglePayload struct {
		AutoRestoreEnabled bool `json:"autoRestoreEnabled"`
	}
	if err := json.Unmarshal([]byte(data), &togglePayload); err != nil {
		t.Fatalf("decoding resolumeRecovery.changed data: %v\ndata: %s", err, data)
	}
	if togglePayload.AutoRestoreEnabled {
		t.Errorf("autoRestoreEnabled = true, want false after the toggle flip")
	}

	// Step 2: a restore finishes (LastReport moves from nil to a real
	// report). Must move the resource again, with no toggle change this
	// time — isolating that THIS is what moved it.
	rec.setLastReport(&ResolumeRecoveryRestoreReportView{
		StartedAt: "2026-08-16T00:00:00Z", FinishedAt: "2026-08-16T00:00:01Z",
		Trigger: "manual", Outcome: "restored", Principal: "admin",
		Layers: []ResolumeRecoveryRestoreLayerView{{Layer: "Whole House 1", Result: "restored"}},
	})
	api.Hub.Notify()
	event, data = readEventWithTimeout(t, r, 5*time.Second)
	if event != "resolumeRecovery.changed" {
		t.Fatalf("after restore finished: event = %q, want resolumeRecovery.changed", event)
	}
	var restorePayload struct {
		LastRestore *struct {
			Outcome string `json:"outcome"`
		} `json:"lastRestore"`
	}
	if err := json.Unmarshal([]byte(data), &restorePayload); err != nil {
		t.Fatalf("decoding resolumeRecovery.changed data: %v\ndata: %s", err, data)
	}
	if restorePayload.LastRestore == nil || restorePayload.LastRestore.Outcome != "restored" {
		t.Errorf("lastRestore = %+v, want outcome \"restored\"", restorePayload.LastRestore)
	}
}

// TestStreamResolumeRecoveryChangedNotResentWhenNothingChanged is build
// contract §1.7's quiet-system property: a second Notify with byte-
// identical toggle/record/lastRestore state must not produce a second
// frame. Breaking: production line broken was resolumeRecoveryChangedEventProjection
// changed to also set ServerTime (defeating updateRendered's own diff,
// which — per macroRunChangedEventProjection's identical established
// pattern — must diff on a projection that omits Seq/ServerTime) —
// confirmed this test goes red (a spurious second frame arrived), then
// restored.
func TestStreamResolumeRecoveryChangedNotResentWhenNothingChanged(t *testing.T) {
	cs := &mutableResolumeRecoveryConfigStore{}
	cs.setEnabled(true)
	rec := &mutableResolumeRecoveryProvider{}
	rec.setRecord([]ResolumeRecoveryRecordEntryView{{Layer: "Whole House 1", State: "dark"}})

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Config: cs, ResolumeRecovery: rec, ResolumeRecoverySettleSeconds: 8,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
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

	// First Notify: genuinely new, so exactly one resolumeRecovery.changed.
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "resolumeRecovery.changed" {
		t.Fatalf("first Notify: event = %q, want resolumeRecovery.changed", event)
	}

	// Second Notify: nothing about the toggle, the record, or the last
	// restore has changed.
	api.Hub.Notify()

	select {
	case ev := <-func() chan string {
		ch := make(chan string, 1)
		go func() {
			event, _, err := nextRealEvent(r)
			if err == nil {
				ch <- event
			}
		}()
		return ch
	}():
		t.Fatalf("second Notify with nothing changed produced a spurious %q event; a quiet system must stay quiet", ev)
	case <-time.After(1 * time.Second):
		// Correct: StreamTickInterval is an hour in newStreamTestAPI, so
		// nothing else could legitimately produce a frame in this window.
	}
}
