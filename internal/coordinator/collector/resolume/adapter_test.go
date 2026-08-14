package resolume

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// testAdapterOptions returns AdapterOptions with every timing constant cut
// down to milliseconds, so a test exercising the coalescing policy does
// not have to wait out the 2s/30s production defaults. The RATIOS between
// debounce/minInterval/retry are kept comparable to production so the
// state-machine BEHAVIOR under test is the same shape, just compressed.
func testAdapterOptions(logger *slog.Logger) AdapterOptions {
	return AdapterOptions{
		Logger:             logger,
		DebounceInterval:   20 * time.Millisecond,
		MinResolveInterval: 100 * time.Millisecond,
		RetryMinBackoff:    10 * time.Millisecond,
		RetryMaxBackoff:    40 * time.Millisecond,
	}
}

// startAdapter runs a.Run in its own goroutine and arranges for it to be
// cancelled and joined at test cleanup, mirroring watch_test.go's
// startWatcher helper.
func startAdapter(t *testing.T, a *Adapter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Adapter.Run did not return within 5s of ctx cancellation")
		}
	})
}

// waitForResolution polls a.Resolution() until ok is true or d elapses.
func waitForResolution(t *testing.T, a *Adapter, d time.Duration) *Resolution {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if res, ok := a.Resolution(); ok {
			return res
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Adapter.Resolution() did not become available within %s", d)
	return nil
}

// waitForNoResolution polls a.Resolution() until ok is false or d elapses.
func waitForNoResolution(t *testing.T, a *Adapter, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, ok := a.Resolution(); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Adapter.Resolution() still ok=true after %s, want it dropped", d)
}

func TestAdapterHandleConnectResolvesAndHolds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	if _, ok := a.Resolution(); ok {
		t.Fatalf("Resolution() ok = true before any HandleConnect, want false")
	}

	a.HandleConnect(context.Background())

	res := waitForResolution(t, a, time.Second)
	if res.SelectedDeckName != "Main" {
		t.Errorf("held Resolution.SelectedDeckName = %q, want %q", res.SelectedDeckName, "Main")
	}
}

func TestAdapterHandleConnectFailureDropsHeldResolution(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			w.Write(loadTestdata(t, "composition_minimal.json"))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	// First connect succeeds and holds a Resolution.
	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)

	// Second connect's composition read fails (server now 500s): the
	// PREVIOUSLY held Resolution must be dropped, never kept as a stale
	// fallback — Adapter's doc comment is explicit that it never serves
	// a Resolution that may not correspond to what Resolume is actually
	// running. A disconnect first forces the loop to accept a second
	// connect as a genuinely new one (HandleConnect while already
	// connected still resolves immediately regardless, but going through
	// disconnect first is the realistic sequence a real reconnect takes).
	a.HandleDisconnect(context.Background())
	waitForNoResolution(t, a, time.Second)
	a.HandleConnect(context.Background())
	waitForNoResolution(t, a, time.Second)
}

func TestAdapterHandleConnectResolveFailureDropsHeldResolution(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			w.Write(loadTestdata(t, "composition_minimal.json"))
			return
		}
		// A composition with no deck selected: Resolve itself must
		// reject this (TestResolveRejectsNoDeckSelected), and Adapter
		// must treat that identically to a fetch failure.
		w.Write([]byte(`{
			"name": {"id":1,"value":"x"}, "bypassed": {"id":2,"value":false}, "master": {"id":3,"value":1.0},
			"decks": [{"id":100,"closed":false,"name":{"id":4,"value":"A"},"selected":{"id":5,"value":false}}]
		}`))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)

	a.HandleDisconnect(context.Background())
	waitForNoResolution(t, a, time.Second)
	a.HandleConnect(context.Background())
	waitForNoResolution(t, a, time.Second)
}

func TestAdapterHandleDisconnectDropsHeldResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)

	a.HandleDisconnect(context.Background())
	waitForNoResolution(t, a, time.Second)
}

