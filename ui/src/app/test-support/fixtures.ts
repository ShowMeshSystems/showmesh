/**
 * Locally constructed, schema-shaped fixtures for this seam's view and
 * component tests.
 *
 * Deliberately not imported from `ui/src/api/test-support/fixtures.ts`:
 * that file lives in seam B's owned tree, which is being edited
 * concurrently while this seam's tests are written, and the build's own
 * instructions are to drive views with locally constructed fixtures
 * matching the declared model types rather than reach into that seam at
 * runtime. Every field this project's `Evidence`/`Node`/`FPPInstance`
 * schema marks required is present here for the same reason seam B's
 * fixtures do it: a test that forgets to set one should fail loudly by
 * producing an obviously-wrong fixture, not by the object silently having
 * one fewer field than a real coordinator would ever send.
 *
 * Only types are imported from `../types` (itself a type-only re-export
 * of seam B's generated schema types, per app/types.ts's own comment), so
 * nothing here pulls in seam B's runtime code.
 */
import type {
  Capability,
  CollectorStatus,
  ConnectionState,
  Evidence,
  EventSeq,
  FPPInstance,
  Model,
  Node,
  NodeDeclaration,
  ShowMeshEvent,
} from '../types'

export const NOW = '2026-08-11T12:00:00.000Z'

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

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): a node nobody has declared —
// makeNode's default, matching what an ordinary observed-only node
// actually looks like on the wire (v1.NodeDeclaration's own doc comment:
// "declared: false means every other field is null").
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
      lastWill: makeEvidence({
        signal: 'node.lastWill',
        state: 'not_collected',
        value: null,
        collectedAt: null,
        reason: 'no last will observed yet',
      }),
      heartbeat: makeEvidence({ signal: 'node.heartbeat' }),
    },
    declaration: makeNodeDeclaration(),
    ...overrides,
  }
}

export function makeCapability(id: string, overrides: Partial<Capability> = {}): Capability {
  return {
    id,
    version: 1,
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

export function makeCollectorStatus(id: string, overrides: Partial<CollectorStatus> = {}): CollectorStatus {
  return {
    id,
    state: 'running',
    reason: null,
    ...overrides,
  }
}

/**
 * `Event.seq` is a branded `EventSeq` (see api/domain.ts's comment on why:
 * it must never be structurally interchangeable with a stream frame's
 * per-connection `seq`). The brand is a compile-time-only phantom field,
 * so a plain numeric cast reproduces it without calling seam B's
 * `asEventSeq` helper at runtime.
 */
function eventSeq(n: number): EventSeq {
  return n as EventSeq
}

export function makeEvent(seq: number, overrides: Partial<Omit<ShowMeshEvent, 'seq'>> = {}): ShowMeshEvent {
  return {
    seq: eventSeq(seq),
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

export function makeModel(overrides: Partial<Model> = {}): Model {
  const connection: ConnectionState = { kind: 'live', connectedAt: 0 }
  return {
    connection,
    serverTime: NOW,
    clockSkewMs: 0,
    snapshotReceivedAt: 0,
    serverTimeReceivedAt: 0,
    nodes: [],
    fpp: [],
    collectors: [],
    // Step 9 (STEP-9-SPEC.md section 6.6, ADR-020 decision 3): required
    // on Model — a test that cares about macro runs overrides this.
    macroRuns: [],
    events: [],
    eventsGap: false,
    oldestRetainedSeq: null,
    session: null,
    sessionReceivedAt: null,
    sessionFetchFailed: false,
    ...overrides,
  }
}
