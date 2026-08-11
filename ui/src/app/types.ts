// Domain types this seam (C, views) renders.
//
// UPDATE (mid-build): this file originally hand-mirrored api/openapi.yaml
// because seam B's `ui/src/api` did not exist yet when this seam started
// (see git history / this builder's report for why, and for the two
// reasons that motivated it at the time). Seam B has since landed. Now
// that `../api` is real, hand-mirroring its shapes would itself become
// the exact duplication this project warns against ("Type definitions
// for API payloads must be generated from the same source as the Go
// types or verified against them... Hand-maintaining a second copy of
// the state model will drift" — ADR-015). So this file now re-exports
// seam B's types directly rather than redefining them, and derives the
// small standalone enum aliases this seam's components use (e.g.
// `ControlPlaneState`) from seam B's field types by indexed access,
// rather than hand-copying their literal unions a second time.
//
// This file remains the single import site for domain types across
// src/app, src/views, and src/components, so call sites did not need to
// change when the underlying source did.
export type {
  Capability,
  CollectorStatus,
  ConnectionState,
  ControlPlane,
  Evidence,
  EvidenceState,
  EventSeq,
  FPPInstance,
  Model,
  Node,
  NodeEvidence,
  ResourceRef,
} from '../api'
// Renamed on import, not re-declared: seam B's `Event` is identical to
// the wire schema's Event plus a branded `EventSeq` (see api/domain.ts);
// aliasing it here avoids colliding with the DOM's global `Event` type
// throughout this seam's component props.
export type { Event as ShowMeshEvent } from '../api'

import type {
  ControlPlane as ControlPlaneType,
  Evidence as EvidenceType,
  Event as EventType,
  FPPInstance as FPPInstanceType,
  ResourceRef as ResourceRefType,
} from '../api'

// Derived, not duplicated: these are indexed-access views onto seam B's
// field types, so a value added to any of these unions in
// api/openapi.yaml flows through automatically instead of needing a
// second edit here.
export type ControlPlaneState = ControlPlaneType['state']
export type FPPHealth = FPPInstanceType['health']
export type EventSeverity = EventType['severity']
export type ResourceKind = ResourceRefType['kind']
export type EvidenceQuality = EvidenceType['quality']
