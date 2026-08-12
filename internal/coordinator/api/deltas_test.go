package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is ADR-023's own regression suite: observation-level delta
// frames on the SSE stream, opt-in per connection via
// GET /api/v1/stream?deltas=1. Every test here follows this package's
// standing rule against a negative assertion with no matching positive one
// (BE CAREFUL ABOUT, ADR-023's own implementation task): "X never appears"
// is trivially satisfied by X being entirely broken, so every test that
// asserts an absence also drives a scenario that DOES produce something,
// on the same connection or its sibling, to prove the absence is
// meaningful rather than accidental.

// deltaFPPRes is the fixed ResourceRef every fixture observation in this
// file is "about" — one FPP instance, "player-01", reused across every test
// so the instance's own identity is never itself part of what a test is
// checking.
var deltaFPPRes = observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}

// deltaObs builds a tier-1 Measured observation for signal on player-01,
// sourced fpp-rest, exactly like stream_test.go's own streamObs helper but
// parameterized on the signal name — this file's scenarios need more than
// one distinct, non-health-critical signal at once (streamObs is hardcoded
// to "fpp.status"), specifically so a test can change ONE signal's value
// while leaving another completely untouched and assert the untouched one
// never appears in a delta frame's "changed" list.
func deltaObs(t *testing.T, signal string, value any, observedAt time.Time, validFor time.Duration) observation.Observation {
	t.Helper()
	o, err := observation.Measured(deltaFPPRes, observation.SignalID(signal), value, observedAt,
		observation.WithSource("fpp-rest"), observation.WithValidFor(validFor), observation.WithCollectedAt(observedAt))
	if err != nil {
		t.Fatalf("building fixture observation: %v", err)
	}
	return o
}

// Both signals below are deliberately NOT in healthCriticalSignals
// (mapping.go): this file's scenarios need to move an observation's value
// without incidentally moving the instance's derived Health field too,
// which would confound "observation-only" and "instance-level-only"
// scenarios with each other. Health stays observation.HealthUnknown
// (no health-critical evidence at all) for every FPPInstanceView this file
// constructs.
const (
	sigUptime = "fpp.uptime.seconds"
	sigTemp   = "fpp.sensor.temp"
)

// connectStream opens an SSE connection to api, optionally with
// ?deltas=1, reads and discards stream.start, and returns a reader
// positioned at the first real event, plus a close func the caller must
// defer itself.
//
// Deliberately NOT t.Cleanup for the close: every test in this file also
// `defer srv.Close()`s the httptest.Server, and httptest.Server.Close
// blocks until every still-open connection finishes — t.Cleanup functions
// run only after the test function's own defers have already run (LIFO
// order, but as a strictly later phase), so a t.Cleanup-based close here
// would run AFTER defer srv.Close() already started waiting on this exact
// connection, deadlocking every test that uses it. Returning a close func
// for the caller to `defer` itself, immediately after connectStream's own
// call, keeps this file's ordering identical to every other test in this
// package (stream_test.go's own pattern: `resp, err := http.Get(...);
// defer func() { _ = resp.Body.Close() }()`, deferred AFTER `defer
// srv.Close()` so LIFO closes the connection first).
func connectStream(t *testing.T, srv *httptest.Server, deltas bool) (r *bufio.Reader, closeConn func()) {
	t.Helper()
	url := srv.URL + "/api/v1/stream"
	if deltas {
		url += "?deltas=1"
	}
	resp, err := http.Get(url) //nolint // test-only
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	r = bufio.NewReader(resp.Body)
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start", event)
	}
	return r, func() { _ = resp.Body.Close() }
}

