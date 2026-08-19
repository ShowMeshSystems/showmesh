// Package remoteoutput implements the AUDIO-ENGINE section 8.1 boundary
// for a synchronized remote output: a destination that reproduces media
// from an advance-provisioned copy plus logical playback state, rather
// than receiving the rendered PCM mix. The package is generic — it names
// no destination product — and is exercised in this repository only
// against [FakeDestination], a deterministic double.
//
// Two responsibilities stay structurally separate, per section 8.1:
// advance media provisioning ([Provisioner]) and logical playout
// ([PlayoutOutput]). A type that only drives playout never holds a
// reference capable of provisioning, so a start command cannot trigger a
// transfer through this package's own types.
package remoteoutput
