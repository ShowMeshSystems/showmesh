package fallbackreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/coordsig"
)

type fakeSigner struct{}

func (fakeSigner) Sign(payload []byte) (coordsig.Signature, error) {
	return coordsig.Signature([]byte("test-signature-not-real-ed25519")), nil
}

type recordingAuditWriter struct {
	entries []identity.AuditEntry
}

func (r *recordingAuditWriter) WriteAudit(_ context.Context, entry identity.AuditEntry) error {
	r.entries = append(r.entries, entry)
	return nil
}

const testInstanceUUID = "M4-7840e12f81da4191c0d00fbb6a889314"

var testPlaylistHash = strings.Repeat("1", 64)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func putConfig(t *testing.T, st *store.Store, kind, id, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: 1, PayloadJSON: payload, Source: "test",
	}); err != nil {
		t.Fatalf("create config revision %s/%s: %v", kind, id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, 1); err != nil {
		t.Fatalf("activate config revision %s/%s: %v", kind, id, err)
	}
}

// newPublishableFixture builds one complete, compilable fixture, a
// scaled-down copy of internal/coordinator/fallbackcompile's own
// baseFixture, kept local because that helper is unexported test code in
// another package.
func newPublishableFixture(t *testing.T) (*store.Store, time.Time) {
	t.Helper()
	st := openTestStore(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	showID, nodeID := "halloween", "render-01"

	showPayload, err := config.EncodeShowPayload(config.ShowPayload{Name: "Halloween"})
	if err != nil {
		t.Fatalf("encode show: %v", err)
	}
	putConfig(t, st, config.ShowConfigKind, showID, showPayload)

	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: nodeID, Label: nodeID}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

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
	putConfig(t, st, config.ShowSurfaceConfigKind, "garage", surfacePayload)

	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
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
	putConfig(t, st, config.ShowCueConfigKind, "thriller", cuePayload)

	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	if err != nil {
		t.Fatalf("encode playlist: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, "main", playlistPayload)

	if _, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: testInstanceUUID, PlaylistHash: testPlaylistHash, PlaylistName: "Main",
		DefinitionJSON: `{"mainPlaylist":[{"type":"sequence","sequenceName":"thriller.fseq"}]}`, CapturedAt: now,
	}); err != nil {
		t.Fatalf("put definition: %v", err)
	}

	activePayload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode active show: %v", err)
	}
	putConfig(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, activePayload)

	active, err := assetsync.ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	catalog, err := assetsync.ResolveCueCatalog(context.Background(), st, active, nodeID)
	if err != nil {
		t.Fatalf("resolve cue catalog: %v", err)
	}
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: nodeID, Revision: catalog.Revision, ShowID: active.ShowID, Generation: active.Generation, AcknowledgedAt: now,
	}); err != nil {
		t.Fatalf("ack catalog: %v", err)
	}

	return st, now
}

func TestServiceReconcileOncePublishes(t *testing.T) {
	st, now := newPublishableFixture(t)
	audit := &recordingAuditWriter{}
	svc := NewService(st, fakeSigner{}, audit, nil, time.Hour)
	svc.now = func() time.Time { return now }

	svc.reconcileOnce(context.Background())

	rec, err := st.GetFallbackProgram(context.Background(), testInstanceUUID)
	if err != nil {
		t.Fatalf("GetFallbackProgram: %v", err)
	}
	if rec.Revision == "" {
		t.Fatalf("stored fallback program has no revision")
	}

	found := false
	for _, e := range audit.entries {
		if e.Action == auditActionPublish && e.Target == testInstanceUUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q audit entry recorded for %q; entries: %+v", auditActionPublish, testInstanceUUID, audit.entries)
	}
}

func TestServiceReconcileOnceIsANoOpAgainstUnchangedInputs(t *testing.T) {
	st, now := newPublishableFixture(t)
	audit := &recordingAuditWriter{}
	svc := NewService(st, fakeSigner{}, audit, nil, time.Hour)
	svc.now = func() time.Time { return now }

	svc.reconcileOnce(context.Background())
	first, err := st.GetFallbackProgram(context.Background(), testInstanceUUID)
	if err != nil {
		t.Fatalf("GetFallbackProgram: %v", err)
	}
	firstPackageID := first.PackageID

	svc.reconcileOnce(context.Background())
	second, err := st.GetFallbackProgram(context.Background(), testInstanceUUID)
	if err != nil {
		t.Fatalf("GetFallbackProgram: %v", err)
	}
	if second.PackageID != firstPackageID {
		t.Fatalf("a second reconcile against unchanged inputs republished a new package (%q -> %q)", firstPackageID, second.PackageID)
	}

	publishCount := 0
	for _, e := range audit.entries {
		if e.Action == auditActionPublish {
			publishCount++
		}
	}
	if publishCount != 1 {
		t.Fatalf("publish audit entries = %d, want exactly 1 (the second reconcile must be a no-op)", publishCount)
	}
}

func TestServiceRefusalDoesNotClearPreviouslyPublishedProgram(t *testing.T) {
	st, now := newPublishableFixture(t)
	audit := &recordingAuditWriter{}
	svc := NewService(st, fakeSigner{}, audit, nil, time.Hour)
	svc.now = func() time.Time { return now }

	svc.reconcileOnce(context.Background())
	published, err := st.GetFallbackProgram(context.Background(), testInstanceUUID)
	if err != nil {
		t.Fatalf("GetFallbackProgram: %v", err)
	}

	// Break the node's acknowledgement so the next compile refuses.
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "render-01", Revision: "stale", ShowID: "halloween", Generation: 1, AcknowledgedAt: now,
	}); err != nil {
		t.Fatalf("break ack: %v", err)
	}

	svc.reconcileOnce(context.Background())

	stillPublished, err := st.GetFallbackProgram(context.Background(), testInstanceUUID)
	if err != nil {
		t.Fatalf("GetFallbackProgram after refusal: %v", err)
	}
	if stillPublished.Revision != published.Revision || stillPublished.PackageID != published.PackageID {
		t.Fatalf("a refused reconcile must never touch the previously published program: before %+v, after %+v", published, stillPublished)
	}

	found := false
	for _, e := range audit.entries {
		if e.Action == auditActionRefuse && e.Target == testInstanceUUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q audit entry recorded; entries: %+v", auditActionRefuse, audit.entries)
	}
}
