package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// readSSEFrame reads one SSE dispatch (a run of non-blank lines terminated
// by a blank line) and reports its event name and data, or — for a
// comment-only keepalive frame — both empty. It returns an error if any
// line begins with "id:", which per contract section 6.4 must never appear
// on this stream; every test in this file that reads frames goes through
// this function specifically so that check runs unconditionally, not only
// where a test remembers to add it.
func readSSEFrame(r *bufio.Reader) (event, data string, err error) {
	var lines []string
	for {
		line, rerr := r.ReadString('\n')
		if rerr != nil {
			return "", "", rerr
		}
		line = strings.TrimRight(line, "\n")
		line = strings.TrimRight(line, "\r")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "id ") {
			return "", "", fmt.Errorf("SSE stream emitted a forbidden id: line: %q", line)
		}
		lines = append(lines, line)
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	return event, data, nil
}

// nextRealEvent skips any comment-only (keepalive) frames and returns the
// next actual named event.
func nextRealEvent(r *bufio.Reader) (event, data string, err error) {
	for {
		event, data, err = readSSEFrame(r)
		if err != nil {
			return "", "", err
		}
		if event != "" {
			return event, data, nil
		}
	}
}

// readEventWithTimeout runs nextRealEvent on r in its own goroutine and
// enforces d as a wall-clock timeout, so a bug that makes the hub never
// deliver an expected frame fails this test promptly instead of hanging
// until the whole test binary's timeout.
func readEventWithTimeout(t *testing.T, r *bufio.Reader, d time.Duration) (event, data string) {
	t.Helper()
	type result struct {
		event, data string
		err         error
	}
	ch := make(chan result, 1)
	go func() {
		e, dt, err := nextRealEvent(r)
		ch <- result{e, dt, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("reading SSE frame: %v", res.err)
		}
		return res.event, res.data
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for an SSE frame", d)
		return "", ""
	}
}

// newStreamTestAPI builds an [API] with tick and keepalive intervals long
// enough that neither fires during a short test, so every frame a test
// observes is one it deliberately triggered via [Hub.Notify].
func newStreamTestAPI(deps Dependencies) *API {
	return New(deps, Options{
		Clock:                   fixedClock(testNow),
		Logger:                  testLogger(),
		StreamTickInterval:      time.Hour,
		StreamKeepaliveInterval: time.Hour,
	})
}

func TestStreamStartIsFirstAndLastEventIDIgnored(t *testing.T) {
	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	// A client that thinks it can resume must have no effect whatsoever:
	// contract section 6.4 requires this header be ignored outright.
	req.Header.Set("Last-Event-ID", "999")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	r := bufio.NewReader(resp.Body)
	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "stream.start" {
		t.Fatalf("first event = %q, want \"stream.start\"", event)
	}

	var start struct {
		StreamID         string `json:"streamId"`
		APIVersion       int    `json:"apiVersion"`
		SnapshotRequired bool   `json:"snapshotRequired"`
	}
	if err := json.Unmarshal([]byte(data), &start); err != nil {
		t.Fatalf("decoding stream.start data: %v\ndata: %s", err, data)
	}
	if start.StreamID == "" {
		t.Errorf("streamId is empty")
	}
	if start.APIVersion != 1 {
		t.Errorf("apiVersion = %d, want 1", start.APIVersion)
	}
	if !start.SnapshotRequired {
		t.Errorf("snapshotRequired = false, want true")
	}
	// No "seq" key at all on stream.start: readSSEFrame does not check
	// this (it only forbids "id:"), so assert directly on the raw data
	// string per the contract's standing rule against struct round-trips.
	if strings.Contains(data, "\"seq\"") {
		t.Errorf("stream.start payload must not carry a seq field: %s", data)
	}
}

// readFrameWithTimeout is readEventWithTimeout's sibling for a frame that
// may legitimately be a comment-only keepalive (empty event/data), used by
// TestStreamSurvivesServerWriteTimeout below, which needs to observe
// keepalive frames specifically, not skip past them the way
// readEventWithTimeout/nextRealEvent deliberately do.
func readFrameWithTimeout(t *testing.T, r *bufio.Reader, d time.Duration) (event, data string) {
	t.Helper()
	type result struct {
		event, data string
		err         error
	}
	ch := make(chan result, 1)
	go func() {
		e, dt, err := readSSEFrame(r)
		ch <- result{e, dt, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("reading SSE frame: %v", res.err)
		}
		return res.event, res.data
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for an SSE frame", d)
		return "", ""
	}
}

