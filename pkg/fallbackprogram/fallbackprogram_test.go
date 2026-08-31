package fallbackprogram

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func sampleRevisionInput() RevisionInput {
	return RevisionInput{
		FPPInstanceUUID: "M4-instance",
		Show:            "halloween",
		Generation:      3,
		PlaylistRevisions: map[string]int64{
			"main-playlist": 2,
		},
		CatalogRevisions: map[string]string{
			"node-a": "catalog-rev-a",
		},
		Entries: []EntryMapping{
			{
				EntryKey:    "entry-key-1",
				CueID:       "cue-1",
				CueRevision: 1,
				Targets: []NodeTarget{
					{
						NodeID: "node-a",
						Render: &RenderActivation{
							Sequence:    "thriller",
							Filename:    "thriller.fseq",
							AssetHashes: []string{"aaaa"},
						},
					},
				},
			},
		},
	}
}

func TestComputeRevisionIsDeterministic(t *testing.T) {
	in := sampleRevisionInput()
	r1, err := ComputeRevision(in)
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}
	r2, err := ComputeRevision(sampleRevisionInput())
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("ComputeRevision is not deterministic: %q != %q", r1, r2)
	}
}

// TestComputeRevisionChangesWithContent proves EACH field of RevisionInput
// individually contributes to the hash, one subtest per field, on
// MGR-J's own instruction that a single case could pass by accident: a
// mutation that stopped any ONE field (Entries in particular: retagging
// RevisionInput.Entries as `json:"-"` is the specific mutation this
// table was rewritten to catch) from reaching the hash would leave every
// OTHER subtest green and only that field's own subtest red.
func TestComputeRevisionChangesWithContent(t *testing.T) {
	base, err := ComputeRevision(sampleRevisionInput())
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*RevisionInput)
	}{
		{"FPPInstanceUUID", func(in *RevisionInput) { in.FPPInstanceUUID = "M4-other" }},
		{"Show", func(in *RevisionInput) { in.Show = "christmas" }},
		{"Generation", func(in *RevisionInput) { in.Generation = 4 }},
		{"PlaylistRevisions", func(in *RevisionInput) { in.PlaylistRevisions["main-playlist"] = 3 }},
		{"CatalogRevisions", func(in *RevisionInput) { in.CatalogRevisions["node-a"] = "catalog-rev-b" }},
		{"Entries", func(in *RevisionInput) { in.Entries[0].EntryKey = "entry-key-2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := sampleRevisionInput()
			tc.mutate(&changed)
			other, err := ComputeRevision(changed)
			if err != nil {
				t.Fatalf("ComputeRevision: %v", err)
			}
			if base == other {
				t.Fatalf("ComputeRevision did not change when %s alone changed", tc.name)
			}
		})
	}
}

// TestComputeRevisionExcludesPublishMetadata proves Revision depends
// only on RevisionInput's own content, never on a Program's separate
// PackageID/ExpiresAt/CompiledAt fields (publish metadata, not content:
// see RevisionInput's own doc comment). It calls ComputeRevision TWICE,
// independently, rather than reusing one call's result for both Program
// values under comparison: a version that reused a single call (or that
// replaced ComputeRevision with a function returning a hard-coded
// constant) would still pass a same-value comparison built from a copy,
// which is exactly the gap this rewrite closes.
func TestComputeRevisionExcludesPublishMetadata(t *testing.T) {
	in := sampleRevisionInput()
	r1, err := ComputeRevision(in)
	if err != nil {
		t.Fatalf("ComputeRevision (first call): %v", err)
	}
	r2, err := ComputeRevision(sampleRevisionInput())
	if err != nil {
		t.Fatalf("ComputeRevision (second, independent call): %v", err)
	}

	p1 := Program{
		SchemaVersion: SchemaVersion, PackageID: "pkg-1", Revision: r1,
		ExpiresAt: time.Unix(1000, 0), CompiledAt: time.Unix(500, 0),
		FPPInstanceUUID: in.FPPInstanceUUID, Show: in.Show, Generation: in.Generation,
		PlaylistRevisions: in.PlaylistRevisions, CatalogRevisions: in.CatalogRevisions,
		Entries: in.Entries, Rules: FixedRules,
	}
	p2 := Program{
		SchemaVersion: SchemaVersion, PackageID: "pkg-2", Revision: r2,
		ExpiresAt: time.Unix(2000, 0), CompiledAt: time.Unix(1500, 0),
		FPPInstanceUUID: in.FPPInstanceUUID, Show: in.Show, Generation: in.Generation,
		PlaylistRevisions: in.PlaylistRevisions, CatalogRevisions: in.CatalogRevisions,
		Entries: in.Entries, Rules: FixedRules,
	}

	if p1.Revision != p2.Revision {
		t.Fatalf("two independently computed revisions of the identical content differ (%q vs %q) merely because PackageID/ExpiresAt/CompiledAt differ between the two Programs built from them",
			p1.Revision, p2.Revision)
	}
	// Distinguish this from TestComputeRevisionChangesWithContent's own
	// coverage: prove the two calls above were not BOTH silently
	// returning the same hard-coded constant regardless of input, by
	// confirming a THIRD, genuinely different input still changes it.
	differentContent := sampleRevisionInput()
	differentContent.Generation = 99
	r3, err := ComputeRevision(differentContent)
	if err != nil {
		t.Fatalf("ComputeRevision (third call, different content): %v", err)
	}
	if r3 == r1 {
		t.Fatalf("ComputeRevision returned the same revision for genuinely different content; a constant-returning implementation would also pass the metadata-exclusion check above for the wrong reason")
	}
}

func TestSignedProgramVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	in := sampleRevisionInput()
	revision, err := ComputeRevision(in)
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}
	program := Program{
		SchemaVersion: SchemaVersion, PackageID: "pkg-1", Revision: revision,
		ExpiresAt: time.Unix(1000, 0), CompiledAt: time.Unix(500, 0),
		FPPInstanceUUID: in.FPPInstanceUUID, Show: in.Show, Generation: in.Generation,
		PlaylistRevisions: in.PlaylistRevisions, CatalogRevisions: in.CatalogRevisions,
		Entries: in.Entries, Rules: FixedRules,
	}
	payload, err := program.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	sig := ed25519.Sign(priv, payload)

	sp := SignedProgram{Program: program, Signature: sig}
	if err := sp.Verify(pub); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Tampered payload: verification must fail.
	tampered := sp
	tampered.Program.Generation = in.Generation + 1
	if err := tampered.Verify(pub); err == nil {
		t.Fatalf("Verify unexpectedly succeeded against a tampered program")
	}

	// Wrong key: verification must fail.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := sp.Verify(otherPub); err == nil {
		t.Fatalf("Verify unexpectedly succeeded against the wrong public key")
	}
}
