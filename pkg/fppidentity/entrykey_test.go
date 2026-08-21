package fppidentity

import "testing"

const testUUID = "6f1c1a52-1b6c-4b53-9a0e-9f7c2f0d1b44"

// The expected hex digests below were cross-checked against the C++
// reference implementation (native/src/playlist_identity.cpp in the
// plugin repository), by building a throwaway program against its
// sources and running it. See this task's final report for the exact
// command and printed values.

func TestDeriveEntryKeyMemberOrderMatchesContract(t *testing.T) {
	// contract section 1.3 fixes the canonical member order as
	// instanceUuid, playlistHash, playlistName, position, section.
	id := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "Main Show",
		PlaylistHash: "aa",
		Section:      "mainPlaylist",
		Position:     0,
	}
	key, err := DeriveEntryKey(id)
	if err != nil {
		t.Fatal(err)
	}
	const want = "79af480184fd75001034a45fa79935d2edfd5899da764b05a59ac84117e36f30"
	if key != want {
		t.Errorf("DeriveEntryKey(%+v) = %s, want %s", id, key, want)
	}
	if !IsHash64(key) {
		t.Errorf("key %q is not 64 lowercase hex characters", key)
	}
}

func TestDeriveEntryKeyDiffersByPosition(t *testing.T) {
	base := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "Main Show",
		PlaylistHash: "aa",
		Section:      "mainPlaylist",
		Position:     0,
	}
	pos1 := base
	pos1.Position = 1

	keyBase, err := DeriveEntryKey(base)
	if err != nil {
		t.Fatal(err)
	}
	keyPos1, err := DeriveEntryKey(pos1)
	if err != nil {
		t.Fatal(err)
	}
	const wantPos1 = "b2822186333fa7e0a616d12d39a3cbecb000211e417f6dd8299eff8789102f34"
	if keyPos1 != wantPos1 {
		t.Errorf("DeriveEntryKey(pos1) = %s, want %s", keyPos1, wantPos1)
	}
	if keyBase == keyPos1 {
		t.Error("two entries with identical filenames at different positions must not produce the same key")
	}
}

func TestDeriveEntryKeyIsStableForAnUnchangedIdentity(t *testing.T) {
	id := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "Main Show",
		PlaylistHash: "deadbeef",
		Section:      "leadOut",
		Position:     3,
	}
	a, err := DeriveEntryKey(id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveEntryKey(id)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("an unchanged identity produced different keys: %s vs %s", a, b)
	}
}

func TestDeriveEntryKeyChangesWithEachField(t *testing.T) {
	base := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "Main Show",
		PlaylistHash: "deadbeef",
		Section:      "mainPlaylist",
		Position:     2,
	}
	baseKey, err := DeriveEntryKey(base)
	if err != nil {
		t.Fatal(err)
	}

	variants := map[string]EntryIdentity{
		"instanceUuid": withInstanceUUID(base, "other-uuid"),
		"playlistHash": withPlaylistHash(base, "cafebabe"),
		"playlistName": withPlaylistName(base, "Other Show"),
		"position":     withPosition(base, 5),
		"section":      withSection(base, "leadIn"),
	}
	for field, variant := range variants {
		t.Run(field, func(t *testing.T) {
			key, err := DeriveEntryKey(variant)
			if err != nil {
				t.Fatal(err)
			}
			if key == baseKey {
				t.Errorf("changing %s did not change the entry key", field)
			}
		})
	}
}

func withInstanceUUID(id EntryIdentity, v string) EntryIdentity { id.InstanceUUID = v; return id }
func withPlaylistHash(id EntryIdentity, v string) EntryIdentity { id.PlaylistHash = v; return id }
func withPlaylistName(id EntryIdentity, v string) EntryIdentity { id.PlaylistName = v; return id }
func withPosition(id EntryIdentity, v int) EntryIdentity        { id.Position = v; return id }
func withSection(id EntryIdentity, v string) EntryIdentity      { id.Section = v; return id }

// A playlist name or section containing a separator character a
// delimited key format would use (`|`, `:`) must not collide with a
// different (name, section) split. This is why the key hashes a
// canonical JSON object rather than a joined string.
func TestDeriveEntryKeySeparatorCharactersDoNotCollide(t *testing.T) {
	a := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "show:1",
		PlaylistHash: "aa",
		Section:      "main",
		Position:     0,
	}
	b := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "show",
		PlaylistHash: "aa",
		Section:      "1|main",
		Position:     0,
	}

	keyA, err := DeriveEntryKey(a)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := DeriveEntryKey(b)
	if err != nil {
		t.Fatal(err)
	}
	const wantA = "160ba04ed1e7e0463360e177eda22022d517a81c951d861faf9a938cd4478eed"
	const wantB = "e877b7e33c97731b48f44a137d6995f1be58463df130b0b8a547e70803a02f25"
	if keyA != wantA {
		t.Errorf("keyA = %s, want %s", keyA, wantA)
	}
	if keyB != wantB {
		t.Errorf("keyB = %s, want %s", keyB, wantB)
	}
	if keyA == keyB {
		t.Error("a separator character in the name/section split produced a collision")
	}
}

