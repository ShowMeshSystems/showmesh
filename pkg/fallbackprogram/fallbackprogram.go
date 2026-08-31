// Package fallbackprogram is the shared wire shape, revision-hash
// function, and verification path for ADR-048's coordinator-signed FPP
// fallback program, on the pkg/cuecatalog and pkg/coordsig precedent: it
// carries no store access, no HTTP, and no compilation logic. It exists
// ONLY so the coordinator (which builds and signs a [Program]) and a
// verifier of the signed result, the FPP plugin, in another repository,
// and eventually the node agent for Track J's J3, can each build or
// check the identical wire shape and call the identical
// [ComputeRevision] and [SignedProgram.Verify] functions, so a
// disagreement between what the coordinator compiled and what a host
// holds is detectable rather than assumed away.
//
// internal/agent must never import internal/coordinator (or vice versa);
// this package is the deliberate third place both sides depend on
// instead, the same role pkg/cuecatalog, pkg/fppidentity, and
// pkg/coordsig already play for other coordinator/agent or
// coordinator/plugin boundaries. Signing itself, the private key, and
// the only code permitted to call [internal/coordinator/signingkey.Manager.Sign],
// stays in internal/coordinator; this package's job ends at "does this
// signature verify against this public key for this payload," exactly as
// pkg/coordsig's own doc comment states for the signature primitive this
// package builds on.
package fallbackprogram

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// SchemaVersion is the current fallback-program wire schema version. A
// consumer built against a different version refuses rather than guessing
// at an unfamiliar shape.
const SchemaVersion = 1

// RenderActivation is the render half of one node target's activation:
// the runtime filename a node must open, and the content hashes it must
// verify the opened file against, mirroring
// pkg/cuecatalog.RenderOutput's identical rule. Sequence is a logical
// identity only, never something a node opens directly.
type RenderActivation struct {
	Sequence    string   `json:"sequence"`
	Filename    string   `json:"filename"`
	AssetHashes []string `json:"assetHashes"`
}

// AudioActivation is the audio half of one node target's activation.
// LTCStartOffsetMillis is nil when the Cue declares no LTC output for
// this node; it is never a second file, mirroring
// pkg/cuecatalog.LTCOutput's identical "derived from the audio output's
// own asset at runtime" rule (ADR-043 H0.3).
type AudioActivation struct {
	Asset                string   `json:"asset"`
	Filename             string   `json:"filename"`
	StartOffsetMillis    int      `json:"startOffsetMillis"`
	AssetHashes          []string `json:"assetHashes"`
	LTCStartOffsetMillis *int     `json:"ltcStartOffsetMillis,omitempty"`
}

// NodeTarget is one named node's exact output activation for one entry
// mapping (ADR-048 decision 1: "the named node targets and the exact
// output activation each target may perform"). Render and Audio are both
// optional but at least one is always set; a compiler that resolved
// neither for a node does not include that node as a target at all.
type NodeTarget struct {
	NodeID string            `json:"nodeId"`
	Render *RenderActivation `json:"render,omitempty"`
	Audio  *AudioActivation  `json:"audio,omitempty"`
}

// EntryMapping is one deterministic playlist-entry key and its resolved
// Cue identity and node targets (ADR-048 decision 1).
type EntryMapping struct {
	EntryKey    string       `json:"entryKey"`
	CueID       string       `json:"cueId"`
	CueRevision int64        `json:"cueRevision"`
	Targets     []NodeTarget `json:"targets"`
}

// Rules is ADR-048 decision 1's "fallback start, rest/hold, and local
// shutdown rules" plus decision 4's recovery rule, carried as declared,
// fixed values every program states identically: J1 reserves no
// configuration kind for a per-show override of any of the three, so
// there is nothing yet for these fields to vary with. Later work that
// needs an operator-configurable rule adds the field and the
// configuration kind that backs it; this shape does not preclude that.
type Rules struct {
	// FallbackBoundary states ADR-048 decision 2's own constraint: the
	// plugin may only act at a safe FPP playback boundary, never
	// mid-entry. Fixed at "safe-playback-boundary".
	FallbackBoundary string `json:"fallbackBoundary"`
	// RestHold is the declared behavior at the plugin's configured
	// cutoff (ADR-048 decision 2, state 3: Resting). Fixed at "hold".
	RestHold string `json:"restHold"`
	// LocalShutdown is the declared local-shutdown behavior bundled with
	// RestHold. Fixed at "local-shutdown".
	LocalShutdown string `json:"localShutdown"`
	// RecoveryBoundary is ADR-048 decision 4's own rule: the coordinator
	// resumes normal Cue resolution only at the next normal
	// scheduled-show boundary, never by a mid-show takeover. Fixed at
	// "next-scheduled-show-boundary".
	RecoveryBoundary string `json:"recoveryBoundary"`
}

// FixedRules is the one [Rules] value every [Program] this codebase
// compiles carries, see [Rules]'s own doc comment for why these are
// fixed rather than configurable in J1.
var FixedRules = Rules{
	FallbackBoundary: "safe-playback-boundary",
	RestHold:         "hold",
	LocalShutdown:    "local-shutdown",
	RecoveryBoundary: "next-scheduled-show-boundary",
}

