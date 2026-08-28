package fppidentity

// RuntimeSectionMap fixes FPP's runtime playlist-section strings — the
// spelling FPP's playlist callback reports, per FPP 10.0: LeadIn,
// MainPlaylist, LeadOut, or New — to the canonical section value contract
// section 1.2 requires on the wire and section 1.3 hashes into entryKey:
// the playlist definition's own JSON member name (DefinitionSections).
// This is the mapping whose absence let the plugin and the coordinator
// each read a defensible but different spelling; see
// docs/bench/TRACK-H-CHAIN.md.
var RuntimeSectionMap = map[string]string{
	"LeadIn":       "leadIn",
	"MainPlaylist": "mainPlaylist",
	"LeadOut":      "leadOut",
}

// CanonicalSection maps FPP's runtime section string to the canonical wire
// value, contract section 1.2. A runtime string with no entry in
// RuntimeSectionMap — including FPP's "New" — is not a recognized
// definition section and is passed through unchanged rather than mapped to
// a new value or an §1.4 unavailable reason: it will not match any entry
// in the playlist definition's leadIn/mainPlaylist/leadOut arrays, and the
// coordinator's existing unknown-entry outcome is the visible failure.
//
// This is the reference implementation the plugin's C++ mirrors, kept here
// so the shared fixtures (test/fixtures/fpp/section-mapping.json) have a Go
// implementation to check against; it has no caller elsewhere in this
// repository and that is deliberate. The mapping belongs on the sending
// side, before the wire. Calling it on an inbound observation in
// fppobservations.go would silently repair a plugin that sent the wrong
// spelling, which re-hides the exact failure class this mapping exists to
// expose rather than surface it as the coordinator's existing
// unknown-entry outcome. Do not wire it into ingestion.
func CanonicalSection(runtimeSection string) string {
	if canonical, ok := RuntimeSectionMap[runtimeSection]; ok {
		return canonical
	}
	return runtimeSection
}
