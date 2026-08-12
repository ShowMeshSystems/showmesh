/**
 * Fleet-level aggregation for the Dashboard's four new signal-group panels
 * (Step 5 spec section 6, "Dashboard": playback state, controller health,
 * pixel current, network/MQTT state). Pure functions over the model's
 * `fpp` list, framework-free like fppSignals.ts, so they can be unit
 * tested without rendering anything.
 *
 * These never compute a verdict -- they total up numbers and count how
 * many instances answered at all. `instance.health` (the coordinator's
 * own verdict) is the only health judgement this UI ever shows; these
 * aggregates are informational counts alongside it, matching STEP-5
 * section 5.3's own reasoning for keeping fpp.warnings.* out of health.
 */
import type { Evidence } from './types'
import { findObservation } from './fppSignals'

interface FppInstanceLike {
  readonly instanceId: string
  readonly observations: readonly Evidence[]
}

export interface FleetWarningsTotal {
  /** Sum of fpp.warnings.count across every instance that reported a numeric value. Includes stale/age-unknown contributions -- Evidence's own contract makes a value legitimate for current, stale, AND unknown_age (EvidenceValue.tsx's header comment) -- so `instancesStaleOrUnknownAge` is how a caller surfaces that some of this total may not be freshly confirmed, rather than this function silently excluding it. */
  total: number
  /** Instances whose fpp.warnings.count carried a numeric value. */
  instancesReporting: number
  /** Of `instancesReporting`, how many carried a NON-current state (stale or unknown_age) -- Step 5 review finding 6: a fleet total that sums these in with no distinction reads as equally fresh as one built entirely from current evidence. */
  instancesStaleOrUnknownAge: number
  /** Instances where fpp.warnings.count is absent or non-numeric (not_collected, collection_failed, unsupported, or simply never observed) -- excluded from `total`, not assumed to be zero. */
  instancesUnknown: number
}

export function summarizeFleetWarnings(instances: readonly FppInstanceLike[]): FleetWarningsTotal {
  let total = 0
  let instancesReporting = 0
  let instancesStaleOrUnknownAge = 0
  let instancesUnknown = 0
  for (const instance of instances) {
    const count = findObservation(instance.observations, 'fpp.warnings.count')
    if (count !== undefined && typeof count.value === 'number') {
      total += count.value
      instancesReporting += 1
      if (count.state === 'stale' || count.state === 'unknown_age') {
        instancesStaleOrUnknownAge += 1
      }
    } else {
      instancesUnknown += 1
    }
  }
  return { total, instancesReporting, instancesStaleOrUnknownAge, instancesUnknown }
}

export interface FleetPortsTotal {
  /** Sum of fpp.ports.count across every instance that reported a numeric value -- includes instances reporting 0 (a real fact: a Pi with no cape), which is why this is tracked separately from instancesUnknown. */
  totalPorts: number
  /**
   * Sum of fpp.ports.blind_count across instances that reported a NUMERIC
   * blind_count. This is a PARTIAL sum whenever `instancesBlindCountUnknown`
   * is nonzero -- see that field's own comment; it is never silently
   * padded with an assumed 0 for an instance that did not answer.
   */
  totalBlind: number
  instancesReporting: number
  /** Of `instancesReporting`, how many carried a NON-current fpp.ports.count state (stale or unknown_age) -- Step 5 review finding 6: a fleet total that sums these in with no distinction reads as equally fresh as one built entirely from current evidence. */
  instancesStaleOrUnknownAge: number
  /** fpp.ports.count absent, or present but not a number (an absence state). */
  instancesUnknown: number
  /** Of the reporting instances, how many reported exactly 0 -- a fact (no cape), not folded into instancesUnknown. */
  instancesWithNoPorts: number
  /**
   * Of `instancesReporting`, how many had no numeric fpp.ports.blind_count
   * at all. Step 5 review finding 7: this function used to fold that
   * absence straight into `totalBlind` as a bare 0 contribution -- the
   * exact "absent becomes zero" shape this whole step exists to
   * eliminate, blessed by a test that asserted 0 was correct rather than
   * that "unknown" was. A caller must state this count rather than
   * implying `totalBlind` is a confirmed, complete total whenever it is
   * nonzero.
   */
  instancesBlindCountUnknown: number
}

export function summarizeFleetPorts(instances: readonly FppInstanceLike[]): FleetPortsTotal {
  let totalPorts = 0
  let totalBlind = 0
  let instancesReporting = 0
  let instancesStaleOrUnknownAge = 0
  let instancesUnknown = 0
  let instancesWithNoPorts = 0
  let instancesBlindCountUnknown = 0
  for (const instance of instances) {
    const count = findObservation(instance.observations, 'fpp.ports.count')
    if (count !== undefined && typeof count.value === 'number') {
      instancesReporting += 1
      totalPorts += count.value
      if (count.state === 'stale' || count.state === 'unknown_age') {
        instancesStaleOrUnknownAge += 1
      }
      if (count.value === 0) instancesWithNoPorts += 1
      const blind = findObservation(instance.observations, 'fpp.ports.blind_count')
      if (blind !== undefined && typeof blind.value === 'number') {
        totalBlind += blind.value
      } else {
        instancesBlindCountUnknown += 1
      }
    } else {
      instancesUnknown += 1
    }
  }
  return {
    totalPorts,
    totalBlind,
    instancesReporting,
    instancesStaleOrUnknownAge,
    instancesUnknown,
    instancesWithNoPorts,
    instancesBlindCountUnknown,
  }
}
