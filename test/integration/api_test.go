//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// publishHelloBurst connects one throwaway MQTT client directly to the test
// broker (bypassing per-connection agent-subprocess overhead entirely —
// see TestSlowSSEConsumerGetsResetAndDisconnected's doc comment for why
// that overhead defeated an earlier version of that test) and publishes n
// hello messages for n distinct, never-before-seen synthetic node IDs, at
// QoS 0 (fire-and-forget: no PUBACK round trip to wait on between
// messages) and RETAIN=false, back to back as fast as this process can
// call Publish. The goal is many genuine coordinator-side inventory writes
// landing within a window tight enough that internal/coordinator/api.Hub's
// own render pass (triggered by the first one) is still running — or has
// only just finished — by the time several more have already arrived, so
// a single render() call's diff finds many changed nodes at once. Fails t
// immediately on any connect or publish error; callers do not need to
// check a returned error.
func publishHelloBurst(t *testing.T, n int) {
	t.Helper()
	serverURL, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatalf("parse broker url %q: %v", brokerURL, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connected := make(chan struct{}, 1)
	cfg := autopaho.ClientConfig{
		ServerUrls:     []*url.URL{serverURL},
		KeepAlive:      30,
		ConnectTimeout: 10 * time.Second,
		OnConnectionUp: func(*autopaho.ConnectionManager, *paho.Connack) {
			select {
			case connected <- struct{}{}:
			default:
			}
		},
		ClientConfig: paho.ClientConfig{ClientID: "burst-publisher-" + uniqueSuffix()},
	}
	cm, err := autopaho.NewConnection(ctx, cfg)
	if err != nil {
		t.Fatalf("start burst publisher connection: %v", err)
	}
	defer func() { _ = cm.Disconnect(context.Background()) }()

	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatalf("burst publisher did not connect to the broker in time")
	}

	for i := 0; i < n; i++ {
		nodeID := fmt.Sprintf("burst-%s-%d", uniqueSuffix(), i)
		env, err := mqttproto.NewHelloEnvelope(nil, nodeID, mqttproto.HelloPayload{
			Platform: "p", AgentVersion: "v", BootID: "b", StartedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("build hello envelope %d: %v", i, err)
		}
		payload, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal hello envelope %d: %v", i, err)
		}
		topic, err := mqttproto.HelloTopic(nodeID)
		if err != nil {
			t.Fatalf("build hello topic %d: %v", i, err)
		}
		if _, err := cm.Publish(ctx, &paho.Publish{QoS: 0, Retain: false, Topic: topic, Payload: payload}); err != nil {
			t.Fatalf("publish hello %d: %v", i, err)
		}
	}
}

// readStreamFor opens GET /api/v1/stream with headers merged on top of the
// coordinator's own default auth header (if any), reads raw bytes for up to
// d, and returns whatever was captured plus the response's status code.
// Reading is bounded by a request context deadline, not resp.Body.Close —
// the streaming client this package needs must not carry the same
// http.Client.Timeout every other request uses (that would cut a healthy,
// idle stream exactly the way showmeshctl's own watch avoids it — see that
// package's cmdWatch doc comment).
func readStreamFor(t *testing.T, coord *testCoordinator, extraHeaders map[string]string, d time.Duration) (status int, raw string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coord.url("/api/v1/stream"), nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if coord.token != "" {
		req.Header.Set("Authorization", "Bearer "+coord.token)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var buf strings.Builder
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		buf.WriteString(line)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, buf.String()
}

// TestSSEStreamNeverEmitsIDLineAndIgnoresLastEventID pins contract section
// 6.4's two hardest-to-notice rules at the process level, over a real
// socket: no "id:" line is ever written (which would teach a browser's
// EventSource to resume from a local model OPERATOR-UI section 6 forbids
// resuming from), and a request carrying Last-Event-ID is served
// identically to one without it — never rejected, never given different
// content.
func TestSSEStreamNeverEmitsIDLineAndIgnoresLastEventID(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	status, raw := readStreamFor(t, coord, map[string]string{"Last-Event-ID": "999999"}, 2*time.Second)
	if status != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", status)
	}
	if !strings.Contains(raw, "event: stream.start") {
		t.Fatalf("did not observe stream.start with Last-Event-ID set; captured:\n%s", raw)
	}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "id:") {
			t.Errorf("stream emitted a forbidden %q line; contract section 6.4 requires no id: line ever, ever", trimmed)
		}
	}
}

