import type { ControlPlaneState, EventSeverity, FPPHealth } from '../app/types'
import { StatusBadge, type StatusTone } from './StatusBadge'

// Domain-specific status badges, all built on the one StatusBadge
// primitive so the color-is-never-the-only-signal rule (OBSERVABILITY
// section 6.3) is inherited rather than re-implemented per domain.

const FPP_HEALTH: Record<FPPHealth, { tone: StatusTone; icon: string; label: string }> = {
  healthy: { tone: 'good', icon: '●', label: 'healthy' },
  degraded: { tone: 'warn', icon: '⚠', label: 'degraded' },
  failed: { tone: 'bad', icon: '✕', label: 'failed' },
  unknown: { tone: 'unknown', icon: '?', label: 'unknown' },
  suppressed: { tone: 'unknown', icon: '⏸', label: 'suppressed' },
}

export function FPPHealthBadge({ health }: { health: FPPHealth }) {
  const spec = FPP_HEALTH[health]
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
