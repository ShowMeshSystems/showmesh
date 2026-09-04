package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestEmergencyStopSurvivesServerWriteTimeout is this endpoint's own
// version of assets_test.go's TestPostAssetUploadSurvivesServerReadAndWriteTimeouts
// and stream.go's TestStreamSurvivesServerWriteTimeout: a real *http.Server
// with a short WriteTimeout, and a dispatch paced slower than it, proving
// handleEmergencyStop's own SetWriteDeadline extension
// (emergencyStopHTTPWriteDeadline) is what keeps the connection alive —
// not merely that the constant is large on paper.
//
// Before that extension existed, this endpoint had none at all (unlike
// its FPP/audio/Resolume dispatch siblings): a real emergency stop whose
// audio.node.silence dispatch took longer than the server's own default
// WriteTimeout had its connection severed mid-dispatch, telling the
// operator a transport error for a stop that was still being carried out
// server-side — discovered on this exact endpoint once audio.node.silence
// and resolume.blackout joined stopPlaylist as concurrently-dispatched
// families. WriteTimeout is set here deliberately short (not left at
// zero): a passing test against a handler that forgot the extension
// entirely would otherwise still pass, the identical trap
// assets_test.go's own doc comment names.
func TestEmergencyStopSurvivesServerWriteTimeout(t *testing.T) {
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	mustPutAudioNodeDirect(t, st, "node-a")

	// FPP and Resolume are left unconfigured (their default no-op
	// listers): each of those two families resolves near-instantly, so
	// the audio.node family — deliberately slowed below — is what this
	// test actually exercises against the short server WriteTimeout.
	pub := &fakeAudioPublisher{
		result: audioNodeSilenceConfirmedEvidence(),
		// Runs synchronously before AwaitResponse does anything else,
		// pacing this dispatch well past ts.Config.WriteTimeout below,
		// while staying comfortably inside this handler's own (much
		// larger) real emergencyStopHTTPWriteDeadline.
		onAwaitResponse: func() { time.Sleep(300 * time.Millisecond) },
	}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		FPP:      &fakeFPPLister{},
		Identity: svc, Config: st, Commands: st, NightSessions: st,
		Macros: &fakeMacroRunner{}, ResolumeActions: &fakeResolumeActionDispatcher{},
		AudioPublisher: pub,
	}.withDefaults()
	api := New(deps, Options{Clock: fixedClock(now), Logger: testLogger()})

	ts := httptest.NewUnstartedServer(api.Handler)
	// Far shorter than the 300ms dispatch above, and far shorter than
	// this handler's own real emergencyStopHTTPWriteDeadline (tens of
	// seconds) — proving the extension, not merely a generous default.
	ts.Config.WriteTimeout = 50 * time.Millisecond
	ts.Start()
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/emergency-stop/stop", strings.NewReader(`{"idempotencyKey":"write-timeout-key"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("emergency stop paced past the server write timeout failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a dispatch slower than the server's own WriteTimeout must still succeed); body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	byKind := stopOutcomesByTargetKind(t, result)
	node, ok := byKind["node"]
	if !ok {
		t.Fatalf("no stopOutcomes entry carries targetKind %q; the slow dispatch never completed server-side either: %v", "node", result["stopOutcomes"])
	}
	if node["outcome"] != "confirmed" {
		t.Fatalf("node entry outcome = %v, want %q — the slow dispatch must have actually finished, not just the HTTP connection surviving", node["outcome"], "confirmed")
	}
}
