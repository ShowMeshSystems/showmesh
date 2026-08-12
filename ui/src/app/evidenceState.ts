/**
 * The one shared mapping from `Evidence.state` to a display icon, tone,
 * and label, plus the one shared value formatter. Pulled out of
 * EvidenceValue.tsx (rather than exported alongside the component) so
 * that file stays component-only -- a file mixing component and
 * non-component exports breaks React Fast Refresh (react-refresh/only-
 * export-components) -- and so every other renderer of an Evidence
 * envelope (PortGrid's compact per-port cells, FleetSignalBadge's
 * fleet-panel rows) reuses exactly this mapping instead of re-deciding
 * icon/tone/label for the same six EvidenceState values a second time.
 * "If evidence rendering exists in more than one place, the two will
 * diverge and one of them will be wrong" (EvidenceValue.tsx's own header
 * comment) -- this module is what lets every renderer stay one place, even
 * though there is now more than one component doing the rendering.
 */
import type { EvidenceState } from './types'
import type { StatusTone } from '../components/StatusBadge'

export const STATE_LABEL: Record<EvidenceState, string> = {
  current: 'current',
  stale: 'stale',
  unknown_age: 'age unknown',
  not_collected: 'not collected',
  collection_failed: 'collection failed',
  unsupported: 'not supported',
}

export const STATE_ICON: Record<EvidenceState, string> = {
  current: '●', // filled circle
  stale: '⚠', // warning triangle
  unknown_age: '?',
  not_collected: '–', // en dash
  collection_failed: '✕', // heavy X
  unsupported: '∅', // empty set
}

export const STATE_TONE: Record<EvidenceState, StatusTone> = {
  current: 'good',
  stale: 'warn',
  unknown_age: 'unknown',
  not_collected: 'unknown',
  collection_failed: 'bad',
  unsupported: 'unknown',
}

export function formatValue(value: boolean | string | number, unit: string | null): string {
  const text = typeof value === 'boolean' ? (value ? 'true' : 'false') : String(value)
  return unit ? `${text} ${unit}` : text
}
