// Package nodeaudio turns a node's showmesh.node.audio/v1 report
// (pkg/mqttproto.AudioPayload, ingested by internal/coordinator/inventory
// off showmesh/nodes/<node-id>/observed/audio) into
// [observation.Observation] values under the "node" resource kind, so a
// node's audio discovery reaches GET /api/v1/observations, the SSE
// stream, and node reads through the paths those already have.
//
// This package is push-to-poll, mirroring
// internal/coordinator/collector/noderender exactly: internal/coordinator/
// inventory's HandleMessage pushes each node's latest report into [Store]
// as it arrives (push), and [Collector.Poll] renders whatever [Store]
// currently holds into observations on the collector.Runner's own cadence
// (pull). See noderender's package doc comment for why this shape exists
// — this collector never touches the network itself.
//
// # Resource kinds
//
// Engine, device, and the two logical buses attach to
// [observation.ResourceNode], never [observation.ResourceSurface]-style
// per-item resources: one audio engine and one installed interface exist
// per node (AUDIO-ENGINE section 6), so attributing them to anything
// per-route would report one fact N times and imply N independent faults.
// [observation.ResourceAudioSession] is registered by this package but
// emits no observations yet — a later session engine is what populates it.
//
// # ObservedAt (ADR-011)
//
// A node's audio report carries two evidence timestamps, not one:
// DiscoveredAt for the one-shot discovery probe (engine, device, outputs,
// program, LTC availability) and ObservedAt for this tick's own live
// evidence (LTC generator state, sessions). [buildValue] and
// [buildSessionValue] stamp ObservedAt from whichever of the two actually
// backs the signal being built — never the coordinator's own receipt
// time, matching noderender.buildValue's identical rule.
package nodeaudio
