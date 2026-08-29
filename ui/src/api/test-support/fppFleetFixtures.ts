/**
 * Realistic FPP fixtures, translated from the real read-only captures at
 * `<SCRATCH>/probe/{main,remote01,remote04}_{fppd_status,fppd_ports,system_info}.json`
 * (2026-08-11), into Evidence-shaped observations matching the Step 5 spec
 * section 3.1 signal vocabulary.
 *
 * Deployment identity was substituted on 2026-08-14: host names, addresses and
 * board serials are placeholders drawn from the RFC 5737 documentation range,
 * using the same mapping as the Go captures (see
 * `internal/coordinator/collector/fpp/testdata/README.md`). Shape was not
 * touched. Nothing below reads a host name back to prove anything; the names
 * are labels, and `fpp.host_name` is set here but asserted nowhere.
 *
 * What the shape does carry intentionally does NOT invent a tidier fleet than
 * the real one:
 *
 * - fpp-player (player, Raspberry Pi) has an EMPTY ports array -- zero pixel
 *   output ports, a fact, not an error (spec section 3.2).
 * - fpp-remote-a (K16A-B) has 16 real output ports + 16 smart-receiver
 *   positions with no `ma` key at all -- the pre-V5 blind spot.
 * - fpp-remote-b (K16-Max) has 16 real output ports + 32 smart-receiver
 *   positions, reproducing the exact bank/name/row/col layout from the
 *   real capture (see this file's `REMOTE04_RAW_PORTS`), because "48
 *   ports" alone does not exercise this seam's grouping the way the real,
 *   irregular bank layout does.
 * - fpp-remote-a deliberately runs a master build
 *   ("9.x-master-822-g56515e4d") while the other two run "9.4" -- the
 *   real version-skew condition this fleet has, not a constructed one.
 * - Every `ma` on every real port reads 0, because the display is
 *   de-energized (CLAUDE.md / spec section 7). This is reproduced
 *   faithfully -- these fixtures do NOT invent a nonzero reading, because
 *   that would be claiming pixel-current verification that has not
 *   happened.
 *
 * The fpp-ghost "ghost" fixture is NOT from a REST capture (fpp-ghost is not in
 * the reference installation) -- it reproduces the MQTT-broker finding
 * from spec section 1.2: a retained, plausible-looking `fppd_status` of
 * completely unknown age, on a host absent from the reference fleet.
 *
 * One further scenario -- a port whose current failed to collect, as
 * opposed to a smart-receiver blind spot -- has no real capture to draw
 * from (nothing in the reference fleet has actually failed mid-poll).
 * `remote04InstanceWithAFailedPortReading` constructs that one condition
 * on top of the real remote-04 base, clearly named and commented as
 * constructed, per this step's section 7 rule against claiming
 * verification that has not happened.
 */
import type { Evidence, FPPInstance } from '../domain'

export const FLEET_NOW = '2026-08-11T16:20:00.000Z'

function normalizePortKey(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
}

interface EvidenceOverrides {
  observedAt?: string | null
  collectedAt?: string | null
  source?: string
  unit?: string | null
}

function measured(signal: string, value: boolean | string | number, overrides: EvidenceOverrides = {}): Evidence {
  const observedAt = overrides.observedAt === undefined ? FLEET_NOW : overrides.observedAt
  return {
    signal,
    value,
    unit: overrides.unit ?? null,
    state: 'current',
    reason: null,
    observedAt,
    collectedAt: overrides.collectedAt ?? observedAt ?? FLEET_NOW,
    source: overrides.source ?? 'fpp-rest',
    quality: 'direct',
    validForSeconds: 30,
  }
}

function unsupported(signal: string, reason: string, overrides: EvidenceOverrides = {}): Evidence {
  return {
    signal,
    value: null,
    unit: overrides.unit ?? null,
    state: 'unsupported',
    reason,
    observedAt: null,
    collectedAt: overrides.collectedAt ?? FLEET_NOW,
    source: overrides.source ?? 'fpp-rest',
    quality: 'direct',
    validForSeconds: null,
  }
}

function collectionFailed(signal: string, reason: string, overrides: EvidenceOverrides = {}): Evidence {
  return {
    signal,
    value: null,
    unit: overrides.unit ?? null,
    state: 'collection_failed',
    reason,
    observedAt: null,
    collectedAt: overrides.collectedAt ?? FLEET_NOW,
    source: overrides.source ?? 'fpp-rest',
    quality: 'direct',
    validForSeconds: null,
  }
}

