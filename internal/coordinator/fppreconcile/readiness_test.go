package fppreconcile

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// putSurface stores a minimally valid show.surface object for showID/
// nodeID, HDMI-transport (nothing here exercises transport-specific
// behavior).
func putSurface(t *testing.T, st *store.Store, id, showID, nodeID string) {
	t.Helper()
	payload, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: showID, Name: id, Node: nodeID,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 3},
		Geometry:     config.ShowSurfaceGeometry{Width: 1, Height: 1, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportHDMI, HDMI: &config.ShowSurfaceHDMI{Display: "hdmi0"}},
	})
	if err != nil {
		t.Fatalf("encode show.surface payload: %v", err)
	}
	putConfig(t, st, config.ShowSurfaceConfigKind, id, payload)
}

// putSurfacePipelineState records surfaceID as currently assigned to and
// supervised by nodeID, mirroring what internal/coordinator/collector/
// noderender.Collector.Poll would have written from a real render report.
func putSurfacePipelineState(t *testing.T, st *store.Store, surfaceID, nodeID, state string, observedAt time.Time) {
	t.Helper()
	res := observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID}
	o, err := observation.Measured(res, noderender.SignalSurfacePipelineState, state, observedAt,
		observation.WithSource(noderender.SourceFor(nodeID)))
	if err != nil {
		t.Fatalf("build surface.pipeline.state observation: %v", err)
	}
	if err := st.UpsertObservation(context.Background(), o); err != nil {
		t.Fatalf("upsert surface.pipeline.state observation: %v", err)
	}
}

// putDefinitionWithEntries stores a definition whose mainPlaylist section
// holds exactly one entry at position 0, with the given filenames.
func putDefinitionWithEntries(t *testing.T, st *store.Store, instanceUUID, playlistHash, seqName, mediaName string) {
	t.Helper()
	def := `{"leadIn":[],"mainPlaylist":[{"type":"sequence","sequenceName":"` + seqName + `","mediaName":"` + mediaName + `"}],"leadOut":[]}`
	_, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: instanceUUID, PlaylistHash: playlistHash, PlaylistName: "def",
		DefinitionJSON: def, CapturedAt: time.Unix(1000, 0).UTC(), ReceivedAt: time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
}

// putDefinitionAt stores a definition, with a single mainPlaylist entry at
// position 0 (matching singleEntryPlaylist's own binding shape), under
// playlistHash, captured at capturedAt, so a test can control which of
// several definitions for the same instance/playlist name is "newer"
// while still satisfying the entry-position checks that run after
// definition-superseded.
func putDefinitionAt(t *testing.T, st *store.Store, instanceUUID, playlistHash, playlistName string, capturedAt time.Time) {
	t.Helper()
	_, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: instanceUUID, PlaylistHash: playlistHash, PlaylistName: playlistName,
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     capturedAt, ReceivedAt: capturedAt,
	})
	if err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
}

// putDefinitionAtTimes is [putDefinitionAt] with CapturedAt and ReceivedAt
// controlled independently, for tests that need to prove ordering is
// decided by ReceivedAt (the coordinator's own arrival order) rather than
// CapturedAt (the plugin's own, less trustworthy, wall-clock claim).
func putDefinitionAtTimes(t *testing.T, st *store.Store, instanceUUID, playlistHash, playlistName string, capturedAt, receivedAt time.Time) {
	t.Helper()
	_, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: instanceUUID, PlaylistHash: playlistHash, PlaylistName: playlistName,
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     capturedAt, ReceivedAt: receivedAt,
	})
	if err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
}

func putObservation(t *testing.T, st *store.Store, rec store.FPPPlaylistEntryObservationRecord) {
	t.Helper()
	if err := st.PutFPPPlaylistEntryObservation(context.Background(), rec); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}
}

