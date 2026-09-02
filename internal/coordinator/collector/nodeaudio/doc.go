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
// Every node.audio.* signal this package builds stamps ObservedAt from
// AudioPayload.ObservedAt, this report tick's own evidence time, never
// from AudioPayload.DiscoveredAt (the agent's one-shot startup probe) and
// never from the coordinator's own receipt time. [Store] keeps only a
// node's most recent report and nothing evicts it, so a signal stamped
// with anything but the current tick's own evidence time would read
// current forever, even off a node that stopped reporting: the defect
// this package's tests guard against for every signal, cached-discovery
// ones (device, outputs, program, LTC) included.
package nodeaudio