// ---------------------------------------------------------------------
// Ports: real raw shapes from the captures, translated per spec section
// 3.1/3.2's rules (a missing `ma` is Unsupported, never 0; `pixelCount` is
// absent everywhere on this fleet, so always Unsupported).
// ---------------------------------------------------------------------

interface RawOutputPort {
  name: string
  bank: string
  enabled: boolean
  status: boolean
  ma: number
}

interface RawSmartReceiverPort {
  name: string
}

function portObservations(outputs: RawOutputPort[], smartReceivers: RawSmartReceiverPort[]): Evidence[] {
  const observations: Evidence[] = []
  observations.push(measured('fpp.ports.count', outputs.length + smartReceivers.length))
  observations.push(measured('fpp.ports.blind_count', smartReceivers.length))
  for (const port of outputs) {
    const key = normalizePortKey(port.name)
    observations.push(measured(`fpp.port.${key}.kind`, 'output'))
    // Every real `ma` reads 0 -- the display is de-energized. This is
    // reproduced exactly, not smoothed over: see this file's header
    // comment and spec section 7.
    observations.push(measured(`fpp.port.${key}.current_ma`, port.ma, { unit: 'milliamps' }))
    observations.push(measured(`fpp.port.${key}.enabled`, port.enabled))
    observations.push(measured(`fpp.port.${key}.status`, port.status))
    observations.push(measured(`fpp.port.${key}.bank`, port.bank))
    observations.push(
      unsupported(
        `fpp.port.${key}.pixel_count`,
        "pixelCount absent from this FPP's port document; the pixel-count operation has never been run on this host",
      ),
    )
  }
  for (const port of smartReceivers) {
    const key = normalizePortKey(port.name)
    observations.push(measured(`fpp.port.${key}.kind`, 'smart_receiver'))
    observations.push(
      unsupported(
        `fpp.port.${key}.current_ma`,
        'smart receiver position: pre-V5 receivers report no per-port current',
        { unit: 'milliamps' },
      ),
    )
    observations.push(unsupported(`fpp.port.${key}.enabled`, 'smart receiver position: no `enabled` key on this element'))
    observations.push(unsupported(`fpp.port.${key}.status`, 'smart receiver position: no `status` key on this element'))
    observations.push(unsupported(`fpp.port.${key}.bank`, 'smart receiver position: no `bank` key on this element'))
    observations.push(
      unsupported(
        `fpp.port.${key}.pixel_count`,
        "pixelCount absent from this FPP's port document; the pixel-count operation has never been run on this host",
      ),
    )
  }
  return observations
}

// fpp-player: the real capture's ports array is `[]`. Zero elements, zero
// blind spots -- see PORTS section of the Step 5 spec: "a true statement
// about a Pi with no cape", not an error.
const MAIN_PORTS = portObservations([], [])

// fpp-remote-a (K16A-B): 16 real output ports (bank "Ports 1-4"..."Ports
// 21-24" per the capture) + 16 smart-receiver positions (Ports 17-32).
const REMOTE01_OUTPUTS: RawOutputPort[] = [1, 2, 3, 4].map((n) => ({ name: `Port ${n}`, bank: 'Ports 1-4', enabled: false, status: true, ma: 0 }))
REMOTE01_OUTPUTS.push(...[5, 6, 7, 8].map((n) => ({ name: `Port ${n}`, bank: 'Ports 5-8', enabled: false, status: true, ma: 0 })))
REMOTE01_OUTPUTS.push(...[9, 10, 11, 12].map((n) => ({ name: `Port ${n}`, bank: 'Ports 13-16', enabled: false, status: true, ma: 0 })))
REMOTE01_OUTPUTS.push(...[13, 14, 15, 16].map((n) => ({ name: `Port ${n}`, bank: 'Ports 13-16', enabled: false, status: true, ma: 0 })))
const REMOTE01_SMART: RawSmartReceiverPort[] = Array.from({ length: 16 }, (_, i) => ({ name: `Port ${17 + i}` }))
const REMOTE01_PORTS = portObservations(REMOTE01_OUTPUTS, REMOTE01_SMART)

