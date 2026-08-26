/**
 * Minimal, schema-valid wire fixtures for store.test.ts — every field
 * api/openapi.yaml marks `required` is present, so a test that forgets
 * to set one fails loudly by producing a Node/Event that looks wrong,
 * rather than the store silently tolerating an incomplete fixture no
 * real coordinator would ever send.
 */
import type { components } from '../generated/schema'

type Evidence = components['schemas']['Evidence']
type Node = components['schemas']['Node']
type NodeDeclaration = components['schemas']['NodeDeclaration']
type FPPInstance = components['schemas']['FPPInstance']
type ResolumeInstance = components['schemas']['ResolumeInstance']
type Snapshot = components['schemas']['Snapshot']
type EventsResponse = components['schemas']['EventsResponse']
type Event = components['schemas']['Event']
type Problem = components['schemas']['Problem']
type SessionResponse = components['schemas']['SessionResponse']
// Step 9 (STEP-9-SPEC.md sections 5, 6).
type ConfigShowAction = components['schemas']['ConfigShowAction']
type ConfigShowMacro = components['schemas']['ConfigShowMacro']
type MacroRunSummary = components['schemas']['MacroRunSummary']
type MacroRunStep = components['schemas']['MacroRunStep']
type MacroRun = components['schemas']['MacroRun']
type MacroRunStepCommand = components['schemas']['MacroRunStepCommand']
// Track F seam F2.
type NightSessionState = components['schemas']['NightSessionState']
type NightSessionChangedEvent = components['schemas']['NightSessionChangedEvent']
// TRACK-H-H2-SPEC.md §5.1.
type FPPPlaylistEntryObservation = components['schemas']['FPPPlaylistEntryObservation']

const NOW = '2026-08-11T12:00:00.000Z'

export function makeEvidence(overrides: Partial<Evidence> = {}): Evidence {
  return {
    signal: 'node.heartbeat',
    value: true,
    unit: null,
    state: 'current',
    reason: null,
    observedAt: NOW,
    collectedAt: NOW,
    source: 'test',
    quality: 'direct',
    validForSeconds: 30,
    ...overrides,
  }
}

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): matches v1.NodeDeclaration's
// own "declared: false means every other field is null" contract.
export function makeNodeDeclaration(overrides: Partial<NodeDeclaration> = {}): NodeDeclaration {
  return {
    declared: false,
    label: null,
    notes: null,
    declaredAt: null,
    declaredByPrincipalId: null,
    declaredByPrincipalName: null,
    discoveryState: 'not_applicable',
    discoveryReason: null,
    lastDiscoveryRunId: null,
    lastDiscoveredAt: null,
    notSeenAsOfRunId: null,
    notSeenAsOfRunFinishedAt: null,
    ...overrides,
  }
}

export function makeNode(nodeId: string, overrides: Partial<Node> = {}): Node {
  return {
    nodeId,
    label: null,
    platform: 'linux/amd64',
    agentVersion: '0.0.0-test',
    bootId: 'boot-1',
    startedAt: NOW,
    firstSeenAt: NOW,
    updatedAt: NOW,
    capabilities: [],
    controlPlane: { state: 'online', reason: null },
    evidence: {
      hello: makeEvidence({ signal: 'node.hello' }),
      lastWill: makeEvidence({ signal: 'node.lastWill', state: 'not_collected', value: null, collectedAt: null, reason: 'no last will observed yet' }),
      heartbeat: makeEvidence({ signal: 'node.heartbeat' }),
    },
    declaration: makeNodeDeclaration(),
    render: [],
    audio: [],
    ...overrides,
  }
}

export function makeFPPInstance(instanceId: string, overrides: Partial<FPPInstance> = {}): FPPInstance {
  return {
    instanceId,
    endpoint: 'http://fpp.example.invalid',
    health: 'healthy',
    observations: [],
    lastPollAt: NOW,
    lastPollError: null,
    instanceUuid: null,
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
    ...overrides,
  }
}

export function makeResolumeInstance(instanceId: string, overrides: Partial<ResolumeInstance> = {}): ResolumeInstance {
  return {
    instanceId,
    health: 'healthy',
    observations: [],
    composition: null,
    ...overrides,
  }
}

export function makeSnapshot(overrides: Partial<Snapshot> = {}): Snapshot {
  return {
    serverTime: NOW,
    latestEventSeq: 0,
    nodes: [],
    fpp: { instances: [] },
    collectors: [],
    // Step 9 (ADR-020 decision 3, STEP-9-SPEC.md section 6.6): required
    // on every snapshot, defaulting empty here the same way `nodes`/`fpp`
    // above do — a test that cares about in-flight runs overrides this.
    macroRuns: [],
    // Required on every snapshot, same defaulting pattern as `macroRuns`
    // above.
    resolume: [],
    ...overrides,
  }
}

export function makeEvent(seq: number, overrides: Partial<Event> = {}): Event {
  return {
    seq,
    recordedAt: NOW,
    occurredAt: NOW,
    source: 'test',
    resource: { kind: 'node', id: 'n1' },
    category: 'test',
    severity: 'informational',
    summary: `event ${seq}`,
    details: {},
    correlationId: null,
    ...overrides,
  }
}

export function makeEventsResponse(overrides: Partial<EventsResponse> = {}): EventsResponse {
  return {
    serverTime: NOW,
    events: [],
    latestSeq: 0,
    gap: false,
    oldestRetainedSeq: null,
    ...overrides,
  }
}

