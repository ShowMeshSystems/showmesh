import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, describeSignInState, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ConfigObjectSummary } from '../app/types'
import type { Node } from '../app/types'
import { EmptyBlock, FailedBlock, LoadingBlock, StaleBlock, UnavailableBlock } from '../components/SharedLayouts'

// ADR-018/ADR-039: the audio.node object list. `config:write` gates both
// reads and writes alike, matching audio.node's own GET/PUT (no separate
// read scope exists for this kind).
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

function capabilityRoutes(node: Node | undefined, capabilityId: string): string[] {
  const capability = node?.capabilities.find((candidate) => candidate.id === capabilityId)
  const routes = capability?.attributes?.routes
  return Array.isArray(routes) ? routes.filter((route): route is string => typeof route === 'string') : []
}

function nodeStatus(node: Node | undefined, connectionKind: string): string {
  if (connectionKind !== 'live') return `Disconnected: API ${connectionKind}`
  if (node === undefined) return 'Disconnected: no live evidence'
  return node.controlPlane.state === 'online' ? 'Online' : `Disconnected: ${node.controlPlane.state}`
}

export function AudioNodes() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
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

  return (
    <div className="operator-page audio-nodes-page">
      <p className="settings-breadcrumb"><a href="/config">Settings</a> / Audio routing</p>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <h2 className="panel__title">Audio nodes</h2>
        {scopeGate.allowed ? (
          <Link className="entity-link" to="/config/audio.node/new">
            New audio node
          </Link>
        ) : (
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={scopeGate.reason}>
              New audio node
            </button>
            <span className="scoped-button__reason">{scopeGate.reason}</span>
          </span>
        )}
      </div>
      <p className="text-muted">
        Each node&rsquo;s own program route, LTC route, channel assignment, and declared clock
        domain: the audio.node configuration kind, previously reachable only through{' '}
        <code>showmeshctl</code>. A node with no row here has never had one written for it.
      </p>

      {permissionState}

      {scopeGate.allowed && state.kind === 'loading' && <LoadingBlock title="Loading audio nodes" reason="Loading coordinator configuration…" />}
      {scopeGate.allowed && state.kind === 'error' && (
        <FailedBlock title="Audio nodes could not be loaded" reason={<>{state.message} <button type="button" onClick={() => setReloadGeneration((g) => g + 1)}>Retry</button></>} />
      )}
      {scopeGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <EmptyBlock title="No audio nodes configured" reason="No audio.node object is configured yet."><Link className="entity-link" to="/config/audio.node/new">Create an audio node</Link></EmptyBlock>
          ) : (
            <div className="table-scroll">
              <table className="config-table" aria-label="Configured audio nodes">
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
                    return (
                      <tr key={obj.id}>
                        <td>
                          <Link className="entity-link" to={`/config/audio.node/${encodeURIComponent(obj.id)}`}>
                            {obj.id}
                          </Link>
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
        </>
      )}
    </div>
  )
}
