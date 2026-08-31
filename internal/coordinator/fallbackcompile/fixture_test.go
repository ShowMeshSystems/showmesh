package fallbackcompile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/coordsig"
)

// fakeSigner is [Signer] with a caller-controlled outcome, so a test can
// prove the compiler turns a signing failure into [OutcomeUnsigned]
// rather than a bare error.
type fakeSigner struct {
	err error
}

func (f fakeSigner) Sign(payload []byte) (coordsig.Signature, error) {
	if f.err != nil {
		return nil, f.err
	}
	return coordsig.Signature([]byte("test-signature-not-real-ed25519")), nil
}

const testInstanceUUID = "M4-7840e12f81da4191c0d00fbb6a889314"

var testPlaylistHash = strings.Repeat("1", 64)

// testNow is the fixed compile time every test in this package uses, so
// ExpiresAt/CompiledAt are deterministic without each test threading its
// own clock value through.
func testNow() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func putConfig(t *testing.T, st *store.Store, kind, id, payload string) int64 {
	t.Helper()
	ctx := context.Background()
	obj, err := st.GetConfigObject(ctx, kind, id)
	nextRev := int64(1)
	if err == nil {
		nextRev = obj.CurrentRevision + 1
	} else if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Fatalf("get config object %s/%s: %v", kind, id, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: nextRev, PayloadJSON: payload, Source: "test",
	}); err != nil {
		t.Fatalf("create config revision %s/%s/%d: %v", kind, id, nextRev, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, nextRev); err != nil {
		t.Fatalf("activate config revision %s/%s/%d: %v", kind, id, nextRev, err)
	}
	return nextRev
}

func putShow(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	payload, err := config.EncodeShowPayload(config.ShowPayload{Name: name})
	if err != nil {
		t.Fatalf("encode show payload: %v", err)
	}
	putConfig(t, st, config.ShowConfigKind, id, payload)
}

func putActiveShow(t *testing.T, st *store.Store, showID string) int64 {
	t.Helper()
	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	return putConfig(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, payload)
}

func putSurface(t *testing.T, st *store.Store, surfaceID, showID, nodeID string) {
	t.Helper()
	payload, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: showID, Name: surfaceID, Node: nodeID,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 12},
		Geometry:     config.ShowSurfaceGeometry{Width: 2, Height: 2, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "test"}},
	})
	if err != nil {
		t.Fatalf("encode show.surface payload: %v", err)
	}
	putConfig(t, st, config.ShowSurfaceConfigKind, surfaceID, payload)
}

func declareNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: nodeID, Label: nodeID}); err != nil {
		t.Fatalf("declare node %q: %v", nodeID, err)
	}
}

func putAudioNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", LTCRoute: "usb-interface",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "single interface, both routes on it",
	})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	putConfig(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

func createAsset(t *testing.T, st *store.Store, showID, sequenceID, contentHash, filename string) {
	t.Helper()
	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-show-" + sequenceID, ShowID: showID, SequenceID: sequenceID,
		TargetKind: store.AssetTargetKindShow, TargetID: "", MediaType: "fseq", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	}); err != nil {
		t.Fatalf("create asset %q: %v", sequenceID, err)
	}
}

func putCue(t *testing.T, st *store.Store, cueID, showID string, payload config.ShowCuePayload) int64 {
	t.Helper()
	payload.Show = showID
	raw, err := config.EncodeShowCuePayload(payload)
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	return putConfig(t, st, config.ShowCueConfigKind, cueID, raw)
}

