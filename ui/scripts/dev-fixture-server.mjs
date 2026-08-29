/*
 * A fixture coordinator for the operator UI rebuild's visual gate. It serves
 * one coherent scenario so a rebuilt screen can be compared against its mock
 * with data in it. It is a development tool: it never ships, and nothing in
 * ui/src knows it exists.
 *
 *   node scripts/dev-fixture-server.mjs        # listens on 8099
 *   SHOWMESH_DEV_API=http://localhost:8099 npx vite
 */
import { createServer } from 'node:http'

const PORT = Number(process.env.PORT ?? 8099)
const NOW = () => new Date().toISOString()
const ago = (ms) => new Date(Date.now() - ms).toISOString()

const evidence = (signal, value, state, observedAt) => ({
  resource: { kind: 'node', id: 'media-garage' },
  signal,
  value,
  unit: null,
  state,
  reason: state === 'current' ? null : 'Last report is older than this signal’s freshness window.',
  observedAt,
  collectedAt: observedAt,
  source: 'agent',
  quality: 'reported',
})

const node = (nodeId, label, state, lastHeardMs, signals) => ({
  nodeId,
  label,
  platform: 'linux/arm64',
  agentVersion: '0.9.4',
  bootId: 'b1',
  startedAt: ago(86_400_000),
  firstSeenAt: ago(86_400_000),
  updatedAt: ago(lastHeardMs),
  capabilities: [{ id: nodeId === 'media-garage' ? 'display.hdmi' : 'transport.ndi.send', version: 1 }],
  controlPlane: { state, reason: state === 'offline' ? 'No heartbeat within the expected interval.' : null },
  evidence: {
    hello: evidence('node.hello', true, 'current', ago(86_400_000)),
    lastWill: evidence('node.last_will', false, 'not_collected', null),
    heartbeat: evidence('node.heartbeat', true, state === 'offline' ? 'stale' : 'current', ago(lastHeardMs)),
  },
  declaration: { declared: true, discoveryState: 'present', discoveryReason: '', lastDiscoveryRunId: null, lastDiscoveredAt: ago(3_600_000), notSeenAsOfRunId: null, notSeenAsOfRunFinishedAt: null },
  render: signals.render ?? [],
  audio: signals.audio ?? [],
  fppConnect: signals.fppConnect ?? [],
})

const surfaceSignals = (surfaceId, state, rate, at) => [
  { ...evidence('surface.pipeline.state', state === 'current' ? 'running' : 'unknown', state, at), resource: { kind: 'surface', id: surfaceId } },
  { ...evidence('surface.frames.rate', rate, state, at), resource: { kind: 'surface', id: surfaceId } },
  { ...evidence('surface.content.fseq_filename', 'carol-of-the-bells.fseq', state, at), resource: { kind: 'surface', id: surfaceId } },
]

const signalSet = (count, state, prefix, nodeId = 'fleet') =>
  Array.from({ length: count }, (_, i) => ({
    ...evidence(`${prefix}.${i}`, 'x', state, state === 'not_collected' ? null : ago(30_000)),
    resource: { kind: 'node', id: nodeId },
  }))

