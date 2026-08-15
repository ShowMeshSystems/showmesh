package resolume

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// --- test helpers -----------------------------------------------------

// newWSTestServer starts a real HTTP server that upgrades every request to
// a real WebSocket connection (via gorilla, the same library Watcher uses)
// and hands it to handle in its own goroutine. Using a real server and real
// frames rather than a fake reader is deliberate: CLAUDE.md is explicit that
// a test environment differing from the deployment environment reports
// success on exactly that difference.
func newWSTestServer(t *testing.T, handle func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go handle(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	u.Scheme = "ws"
	return u.String()
}

// startWatcher runs w.Run in its own goroutine and arranges for it to be
// cancelled and joined at test cleanup, even if the test forgets to do so
// itself. It returns the cancel func and a channel closed when Run returns,
// for tests that need to synchronize on shutdown explicitly.
func startWatcher(t *testing.T, w *Watcher) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Watcher.Run did not return within 5s of ctx cancellation")
		}
	})
	return cancel, done
}

// bigFullPushJSON builds a valid, untyped top-level JSON object (a "layers"
// key with no "type" key, matching the bench capture's full composition
// push shape) at least approxBytes long, padded with throwaway array
// elements.
func bigFullPushJSON(approxBytes int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"layers":[`)
	const item = `{"id":1765224917300,"name":"padding layer padding layer padding"},`
	for buf.Len() < approxBytes {
		buf.WriteString(item)
	}
	buf.WriteString(`{"id":0,"name":"end"}],"columns":[]}`)
	return buf.Bytes()
}

// --- Watcher, over a real WebSocket -----------------------------------

func TestWatcherOnConnectFiresBeforeFirstMessage(t *testing.T) {
	const serverDelay = 250 * time.Millisecond
	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		time.Sleep(serverDelay)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"thumbnail_update","value":1}`))
		time.Sleep(100 * time.Millisecond)
	})

	connectedAt := make(chan time.Time, 1)
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnConnect: func(ctx context.Context) {
			select {
			case connectedAt <- time.Now():
			default:
			}
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	start := time.Now()
	startWatcher(t, w)

	select {
	case <-connectedAt:
		if elapsed := time.Since(start); elapsed >= serverDelay {
			t.Fatalf("OnConnect fired after %s, not before the server's %s delayed first message", elapsed, serverDelay)
		}
	case <-time.After(serverDelay):
		t.Fatal("OnConnect never fired before the server's delayed first message arrived")
	}
}

func TestWatcherFullPushOver2MBFiresOnChangeOnce(t *testing.T) {
	payload := bigFullPushJSON(2*1024*1024 + 50_000)
	if len(payload) < 2*1024*1024 {
		t.Fatalf("test payload too small: %d bytes", len(payload))
	}

	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, payload)
		time.Sleep(300 * time.Millisecond) // hold the connection so no reconnect occurs mid-assertion
	})

	var changeCount int32
	changed := make(chan struct{}, 8)
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnChange: func(ctx context.Context) {
			atomic.AddInt32(&changeCount, 1)
			select {
			case changed <- struct{}{}:
			default:
			}
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	startWatcher(t, w)

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("OnChange did not fire for the full push")
	}
	time.Sleep(150 * time.Millisecond) // settle time, to catch any double-fire

	stats := w.Stats()
	if stats.FullPushes != 1 {
		t.Errorf("FullPushes = %d, want 1", stats.FullPushes)
	}
	if stats.TypedMessages != 0 {
		t.Errorf("TypedMessages = %d, want 0", stats.TypedMessages)
	}
	if got := atomic.LoadInt32(&changeCount); got != 1 {
		t.Errorf("OnChange fired %d times, want exactly 1", got)
	}
}

func TestWatcherTypedParameterUpdateFiresOnChange(t *testing.T) {
	const msg = `{"type":"parameter_update","parameter":"/parameter/by-id/1786724946918","value":` +
		`{"valuetype":"ParamState","id":1786724946918,"value":"Connected","index":3,` +
		`"options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}}`

	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
		time.Sleep(300 * time.Millisecond)
	})

	var changeCount int32
	changed := make(chan struct{}, 8)
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnChange: func(ctx context.Context) {
			atomic.AddInt32(&changeCount, 1)
			select {
			case changed <- struct{}{}:
			default:
			}
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	startWatcher(t, w)

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("OnChange did not fire for parameter_update")
	}
	time.Sleep(150 * time.Millisecond)

	stats := w.Stats()
	if stats.TypedMessages != 1 {
		t.Errorf("TypedMessages = %d, want 1", stats.TypedMessages)
	}
	if stats.FullPushes != 0 {
		t.Errorf("FullPushes = %d, want 0 (a parameter_update must never be counted as a full push)", stats.FullPushes)
	}
	if got := atomic.LoadInt32(&changeCount); got != 1 {
		t.Errorf("OnChange fired %d times, want exactly 1", got)
	}
}

