/**
 * Shows / Media Playlists tab: the media.playlist configuration kind
 * (mediaplaylist.go), a sibling of show.playlist in the same workspace
 * (docs/ui-rebuild/OPEN-DECISIONS.md, "Media Playlists sits beside
 * Playlists"). Structured after ShowsPlaylists.tsx: a list Section on the
 * page body, a Drawer-hosted editor for the selected row or a new draft.
 *
 * The item editor here is the same audio-asset picker the night session's
 * background-audio item editor uses (audioAssetPicker.tsx), relocated
 * rather than reinvented.
 */
import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  deleteMediaPlaylist,
  getMediaPlaylist,
  getMediaPlaylistRevisions,
  listAssets,
  listConfigObjects,
  putMediaPlaylist,
  type Asset,
  type ConfigMediaPlaylist,
  type ConfigMediaPlaylistItem,
  type MediaPlaylistConfigResponse,
} from '../api'
import { Button, ButtonRow, Field, Input, Panes, RevisionHistory, RuledStrip, Section, SelectableRow, Segmented, Select, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { audioAssetOptions, slugify, type AudioAssetOption } from './showsModel'
import { AudioAssetPicker } from './audioAssetPicker'

type Playlist = MediaPlaylistConfigResponse

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; playlists: Playlist[]; assets: Asset[] }
  | { kind: 'failed'; reason: string }

function useMediaPlaylists(showId: string): { state: ListState; reload: () => void; updatePlaylist: (response: Playlist) => void; removePlaylist: (id: string) => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([listConfigObjects('media.playlist', showId), listAssets({ show: showId })])
      .then(async ([summaries, assetsResponse]) => {
        const playlists = await Promise.all(summaries.objects.map((s) => getMediaPlaylist(s.id)))
        if (!cancelled) setState({ kind: 'loaded', playlists, assets: assetsResponse.assets })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, attempt])

  const updatePlaylist = (response: Playlist) => {
    setState((prev) => (prev.kind === 'loaded' ? { ...prev, playlists: prev.playlists.map((p) => (p.id === response.id ? response : p)) } : prev))
  }
  const removePlaylist = (id: string) => {
    setState((prev) => (prev.kind === 'loaded' ? { ...prev, playlists: prev.playlists.filter((p) => p.id !== id) } : prev))
  }

  return { state, reload: () => setAttempt((n) => n + 1), updatePlaylist, removePlaylist }
}

const REPEAT_OPTIONS = [
  { value: 'none', label: 'None' },
  { value: 'item', label: 'Item' },
  { value: 'playlist', label: 'Playlist' },
] as const
const RESUME_OPTIONS = [
  { value: 'resume', label: 'Resume' },
  { value: 'restart', label: 'Restart' },
] as const
const TRANSITION_OPTIONS = [
  { value: 'sequential', label: 'Sequential' },
  { value: 'gapless', label: 'Gapless' },
  { value: 'crossfade', label: 'Crossfade' },
] as const

type ItemDraft = { sequence: string; target: string }
type BedDraft = {
  label: string
  items: ItemDraft[]
  repeat: ConfigMediaPlaylist['repeat']
  resume: ConfigMediaPlaylist['resume']
  itemTransition: ConfigMediaPlaylist['itemTransition']
  crossfadeMs: string
  maxGainDb: string
  fadeOutMs: string
  fadeInMs: string
}

const blankItem = (): ItemDraft => ({ sequence: '', target: '' })
const blankBed = (): BedDraft => ({
  label: '', items: [blankItem()], repeat: 'none', resume: 'resume', itemTransition: 'sequential',
  crossfadeMs: '', maxGainDb: '', fadeOutMs: '', fadeInMs: '',
})

function draftFromPlaylist(payload: ConfigMediaPlaylist): BedDraft {
  return {
    label: payload.label,
    items: payload.items.length === 0 ? [blankItem()] : payload.items.map((i) => ({ sequence: i.sequence, target: i.target })),
    repeat: payload.repeat, resume: payload.resume, itemTransition: payload.itemTransition,
    crossfadeMs: payload.crossfadeMs === undefined ? '' : String(payload.crossfadeMs),
    maxGainDb: String(payload.maxGainDb),
    fadeOutMs: payload.fadeOutMs === undefined ? '' : String(payload.fadeOutMs),
    fadeInMs: payload.fadeInMs === undefined ? '' : String(payload.fadeInMs),
  }
}

