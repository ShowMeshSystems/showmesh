import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../../api'
import { useModelContext } from '../../app/ModelContext'
import { describeApiError, describeSignInState, evaluateScope } from '../../app/session'
import { formatAbsolute } from '../../app/time'
import type { ConfigObjectSummary, Node } from '../../app/types'
import { EmptyBlock, FailedBlock, LoadingBlock, StaleBlock, UnavailableBlock } from '../../components/SharedLayouts'
import { ScopedButton } from '../../components/ScopedButton'
import { AudioNodeDetail } from '../AudioNodeDetail'
import { SettingsShell } from './SettingsShell'

// Revision-1 Settings.dc.html, Node routing tab: list-then-detail. The
// owner's question was how an operator sees every configured audio.node
// object, or creates one for a node that has not been declared yet
// (BUILDER-BRIEF.md task). The two tables below answer that:
//
// - "Audio nodes" lists every *declared* audio.node object (config:write
//   gates its read the same as its write -- ADR-018/ADR-039, matching the
//   AudioNodes.tsx list this tab now folds in).
// - "Not declared yet" is DERIVED, never stored: nodes advertising an
//   audio capability minus nodes that already have an audio.node object.
//   Declare reuses the agent's own reported node id -- the node already
//   announced itself, only the routing object is missing, so there is
//   nothing to type.
//
// The six audio capability ids (DESIGN-DECISIONS-AND-API-FACTS.md
// section 6 / pkg/capability/id.go): audio.playback, audio.multichannel,
// audio.dante and timecode.ltc.generate were withdrawn and must never
// appear here.
const CONFIG_WRITE_SCOPE = 'config:write'
const AUDIO_CAPABILITY_IDS: string[] = [
  'audio.engine',
  'audio.output.local',
  'audio.output.fm',
  'audio.output.ltc',
  'audio.output.dante',
  'timecode.ltc.observe',
]

function hasAudioCapability(node: Node): boolean {
  return node.capabilities.some((c) => AUDIO_CAPABILITY_IDS.includes(c.id))
}

function capabilityRoutes(node: Node | undefined, capabilityId: string): string[] {
  const capability = node?.capabilities.find((c) => c.id === capabilityId)
  const routes = capability?.attributes?.routes
  return Array.isArray(routes) ? routes.filter((r): r is string => typeof r === 'string') : []
}

function nodeStatus(node: Node | undefined, connectionKind: string): string {
  if (connectionKind !== 'live') return `Disconnected: API ${connectionKind}`
  if (node === undefined) return 'Disconnected: no live evidence'
  return node.controlPlane.state === 'online' ? 'Online' : `Disconnected: ${node.controlPlane.state}`
}

type ListState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

type Selection = { kind: 'existing'; nodeId: string } | { kind: 'declaring'; nodeId: string } | { kind: 'none' }