func TestWatcherTypedThumbnailUpdateDoesNotFireOnChange(t *testing.T) {
	const msg = `{"type":"thumbnail_update","clip":1786724953484,"value":"base64thumbnaildata"}`

	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
		time.Sleep(300 * time.Millisecond)
	})

	var changeCount int32
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnChange: func(ctx context.Context) {
			atomic.AddInt32(&changeCount, 1)
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	startWatcher(t, w)

	// Poll for the message to be counted, then confirm OnChange never fired.
	deadline := time.Now().Add(5 * time.Second)
	for w.Stats().TypedMessages == 0 {
		if time.Now().After(deadline) {
			t.Fatal("thumbnail_update was never counted as a typed message")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // settle time in case OnChange fires late

	if got := atomic.LoadInt32(&changeCount); got != 0 {
		t.Errorf("OnChange fired %d times for thumbnail_update, want 0", got)
	}
	if stats := w.Stats(); stats.TypedMessages != 1 || stats.FullPushes != 0 {
		t.Errorf("stats = %+v, want TypedMessages=1 FullPushes=0", stats)
	}
}

func TestWatcherAbruptCloseReconnects(t *testing.T) {
	var attempt int32
	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		n := atomic.AddInt32(&attempt, 1)
		if n == 1 {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"layers":[],"columns":[]}`))
			time.Sleep(30 * time.Millisecond)
			// Sever the TCP connection with no WebSocket close frame — the
			// bench capture's §5.5 close code 1006, indistinguishable
			// in-band from the network dropping.
			_ = conn.UnderlyingConn().Close()
			return
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(500 * time.Millisecond)
	})

	connects := make(chan struct{}, 8)
	disconnects := make(chan struct{}, 8)
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnConnect: func(ctx context.Context) {
			select {
			case connects <- struct{}{}:
			default:
			}
		},
		OnDisconnect: func(ctx context.Context) {
			select {
			case disconnects <- struct{}{}:
			default:
			}
		},
		MinBackoff: 5 * time.Millisecond,
		MaxBackoff: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	startWatcher(t, w)

	select {
	case <-connects:
	case <-time.After(2 * time.Second):
		t.Fatal("first OnConnect never fired")
	}
	select {
	case <-disconnects:
	case <-time.After(2 * time.Second):
		t.Fatal("OnDisconnect never fired after the abrupt close")
	}
	select {
	case <-connects:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not reconnect after the abrupt close")
	}

	if stats := w.Stats(); stats.Connects < 2 {
		t.Errorf("Connects = %d, want >= 2", stats.Connects)
	}
}

func TestWatcherRunLeavesNoGoroutineAfterCancel(t *testing.T) {
	// Review finding F (2026-08-14): a fixed 3s server-side sleep let this
	// test pass even with runConnection's force-close goroutine deleted
	// entirely, because the SERVER'S OWN close (at 3s, well inside the
	// test's 5s "did Run return" deadline) was what unblocked the client
	// read — not ctx cancellation, which is the only thing this test
	// claims to verify.
	//
	// The fix is not merely "sleep longer": a server goroutine blocked in
	// time.Sleep keeps running regardless of what the client does, so a
	// long fixed sleep would ALSO show up as a "leaked" goroutine in this
	// test's own baseline comparison even when Watcher's code is entirely
	// correct (confirmed while developing this fix — a naive 30s
	// time.Sleep in the handler failed this test 100% of the time even
	// with force-close intact, for exactly that reason). The handler
	// instead blocks on ReadMessage with no deadline: nothing the SERVER
	// does can end it early, so only the CLIENT force-closing the
	// connection (correct behavior) or the test's own 5s timeout (broken
	// behavior) can end this handler — which is the actual property this
	// test needs, expressed as blocking-on-nothing-the-mutation-can-satisfy
	// rather than as a duration to out-wait.
	//
	// Before trusting this test, runConnection's force-close goroutine
	// (the "go func() { select { case <-connCtx.Done(): conn.Close()
	// ...") was temporarily deleted and this test was re-run: it failed,
	// blocking past its own 5s deadline waiting for Watcher.Run to
	// return, because with nothing forcing the close, neither side of the
	// connection had any reason to unblock. Restored afterward.
	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage() // blocks until the CLIENT closes; nothing here can end it first
	})

	connected := make(chan struct{})
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnConnect: func(ctx context.Context) {
			close(connected)
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	runtime.GC()
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("watcher never connected")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher.Run did not return after ctx cancellation")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		if n := runtime.NumGoroutine(); n <= baseline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: baseline=%d now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatcherSilenceProducesNoCallback(t *testing.T) {
	// The bench capture measured 12s of true silence after Arena's initial
	// push, with no heartbeat of any kind (§5.1). Scaled down here for test
	// speed: the property under test is that Watcher never calls
	// SetReadDeadline and never infers anything from the absence of
	// traffic, which is a fact about the code path, not about how long we
	// wait — a short wait exercises the identical branch a 12s wait would.
	const silence = 300 * time.Millisecond

	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		time.Sleep(silence)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"parameter_update","value":1}`))
		time.Sleep(150 * time.Millisecond)
	})

	var changeCount int32
	changed := make(chan struct{}, 1)
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnChange: func(ctx context.Context) {
			atomic.AddInt32(&changeCount, 1)
			select {
			case changed <- struct{}{}:
			default:
			}
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	startWatcher(t, w)

	time.Sleep(silence - 75*time.Millisecond)
	if got := atomic.LoadInt32(&changeCount); got != 0 {
		t.Fatalf("OnChange fired %d times during silence, want 0", got)
	}
	if stats := w.Stats(); stats.LastError != "" {
		t.Fatalf("LastError = %q during silence, want empty", stats.LastError)
	}
	if !w.Stats().Connected {
		t.Fatal("watcher reports disconnected during silence, want still connected — silence must never be treated as evidence of a dead connection")
	}

	// Confirm the connection is still genuinely alive, not silently wedged.
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("no OnChange after silence ended; connection may have gone unusable without erroring")
	}
}