// expectNoFrameWithin proves a negative assertion is not vacuous by racing
// a bounded wait against the hub's actual (long) tick interval: every
// caller in this file uses newStreamTestAPI, whose StreamTickInterval is an
// hour, so nothing OTHER than a bug under test could legitimately deliver a
// frame in this window — matching the same reasoning stream_test.go's own
// negative-assertion tests already use (e.g.
// TestStreamNodeChangedNotResentWhenNothingChangedAsClockAdvances).
//
// SAFE ONLY AS THE LAST THING a test does with r. The read that proves the
// negative runs in its own goroutine because bufio.Reader has no way to
// cancel an in-flight blocking Read; on a timeout this function returns
// anyway, leaving that goroutine still parked mid-read on r. A test that
// calls this mid-sequence and then keeps reading r afterward races that
// leaked goroutine against its own next readEventWithTimeout call over the
// same non-concurrency-safe bufio.Reader — whichever wins consumes the
// frame, and if the leaked goroutine wins, it hands the frame to a channel
// nothing is listening to anymore, and the test's own next read hangs.
// (Found by exactly this failure while writing this file's own
// mid-sequence checks — see git history — which is why every test in this
// file now calls this only once, at the very end, relying on each
// intermediate step's own POSITIVE assertion — "the next frame is exactly
// X" — to already catch an unwanted extra frame queued ahead of it: a
// mismatched event name fails immediately and deterministically, with no
// race, which is strictly stronger than a timed check would have been
// anyway.)
func expectNoFrameWithin(t *testing.T, r *bufio.Reader, d time.Duration, msg string) {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		event, _, err := nextRealEvent(r)
		if err == nil {
			ch <- event
		}
	}()
	select {
	case ev := <-ch:
		t.Fatalf("%s: got unexpected event %q", msg, ev)
	case <-time.After(d):
	}
}

// TestStreamDeltasParamOnlyExactLiteral1Enables proves the ServeHTTP doc
// comment's own claim: only the literal query value "1" opts a connection
// into deltas. Every other value behaves exactly like an absent parameter.
// This is the query-parsing half of ADR-023 decision 1's additive-only
// argument — a lenient/truthy parse here would let an unrelated
// "?deltas=something-else" a real client sent for its own reasons silently
// change that client's behavior.
func TestStreamDeltasParamOnlyExactLiteral1Enables(t *testing.T) {
	for _, tc := range []struct {
		name    string
		query   string
		wantOn  bool
		explain string
	}{
		{"absent", "", false, "no parameter at all"},
		{"empty", "?deltas=", false, "present but empty"},
		{"zero", "?deltas=0", false, "the literal off value some APIs use"},
		{"true", "?deltas=true", false, "a truthy spelling this API does not accept"},
		{"yes", "?deltas=yes", false, "another truthy spelling this API does not accept"},
		{"one", "?deltas=1", true, "the one documented opt-in value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fpp := &mutableFPPLister{}
			pollAt := testNow
			fpp.setViews([]FPPInstanceView{{
				InstanceID: "player-01", Endpoint: "http://10.0.1.20",
				Observations: []observation.Observation{deltaObs(t, sigUptime, int64(1), pollAt, time.Minute)},
				LastPollAt:   &pollAt,
			}})

			api := newStreamTestAPI(Dependencies{
				Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
				Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go api.Hub.Run(ctx)

			srv := httptest.NewServer(api.Handler)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/v1/stream" + tc.query) //nolint // test-only
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			r := bufio.NewReader(resp.Body)
			if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
				t.Fatalf("first event = %q, want stream.start", event)
			}

			// First Notify: the instance is new, so fpp.changed always
			// fires regardless of deltas — not the thing under test here.
			api.Hub.Notify()
			if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
				t.Fatalf("first Notify: event = %q, want fpp.changed", event)
			}

			// Second poll: an OBSERVATION-ONLY value change. This is what
			// distinguishes a delta connection (fpp.observations.changed)
			// from a non-delta one (fpp.changed only) — see this file's
			// other tests for why.
			pollAt2 := pollAt.Add(15 * time.Second)
			fpp.setViews([]FPPInstanceView{{
				InstanceID: "player-01", Endpoint: "http://10.0.1.20",
				Observations: []observation.Observation{deltaObs(t, sigUptime, int64(16), pollAt2, time.Minute)},
				LastPollAt:   &pollAt2,
			}})
			api.Hub.Notify()

			event, _ := readEventWithTimeout(t, r, 5*time.Second)
			gotOn := event == "fpp.observations.changed"
			if gotOn != tc.wantOn {
				t.Fatalf("query %q (%s): got event %q (deltas enabled = %v), want deltas enabled = %v",
					tc.query, tc.explain, event, gotOn, tc.wantOn)
			}
			if !tc.wantOn && event != "fpp.changed" {
				t.Fatalf("query %q: want fpp.changed for a non-delta connection, got %q", tc.query, event)
			}
		})
	}
}

