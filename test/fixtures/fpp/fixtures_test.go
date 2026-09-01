// Package fppfixtures_test consumes the plain JSON fixture files in this
// directory the same way the plugin repository's C++ tests do: read the
// file from disk, run this coordinator's own implementation over each
// case, and compare against the frozen expected values recorded in the
// file. See README.md for the file formats and
// docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 4 for why these
// fixtures exist and are not a Go package the plugin could import
// directly.
//
// The ingestion.json half of this fixture set is consumed by
// internal/coordinator/api/fppobservations_fixtures_test.go instead of
// here, because exercising the real HTTP handler requires that package's
// existing store/identity test scaffolding (fppobservations_test.go),
// which this package has no access to and should not duplicate.
package fppfixtures_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// fixtureDir resolves to this source file's own directory regardless of
// the working directory the test binary runs from.
func fixtureDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve this test file's own path")
	}
	return filepath.Dir(thisFile)
}

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// --- canonicalization.json ---

type canonicalizationFixture struct {
	Description string                 `json:"description"`
	Cases       []canonicalizationCase `json:"cases"`
}

type canonicalizationCase struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Input             string `json:"input"`
	InputHex          string `json:"inputHex"`
	ExpectedCanonical string `json:"expectedCanonical"`
	ExpectedSha256    string `json:"expectedSha256"`
	ExpectError       bool   `json:"expectError"`
	ErrorKind         string `json:"errorKind"`
}

// caseBytes resolves a canonicalizationCase's input: exactly one of Input
// or InputHex must be set. InputHex exists because a JSON string cannot
// carry a byte sequence that is not valid UTF-8 (see README.md); a case
// supplying neither or both is a fixture bug, not an empty-input case, and
// must fail loudly rather than silently canonicalize an empty string.
func caseBytes(t *testing.T, tc canonicalizationCase) []byte {
	t.Helper()
	hasInput := tc.Input != ""
	hasHex := tc.InputHex != ""
	switch {
	case hasInput && hasHex:
		t.Fatalf("case %q: sets both input and inputHex; exactly one is required", tc.Name)
		return nil
	case !hasInput && !hasHex:
		t.Fatalf("case %q: sets neither input nor inputHex; exactly one is required", tc.Name)
		return nil
	case hasHex:
		b, err := hex.DecodeString(tc.InputHex)
		if err != nil {
			t.Fatalf("case %q: inputHex is not valid lowercase hex: %v", tc.Name, err)
		}
		return b
	default:
		return []byte(tc.Input)
	}
}

func TestCanonicalizationFixtures(t *testing.T) {
	var fixture canonicalizationFixture
	loadFixture(t, "canonicalization.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("canonicalization.json has no cases")
	}
	seen := map[string]bool{}
	for _, tc := range fixture.Cases {
		tc := tc
		if seen[tc.Name] {
			t.Fatalf("duplicate case name %q in canonicalization.json", tc.Name)
		}
		seen[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			canonical, hashHex, err := fppidentity.HashCanonical(caseBytes(t, tc))
			if tc.ExpectError {
				if err == nil {
					t.Fatalf("case %q: expected an error (%s), got canonical=%q", tc.Name, tc.ErrorKind, canonical)
				}
				return
			}
			if err != nil {
				t.Fatalf("case %q: HashCanonical error: %v", tc.Name, err)
			}
			if string(canonical) != tc.ExpectedCanonical {
				t.Errorf("case %q: canonical = %q, want %q", tc.Name, canonical, tc.ExpectedCanonical)
			}
			if hashHex != tc.ExpectedSha256 {
				t.Errorf("case %q: sha256 = %s, want %s", tc.Name, hashHex, tc.ExpectedSha256)
			}
		})
	}
}

// --- entry-key.json ---

type entryKeyFixture struct {
	Description string         `json:"description"`
	Cases       []entryKeyCase `json:"cases"`
}

type entryKeyCase struct {
	Name                       string           `json:"name"`
	Description                string           `json:"description"`
	Identity                   entryKeyIdentity `json:"identity"`
	ExpectedCanonicalKeyObject string           `json:"expectedCanonicalKeyObject"`
	ExpectedEntryKey           string           `json:"expectedEntryKey"`
}

type entryKeyIdentity struct {
	InstanceUUID string `json:"instanceUuid"`
	PlaylistName string `json:"playlistName"`
	PlaylistHash string `json:"playlistHash"`
	Section      string `json:"section"`
	Position     int    `json:"position"`
}