// putNodeOnline records nodeID as currently online: a live last-will
// "online: true" plus a fresh health heartbeat, exactly what
// internal/coordinator/inventory.deriveLiveness requires to return
// [inventory.LivenessOnline] rather than offline/unknown. Both timestamps
// are stamped from the real wall clock (time.Now()) rather than a fixed
// value, since deriveLiveness's own staleness check is against the real
// clock too and this must read as fresh whenever the test actually runs.
func putNodeOnline(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	now := time.Now()
	if err := st.RecordLWT(context.Background(), nodeID, store.LWTRecord{
		Online: true, ObservedAt: &now, Provenance: store.ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("record lwt for %q: %v", nodeID, err)
	}
	if _, err := st.RecordHealth(context.Background(), nodeID, store.HealthRecord{
		BootID: "boot-1", Sequence: 1, AgentState: "running",
		ObservedAt: &now, Provenance: store.ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("record health for %q: %v", nodeID, err)
	}
}

// putNodeOffline records nodeID's last-will evidence as offline, with no
// health evidence to disagree with it — internal/coordinator/inventory.
// deriveLiveness's unconditional [inventory.LivenessOffline] case.
func putNodeOffline(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	now := time.Now()
	if err := st.RecordLWT(context.Background(), nodeID, store.LWTRecord{
		Online: false, Reason: "clean shutdown", ObservedAt: &now, Provenance: store.ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("record lwt for %q: %v", nodeID, err)
	}
}

func TestPlaylistReadinessDefinitionMissing(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	// Deliberately no definition stored.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: no definition is stored")
	}
	if report.FailingCondition != ReadinessDefinitionMissing {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessDefinitionMissing)
	}
}

func TestPlaylistReadinessEntryNotInDefinition(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	// The entry is bound at position 5, but the definition only has one
	// entry, at position 0.
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 5, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: entry position 5 does not exist in the stored definition")
	}
	if report.FailingCondition != ReadinessEntryNotInDefinition {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessEntryNotInDefinition)
	}
}

func TestPlaylistReadinessEntryFilenameMismatch(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "SomethingElse.fseq", "")

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: expected sequence filename disagrees with the definition")
	}
	if report.FailingCondition != ReadinessEntryFilenameMismatch {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessEntryFilenameMismatch)
	}
}

func TestPlaylistReadinessCueNotReady(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	// Build the playlist payload by hand, naming a Cue that is never
	// written to the store — singleEntryPlaylist always writes its Cue,
	// so this test cannot reuse it.
	p := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "does-not-exist",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: the referenced cue does not exist")
	}
	if report.FailingCondition != ReadinessCueNotReady {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessCueNotReady)
	}
}

// TestPlaylistReadinessCorruptedCueRevisionLogsWarnAndStillReportsCueNotReady
// asserts that a cue whose stored revision fails to decode is
// silently demoted to [ReadinessCueNotReady], the same closed-vocabulary
// answer as "the cue does not exist"; unlike that case, though, an operator
// looking only at the readiness reason has no way to learn a decode
// failure, not a missing cue, was the actual cause. The returned readiness
// value must not change (still ReadinessCueNotReady, still not Ready); a
// warn-level log naming the cue id and the error is what closes the gap.
func TestPlaylistReadinessCorruptedCueRevisionLogsWarnAndStillReportsCueNotReady(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")

	// Overwrite cue-1's own current revision with truncated JSON, the
	// same "an interrupted write could really leave this behind" shape
	// reconcile_test.go's corrupted-payload test uses, written directly
	// through CreateConfigRevision rather than the encoder (which would
	// refuse to produce it).
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: "cue-1", Revision: 2,
		PayloadJSON: `{"show":"show-1","name":"Thriller","outputs":{`, Source: "api",
	}); err != nil {
		t.Fatalf("create corrupted cue revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, "cue-1", 2); err != nil {
		t.Fatalf("activate corrupted cue revision: %v", err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	report, err := PlaylistReadiness(ctx, st, logger, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: the referenced cue's current revision does not decode")
	}
	if report.FailingCondition != ReadinessCueNotReady {
		t.Fatalf("FailingCondition = %q, want %q (readiness value must not change)", report.FailingCondition, ReadinessCueNotReady)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("log output = %q, want a WARN-level entry", logged)
	}
	if !strings.Contains(logged, "cue-1") {
		t.Errorf("log output = %q, want it to name cue id %q", logged, "cue-1")
	}
}

func TestPlaylistReadinessObservationHashMismatchIsFailureWhenObservationExists(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")

	obs := baseObservation("inst-1")
	obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash64("different"), "mainPlaylist", 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	putObservation(t, st, obs)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: the latest observation's playlistHash disagrees with the binding's")
	}
	if report.FailingCondition != ReadinessObservationHashMismatch {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessObservationHashMismatch)
	}
	if report.Warning != "" {
		t.Fatalf("Warning = %q, want empty: this is the hard-failure form, not the warning form", report.Warning)
	}
}

