package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSSEReaderParsesFramesAndIgnoresCommentsAndID pins the framing rules
// contract §6.4 specifies: comment lines (keepalive) are skipped, and any
// "id:" line is ignored outright — this client must never look at it, per
// the "no Last-Event-ID resumption, ever" rule.
func TestSSEReaderParsesFramesAndIgnoresCommentsAndID(t *testing.T) {
	raw := "event: stream.start\n" +
		"data: {\"streamId\":\"s1\"}\n" +
		"\n" +
		": keepalive\n" +
		"\n" +
		"id: should-be-ignored\n" +
		"event: node.changed\n" +
		"data: {\"seq\":1}\n" +
		"\n"

	r := newSSEReader(strings.NewReader(raw))

	f1, err := r.next()
	if err != nil {
		t.Fatalf("first next(): %v", err)
	}
	if f1.event != "stream.start" || f1.data != `{"streamId":"s1"}` {
		t.Errorf("first frame = %+v, want event=stream.start data={\"streamId\":\"s1\"}", f1)
	}

	f2, err := r.next()
	if err != nil {
		t.Fatalf("second next(): %v", err)
	}
	if f2.event != "node.changed" || f2.data != `{"seq":1}` {
		t.Errorf("second frame = %+v, want event=node.changed data={\"seq\":1} (the keepalive comment and id: line must be skipped, not merged in)", f2)
	}

	if _, err := r.next(); err == nil {
		t.Error("third next() = nil error, want io.EOF at end of stream")
	}
}

func TestSeqTrackerDetectsGapAndRearms(t *testing.T) {
	var s seqTracker

	if gap := s.observe(1); gap {
		t.Error("first observed seq must never be reported as a gap (nothing to compare against yet)")
	}
	if gap := s.observe(2); gap {
		t.Error("observe(2) after observe(1) should not be a gap")
	}
	if gap := s.observe(4); !gap {
		t.Error("observe(4) after expecting 3 should be reported as a gap")
	}
	// Rearmed from 4: the next call should treat 5 as in-sequence, per
	// "seq is per-connection, not a global cursor" — after a gap there is
	// nothing left to compare against but whatever arrives next.
	if gap := s.observe(5); gap {
		t.Error("observe(5) right after a gap at 4 should not itself be reported as a gap")
	}
}

// sseTestServer builds an httptest.Server serving a scripted SSE stream on
// /api/v1/stream and a snapshot on /api/v1/snapshot, counting snapshot
// fetches so the test can assert exactly when refetches happened.
func sseTestServer(t *testing.T, frames []string) (*httptest.Server, *int32) {
	t.Helper()
	var snapshotHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&snapshotHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","latestEventSeq":0,"nodes":[],"fpp":{"instances":[]},"collectors":[]}`)
	})
	mux.HandleFunc("/api/v1/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("ShowMesh-API-Version", "1")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support Flusher")
		}
		for _, f := range frames {
			_, _ = fmt.Fprint(w, f)
			flusher.Flush()
		}
		// Handler returning closes the connection, which is watchOnce's
		// "stream closed by coordinator" exit path — clean and
		// deterministic for a test, unlike waiting on a live disconnect.
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &snapshotHits
}

const nodeChangedTmpl = `event: node.changed
data: {"seq":%d,"serverTime":"2026-08-10T21:00:0%dZ","node":{"nodeId":"media-03","controlPlane":{"state":"online","reason":null},"evidence":{"hello":{"signal":"node.hello","state":"current"},"lastWill":{"signal":"node.lastWill","state":"not_collected","reason":"x"},"heartbeat":{"signal":"node.heartbeat","state":"current","observedAt":"2026-08-10T21:00:00Z","collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct"}}}}

`