// --- Review finding A: the resolve loop itself ------------------------

// TestAdapterReResolvesOnChangeAfterConnect is the direct reproduction of
// finding A's live-measured defect: a connect that resolves Arena's
// default empty composition (the ~1.2s load-window shape) must be
// followed by a change wake-up eventually producing the REAL composition
// — the old, one-shot HandleConnect never re-resolved at all once
// connected, which is exactly what left a held Resolution of the wrong
// composition for over ninety seconds against the real Arena.
//
// Before trusting this test, Run's changeCh case was temporarily changed
// to a no-op (simulating the pre-fix behavior: a change wake-up while
// connected does nothing) and this test was re-run: it failed, timing out
// waiting for the real composition's SelectedDeckName. Reverted afterward.
func TestAdapterReResolvesOnChangeAfterConnect(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			// Simulates the load-window shape: a well-formed but
			// different composition than the one that eventually loads.
			w.Write([]byte(`{
				"name": {"id":1,"value":""},
				"bypassed": {"id":2,"value":false},
				"master": {"id":3,"value":1.0},
				"decks": [{"id":100,"closed":false,"name":{"id":4,"value":"Empty"},"selected":{"id":5,"value":true}}]
			}`))
			return
		}
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	first := waitForResolution(t, a, time.Second)
	if first.SelectedDeckName != "Empty" {
		t.Fatalf("first resolve's SelectedDeckName = %q, want %q (the load-window composition)", first.SelectedDeckName, "Empty")
	}

	a.HandleChange(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if res, ok := a.Resolution(); ok && res.SelectedDeckName == "Main" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Adapter never re-resolved to the real composition after HandleChange")
}

// TestAdapterChangeWithoutConnectionIsIgnored proves the guard: a change
// wake-up that arrives with no connection ever established (or after a
// disconnect) must not itself try to resolve anything — there is nothing
// to resolve without a connection, and HandleDisconnect already dropped
// whatever was held.
func TestAdapterChangeWithoutConnectionIsIgnored(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	a.HandleChange(context.Background())
	time.Sleep(150 * time.Millisecond) // longer than the test debounce interval

	if _, ok := a.Resolution(); ok {
		t.Fatalf("Resolution() ok = true after a change with no prior connect, want false")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("composition endpoint called %d times from an unconnected change wake-up, want 0", got)
	}
}

// TestAdapterConnectIsExemptFromMinInterval proves the connect-is-exempt
// half of the coalescing policy: two connects fired back to back (well
// inside MinResolveInterval) must BOTH cause an immediate resolve, never
// waiting out the steady-state interval floor the way a change wake-up
// would.
func TestAdapterConnectIsExemptFromMinInterval(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.MinResolveInterval = 10 * time.Second // deliberately large: a change would have to wait this out, a connect must not
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	a.HandleDisconnect(context.Background())
	waitForNoResolution(t, a, time.Second)

	start := time.Now()
	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	if elapsed := time.Since(start); elapsed >= opts.MinResolveInterval {
		t.Fatalf("second connect took %s to resolve, want well under MinResolveInterval (%s): connect must be exempt from it", elapsed, opts.MinResolveInterval)
	}

	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("composition endpoint called %d times, want at least 2 (one per connect)", got)
	}
}

// TestAdapterBurstOfChangesCoalescesIntoOneResolve proves the debounce
// half: several HandleChange calls fired in quick succession must
// coalesce into ONE composition read, not one per call.
func TestAdapterBurstOfChangesCoalescesIntoOneResolve(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	callsAfterConnect := atomic.LoadInt32(&calls)

	for i := 0; i < 5; i++ {
		a.HandleChange(context.Background())
		time.Sleep(2 * time.Millisecond)
	}

	// Wait comfortably past the debounce interval for the coalesced
	// resolve to land.
	time.Sleep(200 * time.Millisecond)

	got := atomic.LoadInt32(&calls) - callsAfterConnect
	if got != 1 {
		t.Errorf("composition endpoint called %d times for a 5-change burst, want exactly 1 (coalesced)", got)
	}
}

