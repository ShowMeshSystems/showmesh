/**
 * Pure, framework-free helpers for making sense of an FPPInstance's
 * `observations` list once it stops being 12 signals and becomes well over
 * a hundred (a K16-Max reports 48 port elements alone, each with several
 * signals). Everything here is presentation logic over data the coordinator
 * already sends — no new wire fields, per Step 5 spec section 6 ("The UI
 * needs no API change").
 *
 * Nothing here classifies health or invents a verdict: grouping only
 * decides WHERE a signal is displayed, never what it means. The state and
 * reason on each `Evidence` envelope are rendered verbatim by
 * `EvidenceValue`/`PortGrid`, exactly as ADR-011 requires.
 */
import type { Evidence } from './types'

// ---------------------------------------------------------------------
// Grouping (Step 5 spec section 6, "Grouping").
//
// Groups are derived from signal-ID prefixes, in this fixed priority
// order. A signal matching none of the known prefixes still renders --
// in 'other' -- rather than being silently dropped. That is the ADR-002
// lesson ("nodes are modeled by capabilities, not hardware types") one
// layer up: an unrecognized signal ID must not disappear just because
// nobody wrote a case for it yet.
// ---------------------------------------------------------------------

export type FppGroupId = 'playback' | 'controller' | 'ports' | 'sensors' | 'platform' | 'other'

export const FPP_GROUP_ORDER: readonly FppGroupId[] = [
  'playback',
  'controller',
  'ports',
  'sensors',
  'platform',
  'other',
]

export const FPP_GROUP_LABELS: Record<FppGroupId, string> = {
  playback: 'Playback',
  controller: 'Controller & network',
  ports: 'Pixel ports',
  sensors: 'Sensors',
  platform: 'Platform',
  other: 'Other',
}

// fpp.port.<key>.* AND fpp.ports.* (the plural summary signals
// fpp.ports.count / fpp.ports.blind_count / fpp.ports.decode_failed) both
// belong to the ports group -- they describe the same subsystem, just at
// different granularity.
const PORT_PREFIXES = ['fpp.port.', 'fpp.ports.']
const SENSOR_PREFIX = 'fpp.sensor.'
const PLAYBACK_PREFIXES = ['fpp.playlist.', 'fpp.sequence.', 'fpp.position.', 'fpp.song.', 'fpp.scheduler.']
const PLATFORM_PREFIXES = ['fpp.disk.', 'fpp.utilization.', 'fpp.os.']
const PLATFORM_EXACT = new Set(['fpp.platform', 'fpp.variant', 'fpp.kernel'])
// "Controller & network": everything spec section 3.1 calls "controller
// and network health", plus the pre-Step-5 instance-identity signals
// (reachable/version/mode/status/multisync/uptime) that don't belong to
// any of the other four buckets and are not obscure enough to bury in
// "other".
const CONTROLLER_PREFIXES = [
  'fpp.fppd.',
  'fpp.power.',
  'fpp.channel_inputs.',
  'fpp.channel_outputs.',
  'fpp.mqtt.',
  'fpp.warnings.',
  'fpp.multisync.',
  'fpp.uptime.',
]
const CONTROLLER_EXACT = new Set([
  'fpp.bridging',
  'fpp.branch',
  'fpp.uuid',
  'fpp.host_name',
  'fpp.volume',
  'fpp.reachable',
  'fpp.version',
  'fpp.mode',
  'fpp.status',
])

function startsWithAny(signal: string, prefixes: readonly string[]): boolean {
  return prefixes.some((prefix) => signal.startsWith(prefix))
}

/** Classify one signal ID into a display group. Pure and total: every string input produces a group, never throws, never returns undefined. */
export function classifyFppSignal(signal: string): FppGroupId {
  if (startsWithAny(signal, PORT_PREFIXES)) return 'ports'
  if (signal.startsWith(SENSOR_PREFIX)) return 'sensors'
  if (startsWithAny(signal, PLAYBACK_PREFIXES)) return 'playback'
  if (startsWithAny(signal, PLATFORM_PREFIXES) || PLATFORM_EXACT.has(signal)) return 'platform'
  if (startsWithAny(signal, CONTROLLER_PREFIXES) || CONTROLLER_EXACT.has(signal)) return 'controller'
  return 'other'
}

export interface FppSignalGroup {
  id: FppGroupId
  label: string
  observations: Evidence[]
}

/**
 * Buckets `observations` by `classifyFppSignal`, in `FPP_GROUP_ORDER`.
 * Only groups that actually have at least one observation are returned --
 * an instance with no sensors reports no "Sensors" section rather than an
 * empty one. Within each group, observations keep the order the API sent
 * them in (ascending by signal, per api/openapi.yaml's guarantee on
 * FPPInstance.observations), so this is a stable partition, not a re-sort.
 */
