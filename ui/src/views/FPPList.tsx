import { useModelContext } from '../app/ModelContext'
import { summarizeFleetVersions } from '../app/fppSignals'

// Formerly the standalone `/fpp` list (FPPList): Monitor's Fleet facet
// (Monitor.tsx / monitor/fleetRows.ts) now carries every FPP instance as
// a Fleet row with its own health and endpoint, one table across every
// resource kind rather than a per-kind list. The one fact that table
// cannot state per-row -- that the fleet's REPORTED `fpp.version` values
// disagree with each other -- keeps its own home here, presentation of
// collected facts only (never a verdict on which version is "right", and
// never folded into any one instance's health badge).
export function FPPVersionSkewNotice() {
  const model = useModelContext()
  const summary = summarizeFleetVersions(model.fpp)
  if (!summary.disagreement) return null
  return (
    <p className="t-small text-muted" role="status" style={{ marginTop: '10px' }}>
      Reported FPP versions do not agree across the fleet:{' '}
      {summary.versions.map((entry) => `${entry.version} (${entry.instanceIds.length})`).join(', ')}. This
      states what each instance reported, not a fault.
    </p>
  )
}