// TestWatchOnceResnapshotsOnConnectOnGapAndOnReset is the test task spec
// §4 explicitly requires: "A watch test over a real httptest.Server
// emitting a real text/event-stream, asserting resnapshot on connect, on
// reset, and on a seq gap." One connection carries all three cases in
// sequence: stream.start (connect), seq 1 (in order, no refetch), seq 3
// (a gap — 2 was skipped), then stream.reset.
func TestWatchOnceResnapshotsOnConnectOnGapAndOnReset(t *testing.T) {
	frames := []string{
		"event: stream.start\ndata: {\"streamId\":\"s1\",\"apiVersion\":1,\"serverTime\":\"2026-08-10T21:00:00Z\",\"snapshotRequired\":true}\n\n",
		fmt.Sprintf(nodeChangedTmpl, 1, 1),
		fmt.Sprintf(nodeChangedTmpl, 3, 2), // gap: expected 2, got 3
		"event: stream.reset\ndata: {\"seq\":10,\"serverTime\":\"2026-08-10T21:00:03Z\",\"reason\":\"subscriber_too_slow\",\"snapshotRequired\":true}\n\n",
	}
	ts, snapshotHits := sseTestServer(t, frames)

	streamClient, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	snapshotClient, err := newClient(ts.URL, "", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}

	var stdout, stderr bytes.Buffer
	g := &globalFlags{output: outputText, timeout: 5 * time.Second}
	err = watchOnce(context.Background(), streamClient, snapshotClient, g, false, &stdout, &stderr, time.Now, newWatchBackoff())

	if err == nil {
		t.Fatal("watchOnce returned nil, want an error once the coordinator closes the connection")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("watchOnce error = %v, want it to describe the connection closing", err)
	}

	if got := atomic.LoadInt32(snapshotHits); got != 3 {
		t.Errorf("snapshot fetch count = %d, want 3 (initial connect, seq gap, stream.reset); stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "connected: streamId=s1") {
		t.Errorf("stdout missing connect line:\n%s", out)
	}
	if !strings.Contains(out, "snapshot (initial connect)") {
		t.Errorf("stdout missing initial-connect snapshot marker:\n%s", out)
	}
	if !strings.Contains(out, "snapshot (sequence gap)") {
		t.Errorf("stdout missing sequence-gap snapshot marker:\n%s", out)
	}
	if !strings.Contains(out, "snapshot (stream.reset)") {
		t.Errorf("stdout missing stream.reset snapshot marker:\n%s", out)
	}
	if !strings.Contains(out, "[node.changed] media-03") {
		t.Errorf("stdout missing the in-sequence node.changed row:\n%s", out)
	}

	errOut := stderr.String()
	if !strings.Contains(errOut, "sequence gap detected") {
		t.Errorf("stderr missing sequence-gap notice:\n%s", errOut)
	}
	if !strings.Contains(errOut, "stream reset (reason=subscriber_too_slow)") {
		t.Errorf("stderr missing stream.reset notice:\n%s", errOut)
	}
}

// TestWatchOnceRejectsNonStreamStartFirstEvent guards against a
// coordinator (or a proxy mangling the stream) sending anything other
// than stream.start first; contract §6.4 pins this as the required first
// event, and a client that proceeds without it has no snapshot baseline.
func TestWatchOnceRejectsNonStreamStartFirstEvent(t *testing.T) {
	frames := []string{
		"event: node.changed\ndata: {\"seq\":1}\n\n",
	}
	ts, _ := sseTestServer(t, frames)

	streamClient, _ := newClient(ts.URL, "", &http.Client{})
	snapshotClient, _ := newClient(ts.URL, "", &http.Client{Timeout: 5 * time.Second})

	var stdout, stderr bytes.Buffer
	g := &globalFlags{output: outputText, timeout: 5 * time.Second}
	err := watchOnce(context.Background(), streamClient, snapshotClient, g, false, &stdout, &stderr, time.Now, newWatchBackoff())
	if err == nil {
		t.Fatal("watchOnce returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "stream.start") {
		t.Errorf("error = %v, want it to name stream.start as the expected first event", err)
	}
}

// TestWatchBackoffGrowsThenCaps pins the shape of the reconnect backoff:
// it must grow, and it must stop growing at the labelled ceiling.
func TestWatchBackoffGrowsThenCaps(t *testing.T) {
	b := newWatchBackoff()
	first := b.Next()
	second := b.Next()
	if second <= first {
		t.Errorf("backoff did not grow: first=%v second=%v", first, second)
	}
	for i := 0; i < 10; i++ {
		b.Next()
	}
	if got := b.Next(); got > 30*time.Second {
		t.Errorf("backoff = %v, want it capped at 30s", got)
	}
	b.Reset()
	if got := b.Next(); got != first {
		t.Errorf("after Reset, first Next() = %v, want %v (the initial interval)", got, first)
	}
}

// TestWatchOnceSendsDeltasQueryParamOnlyWhenRequested is this program's own
// half of ADR-023's additive-compatibility argument: --deltas must add
// EXACTLY "deltas=1" to the stream request, and its absence must add no
// such parameter at all — matching what the coordinator side actually
// requires (internal/coordinator/api's Hub.ServeHTTP treats anything other
// than the literal "1" as equivalent to omitting the parameter), and
// proving this client cannot accidentally send a spelling that fails to
// opt in.
func TestWatchOnceSendsDeltasQueryParamOnlyWhenRequested(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deltas    bool
		wantQuery string
	}{
		{"disabled", false, ""},
		{"enabled", true, "deltas=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","latestEventSeq":0,"nodes":[],"fpp":{"instances":[]},"collectors":[]}`)
			})
			mux.HandleFunc("/api/v1/stream", func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("ShowMesh-API-Version", "1")
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Fatal("ResponseWriter does not support Flusher")
				}
				_, _ = fmt.Fprint(w, "event: stream.start\ndata: {\"streamId\":\"s1\",\"apiVersion\":1,\"serverTime\":\"2026-08-10T21:00:00Z\",\"snapshotRequired\":true}\n\n")
				flusher.Flush()
				// Handler returns immediately after stream.start, closing
				// the connection — this test only needs to observe the
				// REQUEST, not drive any further frames.
			})
			ts := httptest.NewServer(mux)
			t.Cleanup(ts.Close)

			streamClient, err := newClient(ts.URL, "", &http.Client{})
			if err != nil {
				t.Fatalf("newClient: %v", err)
			}
			snapshotClient, err := newClient(ts.URL, "", &http.Client{Timeout: 5 * time.Second})
			if err != nil {
				t.Fatalf("newClient: %v", err)
			}

			var stdout, stderr bytes.Buffer
			g := &globalFlags{output: outputText, timeout: 5 * time.Second}
			// The connection closes right after stream.start, so
			// watchOnce always returns an error here; this test only
			// cares about the REQUEST it made, not that return value.
			_ = watchOnce(context.Background(), streamClient, snapshotClient, g, tc.deltas, &stdout, &stderr, time.Now, newWatchBackoff())

			if gotQuery != tc.wantQuery {
				t.Errorf("--deltas=%v: stream request query = %q, want %q", tc.deltas, gotQuery, tc.wantQuery)
			}
		})
	}
}

const fppObservationsChangedTmpl = `event: fpp.observations.changed
data: {"seq":%d,"serverTime":"2026-08-10T21:00:0%dZ","instanceId":"player-01","changed":[{"signal":"fpp.uptime.seconds","value":42,"unit":null,"state":"current","reason":null,"observedAt":"2026-08-10T21:00:00Z","collectedAt":"2026-08-10T21:00:00Z","source":"fpp-rest","quality":"direct","validForSeconds":60}],"removed":["fpp.sensor.temp"]}

`

// TestWatchOnceDecodesAndPrintsFPPObservationsChangedFrame proves this
// client can actually consume the frame ADR-023 adds, not merely request
// it: a real fpp.observations.changed frame over a live
// text/event-stream, decoded and rendered with both the signal that
// changed AND the one reported removed — a positive assertion paired with
// a positive assertion (BOTH must appear), not a bare "it did not crash".
func TestWatchOnceDecodesAndPrintsFPPObservationsChangedFrame(t *testing.T) {
	frames := []string{
		"event: stream.start\ndata: {\"streamId\":\"s1\",\"apiVersion\":1,\"serverTime\":\"2026-08-10T21:00:00Z\",\"snapshotRequired\":true}\n\n",
		fmt.Sprintf(fppObservationsChangedTmpl, 1, 1),
	}
	ts, snapshotHits := sseTestServer(t, frames)

	streamClient, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	snapshotClient, err := newClient(ts.URL, "", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}

	var stdout, stderr bytes.Buffer
	g := &globalFlags{output: outputText, timeout: 5 * time.Second}
	err = watchOnce(context.Background(), streamClient, snapshotClient, g, true, &stdout, &stderr, time.Now, newWatchBackoff())
	if err == nil {
		t.Fatal("watchOnce returned nil, want an error once the coordinator closes the connection")
	}

	// Exactly one snapshot fetch: the initial connect. seq 1 is the first
	// event after stream.start, so it is in-sequence — no gap, no extra
	// refetch — pinning that this new frame kind participates in gap
	// detection identically to every other frame kind.
	if got := atomic.LoadInt32(snapshotHits); got != 1 {
		t.Errorf("snapshot fetch count = %d, want 1 (initial connect only); stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "[fpp.observations.changed] player-01") {
		t.Errorf("stdout missing the fpp.observations.changed line:\n%s", out)
	}
	if !strings.Contains(out, "fpp.uptime.seconds") {
		t.Errorf("stdout missing the changed signal fpp.uptime.seconds:\n%s", out)
	}
	if !strings.Contains(out, "fpp.sensor.temp") {
		t.Errorf("stdout missing the removed signal fpp.sensor.temp:\n%s", out)
	}
}