// TestAPITokenEnforcedWhenSet is contract section 6.8 and Task F spec item
// 6, end to end: with SHOWMESH_API_TOKEN set, every /api/v1/* request
// (including the SSE stream) is 401 without the header and 200 with it,
// while /healthz and /readyz stay open.
func TestAPITokenEnforcedWhenSet(t *testing.T) {
	requireBroker(t)
	token := "s3cr3t-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(), apiToken: token,
	})

	if status, _ := coord.getRawWithHeaders(t, "/healthz", nil); status != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200 (probes stay open even with auth enabled)", status)
	}
	if status, _ := coord.getRawWithHeaders(t, "/readyz", nil); status != http.StatusOK && status != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 200 or 503, but never 401", status)
	}

	if status, body := coord.getRawWithHeaders(t, "/api/v1/nodes", nil); status != http.StatusUnauthorized {
		t.Errorf("/api/v1/nodes with no Authorization header: status = %d, want 401; body: %s", status, body)
	}
	if status, body := coord.getRawWithHeaders(t, "/api/v1/nodes", map[string]string{"Authorization": "Bearer wrong-token"}); status != http.StatusUnauthorized {
		t.Errorf("/api/v1/nodes with a wrong token: status = %d, want 401; body: %s", status, body)
	}
	if status, body := coord.getRaw(t, "/api/v1/nodes"); status != http.StatusOK { // coord.getRaw attaches the correct token
		t.Errorf("/api/v1/nodes with the correct token: status = %d, want 200; body: %s", status, body)
	}

	// The stream too, per contract section 6.8's explicit "enforce it on
	// /api/v1/stream too".
	streamStatus, _ := readStreamFor(t, coord, map[string]string{"Authorization": ""}, 500*time.Millisecond)
	// Step 3 review finding 4.6: this comment used to claim that setting an
	// empty Authorization value results in no header being sent at all.
	// That is wrong — net/http's http.Header.Write emits every header key
	// it holds regardless of value, so req.Header.Set("Authorization", "")
	// does put a literal "Authorization:" line on the wire with an empty
	// value, not an absent one. What this actually exercises is a present
	// but empty/malformed bearer credential, which the server's constant-time
	// comparison correctly rejects the same way it rejects a genuinely
	// absent header or a wrong token (both covered elsewhere in this test)
	// — 401 either way, just for a different reason than "no header at all".
	if streamStatus != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/stream with no token: status = %d, want 401", streamStatus)
	}
}

// TestAPIStreamOpensSuccessfullyWithAuthEnabled is Step 3 review finding
// 4.5's first missing-coverage item: every other test in this package that
// combines auth with /api/v1/stream only ever proves the 401 case
// (TestAPITokenEnforcedWhenSet above). Nothing proved the stream actually
// opens and delivers real frames when a token IS presented correctly —
// the endpoint contract section 6.8 itself calls out as "most likely to be
// special-cased later" because a browser EventSource cannot set an
// Authorization header at all (the reason this contract reads the stream
// with fetch rather than EventSource in the first place).
func TestAPIStreamOpensSuccessfullyWithAuthEnabled(t *testing.T) {
	requireBroker(t)
	token := "s3cr3t-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(), apiToken: token,
	})

	status, raw := readStreamFor(t, coord, nil, 2*time.Second) // readStreamFor's nil extraHeaders still gets coord.token attached, since coord.token != ""
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/stream with the correct token: status = %d, want 200; body: %s", status, raw)
	}
	if !strings.Contains(raw, "event: stream.start") {
		t.Errorf("GET /api/v1/stream with the correct token never emitted stream.start; captured:\n%s", raw)
	}
	if !strings.Contains(raw, "\"snapshotRequired\":true") {
		t.Errorf("GET /api/v1/stream with the correct token: stream.start payload missing snapshotRequired:true; captured:\n%s", raw)
	}
}