func putPlaylist(t *testing.T, st *store.Store, playlistID string, payload config.ShowPlaylistPayload) int64 {
	t.Helper()
	raw, err := config.EncodeShowPlaylistPayload(payload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	return putConfig(t, st, config.ShowPlaylistConfigKind, playlistID, raw)
}

// putDefinition stores a stored FPP playlist definition whose sole
// mainPlaylist entry sits at position 0, matching the fixture's single
// authored entry.
func putDefinition(t *testing.T, st *store.Store, instanceUUID, playlistHash, sequenceFilename string) {
	t.Helper()
	definitionJSON := `{"mainPlaylist":[{"type":"sequence","sequenceName":"` + sequenceFilename + `"}]}`
	if _, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: instanceUUID, PlaylistHash: playlistHash, PlaylistName: "Main",
		DefinitionJSON: definitionJSON, CapturedAt: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
}

// ackNodeCatalog resolves nodeID's current catalog and acknowledges it ,
// the fixture's stand-in for a real node's own TRACK-H-H3-SPEC.md
// section 4 acknowledgement.
func ackNodeCatalog(t *testing.T, st *store.Store, active assetsync.ActiveShow, nodeID string, now time.Time) assetsync.Catalog {
	t.Helper()
	catalog, err := assetsync.ResolveCueCatalog(context.Background(), st, active, nodeID)
	if err != nil {
		t.Fatalf("resolve cue catalog for %q: %v", nodeID, err)
	}
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: nodeID, Revision: catalog.Revision, ShowID: active.ShowID, Generation: active.Generation, AcknowledgedAt: now,
	}); err != nil {
		t.Fatalf("ack node cue catalog for %q: %v", nodeID, err)
	}
	return catalog
}

// baseFixture is one complete, minimal, publishable fixture: one show,
// one active-show pointer, one render node with one surface, one Cue
// ("thriller") with a render output and its asset uploaded, one
// fpp-runner playlist binding entry 0 of mainPlaylist to that Cue, and a
// stored playlist definition whose single entry matches. Every acceptance
// and refusal test starts here and mutates exactly one thing.
type baseFixture struct {
	st     *store.Store
	showID string
	nodeID string
	now    time.Time
}

func newBaseFixture(t *testing.T) baseFixture {
	t.Helper()
	st := openTestStore(t)
	now := testNow()
	showID := "halloween"
	nodeID := "render-01"

	putShow(t, st, showID, "Halloween")
	declareNode(t, st, nodeID)
	putSurface(t, st, "garage", showID, nodeID)
	createAsset(t, st, showID, "thriller", strings.Repeat("a", 64), "thriller.fseq")
	putCue(t, st, "thriller", showID, config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putDefinition(t, st, testInstanceUUID, testPlaylistHash, "thriller.fseq")
	putActiveShow(t, st, showID)

	active, err := assetsync.ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	ackNodeCatalog(t, st, active, nodeID, now)

	return baseFixture{st: st, showID: showID, nodeID: nodeID, now: now}
}

func (f baseFixture) resolveActive(t *testing.T) assetsync.ActiveShow {
	t.Helper()
	active, err := assetsync.ResolveActiveShow(context.Background(), f.st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	return active
}

func (f baseFixture) compile(t *testing.T) Result {
	t.Helper()
	result, err := Compile(context.Background(), f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return result
}

// publishedRecord converts a published Result into the
// [store.FallbackProgramRecord] a real publish path would persist, for a
// test that needs to exercise staleness detection against the store.
func publishedRecord(t *testing.T, result Result) store.FallbackProgramRecord {
	t.Helper()
	if result.Outcome != OutcomePublished || result.Program == nil {
		t.Fatalf("publishedRecord: result is not published: %+v", result)
	}
	raw, err := json.Marshal(result.Program)
	if err != nil {
		t.Fatalf("marshal signed program: %v", err)
	}
	p := result.Program.Program
	return store.FallbackProgramRecord{
		FPPInstanceUUID: p.FPPInstanceUUID, PackageID: p.PackageID, Revision: p.Revision,
		ShowID: p.Show, Generation: p.Generation, ProgramJSON: string(raw),
		SignatureB64: base64.StdEncoding.EncodeToString(result.Program.Signature), ExpiresAt: p.ExpiresAt, CompiledAt: p.CompiledAt,
	}
}

func (f baseFixture) requirePublished(t *testing.T, result Result) *store.Store {
	t.Helper()
	if result.Outcome != OutcomePublished {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomePublished, result.Reason)
	}
	if result.Program == nil {
		t.Fatalf("Compile published but Program is nil")
	}
	return f.st
}
