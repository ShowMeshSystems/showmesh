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
  ConfigObjectSummary,
  ConnectionState,
  Evidence,
  EventSeq,
  FPPInstance,
  Model,
  Node,
  NodeDeclaration,
  ResolumeAction,
  ResolumeActionResult,
  ResolumeCompositionClip,
  ResolumeCompositionDeckSummary,
  ResolumeCompositionLayer,
  ResolumeCompositionResponse,
  ResolumeInstance,
  ResolumeRecoveryRecordEntry,
  ResolumeRecoveryResponse,
  ShowMeshEvent,
} from '../types'

export const NOW = '2026-08-11T12:00:00.000Z'

export function makeConfigObjectSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return {
    id: 'halloween-2026',
    label: 'halloween-2026',
    show: '',
    currentRevision: 1,
    updatedAt: NOW,
    ...overrides,
  }
}

/**
 * The shape `listConfigObjects('show')` resolves to (api/store.ts):
 * enough for `ShowSelect` (components/ShowSelect.tsx) to populate its
 * dropdown from the same list Shows.tsx itself renders, without every
 * test that touches a `show` field having to hand-build the response.
 */
export function makeShowList(
  ids: string[] = ['halloween-2026'],
): { serverTime: string; kind: 'show'; objects: ConfigObjectSummary[] } {
  return {
    serverTime: NOW,
    kind: 'show',
    objects: ids.map((id) => makeConfigObjectSummary({ id, label: id })),
  }
}

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
    render: [],
    audio: [],
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
    instanceUuid: null,
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
    ...overrides,
  }
}

// Track D seam D-4: Resolume fixtures, same "every required field present"
// posture as every builder above.
export function makeResolumeInstance(instanceId: string, overrides: Partial<ResolumeInstance> = {}): ResolumeInstance {
  return {
    instanceId,
    health: 'healthy',
    observations: [],
    composition: null,
    ...overrides,
  }
}

export function makeResolumeAction(overrides: Partial<ResolumeAction> = {}): ResolumeAction {
  return {
    name: 'launchClip',
    params: [
      { name: 'clip', kind: 'string', required: true },
      { name: 'deck', kind: 'string', required: false },
      { name: 'layer', kind: 'string', required: false },
      { name: 'persistent', kind: 'bool', required: false },
    ],
    auditExempt: false,
    coordinatorRequired: true,
    ...overrides,
  }
}

export function makeResolumeActionResult(overrides: Partial<ResolumeActionResult> = {}): ResolumeActionResult {
  return {
    id: 'ra-1',
    idempotencyKey: 'idem-1',
    action: 'launchClip',
    params: { clip: 'Test Clip', deck: 'Deck 1' },
    replay: false,
    outcome: 'confirmed',
    outcomeReason: 'clip connected',
    attributionDegraded: false,
    dispatchedAt: NOW,
    resolvedAt: NOW,
    resolvedId: 'obj-clip-1',
    selectedDeckChanged: false,
    ...overrides,
  }
}

export function makeResolumeCompositionDeck(
  overrides: Partial<ResolumeCompositionDeckSummary> = {},
): ResolumeCompositionDeckSummary {
  return {
    id: 'deck-1',
    name: 'Deck 1',
    nameGenerated: true,
    closed: false,
    clipCount: 1,
    ...overrides,
  }
}

export function makeResolumeCompositionLayer(
  overrides: Partial<ResolumeCompositionLayer> = {},
): ResolumeCompositionLayer {
  return {
    id: 'layer-1',
    index: 0,
    name: 'Layer 1',
    nameGenerated: true,
    ...overrides,
  }
}

export function makeResolumeCompositionClip(overrides: Partial<ResolumeCompositionClip> = {}): ResolumeCompositionClip {
  return {
    id: 'clip-1',
    deckId: 'deck-1',
    layerIndex: 0,
    columnIndex: 0,
    name: 'Test Clip',
    nameGenerated: false,
    ambiguous: false,
    ...overrides,
  }
}

export function makeResolumeCompositionResponse(
  overrides: Partial<ResolumeCompositionResponse> = {},
): ResolumeCompositionResponse {
  return {
    serverTime: NOW,
    composition: {
      name: 'Christmas 25',
      sourceFilename: 'christmas25.avc',
      contentHash: 'sha256:test',
      sizeBytes: 407_000,
      writtenBy: { product: 'Arena', major: 7, minor: 23, micro: 2, revision: 0 },
      canvas: { width: 1920, height: 1080 },
      decks: [makeResolumeCompositionDeck()],
      layerCount: 1,
      layerGroupCount: 0,
      columnCount: 1,
      clipCount: 1,
      persistentClipCount: 0,
    },
    revision: 1,
    activatedAt: NOW,
    decks: [makeResolumeCompositionDeck()],
    layerGroups: [],
    layers: [makeResolumeCompositionLayer()],
    columns: [{ id: 'col-1', deckId: 'deck-1', index: 0, name: 'Column 1', nameGenerated: true }],
    clips: [makeResolumeCompositionClip()],
    persistentClips: [],
    ...overrides,
  }
}

export function makeResolumeRecoveryRecordEntry(
  overrides: Partial<ResolumeRecoveryRecordEntry> = {},
): ResolumeRecoveryRecordEntry {
  return {
    layer: 'Layer 1',
    layerNameGenerated: true,
    state: 'clip',
    clip: 'Test Clip',
    clipNameGenerated: false,
    deck: 'Deck 1',
    establishedAt: NOW,
    source: 'action',
    ...overrides,
  }
}

export function makeResolumeRecoveryResponse(
  overrides: Partial<ResolumeRecoveryResponse> = {},
): ResolumeRecoveryResponse {
  return {
    serverTime: NOW,
    resolumeConfigured: true,
    autoRestoreEnabled: true,
    autoRestoreConfigured: false,
    settleDelaySeconds: 5,
    record: [],
    lastRestore: null,
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
    // Track D seam D-4: required on Model — a test that cares about
    // Resolume overrides this.
    resolume: [],
    // Track F seam F2: required on Model — a test that cares about the
    // night session overrides this.
    nightSession: null,
    events: [],
    eventsGap: false,
    oldestRetainedSeq: null,
    session: null,
    sessionReceivedAt: null,
    sessionFetchFailed: false,
    ...overrides,
  }
}