// TestAPIFPPWireCarriesCollectionFailedAndLastPollError is Step 3 review
// finding 4.5's third and fourth missing-coverage items together:
// collection_failed was pinned nowhere on the wire (no golden file, no
// OpenAPI-validated response contained it) and lastPollError was null in
// every golden file and every assertion this project had, leaving its
// non-nil branch unverified. A real coordinator with a real, syntactically
// valid but never-answering FPP endpoint (the same "127.0.0.1:1" trick
// TestClassifyRequestErrorUnreachable in cmd/showmeshctl uses) reliably
// produces both once its collector's first poll completes.
func TestAPIFPPWireCarriesCollectionFailedAndLastPollError(t *testing.T) {
	requireBroker(t)
	instanceID := "unreachable-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		fppEndpoints: instanceID + "=http://127.0.0.1:1",
	})

	var body []byte
	waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		var status int
		status, body = coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		return status == http.StatusOK && strings.Contains(string(body), "collection_failed")
	}, "the FPP collector's first poll against an unreachable endpoint to produce collection_failed evidence")

	// Raw-key checks against the literal bytes (contract section 1's
	// standing rule), not a re-decoded struct: this endpoint's whole point
	// here is proving these two exact strings/values reach the wire, which
	// a struct field that happened to rename its JSON tag would still let
	// pass if asserted only via Go equality on the decoded value.
	if !strings.Contains(string(body), `"state":"collection_failed"`) {
		t.Errorf(`GET /api/v1/fpp/%s never contains a literal "state":"collection_failed" observation; body: %s`, instanceID, body)
	}

	var resp struct {
		Instance struct {
			LastPollError *string `json:"lastPollError"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /api/v1/fpp/%s: %v; body: %s", instanceID, err, body)
	}
	if resp.Instance.LastPollError == nil {
		t.Fatalf(`GET /api/v1/fpp/%s: lastPollError = null, want a non-nil failure reason for an unreachable instance; body: %s`, instanceID, body)
	}
	if *resp.Instance.LastPollError == "" {
		t.Errorf("GET /api/v1/fpp/%s: lastPollError is present but empty, want a real failure class", instanceID)
	}
}

// TestAPINoTokenConfiguredServesUnauthenticated proves the documented
// default posture (contract section 6.8): with SHOWMESH_API_TOKEN unset,
// /api/v1/* answers with no Authorization header at all, and this
// coordinator logged the required startup warning about it.
func TestAPINoTokenConfiguredServesUnauthenticated(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	if status, body := coord.getRawWithHeaders(t, "/api/v1/nodes", nil); status != http.StatusOK {
		t.Fatalf("/api/v1/nodes with auth disabled: status = %d, want 200; body: %s", status, body)
	}
	if !strings.Contains(coord.logs.String(), "NO AUTHENTICATION") {
		t.Errorf("coordinator log output does not contain the required startup warning naming the exposure; logs:\n%s", coord.logs.String())
	}
}

// TestAPIVersionNegotiation is Task F spec item 7: an unsupported
// ShowMesh-API-Version request header, and an unknown path version, both
// produce application/problem+json naming the supported versions — over a
// real socket against a real subprocess, not a handler called directly.
func TestAPIVersionNegotiation(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{name: "unsupported header version", path: "/api/v1/nodes", headers: map[string]string{"ShowMesh-API-Version": "2"}},
		{name: "garbage header version", path: "/api/v1/nodes", headers: map[string]string{"ShowMesh-API-Version": "garbage"}},
		{name: "unknown path version", path: "/api/v2/nodes", headers: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := coord.getRawWithHeaders(t, tc.path, tc.headers)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", status, body)
			}
			var problem map[string]any
			if err := json.Unmarshal(body, &problem); err != nil {
				t.Fatalf("decode problem body: %v; body: %s", err, body)
			}
			if problem["type"] != "https://showmesh.dev/problems/unsupported-api-version" {
				t.Errorf(`problem "type" = %v, want the unsupported-api-version URI`, problem["type"])
			}
			versions, ok := problem["supportedVersions"].([]any)
			if !ok || len(versions) == 0 {
				t.Errorf(`problem "supportedVersions" = %v, want a non-empty array`, problem["supportedVersions"])
			}
		})
	}

	// A header explicitly set to "1" (the version this coordinator serves)
	// must succeed, proving the check is a real negotiation, not a blanket
	// rejection of the header's presence.
	if status, body := coord.getRawWithHeaders(t, "/api/v1/nodes", map[string]string{"ShowMesh-API-Version": "1"}); status != http.StatusOK {
		t.Errorf(`status with ShowMesh-API-Version: "1" = %d, want 200; body: %s`, status, body)
	}
}

// TestEventsGaplessAndDuplicateFreeAcrossSnapshotBoundary is Task F spec
// item 9: take a snapshot, cause events (via a real control-plane
// transition — see internal/coordinator/inventory's recordLivenessTransition,
// this session's fix for "nothing ever called AppendEvent outside a test"),
// fetch from latestEventSeq, and assert exactly the events that happened
// after the snapshot: no gap, no duplicate.
func TestEventsGaplessAndDuplicateFreeAcrossSnapshotBoundary(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})
	waitOnline(t, coord, nodeID)

	snap := coord.snapshot(t)
	before := snap.LatestEventSeq

	// Cause at least one more event: an unclean kill is a genuine, fast
	// control-plane transition (online -> offline).
	agent.sigkill(t)
	waitOffline(t, coord, nodeID)

	// Bounded poll for at least one new event past the snapshot boundary —
	// recordLivenessTransition runs asynchronously relative to this test.
	var events []struct {
		Seq uint64 `json:"seq"`
	}
	var latest uint64
	var gap bool
	waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, fmt.Sprintf("/api/v1/events?since=%d", before))
		if status != http.StatusOK {
			t.Fatalf("GET /api/v1/events?since=%d: status %d, body: %s", before, status, body)
		}
		var resp struct {
			Events []struct {
				Seq uint64 `json:"seq"`
			} `json:"events"`
			LatestSeq uint64 `json:"latestSeq"`
			Gap       bool   `json:"gap"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode events response: %v; body: %s", err, body)
		}
		events, latest, gap = resp.Events, resp.LatestSeq, resp.Gap
		return len(events) > 0
	}, "at least one event recorded after the snapshot boundary (a control-plane transition)")

	if gap {
		t.Errorf("gap = true immediately after a snapshot boundary with no retention pruning in play, want false")
	}
	if latest < before {
		t.Errorf("latestSeq = %d, want >= the pre-transition snapshot's latestEventSeq %d", latest, before)
	}

	seen := map[uint64]bool{}
	for _, ev := range events {
		if ev.Seq <= before {
			t.Errorf("event seq %d is <= the snapshot boundary %d; want only events strictly after it (no duplicate of pre-snapshot history)", ev.Seq, before)
		}
		if seen[ev.Seq] {
			t.Errorf("event seq %d appears more than once in one response", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}

// TestSlowSSEConsumerGetsResetAndDisconnected is Task F spec item 4, closed
// at the process level per its own instruction: the API package's own unit
// suite proved the reset mechanism (a white-box test) and a direct
// signal-injection test, but left "the wiring... not proven" because an
// end-to-end flood test was found genuinely flaky (TCP buffering and the
// drain goroutine outracing the test).
//
// This test makes the overflow deterministic BY CONSTRUCTION instead of by
// racing a flood, per the task spec's own suggested fix: it starts the
// coordinator with an artificially tiny per-subscriber buffer
// (SHOWMESH_TEST_STREAM_SUBSCRIBER_BUFFER — see
// internal/coordinator/api's envStreamSubscriberBufferOverride, added this
// session specifically so this test could exist deterministically), opens
// a real stream connection and deliberately does not read its body for a
// window, then fires a burst of real node changes — comfortably more than
// the tiny buffer holds — during that window.
//
// TWO EARLIER VERSIONS OF THIS TEST WERE WRONG, and both failures are worth
// recording because they are exactly the shape of mistake this task's
// standing rule warns about:
//
//  1. A version driving a raw net.Conn and treating "the request line and
//     headers were sent with Connection: keep-alive" as license to detect
//     closure via a raw byte-level Read returning any error. It never
//     failed loudly — it just hung until the read deadline, every time,
//     because net/http's chunked-encoding framing means the SSE response
//     can end (its final "0\r\n\r\n" chunk written) while the underlying
//     TCP connection is kept alive for a next request that never comes.
//     A raw socket read cannot see that the logical response ended; only
//     something that understands HTTP framing (net/http's own Transport,
//     via resp.Body) can. Discovered by comparing a raw-socket capture
//     against an http.Client capture of the identical scenario side by
//     side — the http.Client one correctly saw stream.reset followed by
//     EOF on resp.Body in under a second; the raw one never did.
//  2. An even earlier version used real showmesh-agent subprocesses to
//     produce the burst. Real process spawns and MQTT handshakes are slow
//     and spread out enough in wall-clock time that
//     internal/coordinator/api.Hub's render pass (triggered promptly by
//     internal/coordinator/inventory's onChange hook) kept draining each
//     change individually — the tiny buffer was never actually exceeded,
//     so the test always "passed" for the wrong reason until a stricter
//     timeout-vs-close distinction (finding 1, above) exposed that it
//     never actually observed a close at all.
//
// publishHelloBurst (a direct, single-connection MQTT client) replaced the
// agent-subprocess burst for exactly this reason: it can fire hundreds of
// genuinely distinct node changes within single-digit milliseconds,
// reliably landing several within one render pass.
func TestSlowSSEConsumerGetsResetAndDisconnected(t *testing.T) {
	requireBroker(t)
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		streamSubscriberBuffer: 2,
	})

	req, err := http.NewRequest(http.MethodGet, coord.url("/api/v1/stream"), nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	// Deliberately do not read resp.Body at all yet: this is the "slow
	// consumer" the test needs. Fire a burst of real, distinct node
	// changes while it is not being drained — internal/coordinator/inventory's
	// onChange hook (this session's wiring) pokes the hub on every one, and
	// several of them landing while a single render() pass is still
	// catching up on the first is exactly the condition that overflows a
	// per-subscriber buffer of 2.
	const burst = 200
	publishHelloBurst(t, burst)

	// NOW read: per finding 1 above, only a proper HTTP-body read can tell
	// "the stream ended" (a completed chunked body, surfaced as io.EOF on
	// resp.Body) apart from "the connection is idle but still open for
	// keep-alive reuse". A read TIMEOUT and an actual EOF are not the same
	// outcome, and conflating them would be exactly the "test that passes
	// whether or not the bug is present" this project's standing rule
	// warns against — see this function's own doc comment. Only io.EOF (or
	// any other non-timeout error) counts as proof the coordinator ended
	// the stream; a timeout fails the test with an explicit message
	// instead of silently passing.
	//
	// Step 3 review finding 1.2: an earlier version of this test read into
	// a bare byte counter and asserted only that the read eventually ended
	// (any io.EOF or other error), never looking at the bytes themselves.
	// Confirmed by mutation: replacing the reset arm in
	// internal/coordinator/api/stream.go with a bare `return` (so the
	// connection is simply closed with no stream.reset frame at all) left
	// this test green, because a bare close also produces io.EOF. Keep the
	// full body instead of discarding it, and require it to actually
	// contain the "event: stream.reset" frame naming
	// "subscriber_too_slow" — the one thing that distinguishes "the
	// coordinator told this client why" from "the connection merely died".
	type readResult struct {
		body bytes.Buffer
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		var res readResult
		_, res.err = res.body.ReadFrom(resp.Body)
		done <- res
	}()

	select {
	case res := <-done:
		if !errors.Is(res.err, io.EOF) {
			t.Logf("stream ended with a non-EOF error (still counts as ended): %v", res.err)
		}
		body := res.body.String()
		t.Logf("stream ended after %d total bytes read (following a burst of %d changes with a buffer of 2)", res.body.Len(), burst)
		if !strings.Contains(body, "event: stream.reset") {
			t.Fatalf("stream body never contained an \"event: stream.reset\" frame before the connection ended; body:\n%s", body)
		}
		if !strings.Contains(body, "subscriber_too_slow") {
			t.Fatalf("stream body contained a stream.reset frame but never named reason \"subscriber_too_slow\"; body:\n%s", body)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("stream was not closed by the coordinator within the 20s wait bound; "+
			"a non-draining subscriber with a buffer of 2 receiving a burst of %d changes must be disconnected per contract section 6.4", burst)
	}
}
