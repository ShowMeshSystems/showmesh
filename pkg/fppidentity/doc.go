// Package fppidentity implements the FPP plugin coordinator contract fixed
// by docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md sections 1.3 and 1.4:
// RFC 8785 JSON Canonicalization (JCS) and the two SHA-256 hashes derived
// from it, playlistHash and entryKey.
//
// The reference implementation is the plugin repository's
// native/src/json.cpp and native/src/playlist_identity.cpp, which are
// merged and shipping. This package matches that C++ byte for byte,
// including where a strictly "more correct" RFC 8785 reading would
// disagree with it. A disagreement between this package and the C++ is a
// blocker to raise against both, never a unilateral fix on either side.
//
// # Why not encoding/json
//
// encoding/json's default decode into map[string]any cannot be used here:
// it silently drops duplicate object member names (JCS requires rejecting
// them), it does not preserve the exact number literal (needed to match
// the C++ parser's own strtod-based double conversion), and Go map
// iteration order carries no information this package can use for sorting.
// Canonicalize therefore parses with a small hand-written recursive
// descent parser that mirrors the C++ Parser in json.cpp exactly: same
// grammar, same duplicate-key rejection, same JSON number grammar, and the
// same conversion to a float64 via strconv.ParseFloat, which uses the
// portable round-to-nearest algorithm C's strtod uses on every platform
// this coordinator targets.
package fppidentity