// fpp-remote-b (K16-Max): the real capture's exact 16 output ports (banks
// "Ports 1-4", "Ports 5-8", "Ports 17-20", "Ports 21-24" -- non-contiguous
// per spec section 1.1) plus its exact 32 smart-receiver positions (Ports
// 17-48).
const REMOTE04_OUTPUTS: RawOutputPort[] = [
  ...[1, 2, 3, 4].map((n) => ({ name: `Port ${n}`, bank: 'Ports 1-4', enabled: false, status: true, ma: 0 })),
  ...[5, 6, 7, 8].map((n) => ({ name: `Port ${n}`, bank: 'Ports 5-8', enabled: false, status: true, ma: 0 })),
  ...[9, 10, 11, 12].map((n) => ({ name: `Port ${n}`, bank: 'Ports 17-20', enabled: false, status: true, ma: 0 })),
  ...[13, 14, 15, 16].map((n) => ({ name: `Port ${n}`, bank: 'Ports 21-24', enabled: false, status: true, ma: 0 })),
]
const REMOTE04_SMART: RawSmartReceiverPort[] = Array.from({ length: 32 }, (_, i) => ({ name: `Port ${17 + i}` }))
const REMOTE04_PORTS = portObservations(REMOTE04_OUTPUTS, REMOTE04_SMART)

// ---------------------------------------------------------------------
// Status/system-info-derived signals, translated per spec section 3.1's
// table directly from the captured documents' actual field values.
// ---------------------------------------------------------------------

function commonSensorObservations(sensors: { key: string; value: number; type: string }[]): Evidence[] {
  const observations: Evidence[] = []
  for (const sensor of sensors) {
    observations.push(measured(`fpp.sensor.${sensor.key}.value`, sensor.value))
    observations.push(measured(`fpp.sensor.${sensor.key}.type`, sensor.type))
  }
  return observations
}

/** fpp-player: player mode, real capture's fppd_status/system_info fields. */
export function makeMainInstance(overrides: Partial<FPPInstance> = {}): FPPInstance {
  const observations: Evidence[] = [
    measured('fpp.reachable', true),
    measured('fpp.version', '9.4'),
    measured('fpp.mode', 'player'),
    measured('fpp.status', 'idle'),
    // current_playlist.playlist is "" (a real value) on this capture, so
    // fpp.playlist.name falls back to nothing usable either -- rendered
    // as the real empty string per spec section 3.1's "empty string is a
    // real value" note on the sibling fpp.song.name field.
    measured('fpp.playlist.name', ''),
    measured('fpp.sequence.name', ''),
    measured('fpp.position.seconds', 0),
    measured('fpp.position.remaining.seconds', 0),
    measured('fpp.multisync.enabled', true),
    measured('fpp.multisync.systems', 0),
    measured('fpp.scheduler.status', 'idle'),
    measured('fpp.uptime.seconds', 12432),
    measured('fpp.song.name', ''),
    measured('fpp.playlist.repeat_mode', 0),
    measured('fpp.playlist.index', 0),
    measured('fpp.playlist.count', 0),
    measured('fpp.playlist.type', ''),
    measured('fpp.scheduler.enabled', true),
    measured('fpp.scheduler.next_playlist', 'No playlist scheduled.'),
    measured('fpp.scheduler.next_start_time', 0),
    unsupported('fpp.media.filename', 'host is in player mode; FPP does not report media_filename'),
    unsupported('fpp.position.elapsed.seconds', 'host is in player mode; FPP does not report seconds_elapsed'),
    measured('fpp.fppd.state', 'running'),
    measured('fpp.power.bad', false),
    measured('fpp.bridging', false),
    measured('fpp.channel_inputs.enabled', false),
    measured('fpp.channel_outputs.enabled', false),
    measured('fpp.branch', 'v9.4'),
    measured('fpp.uuid', 'M1-0000000000000001'),
    measured('fpp.host_name', 'fpp-player'),
    measured('fpp.volume', 80),
    measured('fpp.mqtt.configured', true),
    measured('fpp.mqtt.connected', true),
    measured('fpp.warnings.count', 1),
    measured('fpp.warnings.summary', 'A Log Level is set to Debug'),
    ...commonSensorObservations([{ key: 'cpu', value: 51.54, type: 'Temperature' }]),
    measured('fpp.os.version', 'v2025-11'),
    measured('fpp.os.release', 'Debian GNU/Linux 12 (bookworm)'),
    measured('fpp.platform', 'Raspberry Pi'),
    measured('fpp.variant', 'Raspberry Pi 3 Model B Plus Rev 1.3'),
    measured('fpp.kernel', '6.12.34-v7+'),
    measured('fpp.utilization.cpu', 8.5, { unit: 'percent' }),
    measured('fpp.utilization.memory', 41.2, { unit: 'percent' }),
    measured('fpp.disk.media.free_bytes', 9_800_000_000, { unit: 'bytes' }),
    measured('fpp.disk.media.total_bytes', 15_600_000_000, { unit: 'bytes' }),
    measured('fpp.disk.root.free_bytes', 9_800_000_000, { unit: 'bytes' }),
    measured('fpp.disk.root.total_bytes', 15_600_000_000, { unit: 'bytes' }),
    ...MAIN_PORTS,
  ]
  return {
    instanceId: 'fpp-main',
    endpoint: 'http://192.0.2.10',
    health: 'healthy',
    observations,
    lastPollAt: FLEET_NOW,
    lastPollError: null,
    instanceUuid: null,
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
    ...overrides,
  }
}

