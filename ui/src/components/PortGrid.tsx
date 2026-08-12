import type { Evidence } from '../app/types'
import { buildPortEntries, findObservation, portKindOf, type PortEntry, type PortKind } from '../app/fppSignals'
import { STATE_ICON, STATE_LABEL, STATE_TONE, formatValue } from '../app/evidenceState'
import { StatusBadge } from './StatusBadge'

// The port set as a compact grid (Step 5 spec section 6, "Ports").
//
// The single most important thing this component does: a measured
// current, a smart-receiver BLIND SPOT, and a port whose current failed
// to collect must be visually distinct at a glance, and a blind spot must
// never look like a measured 0 mA. On the real fleet every `ma` currently
// reads 0 (the display is de-energized -- see CLAUDE.md/spec section 7),
// so "0 mA, measured" and "we cannot see this port at all" sit right next
// to each other and the only thing keeping them apart is this rendering.
//
// The three states are driven by `fpp.port.<key>.current_ma`'s own
// Evidence.state, not by re-deriving "is this a smart receiver" from
// whether a value is present -- that inference is exactly the mistake
// spec section 3.2 exists to rule out on the collector side, and doing it
// again here would reintroduce it one layer up.
type CurrentCellMode = 'measured' | 'blind_spot' | 'failed' | 'not_collected'

function currentCellMode(currentMa: Evidence | undefined): CurrentCellMode {
  if (currentMa === undefined) return 'not_collected'
  switch (currentMa.state) {
    case 'unsupported':
      return 'blind_spot'
    case 'collection_failed':
      return 'failed'
    case 'not_collected':
      return 'not_collected'
    default:
      // current / stale / unknown_age: a value is guaranteed non-null for
      // all three (EvidenceValue's own contract comment) -- "measured"
      // covers all of them, and the badge below still shows which one.
      return 'measured'
  }
}

const CELL_MODE_LABEL: Record<CurrentCellMode, string> = {
  measured: 'measured',
  blind_spot: 'blind spot',
  failed: 'failed to collect',
  not_collected: 'not collected',
}

function portLabel(key: string): string {
  return key.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
}

function AttributeLine({ label, evidence }: { label: string; evidence: Evidence | undefined }) {
  if (evidence === undefined) {
    return (
      <div className="port-cell__attr">
        <span className="text-muted">{label}</span> <span className="text-muted">not collected</span>
      </div>
    )
  }
  const hasValue = evidence.value !== null
  return (
    <div className="port-cell__attr">
      <span className="text-muted">{label}</span>{' '}
      <span className={hasValue ? undefined : 'text-muted'}>
        {hasValue ? formatValue(evidence.value as boolean | string | number, evidence.unit) : STATE_LABEL[evidence.state]}
      </span>
      {evidence.reason !== null && evidence.state !== 'current' && (
        <div className="evidence__reason">{evidence.reason}</div>
      )}
    </div>
  )
}

function PortCell({ entry }: { entry: PortEntry }) {
  const mode = currentCellMode(entry.currentMa)
  const label = portLabel(entry.key)
  const bank = entry.bank?.value
  return (
    <details className={`port-cell port-cell--${mode}`}>
      <summary className="port-cell__summary">
        <span className="port-cell__name">{label}</span>
        {typeof bank === 'string' && bank !== '' && <span className="port-cell__bank">{bank}</span>}
        <span className="port-cell__current">
          {mode === 'measured' && entry.currentMa !== undefined ? (
            <StatusBadge
              tone={STATE_TONE[entry.currentMa.state]}
              icon={STATE_ICON[entry.currentMa.state]}
              label={formatValue(entry.currentMa.value as boolean | string | number, entry.currentMa.unit)}
            />
          ) : (
            <StatusBadge
              tone={
                mode === 'blind_spot'
                  ? 'unknown'
                  : mode === 'failed'
                    ? 'bad'
                    : 'unknown'
              }
              icon={mode === 'blind_spot' ? '∅' : mode === 'failed' ? '✕' : '–'}
              label={CELL_MODE_LABEL[mode]}
            />
          )}
        </span>
      </summary>
      {entry.currentMa?.reason !== null && entry.currentMa?.reason !== undefined && (
        <p className="evidence__reason">{entry.currentMa.reason}</p>
      )}
      <AttributeLine label="Enabled" evidence={entry.enabled} />
      <AttributeLine label="Status" evidence={entry.status} />
      <AttributeLine label="Pixel count" evidence={entry.pixelCount} />
    </details>
  )
}