// TestStreamSurvivesServerWriteTimeout is a regression guard for a real
// defect this task's own real-process integration harness found (not
// inspection): mounting this handler on an *http.Server with a
// WriteTimeout configured — exactly what
// internal/coordinator/httpapi.NewServer does, for its own ordinary REST
// probes' benefit — silently killed every SSE connection a few seconds
// after it opened. net/http.Server.WriteTimeout bounds the ENTIRE
// response-writing phase of one request and is reset only when that
// connection's next request is read, which never happens on a connection
// that is, by design, never-ending. [Hub.ServeHTTP] now clears the write
// deadline via http.NewResponseController right after upgrading to a
// stream (and [statusCapturingWriter.Unwrap] is what lets that call see
// through withRequestLogging's wrapper to the real connection). This test
// proves the fix by configuring a WriteTimeout shorter than the keepalive
// interval that would otherwise trip it, and confirming several keepalive
// frames still arrive on schedule.
//
// Before the fix, this test failed with "stream closed (WriteTimeout
// defect regressed)" on or shortly after the first keepalive.
func TestStreamSurvivesServerWriteTimeout(t *testing.T) {
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{
		Clock:              fixedClock(testNow),
		Logger:             testLogger(),
		StreamTickInterval: time.Hour,
		// Step 3 review finding 4.8: this used to be 100ms against a 50ms
		// WriteTimeout below — a margin exactly one scheduling hiccup wide
		// on a loaded CI runner, and one that fails as a fatal read error
		// (indistinguishable, at a glance, from a real regression of the
		// fix this test guards). Widened to a comfortable order of
		// magnitude of slack while keeping the same shape (WriteTimeout
		// shorter than the keepalive interval, deliberately) that proves
		// the fix.
		StreamKeepaliveInterval: 1 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	ts := httptest.NewUnstartedServer(api.Handler)
	ts.Config.WriteTimeout = 100 * time.Millisecond // shorter than the 1s keepalive interval above, deliberately
	ts.Start()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/stream") //nolint // test-only, no context needed
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	r := bufio.NewReader(resp.Body)
	if event, _ := readFrameWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want \"stream.start\"", event)
	}

	// Read several more frames (keepalive comments), each of which —
	// absent the fix — would never arrive, because the connection would
	// already have been closed by the server's 100ms WriteTimeout, well
	// before the 1s keepalive interval that produces them.
	for i := 0; i < 4; i++ {
		readFrameWithTimeout(t, r, 5*time.Second)
	}
}

