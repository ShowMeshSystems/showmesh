package fppreconcile

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

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

func putObservation(t *testing.T, st *store.Store, rec store.FPPPlaylistEntryObservationRecord) {
	t.Helper()
	if err := st.PutFPPPlaylistEntryObservation(context.Background(), rec); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
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
// is review fix item 49-5: a cue whose stored revision fails to decode is
// silently demoted to [ReadinessCueNotReady], the same closed-vocabulary
// answer as "the cue does not exist" — but unlike that case, an operator
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

	// Overwrite cue-1's own current revision with truncated JSON — the
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
