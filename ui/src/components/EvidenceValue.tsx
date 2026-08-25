import type { Evidence } from '../app/types'
import { ageMs, effectiveServerTimeIso, formatAge } from '../app/time'
import { STATE_ICON, STATE_LABEL, STATE_TONE, formatValue } from '../app/evidenceState'
import { StatusBadge } from './StatusBadge'

// The one shared evidence renderer (spec section 6.2): "if evidence
// rendering exists in more than one place, the two will diverge and one
// of them will be wrong." Every place in this UI that shows an Evidence
// envelope -- a node's hello/lastWill/heartbeat, an FPP instance's
// observations, a flat /observations entry -- renders it through this
// component and nothing else. Its state->icon/tone/label mapping and
// value formatter live in app/evidenceState.ts (not here) so PortGrid's
// and FleetSignalBadge's more compact renderings can reuse exactly the
// same mapping instead of a second copy that drifts.
//
// The rule this component exists to enforce (api/openapi.yaml's Evidence
// schema prose, spec section 6.2): `value` is non-null for `current`,
// `stale`, AND `unknown_age`. Only the three genuine absence states --
// `not_collected`, `collection_failed`, `unsupported` -- have a null
// value. A renderer that treats `unknown_age` as if there were no value
// is making exactly the reading error that state exists to prevent, so
// this component branches on `evidence.value === null`, never on
// `evidence.state !== 'current'`, to decide whether there is a value to
// show.

export interface EvidenceValueProps {
  evidence: Evidence
  /** model.serverTime -- ages are computed against this, never the browser clock. */
  serverTime: string | null
  /**
   * model.serverTimeReceivedAt -- the browser-clock instant `serverTime`
   * was captured. Combined with `now`, this advances the effective
   * reference time between responses (app/time.ts's
   * effectiveServerTimeIso) instead of freezing every evidence age at
   * whatever it read the moment the coordinator went quiet. Optional and
   * defaults to null, which falls back to today's behavior (a fixed
   * `serverTime` with no advancement) -- every caller that renders live
   * evidence should pass model.serverTimeReceivedAt.
   */
  serverTimeReceivedAt?: number | null
  /**
   * Whether the browser currently holds a live connection to the
   * coordinator (model.connection.kind === 'live'). When false, this
   * component adds an explicit as-of qualifier: the state badge above is
   * the coordinator's last-reported verdict, not a live one (OPERATOR-UI
   * section 7's "the coordinator cannot reach the thing being displayed"
   * vs. "the UI cannot reach the coordinator" distinction -- this is
   * squarely the second kind of staleness, layered on top of the age
   * line rather than replacing it). This never changes `evidence.state`
   * itself, which stays the coordinator's own verdict per ADR-011.
   * Defaults to true (no qualifier) so a caller that omits it -- as most
   * of this component's own tests do, deliberately, to exercise the
   * ordinary live-rendering path -- behaves exactly as before; both real
   * call sites (NodeDetail, FPPDetail) always pass it explicitly.
   */
  connected?: boolean
  /** Injectable for tests; defaults to Date.now(), like DataFreshnessNotice's `now` prop. */
  now?: number
  /** Optional human label for the signal, shown ahead of the value. */
  label?: string
}

export function EvidenceValue({
  evidence,
  serverTime,
  serverTimeReceivedAt = null,
  connected = true,
  now,
  label,
}: EvidenceValueProps) {
  const hasValue = evidence.value !== null
  const reference = effectiveServerTimeIso(serverTime, serverTimeReceivedAt, now ?? Date.now())
  const age = ageMs(evidence.observedAt, reference)

  let freshnessLine: string
  if (evidence.observedAt !== null) {
    freshnessLine = age !== null ? `observed ${formatAge(age)}` : 'observed at an unknown time relative to now'
  } else if (evidence.collectedAt !== null) {
    // collectedAt is collection bookkeeping, never evidence of the
    // subject's own state (spec section 5.3) -- worded so it cannot be
    // mistaken for a freshness answer about the value itself.
    const collectedAge = ageMs(evidence.collectedAt, reference)
    freshnessLine =
      collectedAge !== null
        ? `observation time unknown; the coordinator last attempted collection ${formatAge(collectedAge)}`
        : 'observation time unknown; collection bookkeeping time unknown'
  } else {
    freshnessLine = 'no collection has ever been attempted'
  }

  const isCurrent = evidence.state === 'current'
  // ADR-011: only "current" ever collapses to one line, and even it keeps
  // its freshness text visible. Every other state keeps the full block
  // below and gets a louder treatment instead, via these modifier classes
  // (root class + a tone class off the existing STATE_TONE mapping) --
  // never a quieter or hidden one.
  const rootClassName = isCurrent
    ? 'evidence'
    : `evidence evidence--attention evidence--tone-${STATE_TONE[evidence.state]}`

  return (
    <div className={rootClassName}>
      <div className="evidence__row">
        {label !== undefined && <span className="text-muted">{label}</span>}
        <span className={hasValue ? 'evidence__value' : 'evidence__value--absent'}>
          {hasValue ? formatValue(evidence.value as boolean | string | number, evidence.unit) : 'no value'}
        </span>
        <StatusBadge
          tone={STATE_TONE[evidence.state]}
          icon={STATE_ICON[evidence.state]}
          label={STATE_LABEL[evidence.state]}
        />
        {/* Compact case: the same freshnessLine text as the full block
            below, just inline after the badge instead of on its own row. */}
        {isCurrent && <span className="evidence__age evidence__age--inline">{freshnessLine}</span>}
      </div>
      {!isCurrent && <span className="evidence__age">{freshnessLine}</span>}
      {!connected && (
        // Requirement 2 of the disconnected-evidence fix (OPERATOR-UI
        // section 7): the badge above is the coordinator's own verdict
        // (never recomputed or replaced here -- ADR-011 puts provenance
        // on the coordinator, not this client), but while the browser
        // itself is disconnected that verdict can only be as of the last
        // time the coordinator was actually reachable. Said explicitly
        // rather than left for the operator to infer from the global
        // connection banner alone, since a panel scrolled away from that
        // banner would otherwise carry no local signal that its own
        // badge is not live.
        <span className="evidence__as-of" role="note">
          Not live — this is the coordinator's state as of last contact, not a live verdict.
        </span>
      )}
      {!isCurrent &&
        (evidence.reason !== null ? (
          <span className="evidence__reason">{evidence.reason}</span>
        ) : (
          // D4: the contract (api/openapi.yaml's Evidence schema prose)
          // guarantees reason is non-null whenever state is not
          // "current". Before this branch existed, a coordinator that
          // violated that guarantee produced `evidence.reason !== null`
          // === false and the whole reason line silently disappeared --
          // a stale/failed/unsupported badge with no explanation, reading
          // as an ordinary state rather than as the contract violation it
          // actually is. Surface the violation instead of absorbing it.
          <span className="evidence__reason evidence__reason--violation" role="alert">
            Contract violation: the coordinator reported state "{evidence.state}" with no reason,
            but reason is guaranteed whenever state is not "current".
          </span>
        ))}
    </div>
  )
}