/** ADR-024. Defaults to the signed-out shape (SessionResponse's own doc comment: authenticated false => principal/session/credentialForm null, scopes []). */
export function makeSessionResponse(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: false,
    principal: null,
    session: null,
    credentialForm: null,
    scopes: [],
    scopesState: 'not_applicable',
    bootstrapRequired: false,
    ...overrides,
  }
}

/** An authenticated SessionResponse, for tests that need one — layered on makeSessionResponse's defaults rather than duplicating them. */
export function makeAuthenticatedSession(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return makeSessionResponse({
    authenticated: true,
    principal: { id: 'p1', name: 'operator', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'test device', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read', 'show:macro:run', 'device:power', 'fpp:command'],
    scopesState: 'current',
    ...overrides,
  })
}

export function makeProblem(overrides: Partial<Problem> = {}): Problem {
  return {
    type: 'https://showmesh.dev/problems/internal-error',
    title: 'error',
    status: 500,
    detail: 'test problem',
    serverTime: NOW,
    ...overrides,
  }
}

// Step 9 (STEP-9-SPEC.md sections 5, 6): show.action / show.macro
// configuration objects and the macro run surface.

export function makeConfigShowAction(overrides: Partial<ConfigShowAction> = {}): ConfigShowAction {
  return {
    show: 'halloween-2026',
    label: 'Start main show',
    description: '',
    safetyClass: 'none',
    target: {
      integration: 'fpp',
      instanceId: 'fpp-main',
      primitive: 'startPlaylist',
      params: { playlist: 'Halloween Main', repeat: false, ifBusy: 'refuse' },
    },
    ...overrides,
  }
}

export function makeConfigShowMacro(overrides: Partial<ConfigShowMacro> = {}): ConfigShowMacro {
  return {
    show: 'halloween-2026',
    label: 'Begin set',
    description: '',
    steps: [
      {
        id: 'start',
        action: 'start-main-show',
        onFailure: 'abort',
        onUnconfirmed: 'continue',
        localFallback: { class: 'coordinator-required', reason: 'the coordinator dispatches every step; nothing runs locally' },
      },
    ],
    ...overrides,
  }
}

export function makeMacroRunSummary(overrides: Partial<MacroRunSummary> = {}): MacroRunSummary {
  return {
    id: 'run-1',
    macroObjectId: 'begin-set',
    macroRevision: 1,
    show: 'halloween-2026',
    trigger: 'ui',
    issuerPrincipalId: 'p1',
    issuerPrincipalName: 'operator',
    createdAt: NOW,
    finishedAt: null,
    state: 'running',
    completed: null,
    confirmed: null,
    reason: '',
    attributionDegraded: false,
    ...overrides,
  }
}

export function makeNightSessionState(overrides: Partial<NightSessionState> = {}): NightSessionState {
  return {
    id: 'night-1',
    configObjectId: 'halloween-night',
    configRevision: 1,
    state: 'inactive',
    stateEnteredAt: NOW,
    cycle: 0,
    finalShowRequested: false,
    finalShowRequestedAt: null,
    admissionClosed: false,
    admissionClosedAt: null,
    shutdownIntent: '',
    armedShowId: '',
    showCommitted: false,
    readiness: { state: 'unknown', reason: 'no run-readiness result yet', sameEpoch: false, fresh: false, checks: [] },
    powerPhase: { state: 'unknown', reason: 'not observed yet' },
    transition: { state: 'unknown', reason: 'not observed yet' },
    cues: { state: 'unknown', reason: 'no cycle started yet', cues: [] },
    backgroundAudio: { state: 'unknown', reason: 'no cycle started yet', steps: [] },
    degraded: false,
    attributionDegraded: false,
    authorization: { state: 'unknown', recordedAt: null },
    updatedAt: NOW,
    ...overrides,
  }
}

export function makeNightSessionChangedEvent(
  overrides: Partial<NightSessionChangedEvent> = {},
): NightSessionChangedEvent {
  return {
    seq: 1,
    serverTime: NOW,
    session: makeNightSessionState(),
    ...overrides,
  }
}

export function makeFPPPlaylistEntryObservation(
  overrides: Partial<FPPPlaylistEntryObservation> = {},
): FPPPlaylistEntryObservation {
  return {
    instanceUuid: 'fpp-uuid-1',
    endpointId: 'fpp-1',
    schemaVersion: 1,
    sequence: 1,
    action: 'playing',
    observedAt: NOW,
    coalescedSincePreviousAcknowledged: 0,
    receivedAt: NOW,
    ...overrides,
  }
}

export function makeMacroRunStepCommand(overrides: Partial<MacroRunStepCommand> = {}): MacroRunStepCommand {
  return {
    state: 'none',
    ...overrides,
  }
}

export function makeMacroRunStep(overrides: Partial<MacroRunStep> = {}): MacroRunStep {
  return {
    stepIndex: 0,
    stepId: 'start',
    actionObjectId: 'start-main-show',
    actionRevision: 1,
    integration: 'fpp',
    safetyClass: 'none',
    localFallbackClass: 'coordinator-required',
    state: 'pending',
    dispatchedAt: null,
    resolvedAt: null,
    outcome: null,
    outcomeState: 'not_collected',
    outcomeReason: '',
    attributionDegraded: false,
    command: makeMacroRunStepCommand(),
    ...overrides,
  }
}

export function makeMacroRun(overrides: Partial<MacroRun> = {}): MacroRun {
  return {
    ...makeMacroRunSummary(),
    steps: [makeMacroRunStep()],
    ...overrides,
  }
}