func TestStreamNodeChangedDeliveredOnNotify(t *testing.T) {
	nodes := &fakeNodeLister{}
	api := newStreamTestAPI(Dependencies{
		Nodes: nodes, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
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

	nodes.setViews([]inventory.NodeView{onlineNodeFixture(t)})
	api.Hub.Notify()

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "node.changed" {
		t.Fatalf("event = %q, want node.changed; data: %s", event, data)
	}
	if !strings.Contains(data, "\"nodeId\":\"media-03\"") {
		t.Errorf("node.changed data does not carry the expected node: %s", data)
	}
	if !strings.Contains(data, "\"seq\":1") {
		t.Errorf("first frame after stream.start should carry seq 1: %s", data)
	}
}

// TestStreamNodeChangedNotResentWhenNothingChangedAsClockAdvances is a
// regression guard for a real defect this task's own real-process
// integration harness found (not inspection, and not this package's own
// prior test suite, which could not have caught it — see below): mapNode's
// synthesized node.hello/node.control_plane.last_will/node.heartbeat
// evidence used to stamp CollectedAt from the render's OWN current time on
// every call. [Hub.updateRendered] detects change by comparing rendered
// JSON byte-for-byte against what it last published (contract section
// 6.5), and CollectedAt is part of that JSON, so a value that advances on
// every render made every node that has ever received a hello look
// "changed" on every single hub tick (every 5 seconds by default),
// forever, with nothing about the node having actually changed at all —
// discovered by watching a single, completely idle node keep re-appearing
// as node.changed on a real coordinator process every few seconds.
//
// EVERY EXISTING TEST IN THIS FILE uses [newStreamTestAPI], whose Clock is
// [fixedClock] — frozen at one instant for the test's whole lifetime. That
// is exactly why this defect was invisible here before: with a frozen
// clock, "now" never advances between two render passes, so the old,
// buggy `WithCollectedAt(now)` produced byte-identical output on a second
// call purely by accident, passing every existing test whether or not the
// bug was present. This test uses an ADVANCING fake clock specifically to
// close that gap: two Notify() calls with the SAME underlying node data,
// clock advanced in between, asserting the SECOND one delivers no frame at
// all.
func TestStreamNodeChangedNotResentWhenNothingChangedAsClockAdvances(t *testing.T) {
	nodes := &fakeNodeLister{}
	nodes.setViews([]inventory.NodeView{onlineNodeFixture(t)})

	clockTime := testNow
	advancingClock := func() time.Time { return clockTime }

	api := New(Dependencies{
		Nodes: nodes, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{
		Clock: advancingClock, Logger: testLogger(),
		StreamTickInterval: time.Hour, StreamKeepaliveInterval: time.Hour,
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

	// First Notify: the node is genuinely new to the hub, so exactly one
	// node.changed frame is expected.
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "node.changed" {
		t.Fatalf("first Notify: event = %q, want node.changed", event)
	}

	// Advance the clock — simulating real wall-clock time passing between
	// hub renders, exactly as production's time.Now()-backed Clock does —
	// and Notify again with NOTHING about the underlying node data changed.
	// If CollectedAt (or anything else) were being re-stamped from "now" on
	// each render, this would produce a second, spurious node.changed
	// frame.
	clockTime = clockTime.Add(10 * time.Second)
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
		t.Fatalf("second Notify with no underlying change produced a spurious %q event; the hub is re-broadcasting unchanged data as the clock advances", ev)
	case <-time.After(1 * time.Second):
		// Correct: no frame arrived. A short, fixed wait is acceptable here
		// (not a sleep-and-hope for a POSITIVE outcome) because this
		// package's own StreamTickInterval is set to an hour above, so
		// nothing else could legitimately produce a frame in this window;
		// the only way this arm is reached wrongly is via the exact bug
		// this test exists to catch, and that bug fires deterministically
		// on the very next render, not intermittently.
	}
}

// TestHubBroadcastSignalsResetOnOverflow is a white-box unit test of
// [Hub.broadcast]'s overflow path with no HTTP or network involved at
// all: it subscribes directly, deliberately never drains sub.frames (the
// "deliberately non-reading client" Task D spec section 5 asks for), and
// pushes one more frame than the buffer holds.
//
// This is deliberately not driven through a real network connection.
// [Hub.ServeHTTP]'s own goroutine drains sub.frames purely by receiving
// from a Go channel — it has no idea whether the client on the other end
// of the socket has called Read, and a small SSE frame fits easily in the
// kernel's own socket send buffer regardless. Actually filling the channel
// over a live httptest.Server connection would depend on winning a race
// against that goroutine's own scheduling, which is exactly the kind of
// flaky timing test Task D spec section 7 asks this suite to avoid.
// Calling [Hub.subscribe] and [Hub.broadcast] directly removes the race
// entirely: this test's only actor is the test goroutine itself.
// [TestStreamServesResetFrameAndClosesOnSignal] below covers the other
// half — that [Hub.ServeHTTP] does the right thing once a reset signal
// exists — without needing to reproduce the overflow condition itself.
func TestHubBroadcastSignalsResetOnOverflow(t *testing.T) {
	hub := newHub(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{StreamSubscriberBuffer: 1}.withDefaults(), testLogger())

	id, sub := hub.subscribe()
	defer hub.unsubscribe(id)

	frame := pendingFrame{event: "node.changed", serverTime: formatTime(testNow), node: &v1.Node{}}

	hub.broadcast(frame) // fills the capacity-1 buffer; no reset yet.
	select {
	case reason := <-sub.reset:
		t.Fatalf("unexpected reset signal after only filling (not overflowing) the buffer: %q", reason)
	default:
	}

	hub.broadcast(frame) // the buffer is still full: this one overflows it.
	select {
	case reason := <-sub.reset:
		if reason != "subscriber_too_slow" {
			t.Errorf("reset reason = %q, want \"subscriber_too_slow\"", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("expected an overflow signal on sub.reset, got none")
	}
}

// TestStreamServesResetFrameAndClosesOnSignal proves the other half of
// contract section 6.4's overflow rule: once a subscriber's reset channel
// carries a reason — exactly what [Hub.broadcast] deterministically
// produces, per [TestHubBroadcastSignalsResetOnOverflow] above — a live
// SSE connection writes a stream.reset frame carrying that reason and then
// closes, rather than continuing to deliver node/fpp/event frames. It
// reaches into the hub's live subscriber map (this test file is in
// package api, not an external test package) to deliver the signal
// directly, again avoiding any dependency on actually winning a buffering
// race over a real socket.
func TestStreamServesResetFrameAndClosesOnSignal(t *testing.T) {
	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
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

	sub := onlySubscriber(t, api.Hub)
	sub.reset <- "subscriber_too_slow"

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "stream.reset" {
		t.Fatalf("event = %q, want stream.reset; data: %s", event, data)
	}
	if !strings.Contains(data, "subscriber_too_slow") {
		t.Errorf("stream.reset data = %s, want it to name reason subscriber_too_slow", data)
	}
	if !strings.Contains(data, "\"snapshotRequired\":true") {
		t.Errorf("stream.reset data = %s, want snapshotRequired true", data)
	}
	if !strings.Contains(data, "\"seq\":1") {
		t.Errorf("stream.reset data = %s, want seq 1 (first frame after stream.start)", data)
	}

	// The connection must close right after: the next read must fail
	// (EOF), never deliver another frame.
	if _, _, err := readSSEFrame(r); err == nil {
		t.Fatalf("expected the connection to close after stream.reset, but another frame was read")
	}
}

// onlySubscriber returns the hub's single active subscriber, failing the
// test if there is not exactly one. Used only by tests that need to reach
// past the public API into the hub's live connection state.
func onlySubscriber(t *testing.T, h *Hub) *subscriber {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) != 1 {
		t.Fatalf("expected exactly 1 active subscriber, got %d", len(h.subscribers))
	}
	for _, s := range h.subscribers {
		return s
	}
	return nil
}

// TestStreamShutdownLeavesNoGoroutinesLeaked opens several SSE connections,
// cancels the hub's context, and asserts the goroutine count returns to
// baseline — contract section 6.4's "shutdown closes every stream cleanly
// and does not leak goroutines," proved under -race per the contract's
// own instruction.
//
// This is finding 1.3's fix as well as the original leak-detector test: an
// earlier version of this test cancelled the hub's context and THEN closed
// every response body itself before checking the goroutine count, which
// independently triggers server-side r.Context().Done() on every open
// connection — so every [Hub.ServeHTTP] goroutine could exit through THAT
// arm regardless of whether Hub.Run's own cancellation (the `case
// <-h.done: return` arm) does anything at all. Confirmed by mutation:
// deleting that arm left the old version of this test green. The fix below
// never closes a response body until AFTER proving each connection was
// already ended by the SERVER — a clean EOF read with no client-side
// action preceding it — which is the only thing that can only happen via
// h.done.
func TestStreamShutdownLeavesNoGoroutinesLeaked(t *testing.T) {
	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() {
		api.Hub.Run(ctx)
		close(hubDone)
	}()

	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const n = 5
	var mu sync.Mutex
	var responses []*http.Response
	for i := 0; i < n; i++ {
		resp, err := http.Get(srv.URL + "/api/v1/stream")
		if err != nil {
			t.Fatalf("GET /api/v1/stream: %v", err)
		}
		r := bufio.NewReader(resp.Body)
		if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
			t.Fatalf("connection %d: first event = %q, want stream.start", i, event)
		}
		mu.Lock()
		responses = append(responses, resp)
		mu.Unlock()
	}

	cancel()
	select {
	case <-hubDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Hub.Run did not return after its context was cancelled")
	}

	// The actual shutdown proof: read each connection to completion
	// WITHOUT closing it from the client side first. Only a clean,
	// client-uninitiated EOF here is evidence the coordinator's own
	// shutdown — not this test — ended the stream.
	for i, resp := range responses {
		buf := make([]byte, 1)
		readDone := make(chan error, 1)
		go func() {
			_, err := resp.Body.Read(buf)
			readDone <- err
		}()
		select {
		case err := <-readDone:
			if !errors.Is(err, io.EOF) {
				t.Errorf("connection %d: expected io.EOF from a server-initiated close after Hub.Run's context was cancelled, got %v", i, err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("connection %d: not closed by the server within 3s of Hub.Run's context being cancelled; the <-h.done shutdown path may not be firing", i)
		}
	}

	// Only now, with the shutdown itself already proven above, close the
	// bodies for cleanup and confirm the goroutine count returns to
	// baseline — the pre-existing leak-detector half of this test, which
	// review confirmed genuinely bites (proved by injecting a deliberate
	// leak).
	for _, resp := range responses {
		_ = resp.Body.Close()
	}
	// http.Get uses http.DefaultClient / http.DefaultTransport, whose
	// per-connection readLoop/writeLoop goroutines wind down once idle
	// connections are reclaimed — nudged explicitly rather than waited
	// for, since this test now reaches this point via a real EOF read
	// (proven above) rather than an immediate client-initiated close, and
	// the transport's own teardown latency is not what this test exists
	// to measure.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}

	deadline := time.Now().Add(5 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		runtime.GC()
		last = runtime.NumGoroutine()
		if last <= baseline+1 { // small slack for the runtime's own bookkeeping
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("goroutine count did not return to baseline: got %d, baseline %d", last, baseline)
}

// waitForSubscriber polls h's subscriber map until it holds exactly one
// entry, or fails the test after d. Used where a test needs the hub to
// have finished registering a connection opened via a raw net.Conn (so
// there is no [http.Response] to synchronize on the way the other tests in
// this file do via reading stream.start).
func waitForSubscriber(t *testing.T, h *Hub, d time.Duration) (id uint64, ok bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.subscribers) == 1 {
			for k := range h.subscribers {
				id = k
			}
			h.mu.Unlock()
			return id, true
		}
		h.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return 0, false
}

// hubHasSubscriber reports whether id is still a live entry in h's
// subscriber map.
func hubHasSubscriber(h *Hub, id uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.subscribers[id]
	return ok
}

// hubHasRenderedKey reports whether h.lastRendered still holds a cached
// diff key for key.
func hubHasRenderedKey(h *Hub, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.lastRendered[key]
	return ok
}

// TestStreamWedgedSubscriberIsReclaimedByWriteDeadline is finding 1.1's
// regression guard, the severe one: a subscriber that stops reading
// entirely, holding its TCP connection open, must not be able to pin
// [Hub.ServeHTTP]'s goroutine — and with it, its subscriber entry and
// every buffered frame — forever. Before the fix, ServeHTTP cleared its
// write deadline unconditionally right after upgrading and never set
// another one, so a write into a full kernel send buffer blocked inside
// w.Write with no way to time out; the hub's own overflow signal on
// sub.reset was queued but never read, because ServeHTTP never returned to
// its select to look for it. See [streamWriteTimeout]'s doc comment for
// the full failure description.
//
// This drives a raw net.Conn rather than net/http's client, specifically
// so "provably never reads" is literal: no background goroutine anywhere,
// not even inside a client library, ever drains this socket after the
// request line is sent. It floods the hub with a large burst of distinct
// node.changed frames via [Hub.broadcast] directly — the same technique
// [TestHubBroadcastSignalsResetOnOverflow] uses and justifies in its own
// doc comment (a real render/Notify-driven flood would race the hub's own
// coalescing and drain goroutine) — comfortably more raw bytes than any
// realistic combination of OS default socket buffers and this
// connection's own shrunk receive window can absorb while nothing drains
// it, which is exactly finding 1.1's condition: ServeHTTP's write into the
// kernel send buffer blocks because the peer is not reading.
func TestStreamWedgedSubscriberIsReclaimedByWriteDeadline(t *testing.T) {
	origTimeout := streamWriteTimeout
	streamWriteTimeout = 200 * time.Millisecond
	defer func() { streamWriteTimeout = origTimeout }()

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing server URL: %v", err)
	}

	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		// Best effort only: a small receive window makes the server's
		// writes block sooner, keeping this test fast. Not required for
		// correctness — the flood below is sized to exceed a realistic
		// default socket buffer even if the OS ignores this hint.
		_ = tcpConn.SetReadBuffer(1024)
	}

	req := "GET /api/v1/stream HTTP/1.1\r\nHost: " + u.Host + "\r\nConnection: keep-alive\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("writing request line: %v", err)
	}
	// From here on this connection NEVER calls Read again — see the
	// function doc comment for why that is the point.

	id, ok := waitForSubscriber(t, api.Hub, 2*time.Second)
	if !ok {
		t.Fatalf("hub never registered a subscriber for the connection")
	}

	// A handful of DELIBERATELY HUGE frames rather than a large count of
	// small ones: a small-JSON flood was tried first and turned out to be
	// exactly the decoy this project's standing rule warns about — it
	// passed whether or not the fix was present, because [subscriber.frames]'s
	// bounded channel (capacity 64 by default) becomes the bottleneck
	// long before enough cumulative bytes ever reach the actual socket to
	// fill a real kernel send buffer; each individual write stayed small
	// enough to complete instantly regardless of whether the peer was
	// reading. One multi-megabyte Label, by contrast, cannot be
	// buffered away by ANYTHING short of the peer actually reading it: a
	// single w.Write call this large is guaranteed to block on a
	// non-draining connection on any realistic kernel socket buffer,
	// which is the actual condition finding 1.1 describes.
	hugeLabel := strings.Repeat("x", 8*1024*1024)
	const floodFrames = 8
	for i := 0; i < floodFrames; i++ {
		api.Hub.broadcast(pendingFrame{
			event: "node.changed", serverTime: formatTime(testNow),
			node: &v1.Node{NodeID: "flood", Label: &hugeLabel},
		})
	}

	// Before the fix, ServeHTTP would block forever inside one of the
	// writes the flood above triggers and never reach its deferred
	// unsubscribe: the subscriber entry, its buffered frames, and this
	// goroutine would all be pinned until process exit. With the fix, the
	// deadline reset immediately before that write (streamWriteTimeout,
	// shrunk above) expires, turning the block into an error, which tears
	// the connection down and runs unsubscribe.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !hubHasSubscriber(api.Hub, id) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wedged subscriber %d was never reclaimed within the wait bound (streamWriteTimeout=%s)", id, streamWriteTimeout)
}

// mutableFPPLister is a thread-safe [FPPLister] test double whose views can
// be replaced between two hub render passes — [fakeFPPLister] in
// fakes_test.go has no equivalent to fakeNodeLister's setViews, and that
// file belongs to a different task's ownership boundary for this build
// step, so this is a small double declared in this file instead, used only
// by the test below that needs to simulate the FPP collector's next poll
// attempt landing with different bookkeeping but the same reported state.
type mutableFPPLister struct {
	mu    sync.Mutex
	views []FPPInstanceView
}

func (f *mutableFPPLister) setViews(views []FPPInstanceView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views = views
}

func (f *mutableFPPLister) ListInstances(context.Context) ([]FPPInstanceView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.views, nil
}

// TestStreamFPPChangedNotResentWhenOnlyCollectionBookkeepingMoves is
// finding 1.5's regression guard. The review observed it live: seventy
// seconds of an idle stream against an unreachable FPP produced two
// fpp.changed frames whose payloads differed only in collectedAt. This
// test reproduces the same shape deterministically: an FPP instance the
// collector keeps failing to reach identically across two poll attempts,
// where only CollectedAt and LastPollAt — pure collection bookkeeping,
// never evidence of the FPP's own state (contract section 6.2) — advance
// between them.
func TestStreamFPPChangedNotResentWhenOnlyCollectionBookkeepingMoves(t *testing.T) {
	res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}
	failureAt := func(collectedAt time.Time) observation.Observation {
		o, err := observation.CollectionFailed(res, "fpp.multisync.enabled", "connection refused",
			observation.WithSource("fpp-rest"), observation.WithCollectedAt(collectedAt))
		if err != nil {
			t.Fatalf("building fixture observation: %v", err)
		}
		return o
	}

	pollAt1 := testNow
	fpp := &mutableFPPLister{}
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{failureAt(pollAt1)},
		LastPollAt:   &pollAt1,
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

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start", event)
	}

	// First poll: genuinely new to the hub, so exactly one fpp.changed.
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("first Notify: event = %q, want fpp.changed", event)
	}

	// Second poll attempt: same failure, same reason — only CollectedAt
	// and LastPollAt advance, simulating the collector's next scheduled
	// attempt against an FPP that is STILL unreachable.
	pollAt2 := pollAt1.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{failureAt(pollAt2)},
		LastPollAt:   &pollAt2,
	}})
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
		t.Fatalf("second poll with only collection bookkeeping changed produced a spurious %q event; the hub is diffing on collectedAt/lastPollAt", ev)
	case <-time.After(1 * time.Second):
		// Correct: this package's own StreamTickInterval is set to an
		// hour above, so nothing else could legitimately produce a frame
		// in this window; see
		// TestStreamNodeChangedNotResentWhenNothingChangedAsClockAdvances
		// for the identical reasoning applied to the node.changed case.
	}
}

