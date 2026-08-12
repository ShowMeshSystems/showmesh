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
type Snapshot = components['schemas']['Snapshot']
type EventsResponse = components['schemas']['EventsResponse']
type Event = components['schemas']['Event']
type Problem = components['schemas']['Problem']
type SessionResponse = components['schemas']['SessionResponse']

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