export function NodeRoutingSettings() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<ListState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [explicitSelection, setExplicitSelection] = useState<Selection | null>(null)
  const signInState = describeSignInState(model.session)
  const permissionState = !scopeGate.allowed && (
    signInState.kind === 'loading' ? <LoadingBlock title="Loading permissions" reason="Waiting for the coordinator to report what this device may do." />
      : signInState.kind === 'bootstrap_required' ? <UnavailableBlock title="Setup required" reason="No administrator exists on this coordinator. Claim the bootstrap code from its data volume to create one before editing audio routing." />
        : signInState.kind === 'signed_out' ? <UnavailableBlock title="Signed out" reason="This device is not signed in, so it cannot edit audio routing." />
          : model.sessionFetchFailed || signInState.session.scopesState !== 'current' ? <StaleBlock title="Stale permission evidence" reason="Audio routing remains unavailable until the coordinator can confirm this device’s current permissions." />
            : <UnavailableBlock title="Insufficient permission" reason={scopeGate.reason} />
  )

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('audio.node')
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', objects: resp.objects })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed, reloadGeneration])

  const declaredIds = state.kind === 'loaded' ? new Set(state.objects.map((o) => o.id)) : new Set<string>()
  const audioCapableNodes = model.nodes.filter(hasAudioCapability)
  const notDeclaredNodes = audioCapableNodes.filter((node) => !declaredIds.has(node.nodeId))
  const noCapabilityNodes = model.nodes.filter((node) => !hasAudioCapability(node))

  const firstDeclared = state.kind === 'loaded' ? state.objects[0] : undefined
  const selection: Selection =
    explicitSelection ?? (firstDeclared !== undefined ? { kind: 'existing', nodeId: firstDeclared.id } : { kind: 'none' })

  return (
    <SettingsShell active="audio">
      <p className="t-small text-muted settings-breadcrumb">
        <Link to="/settings/connections">Settings</Link> / Audio / Node routing
      </p>
      <h2 className="t-heading">Where this node&rsquo;s audio leaves the building</h2>
      <p className="t-small text-muted" style={{ maxWidth: '74ch' }}>
        Program and LTC leave through one interface in one clock domain: the coordinator refuses a
        split. A route this node has not advertised is refused on save.
      </p>

      <section aria-labelledby="node-routing-node-heading" className="node-routing-nodes" style={{ marginTop: 'var(--space-24px)', maxWidth: '780px' }}>
        <div className="node-routing-nodes__head">
          <h3 id="node-routing-node-heading" className="t-meta settings-shell__section-label" style={{ margin: 0 }}>
            Audio nodes{state.kind === 'loaded' && <span className="text-muted"> &middot; {state.objects.length} declared</span>}
          </h3>
          <span className="t-small text-faint">Routing below applies to the selected node</span>
        </div>

        {permissionState}

        {scopeGate.allowed && state.kind === 'loading' && <LoadingBlock title="Loading audio nodes" reason="Loading coordinator configuration…" />}
        {scopeGate.allowed && state.kind === 'error' && (
          <FailedBlock title="Audio nodes could not be loaded" reason={<>{state.message} <button type="button" onClick={() => setReloadGeneration((g) => g + 1)}>Retry</button></>} />
        )}

        {scopeGate.allowed && state.kind === 'loaded' && (
          <>
            {state.objects.length === 0 ? (
              <EmptyBlock title="No audio nodes declared" reason="No audio.node object is configured yet." />
            ) : (
              <div className="table-scroll">
                <table className="config-table" aria-label="Declared audio nodes">
                  <thead>
                    <tr>
                      <th scope="col">Node id</th>
                      <th scope="col">Node status</th>
                      <th scope="col">Program interfaces</th>
                      <th scope="col">LTC interfaces</th>
                      <th scope="col">Revision</th>
                      <th scope="col">Updated</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.objects.map((obj) => {
                      const node = model.nodes.find((candidate) => candidate.nodeId === obj.id)
                      const programRoutes = capabilityRoutes(node, 'audio.output.local')
                      const ltcRoutes = capabilityRoutes(node, 'audio.output.ltc')
                      const isEditing = selection.kind === 'existing' && selection.nodeId === obj.id
                      return (
                        <tr key={obj.id} aria-current={isEditing ? 'true' : undefined}>
                          <td>
                            <button
                              type="button"
                              className="entity-link"
                              onClick={() => setExplicitSelection({ kind: 'existing', nodeId: obj.id })}
                            >
                              {obj.id}
                            </button>
                            {isEditing && <span className="node-routing-editing-badge">Editing</span>}
                          </td>
                          <td>{nodeStatus(node, model.connection.kind)}</td>
                          <td>
                            {programRoutes.length > 0 ? (
                              programRoutes.join(', ')
                            ) : (
                              <>Unavailable from API (configured: <span>{obj.label}</span>)</>
                            )}
                          </td>
                          <td>{ltcRoutes.length > 0 ? ltcRoutes.join(', ') : 'Unavailable from API'}</td>
                          <td>{obj.currentRevision}</td>
                          <td>{formatAbsolute(obj.updatedAt)}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}

            <h3 className="t-small node-routing-subhead">
              Not declared yet{notDeclaredNodes.length > 0 && <span className="text-faint"> &middot; {notDeclaredNodes.length}</span>}
            </h3>

            {notDeclaredNodes.length === 0 ? (
              <p className="t-small text-muted">
                Every node advertising an audio capability already has an <code>audio.node</code> object.
              </p>
            ) : (
              <>
                <p className="t-small text-muted" style={{ maxWidth: '74ch' }}>
                  Advertising audio capabilities with no <code>audio.node</code> object, so nothing
                  routes their output. Declaring one uses the node id the agent already reports: there
                  is nothing to type.
                </p>
                <div className="table-scroll">
                  <table className="config-table" aria-label="Nodes not yet declared">
                    <tbody>
                      {notDeclaredNodes.map((node) => {
                        const advertisedIds = node.capabilities.map((c) => c.id).filter((id) => AUDIO_CAPABILITY_IDS.includes(id))
                        const isDeclaring = selection.kind === 'declaring' && selection.nodeId === node.nodeId
                        return (
                          <tr key={node.nodeId} aria-current={isDeclaring ? 'true' : undefined}>
                            <td>
                              <span className="t-body" style={{ fontWeight: 600 }}>{node.nodeId}</span>
                              <br />
                              <span className="t-data text-faint">{advertisedIds.join(' · ')}</span>
                            </td>
                            <td>{nodeStatus(node, model.connection.kind)}</td>
                            <td>
                              <ScopedButton
                                requiredScope={CONFIG_WRITE_SCOPE}
                                onClick={() => setExplicitSelection({ kind: 'declaring', nodeId: node.nodeId })}
                              >
                                Declare
                              </ScopedButton>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              </>
            )}

            {noCapabilityNodes.length > 0 && (
              <p className="t-small text-faint" style={{ maxWidth: '74ch' }}>
                Nodes advertising no audio capability at all,{' '}
                {noCapabilityNodes.map((node, index) => (
                  <span key={node.nodeId}>
                    {index > 0 && ', '}
                    <code>{node.nodeId}</code>
                  </span>
                ))}
                , are not listed. There is nothing to route on them.
              </p>
            )}
          </>
        )}

        {/* Declare above reuses an advertising agent's own node id, which is the
            safe default and removes a whole class of typo. This is the escape
            hatch for the other case: routing declared ahead of a node that has
            not come up yet, which is the only path that accepts a typed id. */}
        <p className="field__help" style={{ marginTop: 16 }}>
          A node that has not come up yet will not appear above.{' '}
          <Link to="/settings/node-routing/new">Declare routing by typing a node id</Link> to
          prepare one in advance.
        </p>
      </section>

      {scopeGate.allowed && state.kind === 'loaded' && (
        selection.kind === 'existing' ? (
          <AudioNodeDetail key={`existing-${selection.nodeId}`} nodeIdOverride={selection.nodeId} />
        ) : selection.kind === 'declaring' ? (
          <AudioNodeDetail key={`declaring-${selection.nodeId}`} isNew presetNewNodeId={selection.nodeId} />
        ) : (
          <EmptyBlock
            title="No node selected"
            reason="Select a declared node above, or declare one for a node that is not declared yet."
          />
        )
      )}
    </SettingsShell>
  )
}