// --- Review finding D: dial failures are logged and stats are separated ---

// TestWatcherDialFailureIsLoggedAndCounted proves the visibility half of
// finding D: a persistently-refusing dial target must produce at least one
// WARN log line naming the error, and DialFailures — NOT Disconnects, per
// [WatcherStats.Connects]'s own corrected doc comment — must be nonzero.
//
// Before trusting this test, noteDialFailure's WARN call was temporarily
// removed (replaced with only the counter increments) and this test's log
// assertion was confirmed to fail. Restored afterward.
func TestWatcherDialFailureIsLoggedAndCounted(t *testing.T) {
	// A closed listener on loopback: nothing is listening, so every dial
	// attempt fails with ECONNREFUSED, reliably and immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	badURL := wsURL(t, srv)
	srv.Close()

	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	w, err := NewWatcher(WatcherOptions{
		URL:        badURL,
		Logger:     logger,
		MinBackoff: 5 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Stats().DialFailures >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watcher.Run did not return after cancel")
	}

	stats := w.Stats()
	if stats.DialFailures == 0 {
		t.Fatalf("DialFailures = 0, want > 0 for a persistently refused dial")
	}
	if stats.Disconnects != 0 {
		t.Errorf("Disconnects = %d, want 0: a dial failure must never increment Disconnects (no connection was ever established)", stats.Disconnects)
	}

	logged := buf.String()
	if !contains(logged, "dial failed") {
		t.Errorf("log output does not contain a dial-failure WARN line; got:\n%s", logged)
	}
}

// TestWatcherCleanShutdownOfEstablishedConnectionIncrementsDisconnects
// proves the other half of finding D's "wrong in both directions" claim:
// a connection that WAS established, then torn down by ordinary ctx
// cancellation (no error), must still increment Disconnects — the
// previous code path only incremented it on an ERROR-ending connection,
// so a clean shutdown was invisible to the one counter meant to describe
// connection churn.
func TestWatcherCleanShutdownOfEstablishedConnectionIncrementsDisconnects(t *testing.T) {
	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage() // blocks until the client force-closes
	})

	w, err := NewWatcher(WatcherOptions{
		URL:        wsURL(t, srv),
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	cancel, done := startWatcher(t, w)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !w.Stats().Connected {
		time.Sleep(5 * time.Millisecond)
	}
	if !w.Stats().Connected {
		t.Fatal("watcher never connected")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watcher.Run did not return after cancel")
	}

	stats := w.Stats()
	if stats.Disconnects == 0 {
		t.Errorf("Disconnects = 0 after a clean ctx-cancelled shutdown of an established connection, want > 0")
	}
	if stats.LastError != "" {
		t.Errorf("LastError = %q after a CLEAN shutdown, want empty: an ordinary ctx cancellation is not an error", stats.LastError)
	}
}

// --- Review finding E: an unrecognized WS type wakes, not drops --------

// TestWatcherUnrecognizedTypedMessageFiresOnChange is the direct
// reproduction of finding E: a "type" value that is neither a known
// change-worthy type nor one of the four enumerated exclusions must still
// fire OnChange, counted separately (UnrecognizedTypedMessages) — the
// inversion of the old allow-list behavior, which silently dropped
// anything it did not already know about.
//
// Before trusting this test, the switch in runConnection was temporarily
// reverted to the old allow-list form (wake only if
// knownChangeMessageTypes[result.typeValue], drop everything else
// silently) and this test was re-run: it failed, with OnChange never
// firing for the invented "structure_update" type. Reverted afterward.
func TestWatcherUnrecognizedTypedMessageFiresOnChange(t *testing.T) {
	const msg = `{"type":"structure_update","value":1}` // not a real Arena type — invented to prove the "unknown" path

	srv := newWSTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
		time.Sleep(300 * time.Millisecond)
	})

	var changeCount int32
	changed := make(chan struct{}, 8)
	w, err := NewWatcher(WatcherOptions{
		URL: wsURL(t, srv),
		OnChange: func(ctx context.Context) {
			atomic.AddInt32(&changeCount, 1)
			select {
			case changed <- struct{}{}:
			default:
			}
		},
		MinBackoff: time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	startWatcher(t, w)

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("OnChange did not fire for an unrecognized typed message")
	}
	time.Sleep(150 * time.Millisecond)

	stats := w.Stats()
	if stats.TypedMessages != 1 {
		t.Errorf("TypedMessages = %d, want 1", stats.TypedMessages)
	}
	if stats.UnrecognizedTypedMessages != 1 {
		t.Errorf("UnrecognizedTypedMessages = %d, want 1", stats.UnrecognizedTypedMessages)
	}
	if got := atomic.LoadInt32(&changeCount); got != 1 {
		t.Errorf("OnChange fired %d times, want exactly 1", got)
	}
}