export function groupFppObservations(observations: readonly Evidence[]): FppSignalGroup[] {
  const buckets = new Map<FppGroupId, Evidence[]>()
  for (const observation of observations) {
    const id = classifyFppSignal(observation.signal)
    const list = buckets.get(id)
    if (list) {
      list.push(observation)
    } else {
      buckets.set(id, [observation])
    }
  }
  const groups: FppSignalGroup[] = []
  for (const id of FPP_GROUP_ORDER) {
    const items = buckets.get(id)
    if (items && items.length > 0) {
      groups.push({ id, label: FPP_GROUP_LABELS[id], observations: items })
    }
  }
  return groups
}

// ---------------------------------------------------------------------
// Ports (Step 5 spec section 6, "Ports").
//
// A port's <key> is opaque here -- Seam A derives it from FPP's `name`
// field (spec section 3.1) -- this module only ever parses it back out of
// the signal ID, never re-derives or assumes its shape beyond
// [a-z0-9_]+, matching observation.ValidateSignalID's segment rule.
// ---------------------------------------------------------------------

export type PortKind = 'output' | 'smart_receiver'

const PORT_FIELD_RE = /^fpp\.port\.([a-z0-9_]+)\.(kind|current_ma|enabled|status|bank|pixel_count)$/

export interface PortEntry {
  key: string
  kind: Evidence | undefined
  currentMa: Evidence | undefined
  enabled: Evidence | undefined
  status: Evidence | undefined
  bank: Evidence | undefined
  pixelCount: Evidence | undefined
}

/**
 * Reassembles per-port signals (`fpp.port.<key>.kind`,
 * `fpp.port.<key>.current_ma`, ...) into one entry per port key. A signal
 * that does not match the known per-port field shape is left out here --
 * NOT dropped from the UI, because `groupFppObservations` already put the
 * whole `fpp.port.*`/`fpp.ports.*` prefix in the "Pixel ports" group and
 * `FPPDetail` still renders every observation in that group; this
 * function only feeds the compact grid's per-port cells.
 */
export function buildPortEntries(observations: readonly Evidence[]): PortEntry[] {
  const byKey = new Map<string, PortEntry>()
  for (const observation of observations) {
    const match = PORT_FIELD_RE.exec(observation.signal)
    if (match === null) continue
    // Non-null: PORT_FIELD_RE has exactly two required capturing groups,
    // so a successful match always populates both.
    const key = match[1] as string
    const field = match[2] as string
    let entry = byKey.get(key)
    if (!entry) {
      entry = { key, kind: undefined, currentMa: undefined, enabled: undefined, status: undefined, bank: undefined, pixelCount: undefined }
      byKey.set(key, entry)
    }
    switch (field) {
      case 'kind':
        entry.kind = observation
        break
      case 'current_ma':
        entry.currentMa = observation
        break
      case 'enabled':
        entry.enabled = observation
        break
      case 'status':
        entry.status = observation
        break
      case 'bank':
        entry.bank = observation
        break
      case 'pixel_count':
        entry.pixelCount = observation
        break
      default:
        // Unreachable given PORT_FIELD_RE's capture group, but exhaustive
        // rather than silently ignoring a future field added to the regex
        // without a case here.
        break
    }
  }
  return [...byKey.values()].sort((a, b) => a.key.localeCompare(b.key, undefined, { numeric: true }))
}

/** `entry.kind`'s value, narrowed to the two values Seam A's contract declares -- or 'unrecognized' for anything else (including a missing `kind` signal), which the grid still renders rather than silently dropping the port. */
export function portKindOf(entry: PortEntry): PortKind | 'unrecognized' {
  const value = entry.kind?.value
  if (value === 'output' || value === 'smart_receiver') return value
  return 'unrecognized'
}

/** Convenience lookup used for the ports-group summary signals (`fpp.ports.count`, `fpp.ports.blind_count`, `fpp.ports.decode_failed`) that live alongside, not inside, the per-port entries. */
export function findObservation(observations: readonly Evidence[], signal: string): Evidence | undefined {
  return observations.find((observation) => observation.signal === signal)
}

// ---------------------------------------------------------------------
// Version skew (Step 5 spec section 6, "Version skew").
//
// Presentation of collected facts only -- this never ranks or judges a
// version, and it is not a health verdict. FPP-remote-01 deliberately
// runs a master build on this fleet, so disagreement is a stated
// condition, not something this function labels as wrong.
// ---------------------------------------------------------------------

