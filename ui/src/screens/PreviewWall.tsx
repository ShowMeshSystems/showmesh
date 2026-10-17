import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, getDashboardLayout, getPreviews, putDashboardLayout, type DashboardLayoutConfigResponse, type PreviewsResponse } from '../api'
import { Button, PreviewGrid, ReorderButtons, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave, staleOutcome } from '../domain/save'
import { formatClock } from '../domain/time'
import { PreviewWallTile } from './PreviewWallTile'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'
import { applyLayout, hideTile, layoutOf, moveStep, moveTo, showTile, type PreviewLayout } from './previewLayoutModel'

export const PREVIEW_REFRESH_MS = 5000

export const PREVIEW_READ_DENIED =
  'This device does not hold "observation:read", so it cannot show previews. Ask an administrator to grant it.'

/** The saved layout: null when none has been saved, undefined when it could not be read. */
type SavedLayout = DashboardLayoutConfigResponse | null | undefined

type ReadFailure = { kind: 'failed'; reason: string } | { kind: 'denied' }

type PreviewRead = { data: PreviewsResponse | null; readAt: number | null; failure: ReadFailure | null }

async function readSavedLayout(): Promise<SavedLayout> {
  try {
    return await getDashboardLayout()
  } catch (err: unknown) {
    return err instanceof ApiError && err.status === 404 ? null : undefined
  }
}

function usePreviews(savedLayout: React.MutableRefObject<SavedLayout>): PreviewRead & { refresh: () => Promise<void> } {
  const [read, setRead] = useState<PreviewRead>({ data: null, readAt: null, failure: null })
  const alive = useRef(true)
  const refreshSeq = useRef(0)

  const refresh = useCallback(async () => {
    const seq = ++refreshSeq.current
    const current = () => alive.current && seq === refreshSeq.current
    try {
      const [data, layout] = await Promise.all([getPreviews(), readSavedLayout()])
      if (!current()) return
      savedLayout.current = layout
      setRead({ data, readAt: Date.now(), failure: null })
    } catch (err: unknown) {
      if (!current()) return
      if (err instanceof ApiError && err.status === 403) setRead({ data: null, readAt: null, failure: { kind: 'denied' } })
      else setRead((previous) => ({ ...previous, failure: { kind: 'failed', reason: describeApiError(err) } }))
    }
  }, [savedLayout])

  useEffect(() => {
    alive.current = true
    void refresh()
    const timer = setInterval(() => void refresh(), PREVIEW_REFRESH_MS)
    return () => {
      alive.current = false
      clearInterval(timer)
    }
  }, [refresh])

  return { ...read, refresh }
}

type SaveResult = { kind: 'saved'; response: DashboardLayoutConfigResponse } | { kind: 'stale'; stale: StaleWrite } | { kind: 'error'; reason: string }

/** Writes a layout through the shared stale-save check, sending the version it was read at. */
async function saveLayout(loaded: SavedLayout, layout: PreviewLayout): Promise<SaveResult> {
  if (loaded === undefined) return { kind: 'error', reason: 'The saved tile order could not be read, so this change was not saved. Try again in a few seconds.' }
  const request = { previews: layout }
  try {
    if (loaded === null) {
      const outcome = await guardedCreate({ read: getDashboardLayout, write: () => putDashboardLayout(request, 'none') })
      if (outcome.kind === 'created') return { kind: 'saved', response: outcome.response }
      if (outcome.kind === 'unreadable') return { kind: 'error', reason: outcome.reason }
      const current = await readSavedLayout()
      return current ? { kind: 'stale', stale: staleOutcome(null, current) } : { kind: 'error', reason: 'Someone else saved a tile order first. Reload and try again.' }
    }
    const outcome = await guardedSave({ loaded, read: getDashboardLayout, write: () => putDashboardLayout(request, loaded.revision) })
    if (outcome.kind === 'saved') return { kind: 'saved', response: outcome.response }
    if (outcome.kind === 'stale') return { kind: 'stale', stale: outcome }
    return { kind: 'error', reason: outcome.reason }
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 409) {
      const current = await readSavedLayout()
      if (current) return { kind: 'stale', stale: staleOutcome(loaded, current) }
    }
    return { kind: 'error', reason: describeApiError(err) }
  }
}