/** fpp-remote-a (K16A-B, BeagleBone Black): remote mode, real capture's fields -- including its deliberate master-branch version. */
export function makeRemote01Instance(overrides: Partial<FPPInstance> = {}): FPPInstance {
  const observations: Evidence[] = [
    measured('fpp.reachable', true),
    measured('fpp.version', '9.x-master-822-g56515e4d'),
    measured('fpp.mode', 'remote'),
    measured('fpp.status', 'idle'),
    measured('fpp.playlist.name', ''),
    measured('fpp.sequence.name', ''),
    measured('fpp.position.seconds', 0),
    measured('fpp.position.remaining.seconds', 0),
    measured('fpp.multisync.enabled', false),
    measured('fpp.multisync.systems', 0),
    unsupported('fpp.scheduler.status', 'host is in remote mode; FPP does not report a scheduler'),
    measured('fpp.uptime.seconds', 105935),
    measured('fpp.song.name', ''),
    unsupported('fpp.playlist.repeat_mode', 'host is in remote mode; FPP does not report repeat_mode'),
    unsupported('fpp.playlist.index', 'host is in remote mode; FPP does not report current_playlist'),
    unsupported('fpp.playlist.count', 'host is in remote mode; FPP does not report current_playlist'),
    unsupported('fpp.playlist.type', 'host is in remote mode; FPP does not report current_playlist'),
    unsupported('fpp.scheduler.enabled', 'host is in remote mode; FPP does not report a scheduler'),
    unsupported('fpp.scheduler.next_playlist', 'host is in remote mode; FPP does not report a scheduler'),
    unsupported('fpp.scheduler.next_start_time', 'host is in remote mode; FPP does not report a scheduler'),
    measured('fpp.media.filename', ''),
    measured('fpp.position.elapsed.seconds', 0),
    measured('fpp.fppd.state', 'running'),
    measured('fpp.power.bad', false),
    measured('fpp.bridging', false),
    measured('fpp.channel_inputs.enabled', true),
    measured('fpp.channel_outputs.enabled', true),
    measured('fpp.branch', 'master'),
    measured('fpp.uuid', 'M1-000000000002'),
    measured('fpp.host_name', 'fpp-remote-a'),
    measured('fpp.volume', 70),
    measured('fpp.mqtt.configured', true),
    measured('fpp.mqtt.connected', true),
    measured('fpp.warnings.count', 3),
    measured(
      'fpp.warnings.summary',
      'Cannot Ping ArtNet Channel Data Target 192.0.2.20 Ethernet_; Cannot Ping DDP Channel Data Target 192.0.2.21 wled-example; FPP Remote Monitoring could not authenticate.  Try Re-Logging In.',
    ),
    ...commonSensorObservations([
      { key: 'temp1', value: 44.0, type: 'Temperature' },
      { key: 'temp2', value: 47.8, type: 'Temperature' },
      { key: 'vin1', value: 12.35, type: 'Voltage' },
      { key: 'vin2', value: 12.35, type: 'Voltage' },
      { key: 'vin3', value: 12.39, type: 'Voltage' },
      { key: 'vin4', value: 12.39, type: 'Voltage' },
    ]),
    measured('fpp.os.version', 'v2025-11'),
    measured('fpp.os.release', 'Debian GNU/Linux 12 (bookworm)'),
    measured('fpp.platform', 'BeagleBone Black'),
    measured('fpp.variant', 'BeagleBone Black'),
    measured('fpp.kernel', '6.15.0-fpp14'),
    measured('fpp.utilization.cpu', 46.15, { unit: 'percent' }),
    measured('fpp.utilization.memory', 30.47, { unit: 'percent' }),
    measured('fpp.disk.media.free_bytes', 113_235_705_856, { unit: 'bytes' }),
    measured('fpp.disk.media.total_bytes', 125_953_245_184, { unit: 'bytes' }),
    measured('fpp.disk.root.free_bytes', 113_235_705_856, { unit: 'bytes' }),
    measured('fpp.disk.root.total_bytes', 125_953_245_184, { unit: 'bytes' }),
    ...REMOTE01_PORTS,
  ]
  return {
    instanceId: 'fpp-remote-01',
    endpoint: 'http://192.0.2.11',
    health: 'healthy',
    observations,
    lastPollAt: FLEET_NOW,
    lastPollError: null,
    instanceUuid: null,
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
    ...overrides,
  }
}