func TestPlaylistReadinessObservationHashMismatchIsWarningWhenNoObservationReceived(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	// Deliberately no observation stored: the normal afternoon state.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true: no observation received is a warning, not a failure (got %q: %s)", report.FailingCondition, report.Reason)
	}
	if report.Warning == "" {
		t.Fatal("Warning is empty, want a non-empty warning explaining that no observation has been received yet")
	}
}

// TestPlaylistReadinessDetectsEditedPlaylistWhileFPPIdle reproduces the
// operator-visible defect this test file's own new conditions close: an
// operator edits the FPP playlist (the plugin re-scans
// and posts a NEW definition under a new hash, same instance and playlist
// name) and never plays anything afterward. No observation exists at all
// — "the normal afternoon state" — yet the coordinator already holds
// evidence the bound hash is stale, because the definition store is
// evidence independent of playback.
func TestPlaylistReadinessDetectsEditedPlaylistWhileFPPIdle(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	oldHash := hash64("old")
	newHash := hash64("new")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", oldHash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionAt(t, st, "inst-1", oldHash, "Main", time.Unix(1000, 0).UTC())
	putDefinitionAt(t, st, "inst-1", newHash, "Main", time.Unix(2000, 0).UTC())
	// Deliberately no observation stored: FPP has not been played at all
	// since the edit. This is the exact case the issue names: "without
	// anything having to be played first."

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false: a newer definition (%s) is stored for this instance/playlist name than the bound hash (%s), and FPP was never played", newHash, oldHash)
	}
	if report.FailingCondition != ReadinessDefinitionSuperseded {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessDefinitionSuperseded)
	}
}

// TestPlaylistReadinessDefinitionSupersededIgnoresOlderOrOtherPlaylistName
// guards the two ways a naive "any other hash exists" check would
// over-fire: an older definition under the same playlist name (recorded
// before the currently bound one — not an edit, just history) and a
// definition under a different playlist name entirely (a different FPP
// playlist, irrelevant to this binding) must never trip
// definition-superseded.
func TestPlaylistReadinessDefinitionSupersededIgnoresOlderOrOtherPlaylistName(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	boundHash := hash64("bound")
	olderHash := hash64("older")
	otherNameHash := hash64("othername")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", boundHash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionAt(t, st, "inst-1", olderHash, "Main", time.Unix(500, 0).UTC())
	putDefinitionAt(t, st, "inst-1", boundHash, "Main", time.Unix(1000, 0).UTC())
	putDefinitionAt(t, st, "inst-1", otherNameHash, "SomeOtherPlaylist", time.Unix(9000, 0).UTC())

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s); an older same-name definition and a different-name definition must not trip definition-superseded", report.FailingCondition, report.Reason)
	}
}

// TestPlaylistReadinessDefinitionSupersededMatchesOnTheDefinitionsOwnName
// reproduces the defect where definition-superseded silently never fires
// when the binding's own p.FPP.PlaylistName has drifted from the bound
// definition's own stored name: matching candidate rows against the
// binding's copy of the name, instead of against defRec.PlaylistName,
// excludes every genuinely-newer row before it can ever be compared.
func TestPlaylistReadinessDefinitionSupersededMatchesOnTheDefinitionsOwnName(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	boundHash := hash64("bound")
	newHash := hash64("new")
	// The binding's own copy of the playlist name ("Stale Name") disagrees
	// with what the bound definition is actually stored under ("Main") —
	// e.g. an import that predates a rename on the FPP side.
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Stale Name", boundHash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionAt(t, st, "inst-1", boundHash, "Main", time.Unix(1000, 0).UTC())
	putDefinitionAt(t, st, "inst-1", newHash, "Main", time.Unix(2000, 0).UTC())

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: a newer definition is stored under the bound definition's own name, even though the binding's own playlistName field disagrees with it")
	}
	if report.FailingCondition != ReadinessDefinitionSuperseded {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessDefinitionSuperseded)
	}
}

