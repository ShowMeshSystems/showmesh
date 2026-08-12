package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// TestStreamRevalidationClosesConnectionOnSessionRevoke is BUILD-PLAN Step
// 6's own acceptance criterion, proven end to end: "Revoking a session
// closes an open change stream." A short StreamTickInterval (10ms — this
// package's testing standard forbids a real sleep INSIDE an assertion,
// not a short real ticker an event-driven read blocks on with a generous
// timeout, exactly the pattern stream_test.go's own
// TestStreamWedgedSubscriberIsReclaimedByWriteDeadline already uses) makes
// [Hub.revalidateSubscribers] fire well within this test's 5-second read
// timeout, so this is a deterministic wait on a real event, not a race
// against wall time.
func TestStreamRevalidationClosesConnectionOnSessionRevoke(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)

	api := New(authTestDeps(svc), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		StreamTickInterval: 10 * time.Millisecond, StreamKeepaliveInterval: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Cookie", sessionCookieName+"="+cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start; data: %s", event, data)
	}

	sessions, err := svc.ListSessions(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected exactly 1 session for %s, got %d", p.ID, len(sessions))
	}
	if err := svc.RevokeSession(context.Background(), sessions[0].ID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "stream.reset" {
		t.Fatalf("event after revocation = %q, want stream.reset; data: %s", event, data)
	}
	if !strings.Contains(data, "credential_invalidated") {
		t.Fatalf("stream.reset reason = %s, want it to contain \"credential_invalidated\"", data)
	}
	if !strings.Contains(data, `"snapshotRequired":true`) {
		t.Fatalf("stream.reset payload = %s, want snapshotRequired:true (ADR-020's reconnect-and-resnapshot path)", data)
	}

	// The connection itself is actually closed, not merely sent a frame
	// while staying open — ADR-024 decision 5 says "closes the
	// connection", not "notifies and continues".
	if _, _, err := nextRealEvent(r); err == nil {
		t.Fatalf("expected the connection to be closed after stream.reset, but another frame (or no error) was read")
	}
}

// TestStreamRevalidationLeavesUnauthenticatedConnectionAlone is the
// negative case: a connection that opened with NO credential at all
// (cred == nil — the ordinary case while reads are open, which is this
// test's default) has nothing for [Hub.revalidateSubscribers] to check,
// and must never be closed by it. This calls revalidateSubscribers
// directly and synchronously — deliberately not through [Hub.Run]'s real
// ticker (see [TestStreamRevalidationClosesConnectionOnSessionRevoke] for
// the end-to-end wiring proof) — so this test needs no real sleep and
// cannot be timing-flaky: it is not asserting an absence of a disconnect
// over some wall-clock window, it is asserting one specific call did not
// disconnect the subscriber, which is exactly what an implementation that
// dropped [Hub.revalidateSubscribers]'s cred == nil guard would break.
func TestStreamRevalidationLeavesUnauthenticatedConnectionAlone(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))

	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

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

	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start; data: %s", event, data)
	}

	api.Hub.revalidateSubscribers(context.Background(), testNow)

	// Prove the connection is genuinely still alive and functional, not
	// merely still present in the subscribers map: trigger and read a
	// real frame over the same connection.
	api.Hub.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{onlineNodeFixture(t)})
	api.Hub.Notify()
	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "node.changed" {
		t.Fatalf("event after revalidateSubscribers with no credential = %q, want node.changed (connection should be unaffected); data: %s", event, data)
	}
}

// TestStreamRevalidationDoesNotSlideSessionLastUsedAt closes review finding
// 11's third smaller item: [Hub.revalidateSubscribers] used to call
// AuthenticateSession, which slides LastUsedAt on every tick
// (defaultStreamTickInterval, 5s in production) purely because a
// connection stayed open — not because an operator did anything. That made
// ADR-024 decision 5's 90-day idle window unenforceable for exactly the
// case it names ("a forgotten tab... slides its session forever"), and
// cost one UPDATE per tick per open connection.
//
// This test proves the fix (RevalidateSession, not AuthenticateSession —
// see stream.go's revalidateSubscribers) directly: revalidate a real,
// subscribed connection's credential once, well within its idle window,
// then confirm the session genuinely idle-expires when the clock later
// crosses SessionMaxIdle measured from the ORIGINAL login — which is only
// true if the revalidation tick above did not touch LastUsedAt.
func TestStreamRevalidationDoesNotSlideSessionLastUsedAt(t *testing.T) {
	clockTime := testNow
	clock := func() time.Time { return clockTime }
	svc := newTestIdentityService(t, clock)
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)

	api := New(authTestDeps(svc), Options{Clock: clock, Logger: testLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Cookie", sessionCookieName+"="+cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, data := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start; data: %s", event, data)
	}

	// Deep within the idle window, revalidate this subscriber's credential
	// directly — matching TestStreamRevalidationLeavesUnauthenticatedConnectionAlone's
	// own posture of calling it synchronously rather than waiting out a
	// real ticker, so this stays a deterministic call, not a race.
	clockTime = clockTime.Add(identity.SessionMaxIdle - time.Hour)
	api.Hub.revalidateSubscribers(context.Background(), clockTime)

	// Now cross SessionMaxIdle measured from the ORIGINAL login. If the
	// revalidation tick above had slid LastUsedAt (the bug), the session
	// would still authenticate here; it must not.
	clockTime = clockTime.Add(2 * time.Hour)
	if _, err := svc.AuthenticateSession(context.Background(), cookie, clockTime); !errors.Is(err, identity.ErrInvalidCredential) {
		t.Fatalf("session still authenticates after crossing SessionMaxIdle from its original login: err = %v, want ErrInvalidCredential — "+
			"a stream revalidation tick must never slide LastUsedAt the way a genuine request does", err)
	}
}
