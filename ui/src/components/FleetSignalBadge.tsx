import type { Evidence } from '../app/types'
import { STATE_ICON, STATE_LABEL, STATE_TONE, formatValue } from '../app/evidenceState'
import { StatusBadge } from './StatusBadge'

/**
 * A compact one-line rendering of a single Evidence envelope for the
 * Dashboard's fleet panels (Step 5 spec section 6, "Dashboard"), reusing
 * the exact same state->icon/tone/label mapping EvidenceValue uses for the
 * full detail view -- see EvidenceValue.tsx's export comment on why that
 * mapping lives in one place. Deliberately does not show an age line: the
 * Dashboard is an overview, not the drill-down (FPPDetail is, and every
 * signal shown here is also reachable there through the shared
 * EvidenceValue with full freshness).
 *
 * `evidence === undefined` (the signal was never even attempted, or this
 * fixture/instance predates it) renders exactly like a `not_collected`
 * Evidence would -- stated plainly, never rendered blank, matching this
 * component's callers' obligation not to skip a modeled subsystem with no
 * evidence.
 */
export function FleetSignalBadge({ evidence, label }: { evidence: Evidence | undefined; label?: string }) {
  if (evidence === undefined) {
    // Stated plainly even when a `label` prefix is given -- a badge
    // reading just "power bad" with no state word would be exactly the
    // "renders blank" failure this component exists to avoid; a reader
    // must see the word "not collected", not infer it from icon/tone
    // alone.
    const text = STATE_LABEL.not_collected
    return (
      <StatusBadge
        tone={STATE_TONE.not_collected}
        icon={STATE_ICON.not_collected}
        label={label ? `${label}: ${text}` : text}
      />
    )
  }
  const hasValue = evidence.value !== null
  const text = hasValue
    ? formatValue(evidence.value as boolean | string | number, evidence.unit)
    : STATE_LABEL[evidence.state]
  return <StatusBadge tone={STATE_TONE[evidence.state]} icon={STATE_ICON[evidence.state]} label={label ? `${label}: ${text}` : text} />
}
