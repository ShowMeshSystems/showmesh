import { useCallback, useEffect, useRef, useState } from 'react'
import { getPreviews, putDashboardLayout, type PreviewsResponse } from '../api'
import { Button, ButtonRow, PreviewGrid, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { PreviewWallTile } from './PreviewWallTile'
import { applyLayout, hideTile, layoutOf, moveStep, moveTo, showTile, type PreviewLayout } from './previewLayoutModel'

export const PREVIEW_REFRESH_MS = 5000

type PreviewRead = { data: PreviewsResponse | null; readAt: number | null; failed: boolean }

function usePreviews(): PreviewRead & { refresh: () => Promise<void> } {
  const [read, setRead] = useState<PreviewRead>({ data: null, readAt: null, failed: false })
  const alive = useRef(true)

  const refresh = useCallback(async () => {
    try {
      const data = await getPreviews()
      if (alive.current) setRead({ data, readAt: Date.now(), failed: false })
    } catch {
      if (alive.current) setRead((previous) => ({ ...previous, failed: true }))
    }
  }, [])

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

/** The Dashboard's live preview grid. It renders nothing until a gateway is configured. */
export function PreviewWall() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const { data, readAt, failed, refresh } = usePreviews()
  const [pending, setPending] = useState<PreviewLayout | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [showHiddenList, setShowHiddenList] = useState(false)
  const [dragKey, setDragKey] = useState<string | null>(null)
  const [overKey, setOverKey] = useState<string | null>(null)
  const saveSeq = useRef(0)

  if (data === null || data.gateway.state === 'not_configured') return null

  const layout = pending ?? layoutOf(data.tiles)
  const tiles = pending === null ? data.tiles : applyLayout(data.tiles, pending)
  const shown = tiles.filter((tile) => !tile.hidden)
  const hidden = tiles.filter((tile) => tile.hidden)

  const commit = async (next: PreviewLayout) => {
    if (!gate.allowed || next === layout) return
    const seq = ++saveSeq.current
    setPending(next)
    setSaveError(null)
    try {
      await putDashboardLayout({ previews: next })
      await refresh()
    } catch (err: unknown) {
      setSaveError(describeApiError(err))
    } finally {
      if (seq === saveSeq.current) setPending(null)
    }
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
      {data.gateway.state === 'unreachable' && (
        <RuledStrip
          absence="failed"
          label="Gateway unreachable"
          fact={data.gateway.message !== '' ? data.gateway.message : 'The preview gateway did not answer. Check that it is running and that its address in Settings is right.'}
        />
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
              handle={gate.allowed ? <span className="sm-preview__handle" aria-hidden="true">⠿</span> : undefined}
              actions={
                <ButtonRow>
                  <Button
                    size="icon"
                    variant="quiet"
                    aria-label={`Move ${tile.label} earlier`}
                    disabled={!gate.allowed || index === 0}
                    title={!gate.allowed ? gate.reason : index === 0 ? 'Already first.' : undefined}
                    onClick={() => void commit(moveStep(layout, tile.key, -1))}
                  >
                    <span aria-hidden="true">◀</span>
                  </Button>
                  <Button
                    size="icon"
                    variant="quiet"
                    aria-label={`Move ${tile.label} later`}
                    disabled={!gate.allowed || index === shown.length - 1}
                    title={!gate.allowed ? gate.reason : index === shown.length - 1 ? 'Already last.' : undefined}
                    onClick={() => void commit(moveStep(layout, tile.key, 1))}
                  >
                    <span aria-hidden="true">▶</span>
                  </Button>
                  <Button
                    variant="quiet"
                    aria-label={`Hide ${tile.label}`}
                    disabled={!gate.allowed}
                    title={gate.allowed ? undefined : gate.reason}
                    onClick={() => void commit(hideTile(layout, tile.key))}
                  >
                    Hide
                  </Button>
                </ButtonRow>
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
      {saveError !== null && <RuledStrip absence="failed" label="Layout not saved" fact={`${saveError} The tiles are back in their saved order.`} />}
      {failed && readAt !== null && (
        <p className="sm-section__footnote">
          <StatusPair tone="warn" label="Stale" /> Previews could not be refreshed. Showing what was read at {formatClock(new Date(readAt).toISOString())}.
        </p>
      )}
    </Section>
  )
}