// A NUL-adjacent control character embedded in a name must be escaped
// (as ) rather than truncating or otherwise being treated as a
// delimiter.
func TestDeriveEntryKeyControlCharacterInNameIsHashedNotTruncated(t *testing.T) {
	id := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "a" + string(rune(0)) + "b|c",
		PlaylistHash: "aa",
		Section:      "sect",
		Position:     0,
	}
	key, err := DeriveEntryKey(id)
	if err != nil {
		t.Fatal(err)
	}
	const want = "1628dd96e5c902ff1fdffecc0fb01fd846225f0bcb32ff63110a02aae1ea1027"
	if key != want {
		t.Errorf("DeriveEntryKey with a control character = %s, want %s", key, want)
	}
}

// End-to-end: canonicalize a full playlist definition, derive
// playlistHash from it, then derive entryKey from that hash plus the
// remaining identity fields. Cross-checked against
// showmesh::resolveEntryIdentity for the same inputs.
func TestDeriveEntryKeyFromCanonicalizedDefinition(t *testing.T) {
	def := `{"name":"Main Show","repeat":0,` +
		`"mainPlaylist":[` +
		`{"type":"both","sequenceName":"a.fseq","mediaName":"song.mp3","enabled":1},` +
		`{"type":"both","sequenceName":"a.fseq","mediaName":"song.mp3","enabled":1}` +
		`]}`
	_, hashHex, err := HashCanonical([]byte(def))
	if err != nil {
		t.Fatal(err)
	}
	id := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "Main Show",
		PlaylistHash: hashHex,
		Section:      "mainPlaylist",
		Position:     0,
	}
	key, err := DeriveEntryKey(id)
	if err != nil {
		t.Fatal(err)
	}
	const want = "a05bbae06463f820d68436ea26840e1878f2d74c32bb21606ef64d6dcb45bfe4"
	if key != want {
		t.Errorf("DeriveEntryKey = %s, want %s", key, want)
	}
}

// TestDeriveEntryKeyRejectsInvalidUTF8 is finding 6's regression test on
// the entryKey path specifically: DeriveEntryKey builds its object
// directly from Go strings, never through the JSON parser, so a caller
// passing an invalid-UTF-8 playlist name must still be refused rather
// than silently hashed.
func TestDeriveEntryKeyRejectsInvalidUTF8(t *testing.T) {
	id := EntryIdentity{
		InstanceUUID: testUUID,
		PlaylistName: "bad\xffname",
		PlaylistHash: "aa",
		Section:      "main",
		Position:     0,
	}
	if _, err := DeriveEntryKey(id); err == nil {
		t.Error("expected an error for an invalid UTF-8 playlist name, got nil")
	}
}

func TestParseActionRoundTrips(t *testing.T) {
	actions := []Action{ActionStart, ActionPlaying, ActionStop, ActionQueryNext, ActionUnknown}
	for _, a := range actions {
		got, err := ParseAction(string(a))
		if err != nil {
			t.Errorf("ParseAction(%q) error: %v", a, err)
		}
		if got != a {
			t.Errorf("ParseAction(%q) = %q, want %q", a, got, a)
		}
	}
}

func TestParseActionRejectsUnknownValues(t *testing.T) {
	for _, s := range []string{"", "Start", "STOP", "paused", "start "} {
		if _, err := ParseAction(s); err == nil {
			t.Errorf("ParseAction(%q): expected an error, got nil", s)
		}
	}
}

func TestParseUnavailableRoundTrips(t *testing.T) {
	reasons := []Unavailable{
		UnavailableNone,
		UnavailableMissingInstanceUUID,
		UnavailableMissingPlaylistName,
		UnavailableMissingDefinition,
		UnavailableUnsupportedDefShape,
		UnavailableNegativePosition,
		UnavailableTruncatedIdentityField,
	}
	for _, r := range reasons {
		got, err := ParseUnavailable(string(r))
		if err != nil {
			t.Errorf("ParseUnavailable(%q) error: %v", r, err)
		}
		if got != r {
			t.Errorf("ParseUnavailable(%q) = %q, want %q", r, got, r)
		}
	}
}

func TestParseUnavailableAcceptsEmptyStringAsAvailable(t *testing.T) {
	got, err := ParseUnavailable("")
	if err != nil {
		t.Fatalf("ParseUnavailable(\"\") error: %v", err)
	}
	if got != UnavailableNone {
		t.Errorf("ParseUnavailable(\"\") = %q, want the zero value", got)
	}
}

func TestParseUnavailableRejectsUnknownValues(t *testing.T) {
	for _, s := range []string{"missing_instanceuuid", "MISSING_DEFINITION", "unknown_reason"} {
		if _, err := ParseUnavailable(s); err == nil {
			t.Errorf("ParseUnavailable(%q): expected an error, got nil", s)
		}
	}
}

func TestIsHash64(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"", false},
		{"a05bbae06463f820d68436ea26840e1878f2d74c32bb21606ef64d6dcb45bfe4", true},
		{"A05BBAE06463F820D68436EA26840E1878F2D74C32BB21606EF64D6DCB45BFE4", false},
		{"a05bbae06463f820d68436ea26840e1878f2d74c32bb21606ef64d6dcb45bfe", false},   // 63 chars
		{"a05bbae06463f820d68436ea26840e1878f2d74c32bb21606ef64d6dcb45bfe44", false}, // 65 chars
		{"g05bbae06463f820d68436ea26840e1878f2d74c32bb21606ef64d6dcb45bfe4", false},  // non-hex char
	}
	for _, tc := range cases {
		if got := IsHash64(tc.s); got != tc.want {
			t.Errorf("IsHash64(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
