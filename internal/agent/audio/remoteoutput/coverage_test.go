package remoteoutput

import (
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func twoItemPlaylist() pkgaudio.PlaylistRef {
	return pkgaudio.PlaylistRef{
		OwnerKind:           "night_session",
		OwnerID:             "ns-1",
		Repeat:              pkgaudio.RepeatPlaylist,
		Resume:              pkgaudio.ResumePolicyResume,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-1", Index: 0, Media: pkgaudio.MediaRef{AssetID: "a1", ContentHash: "h1"}},
			{ItemID: "item-2", Index: 1, Media: pkgaudio.MediaRef{AssetID: "a2", ContentHash: "h2"}},
		},
	}
}

var requireManuallyVerified = map[ProvisioningState]bool{ProvisioningManuallyVerified: true}

// TestOneVerifiedItemInAMultiItemPlaylistStaysUnsatisfied is the case the
// C8 seam spec names explicitly: one manually verified playlist item
// plus one unverified item must not satisfy a required-output policy.
func TestOneVerifiedItemInAMultiItemPlaylistStaysUnsatisfied(t *testing.T) {
	s := NewEvidenceStore()
	dest := Destination{ID: "listener-1", ConfigRevision: "cfg-1"}
	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "h1", State: ProvisioningManuallyVerified, ObservedAt: time.Now()})
	// h2 has no evidence at all.

	hashes := RequiredContentHashes(playlistPtr(twoItemPlaylist()), nil)
	got := s.Coverage(dest, hashes, requireManuallyVerified)

	if got.Satisfied {
		t.Fatal("Coverage with one verified item and one unverified item: got Satisfied=true, want false")
	}
	if len(got.Missing) != 1 || got.Missing[0] != "h2" {
		t.Errorf("Missing: got %v, want [h2]", got.Missing)
	}
}

func TestCoverageSatisfiedWhenEveryHashQualifies(t *testing.T) {
	s := NewEvidenceStore()
	dest := Destination{ID: "listener-1", ConfigRevision: "cfg-1"}
	now := time.Now()
	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "h1", State: ProvisioningManuallyVerified, ObservedAt: now})
	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "h2", State: ProvisioningManuallyVerified, ObservedAt: now})

	got := s.Coverage(dest, RequiredContentHashes(playlistPtr(twoItemPlaylist()), nil), requireManuallyVerified)
	if !got.Satisfied {
		t.Errorf("Coverage with both items verified: got Satisfied=false, Missing=%v, want true", got.Missing)
	}
}

func TestCoverageAcceptableStateIsACallerPolicyChoice(t *testing.T) {
	s := NewEvidenceStore()
	dest := Destination{ID: "listener-1", ConfigRevision: "cfg-1"}
	s.Record(ProvisioningRecord{Destination: dest, ContentHash: "h1", State: ProvisioningAcknowledged, ObservedAt: time.Now()})

	// Acknowledged does not satisfy a policy that only accepts manual
	// verification (a destination with no status API).
	got := s.Coverage(dest, []string{"h1"}, requireManuallyVerified)
	if got.Satisfied {
		t.Error("Coverage requiring only ManuallyVerified against an Acknowledged record: got Satisfied=true, want false")
	}

	// The same evidence satisfies a policy that accepts Acknowledged.
	got = s.Coverage(dest, []string{"h1"}, map[ProvisioningState]bool{ProvisioningAcknowledged: true})
	if !got.Satisfied {
		t.Error("Coverage requiring Acknowledged against an Acknowledged record: got Satisfied=false, want true")
	}
}

func TestDecideOptionalNeverBlocks(t *testing.T) {
	unsatisfied := CoverageResult{Satisfied: false, Missing: []string{"h2"}}
	eval := Decide(PolicyOptional, unsatisfied)
	if eval.Blocking {
		t.Error("Decide(Optional, unsatisfied): got Blocking=true, want false — an optional remote output never blocks the local/FM path")
	}
	if eval.Reason == "" {
		t.Error("Decide(Optional, unsatisfied): got empty Reason, want a warning reason")
	}
}

func TestDecideRequiredBlocksOnUnsatisfiedCoverage(t *testing.T) {
	unsatisfied := CoverageResult{Satisfied: false, Missing: []string{"h2"}}
	eval := Decide(PolicyRequired, unsatisfied)
	if !eval.Blocking {
		t.Error("Decide(Required, unsatisfied): got Blocking=false, want true")
	}

	eval = Decide(PolicyRequired, CoverageResult{Satisfied: true})
	if eval.Blocking {
		t.Error("Decide(Required, satisfied): got Blocking=true, want false")
	}
}

func TestRequiredContentHashesDeduplicatesAndIncludesExtra(t *testing.T) {
	single := pkgaudio.MediaRef{AssetID: "announce", ContentHash: "h1"}
	got := RequiredContentHashes(nil, &single, pkgaudio.MediaRef{AssetID: "announce", ContentHash: "h1"}, pkgaudio.MediaRef{AssetID: "a3", ContentHash: "h3"})
	if len(got) != 2 {
		t.Errorf("RequiredContentHashes: got %v, want 2 distinct hashes (h1, h3)", got)
	}
}

func playlistPtr(p pkgaudio.PlaylistRef) *pkgaudio.PlaylistRef { return &p }