const KIND_SECTION_TITLE: Record<PortKind | 'unrecognized', string> = {
  output: 'Output ports',
  smart_receiver: 'Smart-receiver positions (no per-port current telemetry)',
  unrecognized: 'Ports with an unrecognized kind',
}

function PortKindSection({ kind, entries }: { kind: PortKind | 'unrecognized'; entries: PortEntry[] }) {
  if (entries.length === 0) return null
  return (
    <div className={`port-kind-section port-kind-section--${kind}`}>
      <h4 className="port-kind-section__title">
        {KIND_SECTION_TITLE[kind]} ({entries.length})
      </h4>
      <div className="port-grid">
        {entries.map((entry) => (
          <PortCell key={entry.key} entry={entry} />
        ))}
      </div>
    </div>
  )
}

export interface PortGridProps {
  /** The full "ports" signal group -- fpp.ports.count/blind_count/decode_failed plus every fpp.port.<key>.* observation. Nothing here filters that set further; every signal passed in is either summarized or placed in a cell. */
  observations: readonly Evidence[]
}

export function PortGrid({ observations }: PortGridProps) {
  const count = findObservation(observations, 'fpp.ports.count')
  const blindCount = findObservation(observations, 'fpp.ports.blind_count')
  const decodeFailed = findObservation(observations, 'fpp.ports.decode_failed')
  const entries = buildPortEntries(observations)

  const outputs = entries.filter((entry) => portKindOf(entry) === 'output')
  const smartReceivers = entries.filter((entry) => portKindOf(entry) === 'smart_receiver')
  const unrecognized = entries.filter((entry) => portKindOf(entry) === 'unrecognized')

  const countValue = count !== undefined && typeof count.value === 'number' ? count.value : null

  return (
    <div className="port-grid-wrap">
      {decodeFailed !== undefined && (
        <p className="evidence__reason evidence__reason--violation" role="alert">
          {decodeFailed.reason ?? 'One or more port elements could not be decoded into a port signal.'}
        </p>
      )}

      {count === undefined ? (
        <p className="text-muted">Port inventory has not been collected for this instance yet.</p>
      ) : countValue === null ? (
        // fpp.ports.count itself is an absence (collection_failed /
        // not_collected / unsupported) -- state that plainly rather than
        // guessing zero, per spec section 3.2's "a missing ma is never
        // 0" principle applied one level up to the whole port document.
        <p className="text-muted">
          Port inventory could not be read for this instance
          {count.reason !== null ? `: ${count.reason}` : '.'}
        </p>
      ) : countValue === 0 ? (
        // A true statement about a Pi with no cape (spec section 3.2) --
        // a fact, not an error, not a blank panel.
        <p className="text-muted">This host reports no pixel output ports.</p>
      ) : (
        <>
          <p className="text-muted">
            {countValue} port element{countValue === 1 ? '' : 's'} reported
            {blindCount !== undefined && typeof blindCount.value === 'number' && blindCount.value > 0
              ? `, ${blindCount.value} of which ${blindCount.value === 1 ? 'is a smart-receiver position' : 'are smart-receiver positions'} with no per-port current telemetry (pre-V5 receiver -- see each cell below)`
              : ''}
            .
          </p>
          <PortKindSection kind="output" entries={outputs} />
          <PortKindSection kind="smart_receiver" entries={smartReceivers} />
          <PortKindSection kind="unrecognized" entries={unrecognized} />
        </>
      )}
    </div>
  )
}