/** fpp-remote-b (K16-Max, PocketBeagle2): remote mode, real capture's fields. This capture's REST status omits `warnings` entirely (spec section 3.4) -- modeled as Unsupported per the collector-side rule this fixture mirrors on the UI side. */
export function makeRemote04Instance(overrides: Partial<FPPInstance> = {}): FPPInstance {
  const observations: Evidence[] = [
    measured('fpp.reachable', true),
    measured('fpp.version', '9.4'),
    measured('fpp.mode', 'remote'),
    measured('fpp.status', 'idle'),
    measured('fpp.playlist.name', ''),
    measured('fpp.sequence.name', ''),
    measured('fpp.position.seconds', 0),
    measured('fpp.position.remaining.seconds', 0),
    measured('fpp.multisync.enabled', false),
    measured('fpp.multisync.systems', 0),
    unsupported('fpp.scheduler.status', 'host is in remote mode; FPP does not report a scheduler'),
    measured('fpp.uptime.seconds', 105974),
    measured('fpp.song.name', ''),
    unsupported('fpp.playlist.repeat_mode', 'host is in remote mode; FPP does not report repeat_mode'),
    unsupported('fpp.playlist.index', 'host is in remote mode; FPP does not report current_playlist'),
    unsupported('fpp.playlist.count', 'host is in remote mode; FPP does not report current_playlist'),
    unsupported('fpp.playlist.type', 'host is in remote mode; FPP does not report current_playlist'),
    unsupported('fpp.scheduler.enabled', 'host is in remote mode; FPP does not report a scheduler'),
    unsupported('fpp.scheduler.next_playlist', 'host is in remote mode; FPP does not report a scheduler'),
    unsupported('fpp.scheduler.next_start_time', 'host is in remote mode; FPP does not report a scheduler'),
    measured('fpp.media.filename', ''),
    measured('fpp.position.elapsed.seconds', 0),
    measured('fpp.fppd.state', 'running'),
    measured('fpp.power.bad', false),
    measured('fpp.bridging', false),
    measured('fpp.channel_inputs.enabled', true),
    measured('fpp.channel_outputs.enabled', true),
    measured('fpp.branch', 'v9.4'),
    measured('fpp.uuid', 'M5-000000000000003'),
    measured('fpp.host_name', 'fpp-remote-b'),
    measured('fpp.volume', 58),
    measured('fpp.mqtt.configured', true),
    measured('fpp.mqtt.connected', true),
    // Spec section 3.4: this capture's REST status omits `warnings`
    // entirely. Until FPP's own source is checked, that absence is
    // modeled as Unsupported, not a claim of "zero warnings" -- the MQTT
    // warnings topic (out of this seam's scope) is what answers this
    // signal positively, per the collector-side precedence rule.
    unsupported(
      'fpp.warnings.count',
      'FPP omits the warnings key from /api/fppd/status; the MQTT warnings topic reports the list positively',
    ),
    unsupported(
      'fpp.warnings.summary',
      'FPP omits the warnings key from /api/fppd/status; the MQTT warnings topic reports the list positively',
    ),
    ...commonSensorObservations([
      { key: 'main0', value: 61.01, type: 'Temperature' },
      { key: 'main1', value: 60.37, type: 'Temperature' },
      { key: 'k16_max', value: 41.06, type: 'Temperature' },
      { key: 'vin1', value: 12.32, type: 'Voltage' },
      { key: 'vin2', value: 11.58, type: 'Voltage' },
      { key: 'vin3', value: 12.16, type: 'Voltage' },
      { key: 'vin4', value: 12.14, type: 'Voltage' },
    ]),
    measured('fpp.os.version', 'v2025-11'),
    measured('fpp.os.release', 'Debian GNU/Linux 12 (bookworm)'),
    measured('fpp.platform', 'BeagleBone 64'),
    measured('fpp.variant', 'PocketBeagle2'),
    measured('fpp.kernel', '6.17.5-arm64-k3-r12'),
    measured('fpp.utilization.cpu', 13.79, { unit: 'percent' }),
    measured('fpp.utilization.memory', 55.35, { unit: 'percent' }),
    measured('fpp.disk.media.free_bytes', 115_468_398_592, { unit: 'bytes' }),
    measured('fpp.disk.media.total_bytes', 124_516_454_400, { unit: 'bytes' }),
    measured('fpp.disk.root.free_bytes', 115_468_398_592, { unit: 'bytes' }),
    measured('fpp.disk.root.total_bytes', 124_516_454_400, { unit: 'bytes' }),
    ...REMOTE04_PORTS,
  ]
  return {
    instanceId: 'fpp-remote-04',
    endpoint: 'http://192.0.2.12',
    health: 'healthy',
    observations,
    lastPollAt: FLEET_NOW,
    lastPollError: null,
    instanceUuid: null,
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
    ...overrides,
  }
}