// TestStreamLastRenderedEvictsResourcesNoLongerPresent is finding 1.6's
// regression guard: a resource that disappears from a render pass (its
// node deleted from inventory, its FPP instance removed from
// configuration) must have its cached diff key forgotten, not pinned in
// [Hub.lastRendered] for the coordinator's entire remaining lifetime. It
// proves eviction actually happened by observing its effect rather than
// only inspecting the map: the SAME node ID reappearing afterwards with
// IDENTICAL content is re-announced as node.changed, which only happens if
// its prior rendering was actually forgotten rather than merely present
// but unread.
func TestStreamLastRenderedEvictsResourcesNoLongerPresent(t *testing.T) {
	nodes := &fakeNodeLister{}
	nodes.setViews([]inventory.NodeView{onlineNodeFixture(t)})

	api := newStreamTestAPI(Dependencies{
		Nodes: nodes, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
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

	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "node.changed" {
		t.Fatalf("first Notify: event = %q, want node.changed", event)
	}
	if !hubHasRenderedKey(api.Hub, "node:media-03") {
		t.Fatalf("expected the hub to have cached a rendering for media-03 after its first appearance")
	}

	// The node disappears from inventory entirely.
	nodes.setViews(nil)
	api.Hub.Notify()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && hubHasRenderedKey(api.Hub, "node:media-03") {
		time.Sleep(20 * time.Millisecond)
	}
	if hubHasRenderedKey(api.Hub, "node:media-03") {
		t.Fatalf("hub.lastRendered still holds media-03's rendering after it disappeared from inventory")
	}

	// It reappears with IDENTICAL content. Because its diff key was
	// evicted, this must be treated as new again.
	nodes.setViews([]inventory.NodeView{onlineNodeFixture(t)})
	api.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "node.changed" {
		t.Fatalf("node reappearing with identical content after eviction should be re-announced as node.changed, got %q", event)
	}
}

// TestRenderNewEventsPassesBatchLimitAndCursorToListEvents is finding
// 1.4's most basic gap closed: [Hub.renderNewEvents] had zero coverage of
// what it actually asks the store for. This pins the batch limit
// ([hubEventsBatchLimit]) and the initial cursor (0, for a hub that has
// never rendered an event before) as the exact arguments passed to
// ListEvents.
func TestRenderNewEventsPassesBatchLimitAndCursorToListEvents(t *testing.T) {
	events := &fakeEventReader{latest: 10}
	hub := newHub(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}, Options{}.withDefaults(), testLogger())

	hub.renderNewEvents(context.Background(), testNow)

	if events.gotSince != 0 {
		t.Errorf("ListEvents since = %d, want 0 for a hub with no prior cursor", events.gotSince)
	}
	if events.gotLimit != hubEventsBatchLimit {
		t.Errorf("ListEvents limit = %d, want hubEventsBatchLimit (%d)", events.gotLimit, hubEventsBatchLimit)
	}
}

// TestRenderNewEventsAdvancesCursorToHighestDeliveredSeqAndStopsWhenCaughtUp
// covers the cursor-advancement half of finding 1.4: every returned record
// becomes exactly one pendingFrame, in the same ascending order ListEvents
// returned them, and [Hub.lastEventSeq] advances to the highest seq
// actually turned into a frame — never further, and never less — after
// which a second call with nothing new recorded returns no frames at all
// (the "latest <= since" early return, also previously unexercised).
func TestRenderNewEventsAdvancesCursorToHighestDeliveredSeqAndStopsWhenCaughtUp(t *testing.T) {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: "media-03"}
	rec := func(seq uint64) EventRecord {
		return EventRecord{
			Seq: seq, RecordedAt: testNow, Source: "mqtt-inventory", Resource: res,
			Category: "control_plane", Severity: "informational", Summary: fmt.Sprintf("event %d", seq),
		}
	}
	events := &fakeEventReader{
		latest:  12,
		records: []EventRecord{rec(10), rec(11), rec(12)},
	}
	hub := newHub(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}, Options{}.withDefaults(), testLogger())

	frames := hub.renderNewEvents(context.Background(), testNow)
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	for i, pf := range frames {
		if pf.event != "event.recorded" {
			t.Errorf("frame %d: event = %q, want event.recorded", i, pf.event)
		}
		wantSeq := uint64(10 + i)
		if pf.ev == nil || pf.ev.Seq != wantSeq {
			t.Errorf("frame %d: seq = %v, want %d (ascending order preserved)", i, pf.ev, wantSeq)
		}
	}

	hub.mu.Lock()
	got := hub.lastEventSeq
	hub.mu.Unlock()
	if got != 12 {
		t.Fatalf("lastEventSeq = %d, want 12 (the highest delivered seq)", got)
	}

	// Caught up: latest (12) <= since (12) now, so no further ListEvents
	// call should even happen.
	events.gotSince, events.gotLimit = 999, 999 // sentinel, must not be overwritten
	frames2 := hub.renderNewEvents(context.Background(), testNow)
	if len(frames2) != 0 {
		t.Errorf("second call returned %d frames, want 0 (already caught up to latest)", len(frames2))
	}
	if events.gotSince != 999 || events.gotLimit != 999 {
		t.Errorf("ListEvents was called again after the cursor caught up to latest; want no further query")
	}
}

