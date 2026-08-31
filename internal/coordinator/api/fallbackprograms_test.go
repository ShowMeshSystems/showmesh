package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackcompile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/signingkey"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
)

// TestFallbackProgramGetForbiddenForViewerNamesMissingScope proves
// GET /api/v1/fallback-programs/{fppInstanceId} is gated by fpp:fallback,
// never merely by observation:read: a viewer holding every read scope
// but that one is rejected, with the problem detail naming it.
func TestFallbackProgramGetForbiddenForViewerNamesMissingScope(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, svc, p.ID)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/M4-7840e12f81da4191c0d00fbb6a889314", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeForbidden {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeForbidden)
	}
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, string(identity.ScopeFPPFallback)) {
		t.Errorf("detail = %q, want it to name %q", detail, identity.ScopeFPPFallback)
	}
}

// TestFallbackProgramAckMismatchedRevisionIsNotCurrent proves an
// acknowledgement whose packageId matches but whose revision does not
// reads back as stale, never current: matching packageId alone does not
// prove a host is running what the coordinator currently holds.
func TestFallbackProgramAckMismatchedRevisionIsNotCurrent(t *testing.T) {
	api, st, auth := fallbackProgramAdminAPI(t)
	const instanceUUID = "M4-7840e12f81da4191c0d00fbb6a889314"

	if err := st.PutFallbackProgram(context.Background(), store.FallbackProgramRecord{
		FPPInstanceUUID: instanceUUID, PackageID: "pkg-1", Revision: "rev-current",
		ShowID: "halloween-2026", Generation: 1,
		ProgramJSON:  `{"program":{"schemaVersion":1},"signature":"dGVzdA=="}`,
		SignatureB64: "dGVzdA==", ExpiresAt: testNow.Add(time.Hour), CompiledAt: testNow,
	}); err != nil {
		t.Fatalf("PutFallbackProgram: %v", err)
	}

	// Matching packageId, but a stale revision: the host verified and
	// installed a package the coordinator has since moved past under the
	// identical packageId.
	ackReq := newJSONRequest(t, http.MethodPost, "/api/v1/fallback-programs/"+instanceUUID+"/acknowledge",
		`{"packageId":"pkg-1","revision":"rev-stale","verificationResult":"verified","installedAt":"2026-08-30T12:01:00Z"}`, auth)
	ackResp, ackBody := doRawRequest(t, api.Handler, ackReq)
	if ackResp.StatusCode != http.StatusOK {
		t.Fatalf("POST fallback-programs acknowledge: status = %d, want 200; body: %s", ackResp.StatusCode, ackBody)
	}

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/"+instanceUUID, auth)
	var got fallbackProgramResponseForTest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode fallback program response: %v", err)
	}
	if got.AcknowledgedStatus == v1.FallbackProgramStatusCurrent {
		t.Fatalf("acknowledging packageId %q with a MISMATCHED revision must never read back as current", "pkg-1")
	}
	if got.AcknowledgedStatus != v1.FallbackProgramStatusStale {
		t.Fatalf("AcknowledgedStatus = %q, want %q", got.AcknowledgedStatus, v1.FallbackProgramStatusStale)
	}
}

