package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// activeShowOnlyCurrentRuns reads nothing but whether show.active exists,
// so a currentRuns.changed frame in the test below can only be caused by
// the activation write itself.
func activeShowOnlyCurrentRuns(st *store.Store, show string) currentrun.Coordinator {
	return currentrun.Coordinator{Read: func(ctx context.Context, _ time.Time) (currentrun.Snapshot, error) {
		obj, err := st.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID)
		switch {
		case errors.Is(err, store.ErrConfigObjectNotFound):
			return currentrun.Snapshot{}, nil
		case err != nil:
			return currentrun.Snapshot{}, err
		}
		return currentrun.Snapshot{Active: currentrun.ActiveContext{
			Configured: true, Show: show, Generation: obj.CurrentRevision,
		}}, nil
	}}
}

// TestPutShowActiveNotifiesTheStreamHub proves the activation itself is
// what refreshes a show-dependent stream payload, rather than the hub's
// periodic re-render happening to recompute the same value moments later.
//
// The recompute is what makes this worth asserting and what makes a naive
// assertion worthless: with the production five second tick, a connected
// client sees the new value whether or not anything told the hub the show
// changed. So the tick and the keepalive are an hour here (the posture
// newStreamTestAPI already uses in stream_test.go): the hub renders once
// per Notify and at no other time in this test's lifetime, so the frame
// below can have exactly one cause. Delete handlePutShowActive's
// notifyStreamHub call and this test fails on the read timeout, because
// the next render would be an hour away.
func TestPutShowActiveNotifiesTheStreamHub(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showObjectsTestDeps(svc, st)
	deps.CurrentRuns = activeShowOnlyCurrentRuns(st, "halloween-2026")
	api := New(deps, Options{
		Clock:                   fixedClock(testNow),
		Logger:                  testLogger(),
		StreamTickInterval:      time.Hour,
		StreamKeepaliveInterval: time.Hour,
	})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	srv := httptest.NewServer(api.Handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatalf("building stream request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/stream: status = %d, want 200", resp.StatusCode)
	}
	r := bufio.NewReader(resp.Body)
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start", event)
	}

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"halloween-2026"}`,
		map[string]string{"Authorization": "Bearer " + token})
	if putResp, body := doRawRequest(t, api.Handler, putReq); putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.active: status = %d, want 200; body: %s", putResp.StatusCode, body)
	}

	// Frame ORDER is not a contract. One notify re-renders every resource,
	// so any of them may diff and publish, and nothing orders those
	// publishes. Read until the one this test is about, bounded by a frame
	// count as well as a timeout so an unexpected flood fails loudly
	// rather than draining until it happens to find what it wanted.
	const maxFrames = 8
	var event, data string
	var seen []string
	for i := 0; i < maxFrames; i++ {
		event, data = readEventWithTimeout(t, r, 5*time.Second)
		if event == "currentRuns.changed" {
			break
		}
		seen = append(seen, event)
	}
	if event != "currentRuns.changed" {
		t.Fatalf("no currentRuns.changed within %d frames; saw %v, last data: %s", maxFrames, seen, data)
	}
	if !containsAll(data, `"show":"halloween-2026"`) {
		t.Errorf("frame does not carry the newly activated show; data: %s", data)
	}
}

// TestFPPParticipationResolvesAgainstTheActivatedShow asserts the value
// rather than the arrival order, which is the property that actually has
// to hold: once show.active is committed, participation for an FPP
// instance is resolved against THAT show, not against the absence of one.
//
// An ordering assertion cannot express this. A frame computed before the
// write commits and delivered afterwards would carry "no show is currently
// active", and whether a reader notices depends on which resource happened
// to diff first. This reads the resolved value after the activation, so it
// fails on a stale computation no matter when the frame arrives or whether
// a frame arrives at all.
func TestFPPParticipationResolvesAgainstTheActivatedShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showObjectsTestDeps(svc, st)
	deps.CurrentRuns = activeShowOnlyCurrentRuns(st, "halloween-2026")
	// Without this the participation resolver has no store, and every
	// answer below would be "no asset manifest store is configured"
	// regardless of the activation, so the test could never fail for the
	// reason it names.
	deps.AssetManifests = st
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	before := getFPPListBody(t, api, token)
	if !containsAll(before, `"state":"not_configured"`) {
		t.Fatalf("before activation participation must be not_configured; body: %s", before)
	}

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"halloween-2026"}`,
		map[string]string{"Authorization": "Bearer " + token})
	if putResp, body := doRawRequest(t, api.Handler, putReq); putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.active: status = %d, want 200; body: %s", putResp.StatusCode, body)
	}

	after := getFPPListBody(t, api, token)
	if containsAll(after, `"reason":"no show is currently active"`) {
		t.Errorf("participation still reports no active show after a committed activation; body: %s", after)
	}
	if !containsAll(after, `"show":"halloween-2026"`) {
		t.Errorf("participation was not resolved against the activated show; body: %s", after)
	}
}

// TestFPPParticipationWithNoStoreDoesNotClaimNoShowIsActive pins the
// distinction the node form already made and the instance form lost: a
// coordinator with no participation data source cannot answer the
// question, and saying "no show is currently active" instead is a
// confident false answer that sends a reader to the wrong remedy.
//
// No production construction reaches this today, since the only one wires
// the store unconditionally. It is guarded because the resolver's inputs
// erase the difference, so a future caller would get the false answer with
// nothing to warn them.
func TestFPPParticipationWithNoStoreDoesNotClaimNoShowIsActive(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showObjectsTestDeps(svc, st)
	deps.AssetManifests = nil
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := getFPPListBody(t, api, token)
	if containsAll(body, `"reason":"no show is currently active"`) {
		t.Errorf("a coordinator with no participation data source must not report that no show is active; body: %s", body)
	}
	if !containsAll(body, `"reason":"no asset manifest store is configured on this coordinator"`) {
		t.Errorf("participation must name the missing data source; body: %s", body)
	}
}

// getFPPListBody reads GET /fpp and fails the test on anything but 200, so
// an empty body can never be mistaken for an absent field below.
func getFPPListBody(t *testing.T, api *API, token string) string {
	t.Helper()
	req := newJSONRequest(t, http.MethodGet, "/api/v1/fpp", "",
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /fpp: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatal("GET /fpp returned an empty body; an empty read must fail loudly rather than read as an absent field")
	}
	return string(body)
}