const SNAPSHOT = () => ({
  serverTime: NOW(),
  latestEventSeq: 412,
  nodes: [
    node('barn-controller', 'barn-controller', 'online', 4_000, { render: [...surfaceSignals('front-projection', 'current', 40, ago(1_200)), ...signalSet(57, 'current', 'surface.pipeline')], audio: signalSet(40, 'current', 'audio.output') }),
    node('roof-north', 'roof-north', 'online', 6_000, { render: [...surfaceSignals('side-wall', 'current', 40, ago(1_200)), ...signalSet(52, 'current', 'surface.pipeline')], audio: signalSet(35, 'current', 'audio.output') }),
    node('media-garage', 'media-garage', 'offline', 1_560_000, { render: [...surfaceSignals('garage-door', 'stale', 40, ago(1_560_000)), ...signalSet(44, 'stale', 'surface.pipeline')], audio: signalSet(11, 'not_collected', 'audio.output') }),
    node('workshop', 'workshop', 'online', 9_000, { render: signalSet(50, 'current', 'surface.pipeline'), audio: signalSet(30, 'current', 'audio.output') }),
  ],
  fpp: {
    instances: [
      { instanceId: 'main-player', endpoint: 'http://198.51.100.11', health: 'healthy', observations: [
        ...signalSet(36, 'current', 'fpp.playlist'),
        evidence('fpp.playlist.name', 'WinterRidge_Main', 'current', ago(2_000)),
        evidence('fpp.playlist.index', 1, 'current', ago(2_000)),
        evidence('fpp.playlist.count', 6, 'current', ago(2_000)),
        evidence('fpp.status.player_state', 'playing', 'current', ago(2_000)),
        evidence('fpp.position.elapsed.seconds', 102, 'current', ago(2_000)),
        evidence('fpp.position.seconds', 168, 'current', ago(2_000)),
        evidence('fpp.media.filename', 'carol-of-the-bells.fseq', 'current', ago(2_000)),
        evidence('fpp.volume', 78, 'current', ago(2_000)),
      ], lastPollAt: ago(2_000), lastPollError: null, instanceUuid: 'u1', instanceUuidFirstObservedAt: ago(86_400_000), instanceUuidChange: null, duplicateInstanceUuidEndpointIds: [] },
      { instanceId: 'barn-player', endpoint: 'http://198.51.100.12', health: 'healthy', observations: signalSet(38, 'current', 'fpp.playlist'), lastPollAt: ago(3_000), lastPollError: null, instanceUuid: 'u2', instanceUuidFirstObservedAt: ago(86_400_000), instanceUuidChange: { previousUuid: 'u2-old', changedAt: ago(780_000) }, duplicateInstanceUuidEndpointIds: [] },
    ],
  },
  collectors: [],
  macroRuns: [],
  resolume: [{ instanceId: 'arena-main', health: 'healthy', observations: signalSet(6, 'current', 'resolume.layer'), composition: { name: 'WinterRidge.avc' } }],
})

const SESSION = {
  serverTime: NOW(),
  authenticated: true,
  principal: { id: 'p1', name: 'erbartos', role: 'admin', disabled: false },
  session: { id: 's1', createdAt: ago(3_600_000), expiresAt: null, deviceLabel: 'Operator UI' },
  credentialForm: 'session',
  scopes: ['config:write', 'night:command', 'fpp:command', 'principal:read', 'principal:write', 'asset:write', 'resolume:action', 'audio:command', 'audit:read'],
  scopesState: 'current',
  bootstrapRequired: false,
}