// TestRenderNewEventsSkipsPrunedRangeWhenOldestIsKnown covers the
// gap-with-known-oldest cursor-skip branch finding 1.4 flagged as
// unexercised: when ListEvents reports gap: true and OldestEventSeq can
// still name the oldest row the store retains, the cursor jumps to
// oldest-1 rather than retrying the pruned range forever.
func TestRenderNewEventsSkipsPrunedRangeWhenOldestIsKnown(t *testing.T) {
	events := &fakeEventReader{
		latest: 100, gap: true, hasOld: true, oldest: 42,
	}
	hub := newHub(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}, Options{}.withDefaults(), testLogger())

	hub.renderNewEvents(context.Background(), testNow)

	hub.mu.Lock()
	got := hub.lastEventSeq
	hub.mu.Unlock()
	if got != 41 {
		t.Fatalf("lastEventSeq = %d, want 41 (oldest-1, skipping the pruned range)", got)
	}
}

// TestRenderNewEventsAdvancesCursorWhenHistoryIsFullyPruned is the
// regression guard for the hazard finding 1.4 flagged without confirming
// reachability: "if oldest-1 <= newCursor the cursor does not advance and
// the hub re-queries the same pruned range on every tick forever." Reading
// internal/coordinator/store/events.go establishes that this IS reachable
// — OldestEventSeq reports ok: false whenever the events table currently
// holds zero rows (every row ever inserted has since been pruned), while
// LatestEventSeq (reading sqlite_sequence, which survives the table being
// fully deleted) can still be greater than the hub's stale cursor, and
// ListEvents legitimately returns gap: true with zero records for that
// same range. Before the fix, this combination left [Hub.lastEventSeq]
// stuck at its old value forever, retrying an identical query every tick.
func TestRenderNewEventsAdvancesCursorWhenHistoryIsFullyPruned(t *testing.T) {
	events := &fakeEventReader{
		latest: 500, gap: true, hasOld: false, // the table holds zero events
	}
	hub := newHub(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}, Options{}.withDefaults(), testLogger())

	hub.renderNewEvents(context.Background(), testNow)

	hub.mu.Lock()
	got := hub.lastEventSeq
	hub.mu.Unlock()
	if got != 500 {
		t.Fatalf("lastEventSeq = %d, want 500 (advanced to latest; nothing further could ever be retrieved for this range)", got)
	}

	// Caught up: a further call must not re-query the store at all.
	events.gotSince, events.gotLimit = 999, 999 // sentinel, must not be overwritten
	hub.renderNewEvents(context.Background(), testNow)
	if events.gotSince != 999 || events.gotLimit != 999 {
		t.Errorf("ListEvents was called again after the cursor caught up to latest; want no further query — the infinite-retry hazard is back")
	}
}

