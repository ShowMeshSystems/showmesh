import type {
  ActionBindingState,
  ControlPlaneState,
  DiscoveryState,
  EventSeverity,
  FPPHealth,
  FPPPlaylistEntryReconciliationOutcome,
  FPPPlaylistReadinessFailingCondition,
  NightCueOutcome,
  NightCueState,
  NightLifecycleState,
  NightPhaseEvidenceState,
  NightReadinessCheckState,
  NightReadinessOutcome,
  ResolumeHealth,
  ResolumeRecoveryLayerState,
  ResolumeRecoveryRestoreResult,
} from '../app/types'
import { StatusBadge, type StatusTone } from './StatusBadge'

// Domain-specific status badges, all built on the one StatusBadge
// primitive so the color-is-never-the-only-signal rule (OBSERVABILITY
// section 6.3) is inherited rather than re-implemented per domain.

// FPPHealth and ResolumeHealth are the identical five-value wire vocabulary
// (both "healthy" | "degraded" | "failed" | "unknown" | "suppressed" —
// api/openapi.yaml's FPPInstance.health and ResolumeInstance.health), so
// one table and one renderer serve both domains rather than risking two
// copies drifting on tone/icon/label.
const HEALTH: Record<FPPHealth, { tone: StatusTone; icon: string; label: string }> = {
  healthy: { tone: 'good', icon: '●', label: 'healthy' },
  degraded: { tone: 'warn', icon: '⚠', label: 'degraded' },
  failed: { tone: 'bad', icon: '✕', label: 'failed' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
  suppressed: { tone: 'unknown', icon: '⏸', label: 'suppressed' },
}

export function FPPHealthBadge({ health }: { health: FPPHealth }) {
  const spec = HEALTH[health]
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// Track D seam D-4. 'unknown' is its own tone here exactly as it is for
// FPP above -- "the system does not know" must never collapse into
// 'warning'/'degraded' -- inherited automatically since both draw from the
// same HEALTH table.
export function ResolumeHealthBadge({ health }: { health: ResolumeHealth }) {
  const spec = HEALTH[health]
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// Wording is deliberate: "offline" describes the MQTT control-plane
// connection only. It must not be readable as "the node is dead" or "a
// show it is running has stopped" (spec section 6.4, openapi.yaml
// ControlPlane description, CLAUDE.md constraint on this exact point).
const CONTROL_PLANE: Record<ControlPlaneState, { tone: StatusTone; icon: string; label: string }> = {
  online: { tone: 'good', icon: '●', label: 'control-plane connected' },
  offline: { tone: 'warn', icon: '⚠', label: 'control-plane connection lost' },
  unknown: { tone: 'unknown', icon: '?', label: 'control-plane state unknown' },
}

export function ControlPlaneBadge({ state }: { state: ControlPlaneState }) {
  const spec = CONTROL_PLANE[state]
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

const SEVERITY: Record<EventSeverity, { tone: StatusTone; icon: string; label: string }> = {
  informational: { tone: 'unknown', icon: 'i', label: 'informational' },
  warning: { tone: 'warn', icon: '⚠', label: 'warning' },
  critical: { tone: 'bad', icon: '✕', label: 'critical' },
}

export function SeverityBadge({ severity }: { severity: EventSeverity }) {
  const spec = SEVERITY[severity]
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// CollectorStatus.state is a plain, extensible string on the wire
// (api/openapi.yaml's CollectorStatus schema; see the Go
// CollectorRunState doc comment for why it is deliberately not a closed
// enum there either) -- today's one collector reports only "running" and
// "not_configured", but a value this table has never seen must still get
// its own visible label and icon, distinguishable from both known states,
// rather than silently rendering as if it were one of them.
const COLLECTOR_STATE_TONE: Record<string, StatusTone> = {
  running: 'good',
  not_configured: 'unknown',
}

const COLLECTOR_STATE_ICON: Record<string, string> = {
  running: '●',
  not_configured: '–',
}

export function CollectorStatusBadge({ state }: { state: string }) {
  const tone = COLLECTOR_STATE_TONE[state] ?? 'unknown'
  const icon = COLLECTOR_STATE_ICON[state] ?? '?'
  return <StatusBadge tone={tone} icon={icon} label={state} />
}

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): a declared node's discovery
// verdict. "not_seen" and "unknown" are both non-good tones — an
// incomplete run's "unknown" must never read as fine (ADR-011), and
// "not_seen" is flagged, never silently indistinguishable from "present",
// per RES-008 D6's whole reason for existing.
const DISCOVERY_STATE: Record<DiscoveryState, { tone: StatusTone; icon: string; label: string }> = {
  present: { tone: 'good', icon: '●', label: 'seen by discovery' },
  not_seen: { tone: 'warn', icon: '⚠', label: 'not seen by the most recent discovery run' },
  unknown: { tone: 'unknown', icon: '?', label: 'discovery evidence unknown' },
  not_applicable: { tone: 'unknown', icon: '–', label: 'not declared' },
}

// discoveryState is typed as the closed DiscoveryState union at compile
// time, but the value actually rendered here came off the wire (contract
// ADR-020: within-a-major-version is additive-only, and clients must
// tolerate an unknown value rather than assume the union TypeScript sees
// is exhaustive at runtime). Indexing DISCOVERY_STATE directly and
// dereferencing the result crashed this component — and, since nothing
// caught it, unmounted the whole nodes list route — the moment a
// coordinator ever adds a fifth verdict. CollectorStatusBadge above
// already gets this right with `?? 'unknown'`; this brings the third
// sibling renderer in line with the other two.
// Track D seam D-3a/D-4: one crash-recovery record entry's own state.
// "unknown" renders its OWN badge here (never dark or blank -- D-3a
// criterion 14, this project's fifth encounter with absence of evidence
// read as evidence of absence) -- the CALLER (the Resolume view) is
// responsible for also rendering entry.reason alongside this badge, since
// this component only ever renders the state word, not the reason text.
const RESOLUME_RECOVERY_LAYER_STATE: Record<ResolumeRecoveryLayerState, { tone: StatusTone; icon: string; label: string }> = {
  clip: { tone: 'good', icon: '●', label: 'clip loaded' },
  dark: { tone: 'unknown', icon: '–', label: 'dark (no clip)' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
}

export function ResolumeRecoveryLayerStateBadge({ state }: { state: ResolumeRecoveryLayerState }) {
  const spec = RESOLUME_RECOVERY_LAYER_STATE[state] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized state (${String(state)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// One restore's own per-layer result (Track D seam D-3a/D-4).
const RESOLUME_RESTORE_RESULT: Record<ResolumeRecoveryRestoreResult, { tone: StatusTone; icon: string; label: string }> = {
  restored: { tone: 'good', icon: '●', label: 'restored' },
  skipped: { tone: 'unknown', icon: '–', label: 'skipped' },
  failed: { tone: 'bad', icon: '✕', label: 'failed' },
}

export function ResolumeRestoreResultBadge({ result }: { result: ResolumeRecoveryRestoreResult }) {
  const spec = RESOLUME_RESTORE_RESULT[result] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized result (${String(result)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

export function DeclarationBadge({ declared, discoveryState }: { declared: boolean; discoveryState: DiscoveryState }) {
  if (!declared) {
    return <StatusBadge tone="unknown" icon="–" label="not declared" />
  }
  const spec = DISCOVERY_STATE[discoveryState] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized discovery state (${String(discoveryState)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// One show.action's pre-show binding check (ADR-029). "unknown" is never
// a soft "ok" — the check could not be performed at all, distinct in tone
// from both "ok" and "broken".
const ACTION_BINDING_STATE: Record<ActionBindingState, { tone: StatusTone; icon: string; label: string }> = {
  ok: { tone: 'good', icon: '●', label: 'ok' },
  broken: { tone: 'bad', icon: '✕', label: 'broken' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
}

export function ActionBindingBadge({ state, reason }: { state: ActionBindingState; reason: string }) {
  const spec = ACTION_BINDING_STATE[state] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized state (${String(state)})`,
  }
  return (
    <span title={reason}>
      <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
    </span>
  )
}

// Track F seam F2 (RESTING-MODE.md §3): the night-session lifecycle's own
// ten states. Never collapsed into a generic health tone — "resting" is
// the show's NORMAL steady state, not a degraded one, so it gets 'good'
// exactly like "live"; only "inactive"/"stopped" (nothing running) reads
// as neutral/unknown.
const NIGHT_LIFECYCLE_STATE: Record<NightLifecycleState, { tone: StatusTone; icon: string; label: string }> = {
  inactive: { tone: 'unknown', icon: '–', label: 'inactive' },
  preparing: { tone: 'unknown', icon: '…', label: 'preparing' },
  preshow: { tone: 'unknown', icon: '…', label: 'preshow' },
  'transition-to-show': { tone: 'unknown', icon: '…', label: 'transitioning to show' },
  live: { tone: 'good', icon: '●', label: 'live' },
  'transition-to-resting': { tone: 'unknown', icon: '…', label: 'transitioning to resting' },
  'resting-intershow': { tone: 'good', icon: '●', label: 'resting (between shows)' },
  'end-of-night-resting': { tone: 'good', icon: '●', label: 'resting (end of night)' },
  'fading-out': { tone: 'unknown', icon: '…', label: 'fading out' },
  stopped: { tone: 'unknown', icon: '–', label: 'stopped' },
}

export function NightLifecycleBadge({ state }: { state: NightLifecycleState }) {
  const spec = NIGHT_LIFECYCLE_STATE[state] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized state (${String(state)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// NightPhaseEvidence.state / NightReadiness.state share one four-value
// vocabulary (recorded/unknown/not_configured/not_available). "recorded"
// alone says nothing about good/bad — a caller wanting the READINESS
// OUTCOME (ready/not_ready/unknown) uses NightReadinessOutcomeBadge below
// instead; this badge is only ever about whether evidence EXISTS at all.
// "not_configured" is deliberately neutral, never a warning (task
// instruction: "never renders as a warning and never as an error").
const NIGHT_EVIDENCE_STATE: Record<NightPhaseEvidenceState, { tone: StatusTone; icon: string; label: string }> = {
  recorded: { tone: 'good', icon: '●', label: 'recorded' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
  not_configured: { tone: 'unknown', icon: '–', label: 'not configured' },
  not_available: { tone: 'unknown', icon: '–', label: 'not available' },
}

export function NightPhaseEvidenceBadge({ state }: { state: NightPhaseEvidenceState }) {
  const spec = NIGHT_EVIDENCE_STATE[state] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized state (${String(state)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

const NIGHT_READINESS_OUTCOME: Record<NightReadinessOutcome, { tone: StatusTone; icon: string; label: string }> = {
  ready: { tone: 'good', icon: '●', label: 'ready' },
  not_ready: { tone: 'bad', icon: '✕', label: 'not ready' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
}

export function NightReadinessOutcomeBadge({ outcome }: { outcome: NightReadinessOutcome }) {
  const spec = NIGHT_READINESS_OUTCOME[outcome] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized outcome (${String(outcome)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

const NIGHT_READINESS_CHECK_STATE: Record<
  NightReadinessCheckState,
  { tone: StatusTone; icon: string; label: string }
> = {
  healthy: { tone: 'good', icon: '●', label: 'healthy' },
  degraded: { tone: 'warn', icon: '⚠', label: 'degraded' },
  failed: { tone: 'bad', icon: '✕', label: 'failed' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
  // Permanently not_verifiable is a structural fact about the check, not
  // a failure (NightReadinessCheck's own schema description) — 'unknown'
  // tone, never 'warn'/'bad'.
  not_verifiable: { tone: 'unknown', icon: '–', label: 'not verifiable' },
  // An absent OPTIONAL configuration is not a fault: not_configured is
  // excluded from the aggregate outcome exactly as not_verifiable is
  // (NightReadinessCheck's own schema description), so it carries the
  // same 'unknown' tone and never 'warn'/'bad'.
  not_configured: { tone: 'unknown', icon: '–', label: 'not configured' },
}

export function NightReadinessCheckBadge({ state }: { state: NightReadinessCheckState }) {
  const spec = NIGHT_READINESS_CHECK_STATE[state] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized state (${String(state)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// NightCue.state: the outbox row's own lifecycle, distinct from `outcome`
// below (ADR-031 decision 3: completed and confirmed must be visually
// distinct — this badge answers "did it run", not "did it work"). Review
// finding 3: `resolved` is a POSITION in the outbox lifecycle, not a
// success claim — the schema never collapses `state` into `outcome`, so a
// cue can be `state: 'resolved'` with `outcome: 'failed'`. `resolved`
// therefore stays neutral ('unknown' tone) exactly like every other
// non-terminal state here; only `outcome` below ever renders success or
// failure.
const NIGHT_CUE_STATE: Record<NightCueState, { tone: StatusTone; icon: string; label: string }> = {
  not_dispatched: { tone: 'unknown', icon: '–', label: 'not dispatched' },
  pending: { tone: 'unknown', icon: '…', label: 'pending' },
  dispatched: { tone: 'unknown', icon: '…', label: 'dispatched' },
  resolved: { tone: 'unknown', icon: '■', label: 'resolved' },
  ambiguous: { tone: 'warn', icon: '⚠', label: 'ambiguous' },
}

export function NightCueStateBadge({ state }: { state: NightCueState }) {
  const spec = NIGHT_CUE_STATE[state] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized state (${String(state)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// NightCue.outcome: "unconfirmed" is deliberately NEUTRAL ('unknown'
// tone), never 'warn' — review finding 4: ADR-031 decision 3's own
// worked example is a run that legitimately reports unconfirmed every
// time it runs perfectly (no expected response was ever declared), so
// painting it amber on every cycle teaches the operator the indicator
// means nothing, the exact outcome the ADR argues against. Every one of
// the six outcomes below has its own tone+icon PAIR, not just its own
// label — `failed`/`refused` no longer share bad+'✕', and
// `unconfirmed`/`unconfirmable` no longer share unknown+the same icon —
// so a viewer distinguishing by shape/color alone (not just reading
// text) can still tell all six apart.
const NIGHT_CUE_OUTCOME: Record<NightCueOutcome, { tone: StatusTone; icon: string; label: string }> = {
  confirmed: { tone: 'good', icon: '●', label: 'confirmed' },
  unconfirmed: { tone: 'unknown', icon: '~', label: 'unconfirmed' },
  unconfirmable: { tone: 'unknown', icon: '–', label: 'unconfirmable' },
  failed: { tone: 'bad', icon: '✕', label: 'failed' },
  refused: { tone: 'bad', icon: '⊘', label: 'refused' },
  ambiguous: { tone: 'warn', icon: '⚠', label: 'ambiguous' },
}

export function NightCueOutcomeBadge({ outcome }: { outcome: NightCueOutcome }) {
  const spec = NIGHT_CUE_OUTCOME[outcome] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized outcome (${String(outcome)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// TRACK-H-H2-SPEC.md §6: FPPPlaylistReadinessResponse.ready is a bare
// boolean, not an enum: this badge is the one place that boolean gets a
// tone+icon+label triple, same "never color alone" rule as every badge
// above. The failing CONDITION (which of §6's eight checks failed) gets
// its own badge below; this one only ever says ready or not.
export function FPPPlaylistReadinessBadge({ ready }: { ready: boolean }) {
  return ready ? (
    <StatusBadge tone="good" icon="●" label="ready" />
  ) : (
    <StatusBadge tone="bad" icon="✕" label="not ready" />
  )
}

const FPP_PLAYLIST_READINESS_FAILING_CONDITION: Record<
  FPPPlaylistReadinessFailingCondition,
  { tone: StatusTone; icon: string; label: string }
> = {
  'definition-missing': { tone: 'bad', icon: '✕', label: 'definition missing' },
  // Detected directly from the definition store (the plugin's periodic
  // re-scan/re-post), never requires FPP to have played anything since
  // the edit — see this condition's own doc comment in
  // fppreconcile/readiness.go.
  'definition-superseded': { tone: 'bad', icon: '✕', label: 'definition superseded' },
  'entry-not-in-definition': { tone: 'bad', icon: '✕', label: 'entry not in definition' },
  'entry-filename-mismatch': { tone: 'bad', icon: '✕', label: 'entry filename mismatch' },
  'cue-not-ready': { tone: 'bad', icon: '✕', label: 'cue not ready' },
  // An observation was received but could not establish identity — a
  // required check that ran and could not conclude anything, never
  // rendered as "ready" with a warning.
  'evidence-unavailable': { tone: 'bad', icon: '✕', label: 'evidence unavailable' },
  'observation-hash-mismatch': { tone: 'bad', icon: '✕', label: 'observation hash mismatch' },
  'node-render-unassigned': { tone: 'bad', icon: '✕', label: 'node render unassigned' },
  'exclusive-claim-conflict': { tone: 'bad', icon: '✕', label: 'exclusive claim conflict' },
  'node-catalog-stale': { tone: 'bad', icon: '✕', label: 'node catalog stale' },
  'audio-ltc-emitter-ambiguous': { tone: 'bad', icon: '✕', label: 'two LTC emitters' },
  'audio-target-unbound': { tone: 'bad', icon: '✕', label: 'audio target unbound' },
  'audio-target-unresolved': { tone: 'bad', icon: '✕', label: 'audio target unresolved' },
}

// Only rendered once `ready` is false: every failing condition gets its
// own distinguishable label, never collapsed into one generic "not
// ready". A stale import, a missing asset, an unresolved reference and an
// unbound playlist must each be tellable apart on sight.
export function FPPPlaylistReadinessFailingConditionBadge({
  condition,
}: {
  condition: FPPPlaylistReadinessFailingCondition
}) {
  const spec = FPP_PLAYLIST_READINESS_FAILING_CONDITION[condition] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized condition (${String(condition)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}

// TRACK-H-H2-SPEC.md §5: FPPPlaylistEntryReconciliationResponse.outcome:
// what the coordinator currently makes of one instance's latest accepted
// observation against the show's bindings. `resolved` is the only good
// outcome; every other value names a DIFFERENT reason it did not resolve,
// each with its own tone/icon/label so an operator distinguishes "we have
// heard nothing yet" (identity-unavailable) from "this entry names a
// Playlist that has never been bound" (unbound) from "the binding names a
// hash this instance is no longer reporting" (stale-import) from a
// genuine data problem (unknown-entry/evidence-mismatch/cross-show).
const FPP_PLAYLIST_RECONCILIATION_OUTCOME: Record<
  FPPPlaylistEntryReconciliationOutcome,
  { tone: StatusTone; icon: string; label: string }
> = {
  resolved: { tone: 'good', icon: '●', label: 'resolved' },
  'identity-unavailable': { tone: 'unknown', icon: '?', label: 'identity unavailable' },
  unbound: { tone: 'warn', icon: '⚠', label: 'unbound' },
  'stale-import': { tone: 'bad', icon: '✕', label: 'stale import' },
  'unknown-entry': { tone: 'bad', icon: '✕', label: 'unknown entry' },
  'evidence-mismatch': { tone: 'bad', icon: '✕', label: 'evidence mismatch' },
  'cross-show': { tone: 'bad', icon: '✕', label: 'cross-show' },
}

export function FPPPlaylistReconciliationOutcomeBadge({
  outcome,
}: {
  outcome: FPPPlaylistEntryReconciliationOutcome
}) {
  const spec = FPP_PLAYLIST_RECONCILIATION_OUTCOME[outcome] ?? {
    tone: 'unknown' as StatusTone,
    icon: '?',
    label: `unrecognized outcome (${String(outcome)})`,
  }
  return <StatusBadge tone={spec.tone} icon={spec.icon} label={spec.label} />
}
