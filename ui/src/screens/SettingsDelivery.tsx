import { useEffect, useState } from 'react'
import { getAssetsSettingsConfig, listAssets, putAssetsSettingsConfig, type AssetsSettingsConfigResponse } from '../api'
import { Button, ButtonRow, Field, Input, NotWired, NotWiredBanner, RuledStrip, Section, Select } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { formatBytes } from './showsModel'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: AssetsSettingsConfigResponse }
  | { kind: 'failed'; reason: string }

export function SettingsDelivery() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [assetCount, setAssetCount] = useState<number | null>(null)
  const [assetCountFailed, setAssetCountFailed] = useState(false)

  const [contentBaseUrl, setContentBaseUrl] = useState('')
  const [maxUploadBytes, setMaxUploadBytes] = useState('')
  const [syncIntervalSeconds, setSyncIntervalSeconds] = useState('')
  const [inventoryIntervalSeconds, setInventoryIntervalSeconds] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<AssetsSettingsConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getAssetsSettingsConfig()
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        setContentBaseUrl(response.payload.contentBaseUrl)
        setMaxUploadBytes(String(response.payload.maxUploadBytes))
        setSyncIntervalSeconds(String(response.payload.syncIntervalSeconds))
        setInventoryIntervalSeconds(String(response.payload.inventoryIntervalSeconds))
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  useEffect(() => {
    let cancelled = false
    listAssets()
      .then((response) => {
        if (!cancelled) setAssetCount(response.assets.length)
      })
      .catch(() => {
        if (!cancelled) setAssetCountFailed(true)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const discard = () => {
    if (state.kind !== 'loaded') return
    setContentBaseUrl(state.response.payload.contentBaseUrl)
    setMaxUploadBytes(String(state.response.payload.maxUploadBytes))
    setSyncIntervalSeconds(String(state.response.payload.syncIntervalSeconds))
    setInventoryIntervalSeconds(String(state.response.payload.inventoryIntervalSeconds))
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    if (state.kind !== 'loaded') return
    const loaded = state.response
    const maxUpload = Number(maxUploadBytes)
    const sync = Number(syncIntervalSeconds)
    const inventory = Number(inventoryIntervalSeconds)
    if (!Number.isFinite(maxUpload) || maxUpload <= 0 || !Number.isFinite(sync) || sync <= 0 || !Number.isFinite(inventory) || inventory <= 0) {
      setSaveError('Max upload size, sync interval and inventory interval must each be a positive number.')
      return
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded,
      read: getAssetsSettingsConfig,
      write: () =>
        putAssetsSettingsConfig({
          contentBaseUrl,
          maxUploadBytes: maxUpload,
          syncIntervalSeconds: sync,
          inventoryIntervalSeconds: inventory,
        }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
          setDirty(false)
          return
        }
        if (outcome.kind === 'stale') {
          setStale(outcome)
          return
        }
        setSaveError(outcome.reason)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Content delivery</p>
      <h2 className="sm-section__title">Where asset bytes live and when they move</h2>
      <p className="sm-page__lede">
        Metadata lives in the coordinator's database; bytes never do. Nodes play from their own disk, so nothing here
        is in the playback path.
      </p>

      <NotWiredBanner
        what="Store backend and path"
        missing="stored field for where asset bytes live"
        detail="Nothing in this coordinator's configuration names a backend or a path. The controls below are drawn to their final shape and are inert."
      />

      <Section id="st-store" title="Store backend">
        <div className="sm-grid sm-form-column">
          <Field label="Backend">
            {(props) => (
              <NotWired>
                <Select {...props}>
                  <option>Coordinator volume</option>
                  <option>Mounted filesystem</option>
                  <option>SMB share</option>
                </Select>
              </NotWired>
            )}
          </Field>
          <Field label="Path">
            {(props) => (
              <NotWired>
                <Input {...props} value="" readOnly />
              </NotWired>
            )}
          </Field>
        </div>
        <RuledStrip
          absence="unobserved"
          label="Disk"
          fact={
            assetCountFailed
              ? 'Asset count could not be read just now.'
              : assetCount === null
                ? 'Counting assets across all shows.'
                : `${assetCount} ${assetCount === 1 ? 'asset' : 'assets'} across all shows.`
          }
          detail="The coordinator does not report store size or capacity. No byte total or percentage is shown because none is reported."
        />
      </Section>

      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for asset delivery settings." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <Section id="st-sync" title="Distribution">
          <div className="sm-grid sm-form-column">
            <Field label="Content base URL" help="Empty is a real, deliberate state: the asset sync service does not run, and nothing ever reaches a node over the network.">
              {(props) => (
                <Input
                  {...props}
                  value={contentBaseUrl}
                  onChange={(e) => {
                    setContentBaseUrl(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
            <Field label="Max upload size" help={`${formatBytes(Number(maxUploadBytes) || 0)}, title carries the exact byte count.`}>
              {(props) => (
                <Input
                  {...props}
                  type="number"
                  min={1}
                  title={`${maxUploadBytes} bytes`}
                  value={maxUploadBytes}
                  onChange={(e) => {
                    setMaxUploadBytes(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
            <Field label="Sync interval (seconds)" help="Plus immediately on every upload.">
              {(props) => (
                <Input
                  {...props}
                  type="number"
                  min={1}
                  value={syncIntervalSeconds}
                  onChange={(e) => {
                    setSyncIntervalSeconds(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
            <Field label="Inventory interval (seconds)">
              {(props) => (
                <Input
                  {...props}
                  type="number"
                  min={1}
                  value={inventoryIntervalSeconds}
                  onChange={(e) => {
                    setInventoryIntervalSeconds(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          </div>
          <p className="sm-section__footnote">
            Sync never runs because a show started. A node missing an asset is a readiness fault found before a show,
            not discovered during one.
          </p>
        </Section>
      )}

      <ButtonRow>
        <Button
          variant="primary"
          onClick={save}
          disabled={!dirty || saving || state.kind !== 'loaded' || !gate.allowed}
          title={gate.allowed ? undefined : gate.reason}
        >
          {saving ? 'Saving…' : 'Save delivery'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
          Discard changes
        </Button>
        {state.kind === 'loaded' && (
          <span className="sm-small sm-muted sm-push-end">
            Active revision <span className="sm-data">{state.response.revision}</span> ·{' '}
            {state.response.createdByPrincipalName ?? 'unknown principal'} {formatClock(state.response.updatedAt) ?? 'at an unrecorded time'}
          </span>
        )}
      </ButtonRow>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            setAttempt((n) => n + 1)
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
    </>
  )
}
