// Package nodeclock turns a node's showmesh.node.clock/v1 report
// (pkg/mqttproto.ClockPayload, ingested by internal/coordinator/inventory
// off showmesh/nodes/<node-id>/observed/clock) into
// [observation.Observation] values under the "node" resource kind — Track
// I seam I1's node.clock.ptp.* signals (RES-019 section 5.2 / 10,
// docs/build/IDENTIFIER-REGISTER.md).
//
// This package is push-to-poll, mirroring internal/coordinator/collector/
// nodeaudio and noderender exactly: internal/coordinator/inventory's
// HandleMessage pushes each node's latest report into [Store] as it
// arrives (push), and [Collector.Poll] renders whatever [Store]
// currently holds into observations on the collector.Runner's own
// cadence (pull). See noderender's package doc comment for why this
// shape exists — this collector never touches the network itself.
//
// A node with no node.clock configuration never publishes to
// observed/clock at all (internal/agent/clock's own package doc
// comment), so [Store] never holds a report for it and [Collector.Poll]
// emits nothing for that node — this package does not itself synthesize
// an "unsynchronized" absence signal; that would require tracking every
// node's existence, which this push-only cache deliberately does not do
// (the coordinator's node inventory, not this collector, is where a
// node's mere existence is known).
package nodeclock