const NIGHT = () => ({
  serverTime: NOW(),
  session: {
    id: 'winter-ridge-2026-night', configObjectId: 'night.session/winter-ridge', configRevision: 4,
    state: 'live', stateEnteredAt: ago(2_700_000), cycle: 3,
    finalShowRequested: false, finalShowRequestedAt: null, admissionClosed: false, admissionClosedAt: null,
    shutdownIntent: '', armedShowId: 'winter-ridge-2026', showCommitted: true,
    readiness: {
      state: 'recorded', reason: '', outcome: 'ready', epochId: 'epoch-3', completedAt: ago(16_740_000),
      sameEpoch: false, fresh: false,
      checks: Array.from({ length: 14 }, (_, i) => ({ name: `check-${i}`, state: 'healthy', reason: 'passed' })),
    },
    powerPhase: { state: 'recorded', reason: 'Garage projector never reported on. Its strike step was refused at 21:02:20 and the phase continued.' },
    transition: { state: 'recorded', reason: 'Enter-show transition completed at 21:02:22. Resting fade-out and audio duck both confirmed against observed evidence.' },
    cues: { state: 'recorded', reason: '', cues: [
      { name: 'Fade down resting lights', phase: 'enterShow', role: 'Resolume', action: 'resting-fade-out', actionRevision: 8, state: 'resolved', outcome: 'confirmed', dispatchedAt: ago(2_700_000), resolvedAt: ago(2_696_000) },
      { name: 'Duck background bed', phase: 'enterShow', role: 'Audio', action: 'background-duck', actionRevision: 4, state: 'resolved', outcome: 'confirmed', dispatchedAt: ago(2_696_000), resolvedAt: ago(2_694_000) },
      { name: 'Strike garage projector', phase: 'enterShow', role: 'Device', action: 'proj-garage-on', actionRevision: 2, state: 'resolved', outcome: 'refused', reason: 'no route to host', dispatchedAt: ago(2_694_000), resolvedAt: null },
      { name: 'Blackout barrier (into resting)', phase: 'enterResting', role: 'Resolume', action: 'blackout', actionRevision: 8, state: 'not_dispatched', dispatchedAt: null, resolvedAt: null },
      { name: 'Restore background bed', phase: 'enterResting', role: 'Audio', action: 'background-restore', actionRevision: 4, state: 'not_dispatched', dispatchedAt: null, resolvedAt: null },
      { name: 'Start resting sequence', phase: 'enterResting', role: 'FPP', action: 'WinterRidge_Rest', actionRevision: 8, state: 'not_dispatched', dispatchedAt: null, resolvedAt: null },
      { name: 'Fade up resting lights', phase: 'enterResting', role: 'Resolume', action: 'resting-fade-in', actionRevision: 8, state: 'not_dispatched', dispatchedAt: null, resolvedAt: null },
    ] },
    backgroundAudio: { state: 'recorded', reason: 'Ducked to -18 dB at 21:02:20, restore armed for the boundary.', steps: [{}, {}, {}, {}] },
    degraded: false, attributionDegraded: true,
    authorization: { state: 'recorded', reason: '', principalId: 'p1', principalName: 'erbartos', command: 'start-night', recordedAt: ago(2_700_000) },
    updatedAt: NOW(),
  },
})

const CURRENT_RUNS = () => ({
  serverTime: NOW(),
  activeShow: { configured: true, show: 'Winter Ridge 2026', generation: 7 },
  runs: [{
    id: 'run-1', runner: 'fpp', show: 'Winter Ridge 2026', generation: 7,
    playlistId: 'main-show', playlistRevision: 3, status: 'playing', statusReason: '',
    playback: { state: 'playing', reason: '', itemId: 'item-4', itemIndex: 4, positionMs: 102_000, media: 'Carol of the Bells', evidence: [] },
    freshness: { state: 'current', reason: '', observedAt: ago(1_000), collectedAt: ago(1_000) },
    reconciliation: { state: 'matched', reason: '' },
    activation: { show: 'Winter Ridge 2026', generation: 7, playlistId: 'main-show', revision: 3, runner: 'fpp' },
    targets: [], next: { itemId: 'item-5', itemIndex: 5, media: 'Wizards in Winter', source: 'playlist' },
  }],
})

const json = (res, body, status = 200) => {
  res.writeHead(status, {
    'content-type': 'application/json',
    'ShowMesh-API-Version': '1',
    'access-control-allow-origin': '*',
  })
  res.end(JSON.stringify(body))
}