/** Client-side shape checks only; a kind "cue" item is never offered here, so the coordinator's own not-implemented refusal is what a stale or hand-authored item would surface, unpre-empted. */
function buildMediaPlaylistPayload(show: string, draft: BedDraft): { ok: true; value: ConfigMediaPlaylist } | { ok: false; error: string } {
  if (draft.label.trim() === '') return { ok: false, error: 'Label is required.' }
  const items: ConfigMediaPlaylistItem[] = []
  for (const [index, item] of draft.items.entries()) {
    if (item.sequence.trim() === '' || item.target.trim() === '') {
      return { ok: false, error: `Item ${index + 1} needs an audio asset selected.` }
    }
    items.push({ kind: 'asset', show, sequence: item.sequence.trim(), target: item.target.trim() })
  }
  if (items.length === 0) return { ok: false, error: 'A media playlist needs at least one item, the same way a show.playlist cannot save empty.' }
  const maxGainDb = Number(draft.maxGainDb)
  if (draft.maxGainDb.trim() === '' || Number.isNaN(maxGainDb) || maxGainDb > 0) {
    return { ok: false, error: 'Maximum gain (ceiling) must be a number no greater than 0 dB.' }
  }
  const fadeOutText = draft.fadeOutMs.trim()
  const fadeInText = draft.fadeInMs.trim()
  if ((fadeOutText === '') !== (fadeInText === '')) {
    return { ok: false, error: 'Fade-out and fade-in must be configured together, or both left empty for an instant cut.' }
  }
  if (draft.itemTransition === 'crossfade' && draft.crossfadeMs.trim() === '') {
    return { ok: false, error: 'Crossfade duration is required when item transition is crossfade.' }
  }
  return {
    ok: true,
    value: {
      label: draft.label.trim(), show, items,
      repeat: draft.repeat, resume: draft.resume, itemTransition: draft.itemTransition, maxGainDb,
      ...(draft.itemTransition === 'crossfade' ? { crossfadeMs: Number(draft.crossfadeMs) } : {}),
      ...(fadeOutText === '' ? {} : { fadeOutMs: Number(fadeOutText), fadeInMs: Number(fadeInText) }),
    },
  }
}