// TestPlaylistReadinessDefinitionSupersededComparesReceivedAtNotCapturedAt
// reproduces the defect where a genuinely newer definition is skipped
// because its plugin-supplied CapturedAt is equal to (here: a same-tick
// collision) the bound definition's own CapturedAt. ReceivedAt — the
// coordinator's own, trustworthy arrival order — says the new row arrived
// after the bound one, so it must still be reported as superseding.
func TestPlaylistReadinessDefinitionSupersededComparesReceivedAtNotCapturedAt(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	boundHash := hash64("bound")
	newHash := hash64("new")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", boundHash, "cue-1", "mainPlaylist", 0, "", "")
	sameTick := time.Unix(1000, 0).UTC()
	putDefinitionAtTimes(t, st, "inst-1", boundHash, "Main", sameTick, sameTick)
	// newHash's CapturedAt ties the bound definition's own (a coarse
	// plugin timestamp collision); only ReceivedAt shows it arrived later.
	putDefinitionAtTimes(t, st, "inst-1", newHash, "Main", sameTick, sameTick.Add(time.Second))

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: a definition received after the bound one, under the same playlist name and a different hash, must not be ignored just because its CapturedAt ties the bound one's")
	}
	if report.FailingCondition != ReadinessDefinitionSuperseded {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessDefinitionSuperseded)
	}
}

// TestPlaylistReadinessObservationUnavailableIsFailureNotWarning is the
// issue's own reported output, reproduced directly: FPP played the edited
// playlist and its last callback (Playlist::GetInfo() gone idle) lost
// identity, so the latest observation exists but carries no hash to
// compare. Readiness must never say "ready" for a check it could not
// evaluate — this is the "I could not check" case, distinct from "I
// checked and it is fine".
func TestPlaylistReadinessObservationUnavailableIsFailureNotWarning(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	// No newer definition posted: this test isolates the observation-side
	// evidence-unavailable case from definition-superseded.

	obs := baseObservation("inst-1")
	obs.Unavailable = "missing_playlist_name"
	putObservation(t, st, obs)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false: the latest observation could not establish identity, so this check could not run; got warning %q", report.Warning)
	}
	if report.FailingCondition != ReadinessEvidenceUnavailable {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessEvidenceUnavailable)
	}
}

func TestPlaylistReadinessAllConditionsPass(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")

	obs := baseObservation("inst-1")
	obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash, "mainPlaylist", 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	putObservation(t, st, obs)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s)", report.FailingCondition, report.Reason)
	}
	if report.Warning != "" {
		t.Fatalf("Warning = %q, want empty: an observation matching the binding's hash was received", report.Warning)
	}
}

// TestPlaylistReadinessNodeRenderUnassignedDefect reproduces the defect
// against unmodified code: cue-1 (via putCue) declares outputs.render,
// which config.DeriveShowCueClaims expands to every show.surface
// belonging to the show, so wall-1 is a surface this show's cues target.
// node-1 has never applied a render assignment for it (no
// surface.pipeline.state evidence at all — the fresh-reboot case ADR-043
// H0.7 produces), yet nothing here fails readiness for that.
func TestPlaylistReadinessNodeRenderUnassignedDefect(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	// Deliberately no surface.pipeline.state observation for node-1/wall-1.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("BUG: readiness reports ready even though node-1 holds no render assignment for wall-1, the surface this show's cues target")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
	if !strings.Contains(report.Reason, "node-1") || !strings.Contains(report.Reason, "wall-1") {
		t.Fatalf("Reason = %q, want it to name both node-1 and wall-1", report.Reason)
	}
}

// TestPlaylistReadinessNodeRenderAssignedPasses is the same shape as the
// defect test above, except node-1 has reported wall-1 in its own render
// surface set (a real render.surface.apply confirmation would produce this
// evidence): readiness must pass.
func TestPlaylistReadinessNodeRenderAssignedPasses(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	putSurfacePipelineState(t, st, "wall-1", "node-1", "running", time.Unix(1000, 0).UTC())

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s)", report.FailingCondition, report.Reason)
	}
}