// Program is ADR-048 decision 1's fallback program: everything one FPP
// host needs to preserve a running show through a coordinator outage,
// entirely pre-resolved and pre-authorized while the coordinator was
// healthy. PackageID identifies package CONTENT identity, not a publish
// event: [internal/coordinator/fallbackcompile.Compile] mints a fresh
// uuid only when Revision changes, and preserves the prior PackageID
// across a pure expiry refresh of an otherwise-unchanged Revision, so an
// acknowledgement pins content identity rather than exact republished
// bytes. Revision is a pure content hash of everything below that
// determines what the plugin may actually do (see [RevisionInput]), two
// compiles of unchanged inputs share both Revision and PackageID even
// when their ExpiresAt and CompiledAt differ.
type Program struct {
	SchemaVersion int `json:"schemaVersion"`

	PackageID  string    `json:"packageId"`
	Revision   string    `json:"revision"`
	ExpiresAt  time.Time `json:"expiresAt"`
	CompiledAt time.Time `json:"compiledAt"`

	// FPPInstanceUUID is the FPP host this program applies to (ADR-048
	// decision 1: "the FPP identity ... it applies to").
	FPPInstanceUUID string `json:"fppInstanceUuid"`

	Show       string `json:"show"`
	Generation int64  `json:"generation"`

	// PlaylistRevisions is every fpp-runner show.playlist object id this
	// program drew entries from, mapped to the config revision compiled.
	// A revision here changing without a new program being published is
	// exactly the staleness ADR-048's "playlist revisions it applies to"
	// exists to make detectable.
	PlaylistRevisions map[string]int64 `json:"playlistRevisions"`

	// CatalogRevisions is every node target's resolved Cue-catalog
	// revision (TRACK-H-H3-SPEC.md section 3.1) at compile time, keyed by
	// node id. ADR-048 decision 1's "catalog revision" field.
	CatalogRevisions map[string]string `json:"catalogRevisions"`

	Entries []EntryMapping `json:"entries"`

	Rules Rules `json:"rules"`
}

// CanonicalBytes serializes p as the RFC 8785 canonical JSON bytes that
// [internal/coordinator/signingkey.Manager.Sign] signs and
// [SignedProgram.Verify] checks against, the identical "one canonical
// serialization" rule pkg/fppidentity's own doc comment states for the
// project's other cross-boundary hashes, so no second, independently
// re-derived byte sequence for the same Program can ever exist.
func (p Program) CanonicalBytes() ([]byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("fallbackprogram: marshal program: %w", err)
	}
	canonical, _, err := fppidentity.HashCanonical(raw)
	if err != nil {
		return nil, fmt.Errorf("fallbackprogram: canonicalize program: %w", err)
	}
	return canonical, nil
}

// RevisionInput is EXACTLY what [ComputeRevision] covers: every field
// that changes what the plugin may actually do, and nothing that is
// merely publish metadata. PackageID, ExpiresAt, and CompiledAt are
// deliberately excluded, mirroring pkg/cuecatalog.RevisionInput's own
// "only genuinely display-only fields are excluded" rule one level up ,
// here the excluded fields are not display-only, they are publish
// metadata that must NOT force a new revision merely because the
// coordinator recompiled unchanged content, which is exactly what lets a
// healthy coordinator's periodic reconciliation (ADR-048 decision 1) stay
// a no-op against unchanged inputs instead of manufacturing a new
// "changed" package every tick. SchemaVersion and Rules are covered too,
// despite being fixed values today: both are ADR-048 decision 1 program
// content, not publish metadata, so a future change to either must still
// bump the revision. Entries must be sorted by EntryKey, each Entry's
// Targets sorted by NodeID, and every AssetHashes slice sorted, by the
// caller before this type is built, pkg/cuecatalog.RevisionInput's
// identical determinism requirement, for the identical reason: JSON
// Schema canonicalization sorts object member names but never reorders
// an array.
type RevisionInput struct {
	SchemaVersion     int               `json:"schemaVersion"`
	FPPInstanceUUID   string            `json:"fppInstanceUuid"`
	Show              string            `json:"show"`
	Generation        int64             `json:"generation"`
	PlaylistRevisions map[string]int64  `json:"playlistRevisions"`
	CatalogRevisions  map[string]string `json:"catalogRevisions"`
	Entries           []EntryMapping    `json:"entries"`
	Rules             Rules             `json:"rules"`
}

// ComputeRevision is the ONE exported function that computes a fallback
// program's revision: SHA-256 over pkg/fppidentity's canonical JSON
// serialization of in, the identical pattern
// pkg/cuecatalog.ComputeRevision establishes next door. Two
// callers given the same in always compute the same revision; no other
// function in this codebase may compute a fallback-program revision a
// second way.
func ComputeRevision(in RevisionInput) (string, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("fallbackprogram: marshal revision input: %w", err)
	}
	_, hashHex, err := fppidentity.HashCanonical(raw)
	if err != nil {
		return "", fmt.Errorf("fallbackprogram: canonicalize revision input: %w", err)
	}
	return hashHex, nil
}

// SignedProgram is a [Program] plus the coordinator's signature over its
// [Program.CanonicalBytes], the complete, verifiable wire object ADR-048
// decision 1 describes: "The program is signed with the existing
// coordinator signing authority."
type SignedProgram struct {
	Program   Program            `json:"program"`
	Signature coordsig.Signature `json:"signature"`
}

// Verify reports whether sp's signature verifies against publicKey for
// sp.Program's own canonical bytes. It never re-derives Revision or any
// other field from Program's contents: verification checks only that
// the coordinator signed exactly this document, exactly as
// [coordsig.Signature.Verify]'s own doc comment states for the primitive
// this method builds on. A caller that also wants to confirm a Program is
// not stale compares sp.Program.Revision against its own independently
// resolved current revision. That is a freshness check, not a signature
// check, and this method deliberately does not conflate the two.
func (sp SignedProgram) Verify(publicKey ed25519.PublicKey) error {
	payload, err := sp.Program.CanonicalBytes()
	if err != nil {
		return err
	}
	return sp.Signature.Verify(payload, publicKey)
}