// TestStreamNonDeltaConnectionNeverReceivesObservationsChangedFrame is the
// negative half of ADR-023 decision 1, driven across THREE different kinds
// of change (instance-level only, observation-level only, and a removed
// signal) so the absence is proven under every condition that could
// plausibly produce an fpp.observations.changed frame, not just one. The
// positive pairing — that the SAME sequence against a delta-subscribed
// connection DOES produce it — is
// [TestStreamDeltaConnectionReceivesObservationsChangedFrame] below; this
// test only proves the negative, so it also positively confirms
// fpp.changed keeps arriving throughout (otherwise "no
// fpp.observations.changed" would be satisfied merely by the connection
// receiving nothing at all).
func TestStreamNonDeltaConnectionNeverReceivesObservationsChangedFrame(t *testing.T) {
	fpp := &mutableFPPLister{}
	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt,
	}})

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	r, closeR := connectStream(t, srv, false)
	defer closeR()

	// 1) First appearance.
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("first Notify: event = %q, want fpp.changed", event)
	}

	// 2) Instance-level-only change: Endpoint moves, observations untouched.
	pollAt2 := pollAt.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21", // changed
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt2,
	}})
	api.Hub.Notify()
	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("endpoint-only change: event = %q, want fpp.changed; data: %s", event, data)
	}

	// 3) Observation-only change.
	pollAt3 := pollAt2.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(16), pollAt3, time.Minute), // changed
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt3,
	}})
	api.Hub.Notify()
	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("observation-only change: event = %q, want fpp.changed; data: %s", event, data)
	} else if !strings.Contains(data, `"value":16`) {
		t.Errorf("fpp.changed data does not carry the new uptime value: %s", data)
	}

	// 4) A signal disappears entirely.
	pollAt4 := pollAt3.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(16), pollAt3, time.Minute), // sigTemp removed
		},
		LastPollAt: &pollAt4,
	}})
	api.Hub.Notify()
	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("signal removed: event = %q, want fpp.changed; data: %s", event, data)
	} else if strings.Contains(data, sigTemp) {
		t.Errorf("fpp.changed data still mentions the removed signal %s: %s", sigTemp, data)
	}

	// Nothing else should be pending: in particular, no
	// fpp.observations.changed at any point above.
	expectNoFrameWithin(t, r, 1*time.Second,
		"non-delta connection: unexpected frame after a sequence of instance-level, observation-level, and removal changes")
}