/**
 * The fpp-ghost ghost (spec section 1.2 / section 4.2's acceptance
 * demonstration): a host on the real broker, absent from the reference
 * installation, that published nothing live during a 60-second capture --
 * every topic arrived retained. Every signal is `unknown_age`
 * (`observedAt: null`), sourced `fpp-mqtt`, and stays that way forever:
 * this fixture is what "can never read as healthy, indefinitely" looks
 * like on the UI side. Not from a REST capture -- fpp-ghost was never
 * queried over HTTP (see CLAUDE.md's absolute rule); this reproduces the
 * MQTT capture's finding, matching Seam B's `observation.MeasuredUnknownAge`
 * contract (spec section 4.2).
 */
export function makeGhostFpp01Instance(overrides: Partial<FPPInstance> = {}): FPPInstance {
  function retained(signal: string, value: boolean | string | number): Evidence {
    return {
      signal,
      value,
      unit: null,
      state: 'unknown_age',
      reason: 'retained MQTT delivery of unknown age; this host published nothing live during collection',
      observedAt: null,
      collectedAt: FLEET_NOW,
      source: 'fpp-mqtt',
      quality: 'direct',
      validForSeconds: null,
    }
  }
  const observations: Evidence[] = [
    retained('fpp.reachable', true),
    retained('fpp.version', '9.2'),
    retained('fpp.mode', 'player'),
    retained('fpp.status', 'idle'),
    retained('fpp.branch', 'v9.2'),
    retained('fpp.host_name', 'fpp-ghost'),
    retained('fpp.fppd.state', 'running'),
    retained('fpp.power.bad', false),
    retained('fpp.mqtt.configured', true),
    retained('fpp.mqtt.connected', true),
    retained('fpp.warnings.count', 0),
    retained('fpp.warnings.summary', ''),
  ]
  return {
    instanceId: 'fpp-01-ghost',
    endpoint: 'http://192.0.2.13',
    // Health is the coordinator's own verdict (never recomputed here);
    // an instance whose only evidence is unknown_age has no health-
    // critical evidence with a known age, so per spec section 5.3 it
    // stays 'unknown' -- this fixture states that explicitly rather than
    // letting a default paper over it.
    health: 'unknown',
    observations,
    lastPollAt: null,
    lastPollError: null,
    instanceUuid: null,
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
    ...overrides,
  }
}

/**
 * CONSTRUCTED, not from a real capture: nothing in the reference fleet has
 * actually failed a port poll mid-flight. This exists solely to exercise
 * the third port-cell state (spec section 6 "Ports": "a port whose
 * current failed to collect"), which the real captures cannot demonstrate
 * because every real port either measured successfully or is a
 * structurally blind smart-receiver position. Built by overriding one
 * output port's current_ma on top of the real remote-04 base.
 */
export function makeRemote04InstanceWithAFailedPortReading(): FPPInstance {
  const base = makeRemote04Instance()
  const observations = base.observations.map((observation) =>
    observation.signal === 'fpp.port.port_3.current_ma'
      ? collectionFailed('fpp.port.port_3.current_ma', 'HTTP request to /api/fppd/ports timed out mid-poll', {
          unit: 'milliamps',
        })
      : observation,
  )
  return { ...base, observations }
}