export interface FleetVersionSummary {
  /** Distinct non-null `fpp.version` values seen, each with the instance IDs reporting it, in first-seen order. Only from instances this function considers reachable -- see isReachableInstance. */
  versions: { version: string; instanceIds: string[] }[]
  /** True when two or more distinct versions were reported. False when zero or one -- there is nothing to compare zero or one values against. */
  disagreement: boolean
}

/**
 * True when `instance` has a CURRENT, true `fpp.reachable` evidence entry.
 * Step 5 review finding 6/7: without this, a version pulled from an
 * unreachable or unknown-age source (the FPP-01 ghost's exact shape --
 * unknown_age throughout, per the retained-MQTT case
 * fppFleetFixtures.ts's `makeGhostFpp01Instance` reproduces) rendered as
 * confidently in the skew statement as a freshly-confirmed one from a
 * live host, and spec section 6 asked for "reachable instances"
 * specifically, not merely "instances with a non-empty version string".
 * `fpp.reachable` is deliberately the gate rather than the version
 * evidence's own state: it is the one signal this coordinator models as
 * "is this source currently answering for this instance at all" (see
 * mapping.go's healthCriticalSignals), and requiring it to be `current`
 * (not merely present) is what actually excludes a retained/unknown-age
 * replay rather than only an outright absence.
 */
function isReachableInstance(instance: { readonly observations: readonly Evidence[] }): boolean {
  const reachable = findObservation(instance.observations, 'fpp.reachable')
  return reachable !== undefined && reachable.state === 'current' && reachable.value === true
}

// ---------------------------------------------------------------------
// Next-item hazard (Step 8, docs/bench/fpp-command-vocabulary.md section
// 3.5): "Next Playlist Item" past the last item ENDS the playlist rather
// than wrapping or no-opping -- measured on both a 3-item and a 1-item
// bench playlist, and FPP answers "Next Item Playing" identically in
// both cases, so its own response text cannot be used to tell them
// apart. A control labelled only "Next" is a stop button whenever the
// playlist is on its last item, and this project's own standing rule
// (ADR-011, CLAUDE.md) is that a control must never render as safe on
// evidence it does not actually have -- so this function reports
// "unknown" rather than "not the last item" whenever it cannot answer
// with CURRENT evidence for both signals, and the caller
// (FPPNextPlaylistItemControl) is required to render that as a caution,
// not as silence.
// ---------------------------------------------------------------------

export interface NextItemHazard {
  /** True only when CURRENT numeric evidence for both fpp.playlist.index and fpp.playlist.count show the current item is the last one (index >= count > 0) -- capture section 3.5's own last-item condition. */
  knownLastItem: boolean
  /** True when there is not enough CURRENT evidence to decide either way -- absence of evidence is not evidence of "not last" (CLAUDE.md's own recurring rule, applied here). */
  unknown: boolean
  index: Evidence | undefined
  count: Evidence | undefined
}

function currentNumericValue(evidence: Evidence | undefined): number | undefined {
  return evidence !== undefined && evidence.state === 'current' && typeof evidence.value === 'number'
    ? evidence.value
    : undefined
}

/** Evaluates whether one more "Next Playlist Item" would end the show, from whatever fpp.playlist.index/fpp.playlist.count evidence `observations` currently carries. Pure and total. */
export function evaluateNextItemHazard(observations: readonly Evidence[]): NextItemHazard {
  const index = findObservation(observations, 'fpp.playlist.index')
  const count = findObservation(observations, 'fpp.playlist.count')
  const indexValue = currentNumericValue(index)
  const countValue = currentNumericValue(count)
  if (indexValue === undefined || countValue === undefined) {
    return { knownLastItem: false, unknown: true, index, count }
  }
  return { knownLastItem: countValue > 0 && indexValue >= countValue, unknown: false, index, count }
}

export function summarizeFleetVersions(
  instances: readonly { instanceId: string; observations: readonly Evidence[] }[],
): FleetVersionSummary {
  const order: string[] = []
  const byVersion = new Map<string, string[]>()
  for (const instance of instances) {
    if (!isReachableInstance(instance)) continue
    const versionEvidence = findObservation(instance.observations, 'fpp.version')
    const value = versionEvidence?.value
    if (typeof value !== 'string' || value === '') continue
    const existing = byVersion.get(value)
    if (existing) {
      existing.push(instance.instanceId)
    } else {
      byVersion.set(value, [instance.instanceId])
      order.push(value)
    }
  }
  const versions = order.map((version) => ({ version, instanceIds: byVersion.get(version) ?? [] }))
  return { versions, disagreement: versions.length > 1 }
}
