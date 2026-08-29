import { useState } from 'react'
import { assetContentUrl, uploadAsset, type Asset } from '../../api'
import { describeApiError, evaluateScope } from '../../app/session'
import { useModelContext } from '../../app/ModelContext'
import { formatAbsolute } from '../../app/time'
import { PlannedFeature } from '../../components/SharedLayouts'
import { historyForIdentity, shortHash } from './assetGrouping'

const ASSET_WRITE_SCOPE = 'asset:write'
const NODE_READ_SCOPE = 'node:read'

type MakeCurrentState = { kind: 'idle' } | { kind: 'working' } | { kind: 'error'; message: string }

/**
 * The show-scoped inspector's "history" pane (Show Assets.dc.html). A
 * version can become CURRENT more than once, so this reads as events, not
 * a one-way list (ADR-028 decision 10 / the design's own rule 5).
 *
 * "Make current" has no dedicated coordinator endpoint — the only
 * documented path back to a superseded version IS re-uploading its exact
 * bytes (POST /assets' own rollback behaviour). This fetches those bytes
 * from this same superseded asset's own content route and re-submits them
 * as a fresh upload, so the mechanism an operator triggers here is the
 * same one the coordinator already recognises, never a fabricated one.
 */
export function AssetHistoryPanel({
  row,
  allAssets,
  onChanged,
}: {
  row: Asset
  allAssets: Asset[]
  onChanged: (asset: Asset, rolledBack: boolean) => void
}) {
  const model = useModelContext()
  const uploadGate = evaluateScope(model.session, model.sessionFetchFailed, ASSET_WRITE_SCOPE)
  const readGate = evaluateScope(model.session, model.sessionFetchFailed, NODE_READ_SCOPE)
  const [makeCurrentState, setMakeCurrentState] = useState<Record<string, MakeCurrentState>>({})

  const history = historyForIdentity(allAssets, row)
  const current = history.find((a) => a.current) ?? row

  async function makeCurrent(supersededId: string): Promise<void> {
    setMakeCurrentState((s) => ({ ...s, [supersededId]: { kind: 'working' } }))
    try {
      const resp = await fetch(assetContentUrl(supersededId), { credentials: 'same-origin' })
      if (!resp.ok) throw new Error(`Fetching this version's bytes failed (${resp.status}).`)
      const blob = await resp.blob()
      const source = history.find((a) => a.id === supersededId)
      if (source === undefined) throw new Error('This version is no longer in the loaded history.')
      const file = new File([blob], source.runtimeFilename, { type: blob.type || 'application/octet-stream' })
      const uploadResp = await uploadAsset(
        file,
        {
          show: source.show,
          sequence: source.sequence,
          mediaType: source.mediaType,
          targetKind: source.targetKind,
          ...(source.targetKind === 'node' ? { target: source.target } : {}),
        },
        () => undefined,
      )
      setMakeCurrentState((s) => ({ ...s, [supersededId]: { kind: 'idle' } }))
      onChanged(uploadResp.asset, uploadResp.rolledBack)
    } catch (err) {
      setMakeCurrentState((s) => ({ ...s, [supersededId]: { kind: 'error', message: describeApiError(err) } }))
    }
  }

  return (
    <div className="card">
      <div className="asset-inspector-head">
        <p className="asset-inspector-eyebrow">Asset variant</p>
        <h2 id="asset-history-heading" className="t-heading">
          {row.sequence}
        </h2>
        <p className="t-small text-muted">
          for <span className="t-data">{row.targetKind === 'node' ? row.target : 'show-wide'}</span>
        </p>
      </div>

      <div className="asset-inspector-section">
        <h3 className="asset-inspector-eyebrow">Identity</h3>
        <dl className="asset-identity">
          <dt className="t-small text-muted">Show</dt>
          <dd className="t-data">{row.show}</dd>
          <dt className="t-small text-muted">Sequence</dt>
          <dd className="t-data">{row.sequence}</dd>
          <dt className="t-small text-muted">Target</dt>
          <dd className="t-data">{row.targetKind === 'node' ? row.target : 'show-wide'}</dd>
          <dt className="t-small text-muted">Hash</dt>
          <dd className="t-data">{shortHash(current.contentHash)}</dd>
        </dl>
        <p className="asset-note">
          Identity is those four facts. The runtime filename <span className="t-data">{row.runtimeFilename}</span> is not one of them: other
          targets in this sequence may use it too.
        </p>
      </div>

      <div className="asset-inspector-section">
        <h3 className="asset-inspector-eyebrow">History</h3>
        <p className="asset-history__lede">A version can become current more than once, so this reads as events, not a one-way list.</p>

        {history.length === 0 ? (
          <p className="t-small text-muted">No history recorded for this identity.</p>
        ) : (
          history.map((event, index) => {
            const wasRollback = event.current && index + 1 < history.length && history.slice(index + 1).some((h) => !h.current && h.contentHash === event.contentHash)
            const sameBytesAsCurrent = !event.current && event.contentHash === current.contentHash
            const mcState = makeCurrentState[event.id] ?? { kind: 'idle' }
            return (
              <div className="asset-history__event" key={event.id}>
                <span className={`asset-history__event-state ${event.current ? 'asset-history__event-state--current' : ''}`}>
                  {event.current ? 'Current' : 'Superseded'}
                </span>
                <div>
                  <p className="asset-history__event-hash">{shortHash(event.contentHash)}</p>
                  <p className="asset-history__event-meta">
                    {event.current ? 'Restored' : 'Uploaded'} {formatAbsolute(event.createdAt)}
                    {event.createdByPrincipalName !== null ? ` by ${event.createdByPrincipalName}` : ' by an unattributed principal'}
                  </p>
                  {wasRollback && <p className="asset-history__event-rollback-note">Rollback: these exact bytes were current before.</p>}
                  {sameBytesAsCurrent && <p className="asset-history__event-same-bytes">Same bytes as current</p>}
                  {!event.current && (
                    <>
                      <button
                        type="button"
                        className="btn btn--secondary btn--compact"
                        disabled={uploadGate.allowed === false || readGate.allowed === false || mcState.kind === 'working'}
                        aria-disabled={uploadGate.allowed === false || readGate.allowed === false || mcState.kind === 'working'}
                        title={uploadGate.allowed === false ? uploadGate.reason : readGate.allowed === false ? readGate.reason : undefined}
                        onClick={() => void makeCurrent(event.id)}
                        style={{ marginTop: '8px' }}
                      >
                        {mcState.kind === 'working' ? 'Making current…' : 'Make current'}
                      </button>
                      {(uploadGate.allowed === false || readGate.allowed === false) && (
                        <p className="btn__reason">{uploadGate.allowed === false ? uploadGate.reason : readGate.allowed === false ? readGate.reason : ''}</p>
                      )}
                      {mcState.kind === 'error' && (
                        <p className="field__error" role="alert">
                          {mcState.message}
                        </p>
                      )}
                    </>
                  )}
                </div>
              </div>
            )
          })
        )}
      </div>

      <div className="asset-history__footer">
        <a className="t-small" href={assetContentUrl(current.id)} download={current.runtimeFilename}>
          Download
        </a>
      </div>

      <PlannedFeature
        title="Re-sync to node"
        why="There is no on-demand sync trigger in this API: /assets/manifest and /nodes/{id}/assets only ever report drift, and delivery itself runs on assets.settings' own syncIntervalSeconds timer or on the next upload. Nothing here can ask a node to fetch right now."
        preview={
          <button type="button" className="btn btn--secondary">
            Re-sync to node
          </button>
        }
      />
    </div>
  )
}