// TestStreamDeltaConnectionReceivesObservationsChangedFrame is the positive
// pairing for the test above: the identical sequence of changes, against a
// delta-subscribed connection this time, DOES produce fpp.observations.changed
// for the observation-level and removal steps, and does NOT produce one for
// the pure instance-level (endpoint-only) step — proving the split lands on
// exactly the line ADR-023 decision 3 draws.
func TestStreamDeltaConnectionReceivesObservationsChangedFrame(t *testing.T) {
	fpp := &mutableFPPLister{}
	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt,
	}})

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	r, closeR := connectStream(t, srv, true)
	defer closeR()

	// 1) First appearance: fpp.changed only (an instance new to the hub has
	// no prior observation baseline to diff against, so there is nothing to
	// report as a "delta" yet). Whether a redundant fpp.observations.changed
	// ALSO snuck in right behind it is proved not by racing a timed read
	// here (see [expectNoFrameWithin]'s doc comment on why a mid-sequence
	// use of that pattern is unsafe against this test's own LATER reads on
	// the same connection) but by step 2 below: it reads the very NEXT
	// frame this connection produces and asserts it is fpp.changed for the
	// endpoint change — which fails immediately, deterministically, and
	// without any race, if step 1 had already queued something ahead of it.
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("first Notify: event = %q, want fpp.changed", event)
	}

	// 2) Instance-level-only change (endpoint): fpp.changed, no
	// fpp.observations.changed — the negative half of decision 3. (Proved
	// the same way as step 1's own negative half: step 3 below reads the
	// NEXT frame and asserts it is fpp.observations.changed, which fails if
	// this step had also queued one.)
	pollAt2 := pollAt.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt2,
	}})
	api.Hub.Notify()
	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("endpoint-only change: event = %q, want fpp.changed; data: %s", event, data)
	} else if !strings.Contains(data, `10.0.1.21`) {
		t.Errorf("fpp.changed data does not carry the new endpoint: %s", data)
	}

	// 3) Observation-only change: fpp.observations.changed ONLY, no
	// fpp.changed — the positive half of decision 3.
	pollAt3 := pollAt2.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(16), pollAt3, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt3,
	}})
	api.Hub.Notify()
	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "fpp.observations.changed" {
		t.Fatalf("observation-only change: event = %q, want fpp.observations.changed; data: %s", event, data)
	}
	if !strings.Contains(data, `"instanceId":"player-01"`) {
		t.Errorf("fpp.observations.changed data missing instanceId: %s", data)
	}
	if !strings.Contains(data, sigUptime) {
		t.Errorf("fpp.observations.changed data does not name the changed signal %s: %s", sigUptime, data)
	}
	if strings.Contains(data, sigTemp) {
		t.Errorf("fpp.observations.changed data mentions the UNCHANGED signal %s — a delta frame must never carry an observation that did not change: %s", sigTemp, data)
	}
	if !strings.Contains(data, `"removed":[]`) {
		t.Errorf("fpp.observations.changed data should carry an empty (not null) removed array when nothing was removed: %s", data)
	}

	// 4) A signal disappears: fpp.observations.changed carrying it in
	// "removed", nothing in "changed" (sigUptime's value is unchanged from
	// step 3).
	pollAt4 := pollAt3.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(16), pollAt3, time.Minute),
		},
		LastPollAt: &pollAt4,
	}})
	api.Hub.Notify()
	event, data = readEventWithTimeout(t, r, 5*time.Second)
	if event != "fpp.observations.changed" {
		t.Fatalf("signal removed: event = %q, want fpp.observations.changed; data: %s", event, data)
	}
	if !strings.Contains(data, `"removed":["`+sigTemp+`"]`) {
		t.Errorf("fpp.observations.changed data does not report %s as removed: %s", sigTemp, data)
	}
	if !strings.Contains(data, `"changed":[]`) {
		t.Errorf("fpp.observations.changed data should carry an empty changed array (only sigTemp moved, and it was removed, not changed): %s", data)
	}

	// Nothing else pending: this IS the safe place for the timed negative
	// check (the very end of the test, with no further read on r
	// afterward — see [expectNoFrameWithin]'s doc comment).
	expectNoFrameWithin(t, r, 500*time.Millisecond, "unexpected extra frame after the full scripted sequence")
}