/** The Dashboard's live preview grid. It renders nothing while no gateway is configured. */
export function PreviewWall() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'dashboard:write')
  const savedLayout = useRef<SavedLayout>(undefined)
  const { data, readAt, failure, refresh } = usePreviews(savedLayout)
  const [pending, setPending] = useState<PreviewLayout | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<StaleWrite | null>(null)
  const [showHiddenList, setShowHiddenList] = useState(false)
  const [dragKey, setDragKey] = useState<string | null>(null)
  const [overKey, setOverKey] = useState<string | null>(null)
  const saving = useRef(false)
  const queued = useRef<PreviewLayout | null>(null)

  if (data === null) {
    // Most installs have no gateway, so the section stays hidden until the first answer says otherwise.
    if (failure === null) return null
    return (
      <Section id="db-previews" title="Live previews">
        {failure.kind === 'denied' ? (
          <RuledStrip absence="noPermission" label="Previews not shown" fact={PREVIEW_READ_DENIED} />
        ) : (
          <RuledStrip absence="failed" label="Previews unread" fact={failure.reason} />
        )}
      </Section>
    )
  }
  if (data.gateway.state === 'not_configured') return null

  const gatewayState: string = data.gateway.state
  const layout = pending ?? layoutOf(data.tiles)
  const tiles = pending === null ? data.tiles : applyLayout(data.tiles, pending)
  const shown = tiles.filter((tile) => !tile.hidden)
  const hidden = tiles.filter((tile) => tile.hidden)

  const commit = async (next: PreviewLayout) => {
    if (!gate.allowed || next === layout) return
    setPending(next)
    setSaveError(null)
    setStale(null)
    if (saving.current) {
      queued.current = next
      return
    }
    saving.current = true
    let current: PreviewLayout | null = next
    while (current !== null) {
      const result = await saveLayout(savedLayout.current, current)
      if (result.kind === 'saved') {
        savedLayout.current = result.response
        await refresh()
      } else if (result.kind === 'stale') {
        queued.current = null
        setStale(result.stale)
      } else if (queued.current === null) {
        setSaveError(result.reason)
      }
      current = queued.current
      queued.current = null
    }
    saving.current = false
    setPending(null)
  }

  const endDrag = () => {
    setDragKey(null)
    setOverKey(null)
  }

  return (
    <Section
      id="db-previews"
      title="Live previews"
      aside={
        hidden.length > 0 ? (
          <Button variant="quiet" aria-expanded={showHiddenList} onClick={() => setShowHiddenList(!showHiddenList)}>
            Hidden tiles ({hidden.length})
          </Button>
        ) : undefined
      }
    >
      {gatewayState === 'unreachable' && <RuledStrip absence="failed" label="Gateway unreachable" fact={data.gateway.message} />}
      {gatewayState === 'connecting' && <RuledStrip absence="loading" label="Connecting" fact={data.gateway.message} />}
      {gatewayState === 'reachable' && data.gateway.ndiDiscovery?.state === 'not_running' && (
        <RuledStrip absence="failed" label="NDI not running" fact={data.gateway.ndiDiscovery.message} />
      )}
      {showHiddenList && hidden.length > 0 && (
        <div className="sm-panel sm-stack-4" role="group" aria-label="Hidden tiles">
          {hidden.map((tile) => (
            <div key={tile.key} className="sm-inline-row">
              <span>{tile.label}</span>
              <Button variant="quiet" disabled={!gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={() => void commit(showTile(layout, tile.key))}>
                Show {tile.label}
              </Button>
            </div>
          ))}
        </div>
      )}
      {shown.length === 0 ? (
        <RuledStrip
          absence="empty"
          label="No tiles"
          fact={hidden.length > 0 ? 'Every tile is hidden. Show one from Hidden tiles above.' : 'The gateway has announced no sources. Add a camera in Settings, or start an NDI source.'}
        />
      ) : (
        <PreviewGrid>
          {shown.map((tile, index) => (
            <PreviewWallTile
              key={tile.key}
              tile={tile}
              enlargeable
              withMessage={gatewayState === 'reachable'}
              handle={gate.allowed ? <span className="sm-preview__handle" aria-hidden="true">⠿</span> : undefined}
              actions={
                <ReorderButtons
                  itemLabel={tile.label}
                  orientation="horizontal"
                  leave="hide"
                  onMoveUp={() => void commit(moveStep(layout, tile.key, -1))}
                  onMoveDown={() => void commit(moveStep(layout, tile.key, 1))}
                  onRemove={() => void commit(hideTile(layout, tile.key))}
                  moveUpReason={!gate.allowed ? gate.reason : index === 0 ? 'Already first.' : undefined}
                  moveDownReason={!gate.allowed ? gate.reason : index === shown.length - 1 ? 'Already last.' : undefined}
                  removeReason={gate.allowed ? undefined : gate.reason}
                />
              }
              dragProps={{
                draggable: gate.allowed,
                dragState: dragKey === tile.key ? 'dragging' : overKey === tile.key ? 'over' : undefined,
                onDragStart: (event) => {
                  event.dataTransfer.setData('text/plain', tile.key)
                  event.dataTransfer.effectAllowed = 'move'
                  setDragKey(tile.key)
                },
                onDragOver: (event) => {
                  if (dragKey === null || dragKey === tile.key) return
                  event.preventDefault()
                  setOverKey(tile.key)
                },
                onDrop: (event) => {
                  event.preventDefault()
                  if (dragKey !== null && dragKey !== tile.key) void commit(moveTo(layout, dragKey, layout.order.indexOf(tile.key)))
                  endDrag()
                },
                onDragEnd: endDrag,
              }}
            />
          ))}
        </PreviewGrid>
      )}
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            void refresh()
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Layout not saved" fact={`${saveError} The tiles are back in their saved order.`} />}
      {failure !== null && readAt !== null && (
        <p className="sm-section__footnote">
          <StatusPair tone="warn" label="Stale" /> Previews could not be refreshed. Showing what was read at {formatClock(new Date(readAt).toISOString())}.
        </p>
      )}
    </Section>
  )
}