// TestPlaylistReadinessNodeRenderDroppedFails covers the "reported once,
// then stopped reporting" half of nodeHoldsRenderAssignment: an explicit
// dropped-surface absence observation (Absence != "") must fail readiness
// exactly like never having reported at all.
func TestPlaylistReadinessNodeRenderDroppedFails(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")

	res := observation.ResourceRef{Kind: observation.ResourceSurface, ID: "wall-1"}
	o, err := observation.NotCollected(res, noderender.SignalSurfacePipelineState,
		"node node-1 no longer reports this surface", observation.WithSource(noderender.SourceFor("node-1")))
	if err != nil {
		t.Fatalf("build absence observation: %v", err)
	}
	if err := st.UpsertObservation(context.Background(), o); err != nil {
		t.Fatalf("upsert absence observation: %v", err)
	}

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: node-1 explicitly stopped reporting wall-1")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
}

// TestPlaylistReadinessNodeRenderWrongNodeSourceFails asserts that an
// observation for the right surface but the WRONG node's source never
// substitutes for the assigned node's own evidence (renderdispatch.go's
// evaluateRenderSurfaceState precedent).
func TestPlaylistReadinessNodeRenderWrongNodeSourceFails(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	// Evidence exists for wall-1, but from node-2, not the surface's own
	// configured node-1 (e.g. stale evidence from a since-reassigned node).
	putSurfacePipelineState(t, st, "wall-1", "node-2", "running", time.Unix(1000, 0).UTC())

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: the only evidence for wall-1 is from node-2, not node-1")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
}

// TestPlaylistReadinessNodeRenderOtherShowSurfaceIgnored asserts that a
// show.surface belonging to a DIFFERENT show never gates this Playlist's
// own readiness, even though it shares the same unassigned node.
func TestPlaylistReadinessNodeRenderOtherShowSurfaceIgnored(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putShow(t, st, "show-2", "Show Two")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-2", "show-2", "node-1")
	// Deliberately no surface.pipeline.state observation for wall-2: it
	// must not matter, since wall-2 belongs to show-2, not this
	// Playlist's show-1.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s): wall-2 belongs to a different show", report.FailingCondition, report.Reason)
	}
}

// TestPlaylistReadinessNoRenderOutputSkipsNodeRenderCheck asserts that a
// Cue declaring no render output at all never triggers condition 6, even
// when a show.surface with no assignment exists for the same show — the
// obligation is the Cue's outputs.render declaration, not mere surface
// existence.
func TestPlaylistReadinessNoRenderOutputSkipsNodeRenderCheck(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "cue-no-render",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	noRenderPayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "show-1", Name: "cue-no-render",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-1"}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, "cue-no-render", noRenderPayload)
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	// Deliberately no surface.pipeline.state observation for wall-1: this
	// must not matter, because no Cue here declares outputs.render.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s): no cue declares outputs.render", report.FailingCondition, report.Reason)
	}
}

// TestPlaylistReadinessNodeRenderUnassignedReasonWhenNodeNeverReported
// covers this condition's central rework target: nodeHoldsRenderAssignment
// previously inferred "node-1 holds no render assignment" from the mere
// ABSENCE of
// surface.pipeline.state evidence, which is exactly as true when a node is
// simply powered off, unreachable, or silent since a coordinator restart.
// Asserting an unqualified "holds no render assignment" in that case
// repeats the issue's own root defect one layer up: an operator would hunt
// for a missing render-surface assignment on a node that is actually down.
// node-1 here has no render-assignment evidence AND no inventory evidence
// of its own (no hello, no last-will, no health ever recorded) — the
// never-reported case — so the Reason must name that, not the assignment.
func TestPlaylistReadinessNodeRenderUnassignedReasonWhenNodeNeverReported(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	// Deliberately no surface.pipeline.state observation, and no hello/
	// LWT/health evidence of any kind for node-1.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: no render-assignment evidence exists for wall-1")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
	if strings.Contains(report.Reason, "holds no render assignment") {
		t.Fatalf("Reason = %q, must not assert the node holds no assignment when the node itself has never been observed reporting anything", report.Reason)
	}
	if !strings.Contains(report.Reason, "not currently reporting") {
		t.Fatalf("Reason = %q, want it to say the node is not currently reporting", report.Reason)
	}
	if !strings.Contains(report.Reason, "node-1") || !strings.Contains(report.Reason, "wall-1") {
		t.Fatalf("Reason = %q, want it to name both node-1 and wall-1", report.Reason)
	}
}