// TestFallbackProgramSignedBytesRoundTripEndToEnd proves the served
// "program" bytes, re-canonicalized, verify against the served signature:
// runs a real [signingkey.Manager] through Compile, storage, and the
// real HTTP handler, never merely asserting a 200 or an in-memory match.
func TestFallbackProgramSignedBytesRoundTripEndToEnd(t *testing.T) {
	ctx := context.Background()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.FallbackPrograms = st
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	const instanceUUID = "M4-7840e12f81da4191c0d00fbb6a889314"
	const showID = "halloween-2026"
	const nodeID = "render-01"
	playlistHash := strings.Repeat("1", 64)

	showPayload, err := config.EncodeShowPayload(config.ShowPayload{Name: "Halloween 2026"})
	if err != nil {
		t.Fatalf("encode show: %v", err)
	}
	putConfigForTest(t, st, config.ShowConfigKind, showID, showPayload)

	mustDeclareNode(t, st, nodeID)
	surfacePayload, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: showID, Name: "garage", Node: nodeID,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 12},
		Geometry:     config.ShowSurfaceGeometry{Width: 2, Height: 2, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "test"}},
	})
	if err != nil {
		t.Fatalf("encode surface: %v", err)
	}
	putConfigForTest(t, st, config.ShowSurfaceConfigKind, "garage", surfacePayload)

	if _, _, err := st.CreateAsset(ctx, store.AssetRecord{
		ID: strings.Repeat("a", 64) + "-show-thriller", ShowID: showID, SequenceID: "thriller",
		TargetKind: store.AssetTargetKindShow, MediaType: "fseq", ContentHash: strings.Repeat("a", 64),
		RuntimeFilename: "thriller.fseq", SizeBytes: 1024, Backend: "volume", StorageKey: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	if err != nil {
		t.Fatalf("encode cue: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, "thriller", cuePayload)

	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: playlistHash},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	if err != nil {
		t.Fatalf("encode playlist: %v", err)
	}
	putConfigForTest(t, st, config.ShowPlaylistConfigKind, "main", playlistPayload)

	if _, err := st.PutFPPPlaylistDefinition(ctx, store.FPPPlaylistDefinitionRecord{
		InstanceUUID: instanceUUID, PlaylistHash: playlistHash, PlaylistName: "Main",
		DefinitionJSON: `{"mainPlaylist":[{"type":"sequence","sequenceName":"thriller.fseq"}]}`, CapturedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}

	putActiveShowForTest(t, st, showID)

	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		t.Fatalf("resolve cue catalog: %v", err)
	}
	if err := st.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
		NodeID: nodeID, Revision: catalog.Revision, ShowID: active.ShowID, Generation: active.Generation, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("ack node cue catalog: %v", err)
	}

	// A REAL signing authority, never fakeSigner: the whole point of this
	// test is verifying a real Ed25519 signature against the exact served
	// bytes.
	manager, err := signingkey.LoadOrGenerate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrGenerate signing key: %v", err)
	}

	result, err := fallbackcompile.Compile(ctx, st, manager, instanceUUID, testNow)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != fallbackcompile.OutcomePublished {
		t.Fatalf("Compile outcome = %q, want published; reason: %s", result.Outcome, result.Reason)
	}

	programJSON, err := json.Marshal(result.Program)
	if err != nil {
		t.Fatalf("marshal signed program: %v", err)
	}
	if err := st.PutFallbackProgram(ctx, store.FallbackProgramRecord{
		FPPInstanceUUID: instanceUUID, PackageID: result.Program.Program.PackageID, Revision: result.Program.Program.Revision,
		ShowID: result.Program.Program.Show, Generation: result.Program.Program.Generation,
		ProgramJSON: string(programJSON), SignatureB64: base64.StdEncoding.EncodeToString(result.Program.Signature),
		ExpiresAt: result.Program.Program.ExpiresAt, CompiledAt: result.Program.Program.CompiledAt,
	}); err != nil {
		t.Fatalf("PutFallbackProgram: %v", err)
	}

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/"+instanceUUID, auth)
	var served fallbackProgramResponseForTest
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("decode fallback program response: %v", err)
	}
	if !served.Published || served.Program == nil {
		t.Fatalf("GET fallback-programs/%s: Published = %v, Program = %s, want true and non-nil", instanceUUID, served.Published, served.Program)
	}

	var servedProgram fallbackprogram.Program
	if err := json.Unmarshal(served.Program, &servedProgram); err != nil {
		t.Fatalf("decode served program bytes: %v", err)
	}
	servedSig, err := base64.StdEncoding.DecodeString(served.SignatureBase64)
	if err != nil {
		t.Fatalf("decode served signatureBase64: %v", err)
	}
	servedSignedProgram := fallbackprogram.SignedProgram{Program: servedProgram, Signature: coordsig.Signature(servedSig)}

	// The actual proof: re-canonicalize the SERVED bytes (never the
	// in-memory result.Program this test already holds) and verify the
	// signature against them.
	if err := servedSignedProgram.Verify(manager.PublicKey()); err != nil {
		t.Fatalf("signature verification of the served program bytes failed: %v", err)
	}
	if servedProgram.Revision != result.Program.Program.Revision {
		t.Fatalf("served program Revision = %q, want %q", servedProgram.Revision, result.Program.Program.Revision)
	}
	if servedProgram.FPPInstanceUUID != instanceUUID {
		t.Fatalf("served program FPPInstanceUUID = %q, want %q", servedProgram.FPPInstanceUUID, instanceUUID)
	}
}