func TestEntryKeyFixtures(t *testing.T) {
	var fixture entryKeyFixture
	loadFixture(t, "entry-key.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("entry-key.json has no cases")
	}
	seen := map[string]bool{}
	keysSeen := map[string]string{} // entryKey -> first case name that produced it
	for _, tc := range fixture.Cases {
		tc := tc
		if seen[tc.Name] {
			t.Fatalf("duplicate case name %q in entry-key.json", tc.Name)
		}
		seen[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			id := fppidentity.EntryIdentity{
				InstanceUUID: tc.Identity.InstanceUUID,
				PlaylistName: tc.Identity.PlaylistName,
				PlaylistHash: tc.Identity.PlaylistHash,
				Section:      tc.Identity.Section,
				Position:     tc.Identity.Position,
			}
			key, err := fppidentity.DeriveEntryKey(id)
			if err != nil {
				t.Fatalf("case %q: DeriveEntryKey error: %v", tc.Name, err)
			}
			if key != tc.ExpectedEntryKey {
				t.Errorf("case %q: entryKey = %s, want %s", tc.Name, key, tc.ExpectedEntryKey)
			}

			// The canonical key object text is checked independently of
			// DeriveEntryKey, by canonicalizing a JSON object built from
			// the same five fields, so a fixture mismatch here points at
			// canonicalization specifically rather than at hashing.
			canonicalObj, canonErr := fppidentity.Canonicalize([]byte(
				`{"instanceUuid":` + jsonQuote(tc.Identity.InstanceUUID) +
					`,"playlistHash":` + jsonQuote(tc.Identity.PlaylistHash) +
					`,"playlistName":` + jsonQuote(tc.Identity.PlaylistName) +
					`,"position":` + jsonNumber(tc.Identity.Position) +
					`,"section":` + jsonQuote(tc.Identity.Section) + `}`))
			if canonErr != nil {
				t.Fatalf("case %q: Canonicalize(key object) error: %v", tc.Name, canonErr)
			}
			if string(canonicalObj) != tc.ExpectedCanonicalKeyObject {
				t.Errorf("case %q: canonicalKeyObject = %q, want %q", tc.Name, canonicalObj, tc.ExpectedCanonicalKeyObject)
			}
			if hashHex := sha256Hex(canonicalObj); hashHex != tc.ExpectedEntryKey {
				t.Errorf("case %q: sha256(canonicalKeyObject) = %s, want entryKey %s", tc.Name, hashHex, tc.ExpectedEntryKey)
			}

			if prior, ok := keysSeen[key]; ok && prior != tc.Name {
				// Two cases are allowed to collide only if the fixture
				// intends it (it never does here): every case in this
				// file is designed to produce a distinct key.
				t.Errorf("case %q produced the same entryKey as case %q: %s", tc.Name, prior, key)
			} else if !ok {
				keysSeen[key] = tc.Name
			}
		})
	}
}

// --- section-mapping.json ---

type sectionMappingFixture struct {
	Description string               `json:"description"`
	Cases       []sectionMappingCase `json:"cases"`
}

type sectionMappingCase struct {
	Name                     string                 `json:"name"`
	Description              string                 `json:"description"`
	RuntimeSection           string                 `json:"runtimeSection"`
	ExpectedCanonicalSection string                 `json:"expectedCanonicalSection"`
	Identity                 sectionMappingIdentity `json:"identity"`
	ExpectedEntryKey         string                 `json:"expectedEntryKey"`
}

type sectionMappingIdentity struct {
	InstanceUUID string `json:"instanceUuid"`
	PlaylistName string `json:"playlistName"`
	PlaylistHash string `json:"playlistHash"`
	Position     int    `json:"position"`
}

// TestSectionMappingFixtures exercises the mapping step entry-key.json
// cannot: it starts from FPP's own runtime section string, maps it with
// fppidentity.CanonicalSection, and only then derives entryKey. A
// reference implementation that hashes the runtime string directly
// (contract section 1.2's defect this fixture exists to catch) produces a
// different entryKey and fails here.
func TestSectionMappingFixtures(t *testing.T) {
	var fixture sectionMappingFixture
	loadFixture(t, "section-mapping.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("section-mapping.json has no cases")
	}
	seen := map[string]bool{}
	for _, tc := range fixture.Cases {
		tc := tc
		if seen[tc.Name] {
			t.Fatalf("duplicate case name %q in section-mapping.json", tc.Name)
		}
		seen[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			canonical := fppidentity.CanonicalSection(tc.RuntimeSection)
			if canonical != tc.ExpectedCanonicalSection {
				t.Fatalf("case %q: CanonicalSection(%q) = %q, want %q", tc.Name, tc.RuntimeSection, canonical, tc.ExpectedCanonicalSection)
			}
			id := fppidentity.EntryIdentity{
				InstanceUUID: tc.Identity.InstanceUUID,
				PlaylistName: tc.Identity.PlaylistName,
				PlaylistHash: tc.Identity.PlaylistHash,
				Section:      canonical,
				Position:     tc.Identity.Position,
			}
			key, err := fppidentity.DeriveEntryKey(id)
			if err != nil {
				t.Fatalf("case %q: DeriveEntryKey error: %v", tc.Name, err)
			}
			if key != tc.ExpectedEntryKey {
				t.Errorf("case %q: entryKey = %s, want %s", tc.Name, key, tc.ExpectedEntryKey)
			}
		})
	}
}

func jsonQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func jsonNumber(n int) string {
	raw, err := json.Marshal(n)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