// TestPlaylistReadinessNodeRenderUnassignedReasonWhenNodeOnline is this
// same condition's other half: node-1 IS currently reporting (a fresh
// last-will "online" plus a fresh health heartbeat), it just genuinely has
// no render assignment for wall-1. The Reason here must make the opposite
// claim from the never-reported case above — the node is there, and it
// really does hold nothing — since that is the one case where "go fix the
// node's assignment" is the correct operator action.
func TestPlaylistReadinessNodeRenderUnassignedReasonWhenNodeOnline(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	putNodeOnline(t, st, "node-1")
	// Deliberately no surface.pipeline.state observation for node-1/wall-1.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: node-1 is online but reports no assignment for wall-1")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
	if !strings.Contains(report.Reason, "is reporting and holds no render assignment") {
		t.Fatalf("Reason = %q, want it to confirm the node is reporting and genuinely holds no assignment", report.Reason)
	}
}

// TestPlaylistReadinessNodeRenderUnassignedReasonWhenNodeOffline asserts
// the offline half of the liveness distinction (as opposed to
// never-having-reported, covered above): node-1's own last-will evidence
// says offline, with nothing else here to disagree. The Reason must still
// name "not currently reporting" rather than assert an unassignment.
func TestPlaylistReadinessNodeRenderUnassignedReasonWhenNodeOffline(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	putNodeOffline(t, st, "node-1")

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
	if strings.Contains(report.Reason, "holds no render assignment") {
		t.Fatalf("Reason = %q, must not assert the node holds no assignment when its own last-will evidence reports it offline", report.Reason)
	}
	if !strings.Contains(report.Reason, "not currently reporting") {
		t.Fatalf("Reason = %q, want it to say the node is not currently reporting", report.Reason)
	}
}

// TestPlaylistReadinessNodeRenderStaleAssignmentFails covers this
// condition's third sub-case: node-1 IS online, and DID report holding
// wall-1 at some point, but that specific surface.pipeline.state evidence
// has aged past its own ValidFor window (noderender.DefaultValidFor). This
// package's own decision (see nodeHoldsRenderAssignment's doc comment) is
// to treat an aged-out reading as unassigned rather than confirmed —
// mirroring this same file's freshness discipline for its other
// conditions, never trusting evidence that stopped being current — and
// the Reason must name it as stale rather than as a settled
// unassignment or a "not reporting" claim (health evidence here is fresh;
// only the render-specific signal itself is old).
func TestPlaylistReadinessNodeRenderStaleAssignmentFails(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putSurface(t, st, "wall-1", "show-1", "node-1")
	putNodeOnline(t, st, "node-1")

	res := observation.ResourceRef{Kind: observation.ResourceSurface, ID: "wall-1"}
	o, err := observation.Measured(res, noderender.SignalSurfacePipelineState, "running", time.Unix(1000, 0).UTC(),
		observation.WithSource(noderender.SourceFor("node-1")), observation.WithValidFor(noderender.DefaultValidFor))
	if err != nil {
		t.Fatalf("build stale surface.pipeline.state observation: %v", err)
	}
	if err := st.UpsertObservation(context.Background(), o); err != nil {
		t.Fatalf("upsert stale surface.pipeline.state observation: %v", err)
	}

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: the only render-assignment evidence for wall-1 has aged past its ValidFor window")
	}
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
	if !strings.Contains(report.Reason, "stale") {
		t.Fatalf("Reason = %q, want it to name the evidence as stale rather than assert an unqualified unassignment", report.Reason)
	}
}

