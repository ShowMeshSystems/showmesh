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

func TestComputeRevisionChangesWithContent(t *testing.T) {
	base, err := ComputeRevision(sampleRevisionInput())
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}
	changed := sampleRevisionInput()
	changed.Generation = 4
	other, err := ComputeRevision(changed)
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}
	if base == other {
		t.Fatalf("ComputeRevision did not change when Generation changed")
	}
}

func TestComputeRevisionExcludesPublishMetadata(t *testing.T) {
	// A Program built from the identical RevisionInput but with different
	// PackageID/ExpiresAt/CompiledAt must still report the identical
	// Revision: those fields are publish metadata, not content (see
	// RevisionInput's own doc comment).
	in := sampleRevisionInput()
	r1, err := ComputeRevision(in)
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}

	p1 := Program{
		SchemaVersion: SchemaVersion, PackageID: "pkg-1", Revision: r1,
		ExpiresAt: time.Unix(1000, 0), CompiledAt: time.Unix(500, 0),
		FPPInstanceUUID: in.FPPInstanceUUID, Show: in.Show, Generation: in.Generation,
		PlaylistRevisions: in.PlaylistRevisions, CatalogRevisions: in.CatalogRevisions,
		Entries: in.Entries, Rules: FixedRules,
	}
	p2 := p1
	p2.PackageID = "pkg-2"
	p2.ExpiresAt = time.Unix(2000, 0)
	p2.CompiledAt = time.Unix(1500, 0)

	if p1.Revision != p2.Revision {
		t.Fatalf("Revision must be independent of PackageID/ExpiresAt/CompiledAt")
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
