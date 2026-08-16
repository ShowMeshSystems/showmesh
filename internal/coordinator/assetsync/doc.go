// Package assetsync computes what a node's asset directory should hold
// against the active show (ADR-028 decision 5/7, TRACK-E-SESSION-SPEC.md
// §4) and runs the background service that closes the gap by dispatching
// "asset.fetch" commands to nodes over MQTT (§5).
//
// This package is deliberately pure with respect to transport: nothing
// here imports net/http, and nothing here is an API handler. A caller —
// today only [Service]'s own tick loop, later an HTTP handler built on
// top of this package — reads a node's state through [BuildNodeManifest]
// or [BuildManifest] and gets back plain Go values.
//
// # Three-valued readiness, computed in one place
//
// [ComputeNodeManifest] is the ONLY function in this codebase permitted
// to decide whether a node is ready, not_ready, or unknown — see its own
// doc comment, and see internal/coordinator/collector/resolume's
// readiness.go for the precedent this mirrors: a shared rule is only
// shared where it is called, so every caller (this package's own sync
// service, and any future manifest API handler) must call this function
// rather than re-deriving the rule at its own call site.
//
// unknown must never render as ready, and a stale report must never
// render as not_ready — a stale report is not evidence of absence. This
// project has manufactured absence from ambiguous evidence once already
// (a 40-second broker outage flipping a discovery run to "not seen") and
// this package exists, in part, to not do it a second time.
//
// # The surface-to-sequence gap is inferred, not modeled
//
// TRACK-E-SESSION-SPEC.md §4.1 point 3 describes a stated gap as "a
// show.surface assigned to this node ... whose sequence has no current
// asset." As built (E1/E2, config/showsurface.go), a show.surface carries
// no sequence reference at all — it describes a node's rendering output
// (channel range, geometry, transport), not a piece of content. This
// package therefore infers the gap from the show's OWN asset rows
// instead: the set of sequence ids the active show has ANY current asset
// for, cross-checked against what each surfaced node already has an
// asset for (node-targeted or show-wide). A node with a surface and zero
// coverage for a sequence the rest of the show already has is reported as
// a gap naming that sequence and every surface assigned to the node — see
// [ExpectedAssetsForNode]'s own doc comment. This is the most literal
// honest reading available in the current schema, not a re-statement of
// the spec text; a real surface-to-sequence relationship, should one get
// added later, supersedes it.
package assetsync