// TestStreamObservationsChangedFrameNeverCarriesUnchangedObservation is a
// focused, minimal regression guard for exactly the rule its name states,
// independent of the broader scenario tests above: with two signals on an
// instance, changing only one must produce a "changed" array with EXACTLY
// one entry, never both — proved by counting occurrences of each signal's
// own "signal" key in the frame, not merely checking one is absent (a
// frame with garbled or duplicated content could still pass a bare
// substring check).
func TestStreamObservationsChangedFrameNeverCarriesUnchangedObservation(t *testing.T) {
	fpp := &mutableFPPLister{}
	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt,
	}})

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)
	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	r, closeR := connectStream(t, srv, true)
	defer closeR()
	api.Hub.Notify()
	readEventWithTimeout(t, r, 5*time.Second) // first appearance, fpp.changed

	pollAt2 := pollAt.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(2), pollAt2, time.Minute), // only this one changes
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt2,
	}})
	api.Hub.Notify()

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "fpp.observations.changed" {
		t.Fatalf("event = %q, want fpp.observations.changed; data: %s", event, data)
	}

	var frame struct {
		Changed []v1.Evidence `json:"changed"`
		Removed []string      `json:"removed"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		t.Fatalf("decoding frame: %v; data: %s", err, data)
	}
	if len(frame.Changed) != 1 {
		t.Fatalf("changed has %d entries, want exactly 1; data: %s", len(frame.Changed), data)
	}
	if frame.Changed[0].Signal != sigUptime {
		t.Errorf("changed[0].Signal = %q, want %q", frame.Changed[0].Signal, sigUptime)
	}
	if len(frame.Removed) != 0 {
		t.Errorf("removed = %v, want empty", frame.Removed)
	}
}

// TestStreamObservationsChangedSuppressesChurnFromTimestampOnlyChanges is
// the delta path's own version of stream_test.go's
// TestFPPInstanceDiffProjectionSuppressesChurnFromTimestampOnlyChanges:
// the FPP collector re-stamps ObservedAt on every poll even when the
// decoded value is byte-identical to the last poll's (Step 5 review
// finding 3, ~43 KB/s per connected browser on an otherwise idle system
// before that finding's fix). [maskEvidenceForDiff] applies the identical
// masking [fppInstanceDiffProjection] already uses for the full-frame path
// to this package's OWN per-signal delta cache; without it, a delta stream
// would reproduce the exact volume problem ADR-023 exists to fix, one
// layer down from fpp.changed instead of at it. Five simulated poll
// cycles with an unchanged value, only ObservedAt/LastPollAt advancing
// each time, must produce zero fpp.observations.changed frames.
func TestStreamObservationsChangedSuppressesChurnFromTimestampOnlyChanges(t *testing.T) {
	fpp := &mutableFPPLister{}
	clock := &syncClock{t: testNow}

	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{deltaObs(t, sigUptime, "idle", pollAt, time.Minute)},
		LastPollAt:   &pollAt,
	}})

	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{
		Clock: clock.now, Logger: testLogger(),
		StreamTickInterval: time.Hour, StreamKeepaliveInterval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)
	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	r, closeR := connectStream(t, srv, true)
	defer closeR()

	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("first Notify: event = %q, want fpp.changed", event)
	}

	for i := 0; i < 5; i++ {
		nextPollAt := clock.advance(15 * time.Second)
		fpp.setViews([]FPPInstanceView{{
			InstanceID: "player-01", Endpoint: "http://10.0.1.20",
			Observations: []observation.Observation{deltaObs(t, sigUptime, "idle", nextPollAt, time.Minute)},
			LastPollAt:   &nextPollAt,
		}})
		api.Hub.Notify()
	}

	expectNoFrameWithin(t, r, 1*time.Second,
		"five further polls with only ObservedAt/LastPollAt advancing on an unchanged value produced a spurious frame; maskEvidenceForDiff is not suppressing current-state observedAt churn in the delta cache")
}

// TestStreamDeltaClientMergingRemovedDoesNotRetainGhostSignal is the
// client-side half of the removal contract: it is not enough for the wire
// frame to name a removed signal (already proved above) — a client that
// actually applies "removed" the way ADR-023's Context section describes
// (delete it from the merged baseline) must end up with NO trace of it,
// not merely "the frame technically listed it". This simulates that
// application directly against the raw frame data, the same shape a real
// client's merge code would take.
func TestStreamDeltaClientMergingRemovedDoesNotRetainGhostSignal(t *testing.T) {
	fpp := &mutableFPPLister{}
	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt,
	}})

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)
	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	r, closeR := connectStream(t, srv, true)
	defer closeR()
	api.Hub.Notify()
	_, startData := readEventWithTimeout(t, r, 5*time.Second)

	var start struct {
		Instance v1.FPPInstance `json:"instance"`
	}
	if err := json.Unmarshal([]byte(startData), &start); err != nil {
		t.Fatalf("decoding fpp.changed: %v", err)
	}
	baseline := map[string]v1.Evidence{}
	for _, o := range start.Instance.Observations {
		baseline[o.Signal] = o
	}
	if _, ok := baseline[sigTemp]; !ok {
		t.Fatalf("baseline snapshot does not carry %s to begin with; test setup is wrong", sigTemp)
	}

	pollAt2 := pollAt.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute), // sigTemp gone
		},
		LastPollAt: &pollAt2,
	}})
	api.Hub.Notify()

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "fpp.observations.changed" {
		t.Fatalf("event = %q, want fpp.observations.changed; data: %s", event, data)
	}
	var delta v1.FPPObservationsChangedEvent
	if err := json.Unmarshal([]byte(data), &delta); err != nil {
		t.Fatalf("decoding fpp.observations.changed: %v", err)
	}

	// The client-side merge a real consumer performs: apply changed, then
	// apply removed.
	for _, o := range delta.Changed {
		baseline[o.Signal] = o
	}
	for _, sig := range delta.Removed {
		delete(baseline, sig)
	}

	if _, ok := baseline[sigTemp]; ok {
		t.Fatalf("a client applying the delta still retains %s after the coordinator reported it removed — this is the ghost-observation defect ADR-023 exists to prevent", sigTemp)
	}
	if _, ok := baseline[sigUptime]; !ok {
		t.Fatalf("a client applying the delta lost %s, which was never removed", sigUptime)
	}
}

// deltaClientModel is a minimal client-side merge, mirroring the shape a
// real client (the Operator UI, or a future showmeshctl consumer) applies:
// fpp.changed always fully replaces both the instance-level fields and the
// entire observation set (ADR-020: a *.changed event carries a resource's
// full current representation); fpp.observations.changed merges Changed by
// signal and deletes Removed signal IDs, touching nothing else.
type deltaClientModel struct {
	endpoint      string
	health        string
	lastPollError *string
	obs           map[string]v1.Evidence
}

func (m *deltaClientModel) applyFPPChanged(inst v1.FPPInstance) {
	m.endpoint = inst.Endpoint
	m.health = inst.Health
	m.lastPollError = inst.LastPollError
	m.obs = make(map[string]v1.Evidence, len(inst.Observations))
	for _, o := range inst.Observations {
		m.obs[o.Signal] = o
	}
}

func (m *deltaClientModel) applyObservationsChanged(ev v1.FPPObservationsChangedEvent) {
	if m.obs == nil {
		m.obs = map[string]v1.Evidence{}
	}
	for _, o := range ev.Changed {
		m.obs[o.Signal] = o
	}
	for _, sig := range ev.Removed {
		delete(m.obs, sig)
	}
}

// sortedEvidenceJSON renders m's observation set as a stable-order JSON
// array, so two independently built models can be compared for exact wire
// equality regardless of Go map iteration order.
func sortedEvidenceJSON(t *testing.T, obs map[string]v1.Evidence) string {
	t.Helper()
	signals := make([]string, 0, len(obs))
	for sig := range obs {
		signals = append(signals, sig)
	}
	sort.Strings(signals)
	list := make([]v1.Evidence, 0, len(signals))
	for _, sig := range signals {
		list = append(list, obs[sig])
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshaling evidence list: %v", err)
	}
	return string(b)
}

// TestDeltaAndFullFrameClientsConverge is ADR-023's own load-bearing
// property, named directly in its Consequences section: "a test must
// assert that a delta-subscribed client and a full-frame client converge
// on the same state from the same sequence of events, because that
// equivalence is the whole safety argument." One hub, two live SSE
// connections (one plain, one ?deltas=1), driven through an IDENTICAL
// sequence of underlying changes — instance-level-only, observation-only,
// a removal, and a combined change — each maintaining its own independent
// client-side model exactly the way a real consumer would
// ([deltaClientModel]). After the sequence, both models' merged
// observation sets, and both models' instance-level fields, must be
// byte-identical.
func TestDeltaAndFullFrameClientsConverge(t *testing.T) {
	fpp := &mutableFPPLister{}
	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt,
	}})

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)
	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	rFull, closeFull := connectStream(t, srv, false)
	defer closeFull()
	rDelta, closeDelta := connectStream(t, srv, true)
	defer closeDelta()

	full := &deltaClientModel{}
	delta := &deltaClientModel{}

	// applyFrame reads exactly one frame from r and applies it to m,
	// failing the test if it is neither fpp.changed nor
	// fpp.observations.changed.
	applyFrame := func(r *bufio.Reader, m *deltaClientModel) (event string) {
		t.Helper()
		event, data := readEventWithTimeout(t, r, 5*time.Second)
		switch event {
		case "fpp.changed":
			var ev v1.FPPChangedEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("decoding fpp.changed: %v; data: %s", err, data)
			}
			m.applyFPPChanged(ev.Instance)
		case "fpp.observations.changed":
			var ev v1.FPPObservationsChangedEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("decoding fpp.observations.changed: %v; data: %s", err, data)
			}
			m.applyObservationsChanged(ev)
		default:
			t.Fatalf("unexpected event %q; data: %s", event, data)
		}
		return event
	}

	// Step 1: first appearance. Both connections get fpp.changed.
	api.Hub.Notify()
	if ev := applyFrame(rFull, full); ev != "fpp.changed" {
		t.Fatalf("full client step 1: got %q, want fpp.changed", ev)
	}
	if ev := applyFrame(rDelta, delta); ev != "fpp.changed" {
		t.Fatalf("delta client step 1: got %q, want fpp.changed", ev)
	}

	// Step 2: instance-level-only change (endpoint). Both get fpp.changed.
	pollAt2 := pollAt.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(1), pollAt, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt2,
	}})
	api.Hub.Notify()
	if ev := applyFrame(rFull, full); ev != "fpp.changed" {
		t.Fatalf("full client step 2: got %q, want fpp.changed", ev)
	}
	if ev := applyFrame(rDelta, delta); ev != "fpp.changed" {
		t.Fatalf("delta client step 2: got %q, want fpp.changed", ev)
	}

	// Step 3: observation-only change (sigUptime's value moves). Full
	// client gets fpp.changed (full replace); delta client gets ONLY
	// fpp.observations.changed.
	pollAt3 := pollAt2.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(99), pollAt3, time.Minute),
			deltaObs(t, sigTemp, 45.0, pollAt, time.Minute),
		},
		LastPollAt: &pollAt3,
	}})
	api.Hub.Notify()
	if ev := applyFrame(rFull, full); ev != "fpp.changed" {
		t.Fatalf("full client step 3: got %q, want fpp.changed", ev)
	}
	if ev := applyFrame(rDelta, delta); ev != "fpp.observations.changed" {
		t.Fatalf("delta client step 3: got %q, want fpp.observations.changed", ev)
	}

	// Step 4: sigTemp disappears entirely.
	pollAt4 := pollAt3.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.21",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(99), pollAt3, time.Minute),
		},
		LastPollAt: &pollAt4,
	}})
	api.Hub.Notify()
	if ev := applyFrame(rFull, full); ev != "fpp.changed" {
		t.Fatalf("full client step 4: got %q, want fpp.changed", ev)
	}
	if ev := applyFrame(rDelta, delta); ev != "fpp.observations.changed" {
		t.Fatalf("delta client step 4: got %q, want fpp.observations.changed", ev)
	}

	// Step 5: a COMBINED change — endpoint AND an observation value move in
	// the same render pass. Full client gets one fpp.changed, as always.
	// Delta client gets ONLY fpp.changed too, NOT also
	// fpp.observations.changed: since the instance-level move already
	// widens fpp.changed's audience to include delta connections (ADR-023
	// decision 3), that one frame already carries this instance's full,
	// current Observations — a separate fpp.observations.changed in the
	// same pass would repeat information the delta client was just given,
	// never add anything new (see [Hub.render]'s fpp instance loop, the
	// `!instChanged` guard on the fpp.observations.changed append).
	pollAt5 := pollAt4.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.22",
		Observations: []observation.Observation{
			deltaObs(t, sigUptime, int64(150), pollAt5, time.Minute),
		},
		LastPollAt: &pollAt5,
	}})
	api.Hub.Notify()
	if ev := applyFrame(rFull, full); ev != "fpp.changed" {
		t.Fatalf("full client step 5: got %q, want fpp.changed", ev)
	}
	if ev := applyFrame(rDelta, delta); ev != "fpp.changed" {
		t.Fatalf("delta client step 5: got %q, want fpp.changed", ev)
	}

	// Neither connection should have anything else pending.
	expectNoFrameWithin(t, rFull, 500*time.Millisecond, "full client: unexpected extra frame after the scripted sequence")
	expectNoFrameWithin(t, rDelta, 500*time.Millisecond, "delta client: unexpected extra frame after the scripted sequence")

	// THE CONVERGENCE CHECK.
	if full.endpoint != delta.endpoint {
		t.Errorf("endpoint diverged: full=%q delta=%q", full.endpoint, delta.endpoint)
	}
	if full.health != delta.health {
		t.Errorf("health diverged: full=%q delta=%q", full.health, delta.health)
	}
	fullObs := sortedEvidenceJSON(t, full.obs)
	deltaObsJSON := sortedEvidenceJSON(t, delta.obs)
	if fullObs != deltaObsJSON {
		t.Errorf("observation sets diverged between the full-frame and delta clients after an identical event sequence:\nfull:  %s\ndelta: %s", fullObs, deltaObsJSON)
	}
	if _, ok := delta.obs[sigTemp]; ok {
		t.Errorf("delta client still holds removed signal %s", sigTemp)
	}
	if got, ok := delta.obs[sigUptime]; !ok {
		t.Errorf("delta client is missing %s entirely", sigUptime)
	} else if v, _ := got.Value.(float64); int64(v) != 150 {
		t.Errorf("delta client's %s value = %v, want 150 (the last value set)", sigUptime, got.Value)
	}
}