// TestWatcherExcludedTypesStillDoNotFireOnChange is the explicit
// non-regression check paired with the above: the four capture-enumerated
// exclusions must still NOT wake, so finding E's fix is "unknown wakes",
// never "everything wakes."
// (sources_update/effects_update/parameter_subscribed alongside
// thumbnail_update, which TestWatcherTypedThumbnailUpdateDoesNotFireOnChange
// already covers on its own.)
func TestWatcherExcludedTypesStillDoNotFireOnChange(t *testing.T) {
	for _, typ := range []string{"sources_update", "effects_update", "parameter_subscribed"} {
		t.Run(typ, func(t *testing.T) {
			msg := `{"type":"` + typ + `","value":1}`
			srv := newWSTestServer(t, func(conn *websocket.Conn) {
				defer func() { _ = conn.Close() }()
				_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
				time.Sleep(300 * time.Millisecond)
			})

			var changeCount int32
			w, err := NewWatcher(WatcherOptions{
				URL: wsURL(t, srv),
				OnChange: func(ctx context.Context) {
					atomic.AddInt32(&changeCount, 1)
				},
				MinBackoff: time.Millisecond,
				MaxBackoff: 10 * time.Millisecond,
			})
			if err != nil {
				t.Fatalf("NewWatcher: %v", err)
			}
			startWatcher(t, w)

			deadline := time.Now().Add(5 * time.Second)
			for w.Stats().TypedMessages == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("%s was never counted as a typed message", typ)
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(150 * time.Millisecond)

			if got := atomic.LoadInt32(&changeCount); got != 0 {
				t.Errorf("OnChange fired %d times for %s, want 0", got, typ)
			}
			if stats := w.Stats(); stats.UnrecognizedTypedMessages != 0 {
				t.Errorf("UnrecognizedTypedMessages = %d for excluded type %s, want 0", stats.UnrecognizedTypedMessages, typ)
			}
		})
	}
}