// TestPlaylistReadinessEveryConditionIsInvokedInOrder is a direct,
// assertable answer to "does PlaylistReadiness's call site still invoke
// every one of the eight ReadinessCondition values, in the documented
// order" — the exact question a merge that folds two branches' additions
// to this same call site raises: a conflict resolution that silently took
// one side's invocations and dropped the other's would still compile and
// would still pass every other, single-condition test in this file
// unchanged, because a condition that is never invoked simply never
// fires.
//
// Each subtest builds a store where its own condition, AND every
// condition ordered after it, are simultaneously true, then asserts only
// its own condition is reported. That proves both that the condition
// under test is actually reached (nothing ordered before it swallowed the
// call), and that nothing ordered after it preempts it (the order these
// comments claim is the order actually enforced).
func TestPlaylistReadinessEveryConditionIsInvokedInOrder(t *testing.T) {
	t.Run("definition-missing", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
		// Nothing at all is stored for (inst-1, hash): every later condition
		// is unreachable from here, which is exactly the point.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessDefinitionMissing {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessDefinitionMissing)
		}
	})

	t.Run("definition-superseded", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		// entry.Cue is repointed at a Cue that was never stored: cue-not-ready
		// (ordered after this one) would also fire if reached.
		p.Entries[0].Cue = "cue-missing"
		// The bound definition's own entry has no sequenceName, so it
		// mismatches the binding's ExpectedSequenceFilename: entry-filename-
		// mismatch (also ordered after this one) would also fire if reached.
		putDefinitionAt(t, st, "inst-1", hash, "Main", time.Unix(1000, 0).UTC())
		putDefinitionAt(t, st, "inst-1", hash64("newer"), "Main", time.Unix(2000, 0).UTC())
		// No observation and no show.surface at all: left broken too, though
		// an absent observation is only ever a warning, never a hard failure.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessDefinitionSuperseded {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessDefinitionSuperseded)
		}
	})

	t.Run("entry-not-in-definition", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		p.Entries[0].Cue = "cue-missing"
		_, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
			InstanceUUID: "inst-1", PlaylistHash: hash, PlaylistName: "Main",
			DefinitionJSON: `{"leadIn":[],"mainPlaylist":[],"leadOut":[]}`,
			CapturedAt:     time.Unix(1000, 0).UTC(), ReceivedAt: time.Unix(1000, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("put fpp playlist definition: %v", err)
		}
		// No newer definition is stored: definition-superseded (ordered
		// before this one) does not fire, isolating this condition.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessEntryNotInDefinition {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessEntryNotInDefinition)
		}
	})

	t.Run("entry-filename-mismatch", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		p.Entries[0].Cue = "cue-missing"
		putDefinitionWithEntries(t, st, "inst-1", hash, "WrongName.fseq", "")

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessEntryFilenameMismatch {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessEntryFilenameMismatch)
		}
	})

	t.Run("cue-not-ready", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		p.Entries[0].Cue = "cue-missing"
		putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")
		obs := baseObservation("inst-1")
		obs.Unavailable = "missing_playlist_name"
		putObservation(t, st, obs)
		// evidence-unavailable (ordered after this one) would also fire if
		// reached.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessCueNotReady {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessCueNotReady)
		}
	})

	t.Run("evidence-unavailable", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")
		obs := baseObservation("inst-1")
		obs.Unavailable = "missing_playlist_name"
		putObservation(t, st, obs)
		putSurface(t, st, "wall-1", "show-1", "node-1")
		// No render-assignment evidence for wall-1: node-render-unassigned
		// (ordered after this one) would also fire if reached.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessEvidenceUnavailable {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessEvidenceUnavailable)
		}
	})

	t.Run("observation-hash-mismatch", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")
		obs := baseObservation("inst-1")
		obs.PlaylistHash = hash64("different")
		putObservation(t, st, obs)
		putSurface(t, st, "wall-1", "show-1", "node-1")
		// No render-assignment evidence for wall-1: node-render-unassigned
		// (ordered after this one) would also fire if reached.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessObservationHashMismatch {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessObservationHashMismatch)
		}
	})

	t.Run("node-render-unassigned", func(t *testing.T) {
		st := openTestStore(t)
		putShow(t, st, "show-1", "Show One")
		hash := hash64("h1")
		p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
		putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")
		obs := baseObservation("inst-1")
		obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash, "mainPlaylist", 0
		obs.EntryKey = entryKeyFor(t, p, "entry-1")
		putObservation(t, st, obs)
		putSurface(t, st, "wall-1", "show-1", "node-1")
		// Deliberately no surface.pipeline.state evidence for node-1/wall-1.
		// This is the last ordered condition, so there is nothing left to
		// prove it isn't preempted by; TestPlaylistReadinessNodeRenderUnassignedDefect
		// already covers this same shape on its own.

		report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		if report.FailingCondition != ReadinessNodeRenderUnassigned {
			t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeRenderUnassigned)
		}
	})
}