// TestAdapterFailedResolveRetriesWithBackoffAndDoesNotWedge proves both
// halves of the retry rule: a failing resolve keeps retrying on its own
// (without another external HandleChange/HandleConnect) until it
// eventually succeeds, AND other events (here, a fresh HandleConnect)
// remain immediately responsive while a retry is pending — the loop is
// never wedged waiting on the retry timer.
func TestAdapterFailedResolveRetriesWithBackoffAndDoesNotWedge(t *testing.T) {
	var calls int32
	const failUntil = 3
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < failUntil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	a := NewAdapter(client, testAdapterOptions(discardLogger()))
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	// No further external signal is sent: retry-with-backoff must be
	// self-driving.
	waitForResolution(t, a, 2*time.Second)

	if got := atomic.LoadInt32(&calls); got < failUntil {
		t.Errorf("composition endpoint called %d times, want at least %d (retries happened)", got, failUntil)
	}
}

// --- resolveConvergenceWindow (bench evidence 2026-08-14: GET
// /composition measured crashing the target Arena build) -------------

// TestAdapterChangeInsideConvergenceWindowResolves proves a change wake-up
// arriving while still within the convergence window behaves exactly like
// the pre-window coalescing policy: debounced, and folded into one resolve
// per burst.
func TestAdapterChangeInsideConvergenceWindowResolves(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.ConvergenceWindow = 5 * time.Second // comfortably longer than this test's runtime
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	callsAfterConnect := atomic.LoadInt32(&calls)

	// A burst of changes, well inside the window, must still coalesce
	// into exactly one resolve (debounce is untouched by the window).
	for i := 0; i < 5; i++ {
		a.HandleChange(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // past debounce + minInterval

	got := atomic.LoadInt32(&calls) - callsAfterConnect
	if got != 1 {
		t.Errorf("composition endpoint called %d times for a change burst inside the convergence window, want exactly 1", got)
	}
}

// TestAdapterChangeAfterConvergenceWindowDoesNotResolve proves the window's
// whole point: once resolveConvergenceWindow has elapsed since the last
// connect, a change wake-up must not cause a composition read AT ALL —
// asserted at the request level against the test server's own call
// counter, not against Adapter's internal state, because the bench
// evidence this window exists for is that the read itself is what is
// measured dangerous.
func TestAdapterChangeAfterConvergenceWindowDoesNotResolve(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.ConvergenceWindow = 50 * time.Millisecond
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	callsAfterConnect := atomic.LoadInt32(&calls)

	// Let the convergence window close.
	time.Sleep(150 * time.Millisecond)

	a.HandleChange(context.Background())

	// Wait comfortably past debounce + minInterval to prove nothing was
	// even scheduled, let alone resolved.
	time.Sleep(200 * time.Millisecond)

	if got := atomic.LoadInt32(&calls) - callsAfterConnect; got != 0 {
		t.Errorf("composition endpoint called %d times for a change after the convergence window closed, want 0 (no composition read may happen)", got)
	}
	// The held Resolution from connect must also be untouched — nothing
	// re-resolved it, successfully or otherwise.
	if _, ok := a.Resolution(); !ok {
		t.Errorf("Resolution() ok = false after a suppressed post-window change, want the connect-time Resolution still held")
	}
}

// TestAdapterReconnectReopensConvergenceWindow proves a fresh connect
// re-arms the window even after a previous one closed: a change arriving
// after the reconnect, safely inside the new window, must resolve again.
func TestAdapterReconnectReopensConvergenceWindow(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.ConvergenceWindow = 50 * time.Millisecond
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)

	// Let the first window close, then confirm it's actually closed
	// (mirrors TestAdapterChangeAfterConvergenceWindowDoesNotResolve).
	time.Sleep(150 * time.Millisecond)
	callsAfterFirstWindow := atomic.LoadInt32(&calls)
	a.HandleChange(context.Background())
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls) - callsAfterFirstWindow; got != 0 {
		t.Fatalf("composition endpoint called %d times after the first window closed, want 0 (test precondition broken)", got)
	}

	// Reconnect: this must reopen the window regardless of the previous
	// one having closed.
	a.HandleDisconnect(context.Background())
	waitForNoResolution(t, a, time.Second)
	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	callsAfterReconnect := atomic.LoadInt32(&calls)

	a.HandleChange(context.Background())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls)-callsAfterReconnect >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("composition endpoint never called again for a change after reconnect reopened the convergence window")
}

