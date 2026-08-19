// Package audio holds the audio session command contract shared by the
// coordinator, the audio agent, and Track F's night-session driver:
// identity and revision rules, closed vocabularies, media/playlist
// references, gain and fade semantics, the reserved operation names, and
// the absent/null/empty tri-state field used on every write surface.
//
// This is a pure data model, matching pkg/command and pkg/capability: no
// GStreamer, no MQTT, no HTTP, no coordinator or agent code, and no
// import of anything under internal/. Each side of a wire boundary
// declares its own JSON types against this vocabulary rather than
// unmarshaling a shared payload struct (see
// internal/agent/renderspec.go's doc comment for the convention this
// package follows).
package audio