func TestNewWatcherValidation(t *testing.T) {
	if _, err := NewWatcher(WatcherOptions{}); err == nil {
		t.Error("NewWatcher with empty URL: want error, got nil")
	}
	if _, err := NewWatcher(WatcherOptions{
		URL:        "ws://example.invalid",
		MinBackoff: 10 * time.Second,
		MaxBackoff: time.Second,
	}); err == nil {
		t.Error("NewWatcher with MinBackoff > MaxBackoff: want error, got nil")
	}
	w, err := NewWatcher(WatcherOptions{URL: "ws://example.invalid"})
	if err != nil {
		t.Fatalf("NewWatcher with only URL set: %v", err)
	}
	if w.opts.MinBackoff != defaultMinBackoff || w.opts.MaxBackoff != defaultMaxBackoff ||
		w.opts.HandshakeTimeout != defaultHandshakeTimeout || w.opts.ReadLimitBytes != defaultReadLimitBytes {
		t.Errorf("defaults not applied: %+v", w.opts)
	}
}

// --- classifyAndDrain, in isolation ------------------------------------

// recordingReader wraps a byte slice and records the largest single Read
// request it was ever asked to satisfy, so a test can prove nothing tried
// to bulk-read (io.ReadAll / json.Unmarshal-on-the-whole-body style) the
// message.
type recordingReader struct {
	data   []byte
	off    int
	maxLen int
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if len(p) > r.maxLen {
		r.maxLen = len(p)
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func TestClassifyAndDrainFullPushNeverBulkReads(t *testing.T) {
	payload := bigFullPushJSON(2*1024*1024 + 100_000)
	rr := &recordingReader{data: payload}

	got, err := classifyAndDrain(rr, discriminatorPrefixBytes)
	if err != nil {
		t.Fatalf("classifyAndDrain: %v", err)
	}
	if got.kind != kindFullPush {
		t.Fatalf("kind = %v, want kindFullPush", got.kind)
	}
	if rr.off != len(payload) {
		t.Fatalf("drained %d of %d bytes, want the whole message consumed", rr.off, len(payload))
	}

	// json.Decoder wrapped in io.LimitReader(r, discriminatorPrefixBytes)
	// and io.Copy(io.Discard, r) both read in small, bounded chunks by
	// construction. io.ReadAll or json.Unmarshal on the whole message would
	// have requested len(payload) (2MB+) in a single call; this bounds how
	// large any one request was allowed to look before we'd have noticed.
	const suspiciouslyLarge = 1 << 20 // 1 MiB, far above any chunk size these paths use
	if rr.maxLen >= suspiciouslyLarge {
		t.Fatalf("a single Read call requested %d bytes — the full push was bulk-read instead of scanned and discarded in chunks", rr.maxLen)
	}
}

func TestClassifyAndDrainTypedMessage(t *testing.T) {
	got, err := classifyAndDrain(strings.NewReader(`{"type":"parameter_update","parameter":"x"}`), discriminatorPrefixBytes)
	if err != nil {
		t.Fatalf("classifyAndDrain: %v", err)
	}
	if got.kind != kindTyped || got.typeValue != "parameter_update" {
		t.Fatalf("got %+v, want kindTyped/parameter_update", got)
	}
}

func TestClassifyAndDrainAmbiguousWhenPrefixExhausted(t *testing.T) {
	// "layers" is present, but only after discriminatorPrefixBytes of
	// unrelated content — the discriminator must not resolve it, because
	// resolving it would mean silently reading further than the documented
	// bound on every message, defeating the bound's whole purpose.
	var buf bytes.Buffer
	buf.WriteString(`{"noise":"`)
	buf.WriteString(strings.Repeat("x", discriminatorPrefixBytes+1000))
	buf.WriteString(`","layers":[]}`)

	got, err := classifyAndDrain(bytes.NewReader(buf.Bytes()), discriminatorPrefixBytes)
	if err != nil {
		t.Fatalf("classifyAndDrain: %v", err)
	}
	if got.kind != kindAmbiguous {
		t.Fatalf("kind = %v, want kindAmbiguous", got.kind)
	}
}

func TestClassifyAndDrainFullPushWithinPrefix(t *testing.T) {
	got, err := classifyAndDrain(strings.NewReader(`{"columns":[],"layers":[{"id":1}]}`), discriminatorPrefixBytes)
	if err != nil {
		t.Fatalf("classifyAndDrain: %v", err)
	}
	if got.kind != kindFullPush {
		t.Fatalf("kind = %v, want kindFullPush", got.kind)
	}
}
