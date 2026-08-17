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
// showmesh/nodes/<node-id>/observed/render is retained (see
// pkg/mqttproto's ObservedDeliveryPolicy): a delivery replayed from the
// broker's retained store is evidence of unknown age, possibly from a node
// that no longer exists. [Store.Put] records whether a delivery was
// retained, and [buildValue] is the one place that decides ObservedAt from
// it, exactly like fppmqtt's buildObservation: retained means
// [observation.MeasuredUnknownAge] (ObservedAt nil), live means
// [observation.Measured] with the coordinator's own receipt time. Never the
// reverse.
package noderender