function BedFields({
  draft,
  onChange,
  assets,
}: {
  draft: BedDraft
  onChange: (patch: Partial<BedDraft>) => void
  assets: readonly AudioAssetOption[]
}) {
  const updateItem = (index: number, patch: Partial<ItemDraft>) =>
    onChange({ items: draft.items.map((item, i) => (i === index ? { ...item, ...patch } : item)) })
  const addItem = () => onChange({ items: [...draft.items, blankItem()] })
  const removeItem = (index: number) => onChange({ items: draft.items.filter((_, i) => i !== index) })

  return (
    <>
      <Field label="Label">{(p) => <Input {...p} value={draft.label} onChange={(e) => onChange({ label: e.target.value })} />}</Field>

      <div className="sm-grid sm-grid--auto sm-stack-3">
        <Segmented label="Repeat" value={draft.repeat} options={REPEAT_OPTIONS} onChange={(value) => onChange({ repeat: value })} />
        <Field label="Resume" help="How the bed resumes after it is re-entered.">
          {(p) => (
            <Select {...p} value={draft.resume} onChange={(e) => onChange({ resume: e.target.value as BedDraft['resume'] })}>
              {RESUME_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Between items">
          {(p) => (
            <Select {...p} value={draft.itemTransition} onChange={(e) => onChange({ itemTransition: e.target.value as BedDraft['itemTransition'] })}>
              {TRANSITION_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {draft.itemTransition === 'crossfade' && (
          <Field label="Crossfade (ms)" help="Required when item transition is crossfade.">
            {(p) => <Input {...p} type="number" min="0" step="1" value={draft.crossfadeMs} onChange={(e) => onChange({ crossfadeMs: e.target.value })} />}
          </Field>
        )}
        <Field label="Maximum gain (dB)" help="Must be 0 dB or lower.">
          {(p) => <Input {...p} type="number" max="0" step="0.1" value={draft.maxGainDb} onChange={(e) => onChange({ maxGainDb: e.target.value })} />}
        </Field>
        <Field label="Fade-out (ms)" help="Set together with fade-in, or leave both blank for an instant cut.">
          {(p) => <Input {...p} type="number" min="0" step="1" value={draft.fadeOutMs} onChange={(e) => onChange({ fadeOutMs: e.target.value })} />}
        </Field>
        <Field label="Fade-in (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.fadeInMs} onChange={(e) => onChange({ fadeInMs: e.target.value })} />}</Field>
      </div>

      <TableWrap label="Bed items, scrollable">
        <Table minWidth={420}>
          <thead>
            <tr>
              <th scope="col">Ord</th>
              <th scope="col">Audio asset</th>
              <th scope="col" />
            </tr>
          </thead>
          <tbody>
            {draft.items.map((item, index) => (
              <tr key={index}>
                <td className="sm-data">{index + 1}</td>
                <td>
                  <AudioAssetPicker
                    aria-label={`Audio asset for item ${index + 1}`}
                    assets={assets}
                    sequence={item.sequence}
                    target={item.target}
                    onPick={(asset) => updateItem(index, { sequence: asset?.sequence ?? '', target: asset?.target ?? '' })}
                  />
                </td>
                <td>
                  <Button variant="quiet" onClick={() => removeItem(index)} disabled={draft.items.length === 1} title={draft.items.length === 1 ? 'A media playlist needs at least one item.' : undefined}>
                    Remove
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </TableWrap>
      <Button onClick={addItem}>Add item</Button>
    </>
  )
}

function MediaPlaylistEditor({
  playlist,
  assets,
  model,
  onSaved,
  onDeleted,
}: {
  playlist: Playlist
  assets: readonly AudioAssetOption[]
  model: ReturnType<typeof useModelContext>
  onSaved: (response: Playlist) => void
  onDeleted: (id: string) => void
}) {
  const [draft, setDraft] = useState<BedDraft>(() => draftFromPlaylist(playlist.payload))
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<Playlist>, { kind: 'stale' }> | null>(null)
  const [deleteConfirmText, setDeleteConfirmText] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  useEffect(() => {
    setDraft(draftFromPlaylist(playlist.payload))
    setDirty(false)
    setSaveError(null)
    setStale(null)
    setDeleteConfirmText('')
    setDeleteError(null)
  }, [playlist])

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const change = (patch: Partial<BedDraft>) => {
    setDraft((current) => ({ ...current, ...patch }))
    setDirty(true)
  }

  const discard = () => {
    setDraft(draftFromPlaylist(playlist.payload))
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    const built = buildMediaPlaylistPayload(playlist.payload.show, draft)
    if (!built.ok) {
      setSaveError(built.error)
      return
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: playlist,
      read: () => getMediaPlaylist(playlist.id),
      write: () => putMediaPlaylist(playlist.id, built.value),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onSaved(outcome.response)
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

  const remove = () => {
    setDeleting(true)
    setDeleteError(null)
    deleteMediaPlaylist(playlist.id)
      .then(() => onDeleted(playlist.id))
      .catch((err: unknown) => setDeleteError(describeApiError(err)))
      .finally(() => setDeleting(false))
  }

  return (
    <Section id="mp-editor" title={`Editing ${playlist.payload.label}`} eyebrow="media.playlist">
      <BedFields draft={draft} onChange={change} assets={assets} />

      <ButtonRow>
        <Button variant="primary" onClick={save} disabled={!dirty || saving || !saveGate.allowed} title={saveGate.allowed ? undefined : saveGate.reason}>
          {saving ? 'Saving…' : 'Save media playlist'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving || !saveGate.allowed} title={saveGate.allowed ? undefined : saveGate.reason}>
          Discard changes
        </Button>
        <div className="sm-push-end">
          <RevisionHistory id="mp-rev" fetch={() => getMediaPlaylistRevisions(playlist.id)} reloadKey={`${playlist.id}:${playlist.revision}`} />
        </div>
      </ButtonRow>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            getMediaPlaylist(playlist.id).then(onSaved).catch((err: unknown) => setSaveError(describeApiError(err)))
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}

      <div className="sm-panel sm-stack-5">
        <h3 className="sm-subsection__title">Delete this media playlist</h3>
        <p className="sm-small sm-muted">A tombstone: nothing else in this codebase's reference graph names a media.playlist id, so deleting one orphans nothing.</p>
        <Field label={`Type ${playlist.payload.label} to confirm`} help="Asks for the playlist's own label before it proceeds.">
          {(p) => <Input {...p} value={deleteConfirmText} onChange={(e) => setDeleteConfirmText(e.target.value)} />}
        </Field>
        {deleteError !== null && <RuledStrip absence="failed" label="Delete failed" fact={deleteError} />}
        <ButtonRow>
          <Button
            variant="danger"
            onClick={remove}
            disabled={deleteConfirmText !== playlist.payload.label || deleting || !saveGate.allowed}
            title={!saveGate.allowed ? saveGate.reason : deleteConfirmText !== playlist.payload.label ? 'Type the label exactly to enable this.' : undefined}
          >
            {deleting ? 'Deleting…' : 'Delete media playlist'}
          </Button>
        </ButtonRow>
      </div>
    </Section>
  )
}

function MediaPlaylistDraft({
  showId,
  assets,
  model,
  onCreated,
  onDiscard,
  onOpenExisting,
}: {
  showId: string
  assets: readonly AudioAssetOption[]
  model: ReturnType<typeof useModelContext>
  onCreated: (response: Playlist) => void
  onDiscard: () => void
  onOpenExisting: (id: string) => void
}) {
  const [draft, setDraft] = useState<BedDraft>(blankBed)
  const [id, setId] = useState('')
  const [idTouched, setIdTouched] = useState(false)
  const [creating, setCreating] = useState(false)
  const [taken, setTaken] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  const createGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const change = (patch: Partial<BedDraft>) => {
    setDraft((current) => {
      const next = { ...current, ...patch }
      if (!idTouched && patch.label !== undefined) setId(slugify(patch.label))
      return next
    })
  }
  const onIdChange = (value: string) => {
    setId(value)
    setIdTouched(true)
  }

  const create = () => {
    if (id === '') return
    const built = buildMediaPlaylistPayload(showId, draft)
    if (!built.ok) {
      setCreateError(built.error)
      return
    }
    setCreating(true)
    setTaken(false)
    setCreateError(null)
    guardedCreate({
      read: () => getMediaPlaylist(id),
      write: () => putMediaPlaylist(id, built.value),
    })
      .then((outcome) => {
        if (outcome.kind === 'taken') {
          setTaken(true)
          return
        }
        if (outcome.kind === 'unreadable') {
          setCreateError(outcome.reason)
          return
        }
        onCreated(outcome.response)
      })
      .catch((err: unknown) => setCreateError(describeApiError(err)))
      .finally(() => setCreating(false))
  }

  return (
    <Section id="mp-editor" title="New media playlist" eyebrow="media.playlist">
      <p className="sm-small sm-faint">
        The coordinator refuses a media playlist with no items, the same way it refuses a show.playlist with no
        entries, so the first item is part of creation.
      </p>
      <Field label="Id" help="Immutable once created.">
        {(p) => <Input {...p} className="sm-data" value={id} onChange={(e) => onIdChange(e.target.value)} />}
      </Field>
      <BedFields draft={draft} onChange={change} assets={assets} />

      {taken && (
        <RuledStrip
          absence="failed"
          label="Id taken"
          fact={
            <>
              <span className="sm-data">{id}</span> already names a media playlist.{' '}
              <button type="button" className="sm-linkbutton" onClick={() => onOpenExisting(id)}>
                Open it
              </button>
            </>
          }
        />
      )}
      {createError !== null && <RuledStrip absence="failed" label="Create failed" fact={createError} />}

      <ButtonRow>
        <Button variant="primary" onClick={create} disabled={id === '' || creating || !createGate.allowed} title={!createGate.allowed ? createGate.reason : undefined}>
          {creating ? 'Creating…' : 'Create media playlist'}
        </Button>
        <Button variant="quiet" onClick={onDiscard} disabled={creating}>
          Discard
        </Button>
      </ButtonRow>
    </Section>
  )
}

export function ShowsMediaPlaylists() {
  const { id: showId = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const { state, reload, updatePlaylist, removePlaylist } = useMediaPlaylists(showId)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [drafting, setDrafting] = useState(false)

  const playlists = state.kind === 'loaded' ? state.playlists : []
  const assets = state.kind === 'loaded' ? audioAssetOptions(state.assets) : []
  const selected = playlists.find((p) => p.id === selectedId) ?? null
  const createGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  if (state.kind === 'loading') {
    return (
      <Section id="mp-list" title="Media playlists in this show">
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's media playlists." />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="mp-list" title="Media playlists in this show">
        <RuledStrip
          absence="failed"
          label="Read failed"
          fact={state.reason}
          detail={
            <button type="button" className="sm-linkbutton" onClick={reload}>
              Try again
            </button>
          }
        />
      </Section>
    )
  }

  const closeInspector = () => {
    setDrafting(false)
    setSelectedId(null)
  }

  return (
    <Panes inspectorOpen={drafting || selected !== null} onInspectorClose={closeInspector} inspectorLabelledBy="mp-editor" inspectorWidth="wide">
      <div>
        <p className="sm-small sm-muted sm-stack-3">
          <span className="sm-data">show.playlist</span> is a list of cues a runner steps through.{' '}
          <span className="sm-data">media.playlist</span> is a list of things the audio engine plays as a bed.
        </p>
        <Section
          id="mp-list"
          title="Media playlists in this show"
          aside={
            <Button
              onClick={() => {
                setDrafting(true)
                setSelectedId(null)
              }}
              disabled={drafting || !createGate.allowed}
              title={!createGate.allowed ? createGate.reason : undefined}
            >
              New media playlist
            </Button>
          }
        >
          {playlists.length === 0 ? (
            <RuledStrip absence="empty" label="None" fact="This show has no media playlist configured." />
          ) : (
            <TableWrap label="Media playlists, scrollable">
              <Table minWidth={420}>
                <thead>
                  <tr>
                    <th scope="col">Bed</th>
                    <th scope="col">Items</th>
                  </tr>
                </thead>
                <tbody>
                  {playlists.map((playlist) => (
                    <SelectableRow
                      key={playlist.id}
                      selected={selected?.id === playlist.id}
                      onActivate={() => {
                        setSelectedId(playlist.id)
                        setDrafting(false)
                      }}
                      ariaLabel={`Edit ${playlist.payload.label}`}
                    >
                      <td>
                        <strong>{playlist.payload.label}</strong>
                        {selected?.id === playlist.id && <span className="sm-viewing">Editing</span>}
                        <br />
                        <span className="sm-small sm-muted">
                          {playlist.payload.repeat === 'none' ? 'No repeat' : `Repeat ${playlist.payload.repeat}`}
                        </span>
                      </td>
                      <td className="sm-data">{playlist.payload.items.length}</td>
                    </SelectableRow>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
          )}
        </Section>
      </div>

      <aside>
        {drafting && (
          <MediaPlaylistDraft
            showId={showId}
            assets={assets}
            model={model}
            onCreated={(response) => {
              setSelectedId(response.id)
              setDrafting(false)
              reload()
            }}
            onDiscard={() => setDrafting(false)}
            onOpenExisting={(id) => {
              setSelectedId(id)
              setDrafting(false)
            }}
          />
        )}
        {!drafting && selected !== null && (
          <MediaPlaylistEditor
            playlist={selected}
            assets={assets}
            model={model}
            onSaved={updatePlaylist}
            onDeleted={(id) => {
              removePlaylist(id)
              closeInspector()
            }}
          />
        )}
      </aside>
    </Panes>
  )
}
