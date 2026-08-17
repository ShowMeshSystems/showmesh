// Package noderender turns a node's showmesh.node.render/v1 report
// (pkg/mqttproto.RenderPayload, ingested by internal/coordinator/inventory
// off showmesh/nodes/<node-id>/observed/render) into
// [observation.Observation] values under the "surface" resource kind, so
// Track B seam B2a's pipeline supervision reaches GET /api/v1/observations,
// the SSE stream, and node reads through the paths those already have,
// rather than a parallel one.
//
// This package is push-to-poll, mirroring
// internal/coordinator/collector/fppmqtt exactly: internal/coordinator/
// inventory's HandleMessage pushes each node's latest report into [Store]
// as it arrives (push), and [Collector.Poll] renders whatever [Store]
// currently holds into observations on the collector.Runner's own cadence
// (pull). See fppmqtt's package doc comment for why this shape exists —
// this collector never touches the network itself.
//
// # The retained/live distinction (ADR-011)
//
// showmesh/nodes/<node-id>/observed/render is retained (see pkg/mqttproto's
// ObservedDeliveryPolicy). Unlike fppmqtt's own buildObservation, this
// package's [buildValue] does NOT gate ObservedAt on [Store.Put]'s retained
// flag: every field this agent reports already carries its own
// NODE-REPORTED evidence timestamp (sf.ObservedAt / MultiSyncObservedAt),
// stamped at the moment of a genuine transition or sample, and that
// timestamp is what decides ObservedAt regardless of whether the MQTT
// delivery that carried it was retained or live. A retained delivery is
// only a reason to treat age as unknown when the payload itself carries no
// evidence timestamp — the node reporting the zero value — in which case
// [buildValue] uses [observation.MeasuredUnknownAge] exactly as it would for
// a live delivery with no timestamp. retained is still recorded on every
// [report] and available to callers, but this package's ObservedAt decision
// runs off the node's own clock, not off retained.
package noderender