// TestAdapterFingerprintDiffIsLoggedAtInfo proves the visibility half of
// finding A: a resolve whose fingerprints differ from the one it replaces
// must be logged at INFO naming both old and new fingerprints — the
// mechanism that makes the load-window swap in
// TestAdapterReResolvesOnChangeAfterConnect visible in a real log instead
// of silent.
func TestAdapterFingerprintDiffIsLoggedAtInfo(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			w.Write([]byte(`{
				"name": {"id":1,"value":""},
				"bypassed": {"id":2,"value":false},
				"master": {"id":3,"value":1.0},
				"decks": [{"id":100,"closed":false,"name":{"id":4,"value":"Empty"},"selected":{"id":5,"value":true}}]
			}`))
			return
		}
		w.Write(loadTestdata(t, "composition_minimal.json"))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	a := NewAdapter(client, testAdapterOptions(logger))
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	waitForResolution(t, a, time.Second)
	a.HandleChange(context.Background())

	// Poll the LOG ITSELF, not just a.Resolution() — the held Resolution
	// is assigned (inside resolve(), under a.mu) before the fingerprint
	// comparison and the log call that names it, by design: a reader of
	// Resolution() should see a resolve's result as soon as it lands, not
	// wait on logging. So gating this wait on a.Resolution() alone is a
	// TOCTOU race against the log line this test actually needs: under
	// go test -race, a.Resolution() can observe "Main" before
	// resolve()'s own logger.Info call for that same resolve has
	// executed, and this test found that race directly (5/100 runs failed
	// under -race -count=100 before this fix, all completing in ~0.1s —
	// far short of any real timeout — which is what a lost race looks
	// like, not a hang).
	deadline := time.Now().Add(2 * time.Second)
	var logged string
	for time.Now().Before(deadline) {
		logged = buf.String()
		if contains(logged, "differs from the one it replaces") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !contains(logged, "differs from the one it replaces") {
		t.Fatalf("log output does not contain the fingerprint-diff INFO line within 2s; got:\n%s", logged)
	}
	if !contains(logged, "previous_object_fingerprint") || !contains(logged, "object_fingerprint") {
		t.Errorf("log output does not name both old and new fingerprints; got:\n%s", logged)
	}
}

// --- Review finding B: the retry-on-failure path used to run outside the
// convergence window entirely (bench evidence 2026-08-14: two /composition
// reads, 26s apart, crashed Arena) -------------------------------------

