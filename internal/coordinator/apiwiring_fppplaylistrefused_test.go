package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file proves fpp-plugin-reports-refused's real HTTP route path end
// to end: a 409 refusal through the actual POST handler sets the marker
// this package's [fppInstanceLister] renders, an accepted report through
// that same handler clears it, and DELETE .../playlist-entry-observations
// clears it too. TestFPPInstanceListerSurfacesPlaylistObservationRefused
// (apiwiring_test.go) proves the read side against a marker set directly
// through the store; this proves the write side is the same marker.

const refusedTestPlaylistHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func refusedTestObservationBody(t *testing.T, instanceUUID string, sequence int64, now time.Time) string {
	t.Helper()
	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: instanceUUID, PlaylistName: "Bench Show", PlaylistHash: refusedTestPlaylistHash, Section: "mainPlaylist", Position: 0,
	})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	m := map[string]any{
		"schemaVersion": 1, "instanceUuid": instanceUUID, "playlistName": "Bench Show",
		"playlistHash": refusedTestPlaylistHash, "section": "mainPlaylist", "position": 0,
		"entryKey": entryKey, "action": "playing", "sequence": sequence,
		"observedAtMillis": now.UnixMilli(), "coalescedSincePreviousAcknowledged": 0,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal observation body: %v", err)
	}
	return string(raw)
}

func refusedTestDo(t *testing.T, h http.Handler, method, target, body, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode response body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, m
}

// TestFPPPlaylistObservationRefusedRoutePath drives the actual POST and
// DELETE handlers, not the store methods directly, then reads the
// resulting condition off the real [fppInstanceLister].
func TestFPPPlaylistObservationRefusedRoutePath(t *testing.T) {
	now := time.Now()
	st := openTestStore(t)
	ctx := context.Background()
	svc := identity.NewService(st, func() time.Time { return now }, filepath.Join(t.TempDir(), "identity"), identity.WithLogger(testLogger()))
	admin, err := svc.CreatePrincipal(ctx, "admin-1", identity.KindHuman, identity.RoleAdmin, "bench-pass-1234")
	if err != nil {
		t.Fatalf("create admin principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, admin.ID, "", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "bench-fpp", "bench-uuid", now); err != nil {
		t.Fatalf("record instance uuid: %v", err)
	}

	apiInstance := api.New(api.Dependencies{
		Identity: svc, FPPObservations: st,
		FPPReconciliation: api.StoreFPPReconciliation{Store: st},
	}, api.Options{Clock: func() time.Time { return now }, Logger: testLogger()})

	lister := fppInstanceLister{st: st, endpoints: fixedFPPEndpoints{{ID: "bench-fpp", URL: "http://127.0.0.1:9"}}}
	requireRefused := func(want bool) {
		t.Helper()
		views, err := lister.ListInstances(ctx)
		if err != nil {
			t.Fatalf("ListInstances: %v", err)
		}
		got := views[0].PlaylistObservationRefused != nil
		if got != want {
			t.Fatalf("PlaylistObservationRefused set = %v, want %v (view: %+v)", got, want, views[0].PlaylistObservationRefused)
		}
	}

	// An accepted report carries no refusal.
	status, _ := refusedTestDo(t, apiInstance.Handler, http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations",
		refusedTestObservationBody(t, "bench-uuid", 5, now), tok.Value)
	if status != http.StatusOK {
		t.Fatalf("accepted post: status = %d", status)
	}
	requireRefused(false)

	// A real 409 sequence-regression refusal through the handler sets it.
	status, body := refusedTestDo(t, apiInstance.Handler, http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations",
		refusedTestObservationBody(t, "bench-uuid", 3, now), tok.Value)
	if status != http.StatusConflict {
		t.Fatalf("regressed post: status = %d, body: %v", status, body)
	}
	requireRefused(true)

	// The next accepted report clears it.
	status, _ = refusedTestDo(t, apiInstance.Handler, http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations",
		refusedTestObservationBody(t, "bench-uuid", 6, now), tok.Value)
	if status != http.StatusOK {
		t.Fatalf("second accepted post: status = %d", status)
	}
	requireRefused(false)

	// Re-trigger the refusal, then clear it through the reset route instead.
	status, body = refusedTestDo(t, apiInstance.Handler, http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations",
		refusedTestObservationBody(t, "bench-uuid", 2, now), tok.Value)
	if status != http.StatusConflict {
		t.Fatalf("second regressed post: status = %d, body: %v", status, body)
	}
	requireRefused(true)

	status, _ = refusedTestDo(t, apiInstance.Handler, http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/bench-uuid", "", tok.Value)
	if status != http.StatusNoContent {
		t.Fatalf("reset route: status = %d", status)
	}
	requireRefused(false)
}