// TestStreamEventRecordedDeliveredOverSSE closes the remaining half of
// finding 1.4's "event.recorded is asserted nowhere": the white-box tests
// above prove [Hub.renderNewEvents]'s internal cursor logic in isolation,
// but nothing previously drove an actual event.recorded frame through a
// live SSE connection at all. This does: two events recorded before this
// hub ever renders are delivered as two event.recorded frames, in order,
// each carrying its OWN per-connection seq (contract section 6.4) rather
// than the durable [EventRecord.Seq] the events themselves carry inside
// their payload — asserted on the raw wire bytes per the contract's
// standing rule against struct round-trips.
func TestStreamEventRecordedDeliveredOverSSE(t *testing.T) {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: "media-03"}
	events := &fakeEventReader{
		latest: 6,
		records: []EventRecord{
			{Seq: 5, RecordedAt: testNow, Source: "mqtt-inventory", Resource: res,
				Category: "control_plane", Severity: "informational", Summary: "first"},
			{Seq: 6, RecordedAt: testNow, Source: "mqtt-inventory", Resource: res,
				Category: "control_plane", Severity: "informational", Summary: "second"},
		},
	}

	api := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
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

	api.Hub.Notify()

	event1, data1 := readEventWithTimeout(t, r, 5*time.Second)
	if event1 != "event.recorded" {
		t.Fatalf("event = %q, want event.recorded; data: %s", event1, data1)
	}
	if !strings.Contains(data1, `"summary":"first"`) {
		t.Errorf("first frame data does not carry the first event's summary: %s", data1)
	}
	if !strings.Contains(data1, `"seq":5`) {
		t.Errorf("first frame data missing the durable EventRecord.Seq (5) inside its event payload: %s", data1)
	}
	if !strings.Contains(data1, `"seq":1`) {
		t.Errorf("first frame's own per-connection seq should be 1 (the first frame after stream.start): %s", data1)
	}

	event2, data2 := readEventWithTimeout(t, r, 5*time.Second)
	if event2 != "event.recorded" {
		t.Fatalf("event = %q, want event.recorded; data: %s", event2, data2)
	}
	if !strings.Contains(data2, `"summary":"second"`) {
		t.Errorf("second frame data does not carry the second event's summary: %s", data2)
	}
	if !strings.Contains(data2, `"seq":2`) {
		t.Errorf("second frame's own per-connection seq should be 2: %s", data2)
	}

	// A second Notify with nothing new recorded must not re-deliver either
	// event.
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
		t.Fatalf("second Notify with nothing new recorded produced a spurious %q event", ev)
	case <-time.After(1 * time.Second):
		// Correct: StreamTickInterval is an hour, and the hub's cursor
		// already caught up to latest on the first Notify.
	}
}