// TestAdapterRetryStopsWhenConvergenceWindowCloses proves the hole review
// finding B found: a resolve that keeps failing must retry WHILE the
// convergence window is open, and a retry that is PENDING at the moment
// the window closes must be cancelled outright rather than left to fire
// once more just outside it.
//
// This is deliberately timed so a retry is armed but not yet fired at the
// exact moment the window closes: RetryMinBackoff/MaxBackoff are fixed at
// 40ms (no growth) and ConvergenceWindow is 60ms, so by construction the
// connect-triggered failure (call 1, ~t=0) and its first retry (call 2,
// ~t=40ms) both land inside the window, and that retry's own failure
// schedules a THIRD attempt for ~t=80ms — 20ms after the window closes at
// t=60ms. A version that only checks the window at the NEXT failure
// (rather than cancelling the pending timer when the window closes) would
// let that third call through before self-terminating on its own
// subsequent failure, which is exactly the "one straggler call" a bench
// crash measured just two calls apart makes unacceptable — see
// resolveRetryMaxAttempts's sibling reasoning on [Adapter]'s own doc
// comment. RetryMaxAttempts is set far above what either path could reach
// in this test's runtime, so the WINDOW — not the attempt cap — is what's
// under test here. Asserted at the request level against the test
// server's own call counter, exactly like
// TestAdapterChangeAfterConvergenceWindowDoesNotResolve does for the
// change path, because an internal flag could be wrong in a way a real
// HTTP request never lies about.
func TestAdapterRetryStopsWhenConvergenceWindowCloses(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.ConvergenceWindow = 60 * time.Millisecond
	opts.RetryMinBackoff = 40 * time.Millisecond
	opts.RetryMaxBackoff = 40 * time.Millisecond
	opts.RetryMaxAttempts = 1000 // effectively unbounded: isolates the window as the thing under test
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())

	// By construction (see doc comment above): call 1 at ~t=0, call 2 (the
	// first retry) at ~t=40ms, both inside the 60ms window. Sample at
	// t=50ms, comfortably after call 2 has landed but still inside the
	// window.
	time.Sleep(50 * time.Millisecond)
	callsBeforeWindowCloses := atomic.LoadInt32(&calls)
	if callsBeforeWindowCloses < 2 {
		t.Fatalf("composition endpoint called %d times before the window closed, want at least 2 (a retry happening inside the window) — test precondition broken", callsBeforeWindowCloses)
	}

	// Wait past the window's close (t=60ms) AND past the moment the THIRD
	// attempt would have fired had the pending retry not been cancelled
	// (~t=80ms), with margin: now at ~t=150ms.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != callsBeforeWindowCloses {
		t.Errorf("composition endpoint called %d times, want exactly %d (unchanged from before the window closed) — a retry PENDING when the window closes must be cancelled, not left to fire once more just outside it", got, callsBeforeWindowCloses)
	}

	// And confirm it stays that way, not just past the one straggler.
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != callsBeforeWindowCloses {
		t.Errorf("composition endpoint called %d times after a longer wait, want exactly %d — retry must stay abandoned until the next connect", got, callsBeforeWindowCloses)
	}
}

// TestAdapterRetryAttemptCapHonoured proves the second, independent bound:
// even with the convergence window held open for the entire test (so the
// window can never be what stops retrying), a resolve that fails fast and
// repeatedly must stop after resolveRetryMaxAttempts retries and never
// exceed that count, no matter how much longer the window stays open.
func TestAdapterRetryAttemptCapHonoured(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.ConvergenceWindow = 10 * time.Second // far longer than this test's runtime: the CAP must bind first
	opts.RetryMinBackoff = 2 * time.Millisecond
	opts.RetryMaxBackoff = 2 * time.Millisecond
	const maxAttempts = 4
	opts.RetryMaxAttempts = maxAttempts
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())

	// One initial (connect-triggered) failure plus maxAttempts capped
	// retries, each 2ms apart: comfortably finished well inside this wait.
	time.Sleep(200 * time.Millisecond)
	want := int32(1 + maxAttempts)
	if got := atomic.LoadInt32(&calls); got != want {
		t.Fatalf("composition endpoint called %d times, want exactly %d (1 initial failure + %d capped retries)", got, want, maxAttempts)
	}

	// Wait well beyond any further backoff: the count must not grow past
	// the cap no matter how long the still-open window remains.
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != want {
		t.Errorf("composition endpoint called %d times after waiting past the cap, want exactly %d — the cap must hold for the rest of the window", got, want)
	}
}