createServer((req, res) => {
  const url = new URL(req.url ?? '/', 'http://localhost')
  console.log(req.method, url.pathname)
  const p = url.pathname.replace(/^\/api\/v1/, '') || '/'

  if (p === '/stream') {
    res.writeHead(200, {
      'content-type': 'text/event-stream',
      'cache-control': 'no-cache',
      connection: 'keep-alive',
      'ShowMesh-API-Version': '1',
      'x-accel-buffering': 'no',
    })
    res.write(`event: stream.start\ndata: ${JSON.stringify({ seq: 0, serverTime: NOW() })}\n\n`)
    const beat = setInterval(() => res.write(': keep-alive\n\n'), 15_000)
    req.on('close', () => clearInterval(beat))
    return
  }
  if (p === '/snapshot') return json(res, SNAPSHOT())
  if (p === '/events') return json(res, { serverTime: NOW(), latestSeq: 412, gap: false, oldestRetainedSeq: 1, events: [
    { seq: 412, recordedAt: ago(300_000), occurredAt: ago(300_000), source: 'coordinator', resource: { kind: 'node', id: 'media-garage' }, category: 'action', severity: 'critical', summary: 'Projector strike refused on media-garage - no route to host', details: {}, correlationId: null },
    { seq: 411, recordedAt: ago(660_000), occurredAt: ago(660_000), source: 'erbartos', resource: { kind: 'coordinator', id: 'coordinator' }, category: 'night', severity: 'informational', summary: 'Night session started, cycle 3 armed', details: {}, correlationId: null },
    { seq: 410, recordedAt: ago(780_000), occurredAt: ago(780_000), source: 'fpp', resource: { kind: 'fpp', id: 'barn-player' }, category: 'binding', severity: 'warning', summary: 'barn-player playlist definition changed - all Main Show bindings held', details: {}, correlationId: null },
    { seq: 409, recordedAt: ago(1_560_000), occurredAt: ago(1_560_000), source: 'broker', resource: { kind: 'node', id: 'media-garage' }, category: 'lifecycle', severity: 'warning', summary: 'media-garage last will received - agent went away', details: {}, correlationId: null },
    { seq: 408, recordedAt: ago(16_740_000), occurredAt: ago(16_740_000), source: 'erbartos', resource: { kind: 'coordinator', id: 'coordinator' }, category: 'night', severity: 'informational', summary: 'Readiness check passed - 14 of 14 checks', details: {}, correlationId: null },
  ] })
  if (p === '/session') return json(res, { ...SESSION, serverTime: NOW() })
  if (p === '/current-runs') return json(res, CURRENT_RUNS())
  if (p === '/night/session') return json(res, NIGHT())
  if (p === '/config/show.macro') return json(res, { serverTime: NOW(), kind: 'show.macro', objects: [
    { id: 'blackout', label: 'Blackout', show: 'Winter Ridge 2026', currentRevision: 3, updatedAt: ago(86_400_000) },
    { id: 'enter-intermission', label: 'Enter intermission', show: 'Winter Ridge 2026', currentRevision: 2, updatedAt: ago(86_400_000) },
  ] })
  if (p === '/config/show.action') return json(res, { serverTime: NOW(), kind: 'show.action', objects: [
    { id: 'resting-fade-out', label: 'Resting fade-out', show: 'Winter Ridge 2026', currentRevision: 8, updatedAt: ago(86_400_000) },
    { id: 'strike-garage-projector', label: 'Strike garage projector', show: 'Winter Ridge 2026', currentRevision: 2, updatedAt: ago(86_400_000) },
  ] })
  if (p === '/config/show.cue') return json(res, { serverTime: NOW(), kind: 'show.cue', objects: [
    { id: 'sponsor-announcement', label: 'Sponsor announcement', show: 'Winter Ridge 2026', currentRevision: 1, updatedAt: ago(86_400_000) },
    { id: 'show-starting-soon', label: 'Show starting soon', show: 'Winter Ridge 2026', currentRevision: 1, updatedAt: ago(86_400_000) },
    { id: 'main-cue', label: 'Main cue', show: 'Winter Ridge 2026', currentRevision: 4, updatedAt: ago(86_400_000) },
  ] })
  if (p.startsWith('/config/show.cue/')) {
    const id = p.split('/').pop()
    const announcement = id === 'main-cue' ? undefined : { policy: id === 'show-starting-soon' ? 'interrupt' : 'duck', duckGainDb: -18, fadeMillis: 400 }
    return json(res, {
      serverTime: NOW(), kind: 'show.cue', id, revision: 1,
      payload: { show: 'Winter Ridge 2026', name: id, outputs: announcement === undefined ? {} : { announcement } },
      updatedAt: ago(86_400_000), createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api',
    })
  }
  if (p === '/') return json(res, { name: 'showmesh-coordinator', version: '0.9.4-fixture', commit: 'a3f91c2', apiVersion: 'v1' })
  return json(res, { type: 'about:blank', title: 'Not Found', status: 404, detail: `No fixture for ${p}` }, 404)
}).listen(PORT, () => console.log(`fixture coordinator on http://localhost:${PORT}`))
