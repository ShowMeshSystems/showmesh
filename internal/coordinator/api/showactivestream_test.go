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

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "currentRuns.changed" {
		t.Fatalf("event = %q, want currentRuns.changed; data: %s", event, data)
	}
	if !containsAll(data, `"show":"halloween-2026"`) {
		t.Errorf("frame does not carry the newly activated show; data: %s", data)
	}
}