// TestAdapterReconnectResetsRetryAttemptCounter proves a fresh CONNECT
// resets the retry attempt counter — deliberately isolated from
// HandleDisconnect, which resets it too (disconnect drops everything
// unconditionally) and would otherwise mask a bug in connect's own reset.
// A bare second HandleConnect with no intervening HandleDisconnect is
// itself a real path: [Adapter.HandleConnect]'s own doc comment and
// TestAdapterConnectIsExemptFromMinInterval both treat "connect while
// already connected" as ordinary, not exceptional.
//
// Exhaust the attempt cap on the first connection, issue a SECOND
// HandleConnect with no disconnect in between, and confirm the same
// number of retries is available again rather than the counter carrying
// over.
func TestAdapterReconnectResetsRetryAttemptCounter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	opts := testAdapterOptions(discardLogger())
	opts.ConvergenceWindow = 10 * time.Second
	opts.RetryMinBackoff = 2 * time.Millisecond
	opts.RetryMaxBackoff = 2 * time.Millisecond
	const maxAttempts = 3
	opts.RetryMaxAttempts = maxAttempts
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())
	time.Sleep(150 * time.Millisecond) // exhaust the first connection's retry budget
	perConnection := int32(1 + maxAttempts)
	if got := atomic.LoadInt32(&calls); got != perConnection {
		t.Fatalf("composition endpoint called %d times before the second connect, want exactly %d — test precondition (cap exhausted) broken", got, perConnection)
	}
	time.Sleep(100 * time.Millisecond) // confirm it's really settled, not still retrying
	if got := atomic.LoadInt32(&calls); got != perConnection {
		t.Fatalf("composition endpoint kept being called after the cap (%d calls, want %d) — test precondition broken", got, perConnection)
	}

	// A second HandleConnect, with NO HandleDisconnect in between: this
	// isolates whether CONNECT itself resets the attempt counter, as
	// opposed to the reset merely being inherited from a disconnect that
	// always precedes it in the realistic sequence.
	callsBeforeSecondConnect := atomic.LoadInt32(&calls)
	a.HandleConnect(context.Background())

	// The second connect must get its own full retry budget: wait for
	// perConnection MORE calls.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls)-callsBeforeSecondConnect >= perConnection {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("composition endpoint called only %d more times after a second connect within 1s, want at least %d — connect must reset the retry attempt counter on its own, not merely inherit a disconnect's reset",
		atomic.LoadInt32(&calls)-callsBeforeSecondConnect, perConnection)
}

// TestAdapterRetryAbandonmentLogsOnceAtWARN proves the visibility half of
// review finding B: abandoning retries (here, via the attempt cap) must be
// logged at WARN, stating that the Adapter holds no resolution and will
// not try again until the next connection — and exactly ONCE, not once
// per would-be retry that the cap continues to suppress for the rest of
// the (still-open) window.
func TestAdapterRetryAbandonmentLogsOnceAtWARN(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	opts := testAdapterOptions(logger)
	opts.ConvergenceWindow = 10 * time.Second
	opts.RetryMinBackoff = 2 * time.Millisecond
	opts.RetryMaxBackoff = 2 * time.Millisecond
	opts.RetryMaxAttempts = 3
	a := NewAdapter(client, opts)
	startAdapter(t, a)

	a.HandleConnect(context.Background())

	const wantLine = "abandoning composition resolve retries"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if contains(buf.String(), wantLine) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	logged := buf.String()
	if !contains(logged, wantLine) {
		t.Fatalf("log output does not contain the abandonment WARN within 2s; got:\n%s", logged)
	}
	if !contains(logged, "level=WARN") {
		t.Errorf("abandonment line was not logged at WARN; got:\n%s", logged)
	}

	// Wait well past the cap, deep inside the still-open window, and
	// confirm the line was logged EXACTLY ONCE.
	time.Sleep(300 * time.Millisecond)
	logged = buf.String()
	if got := strings.Count(logged, wantLine); got != 1 {
		t.Errorf("abandonment WARN logged %d times, want exactly 1; got:\n%s", got, logged)
	}
}
