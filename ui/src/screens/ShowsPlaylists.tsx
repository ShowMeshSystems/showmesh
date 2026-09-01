import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  getFPPPlaylistDefinitionEntries,
  getFPPPlaylistReadiness,
  getShowPlaylist,
  getShowPlaylistRevisions,
  listFPPPlaylistDefinitions,
  putShowPlaylist,
  type ConfigObjectSummary,
  type ConfigShowPlaylist,
  type ConfigShowPlaylistEntry,
  type FPPPlaylistDefinitionEntry,
  type FPPPlaylistDefinitionMetadata,
  type FPPPlaylistReadinessResponse,
  type ShowPlaylistConfigResponse,
} from '../api'
import { randomUUIDv4 } from '../api/uuid'
import { Button, ButtonRow, Callout, DefinitionStrip, Field, Input, Panes, RevisionHistory, RuledStrip, Section, SelectableRow, Segmented, Select, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { fetchShowContents, fetchShowPlaylists } from './showsData'
import { cueLabel, fppInstanceLabel, fppInstanceRoute, newerDefinition, playlistRows, slugify } from './showsModel'

type Playlist = ShowPlaylistConfigResponse

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; cues: ConfigObjectSummary[]; playlists: Playlist[] }
  | { kind: 'failed'; reason: string }

function usePlaylists(showId: string): { state: ListState; reload: () => void; updatePlaylist: (response: Playlist) => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    fetchShowContents(showId)
      .then(async (contents) => {
        const playlists = await fetchShowPlaylists(contents.playlists)
        if (!cancelled) setState({ kind: 'loaded', cues: contents.cues, playlists })
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

  return { state, reload: () => setAttempt((n) => n + 1), updatePlaylist }
}

type FPPEvidence = {
  state: 'loading' | 'loaded' | 'failed'
  entries: FPPPlaylistDefinitionEntry[]
  definitions: FPPPlaylistDefinitionMetadata[]
  reason: string | null
}

function useFPPEvidence(playlist: Playlist | null): FPPEvidence {
  const [state, setState] = useState<FPPEvidence>({ state: 'loading', entries: [], definitions: [], reason: null })

  useEffect(() => {
    if (playlist === null || playlist.payload.runner !== 'fpp' || playlist.payload.fpp === undefined) {
      setState({ state: 'loaded', entries: [], definitions: [], reason: null })
      return
    }
    let cancelled = false
    const binding = playlist.payload.fpp
    setState({ state: 'loading', entries: [], definitions: [], reason: null })
    Promise.all([getFPPPlaylistDefinitionEntries(binding.instanceUuid, binding.playlistHash), listFPPPlaylistDefinitions()])
      .then(([entries, definitions]) => {
        if (!cancelled) setState({ state: 'loaded', entries: entries.entries, definitions: definitions.definitions, reason: null })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ state: 'failed', entries: [], definitions: [], reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [playlist])

  return state
}

type ReadinessState = { state: 'idle' | 'loading' | 'loaded' | 'failed'; response: FPPPlaylistReadinessResponse | null; reason: string | null }

function useReadiness(playlistId: string | null): { state: ReadinessState; check: () => void } {
  const [state, setState] = useState<ReadinessState>({ state: 'idle', response: null, reason: null })

  useEffect(() => {
    setState({ state: 'idle', response: null, reason: null })
  }, [playlistId])

  const check = () => {
    if (playlistId === null) return
    setState({ state: 'loading', response: null, reason: null })
    getFPPPlaylistReadiness(playlistId)
      .then((response) => setState({ state: 'loaded', response, reason: null }))
      .catch((err: unknown) => setState({ state: 'failed', response: null, reason: describeApiError(err) }))
  }

  return { state, check }
}

export function ShowsPlaylists() {
  const { id: showId = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const { state, reload, updatePlaylist } = usePlaylists(showId)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [drafting, setDrafting] = useState(false)

  const playlists = state.kind === 'loaded' ? state.playlists : []
  const selected = playlists.find((p) => p.id === selectedId) ?? null
  const evidence = useFPPEvidence(!drafting && selected !== null && selected.payload.runner === 'fpp' ? selected : null)
  const readiness = useReadiness(drafting ? null : (selected?.id ?? null))
  const createGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  if (state.kind === 'loading') {
    return (
      <Section id="pl-list" title="Playlists in this show">
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's playlists." />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="pl-list" title="Playlists in this show">
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

  const rows = playlistRows(playlists)
  const closeInspector = () => {
    setDrafting(false)
    setSelectedId(null)
  }

  return (
    <Panes inspectorOpen={drafting || selected !== null} onInspectorClose={closeInspector} inspectorLabelledBy="pl-editor" inspectorWidth="wide">
      <div>
        <Section
          id="pl-list"
          title="Playlists in this show"
          aside={
            <Button
              onClick={() => {
                setDrafting(true)
                setSelectedId(null)
              }}
              disabled={drafting || !createGate.allowed}
              title={!createGate.allowed ? createGate.reason : undefined}
            >
              New playlist
            </Button>
          }
        >
          {rows.length === 0 ? (
            <RuledStrip absence="empty" label="None" fact="This show has no playlist configured." />
          ) : (
            <div className="sm-stack-3">
              <TableWrap label="Playlists, scrollable">
                <Table minWidth={480}>
                  <thead>
                    <tr>
                      <th scope="col">Playlist</th>
                      <th scope="col">Runner</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row) => (
                      <SelectableRow
                        key={row.id}
                        selected={selected?.id === row.id}
                        onActivate={() => {
                          setSelectedId(row.id)
                          setDrafting(false)
                        }}
                        ariaLabel={`Edit ${row.label}`}
                      >
                        <td>
                          <strong>{row.label}</strong>
                          {selected?.id === row.id && <span className="sm-viewing">Editing</span>}
                          <br />
                          <span className="sm-small sm-muted">{row.detail}</span>
                        </td>
                        <td className="sm-small sm-muted">{row.runnerLabel}</td>
                      </SelectableRow>
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
            </div>
          )}
        </Section>

        {playlists.length > 1 && (
          <Callout>
            Multiple playlists in the same show run concurrently. FPP advances the lighting sequence while ShowMesh
            independently advances the music bed; two runners are never authoritative for the same playlist.
          </Callout>
        )}
      </div>

      <aside>
        {drafting && (
          <PlaylistDraft
            showId={showId}
            cues={state.kind === 'loaded' ? state.cues : []}
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

        {!drafting && selected !== null && selected.payload.runner === 'fpp' && (
          <FPPPlaylistEditor playlist={selected} cues={state.kind === 'loaded' ? state.cues : []} evidence={evidence} readiness={readiness} model={model} onSaved={updatePlaylist} />
        )}

        {!drafting && selected !== null && selected.payload.runner === 'showmesh-audio' && (
          <AudioPlaylistEditor playlist={selected} cues={state.kind === 'loaded' ? state.cues : []} model={model} onSaved={updatePlaylist} />
        )}
      </aside>
    </Panes>
  )
}

type MismatchPolicy = NonNullable<ConfigShowPlaylist['mismatchPolicy']>

const MISMATCH_OPTIONS: readonly { value: MismatchPolicy; label: string }[] = [
  { value: 'hold', label: 'Hold' },
  { value: 'blackAndSilence', label: 'Black & silence' },
  { value: 'safeCue', label: 'Safe cue' },
]

function FPPPlaylistEditor({
  playlist,
  cues,
  evidence,
  readiness,
  model,
  onSaved,
}: {
  playlist: Playlist
  cues: ConfigObjectSummary[]
  evidence: FPPEvidence
  readiness: { state: ReadinessState; check: () => void }
  model: ReturnType<typeof useModelContext>
  onSaved: (response: Playlist) => void
}) {
  const binding = playlist.payload.fpp

  const [entryCue, setEntryCue] = useState<Record<string, string>>({})
  const storedMismatch: MismatchPolicy | undefined = playlist.payload.mismatchPolicy
  const [safeCueRef, setSafeCueRef] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<Playlist>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    const map: Record<string, string> = {}
    for (const entry of playlist.payload.entries) {
      if (entry.fpp !== undefined) map[`${entry.fpp.section}:${entry.fpp.position}`] = entry.cue
    }
    setEntryCue(map)
    setSafeCueRef(playlist.payload.safeCueRef ?? '')
    setDirty(false)
    setSaveError(null)
    setStale(null)
  }, [playlist])

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  if (binding === undefined) {
    return <RuledStrip absence="unavailable" label="No binding" fact="This FPP-runner playlist has no stored FPP binding." />
  }

  const instanceLabel = fppInstanceLabel(model.fpp, binding.instanceUuid)
  const instanceRoute = fppInstanceRoute(model.fpp, binding.instanceUuid)
  const superseding = newerDefinition(evidence.definitions, binding.instanceUuid, binding.playlistName, binding.playlistHash)

  const discard = () => {
    const map: Record<string, string> = {}
    for (const entry of playlist.payload.entries) {
      if (entry.fpp !== undefined) map[`${entry.fpp.section}:${entry.fpp.position}`] = entry.cue
    }
    setEntryCue(map)
    setSafeCueRef(playlist.payload.safeCueRef ?? '')
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    const entries: ConfigShowPlaylistEntry[] = []
    for (const entry of evidence.entries) {
      const key = `${entry.section}:${entry.position}`
      const cue = entryCue[key]
      if (cue === undefined || cue === '') continue
      const existing = playlist.payload.entries.find((e) => e.fpp !== undefined && `${e.fpp.section}:${e.fpp.position}` === key)
      entries.push({
        id: existing?.id ?? randomUUIDv4(),
        cue,
        fpp: { ...existing?.fpp, section: entry.section, position: entry.position },
      })
    }
    if (entries.length === 0) {
      setSaveError('At least one imported entry must be bound to a cue; a playlist cannot save with no entries.')
      return
    }
    if (storedMismatch === 'safeCue' && safeCueRef === '') {
      setSaveError('Safe cue requires selecting which cue to fall back to.')
      return
    }
    const payload: ConfigShowPlaylist = {
      show: playlist.payload.show,
      name: playlist.payload.name,
      runner: 'fpp',
      fpp: binding,
      ...(storedMismatch === undefined ? {} : { mismatchPolicy: storedMismatch }),
      ...(storedMismatch === 'safeCue' ? { safeCueRef } : {}),
      entries,
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: playlist,
      read: () => getShowPlaylist(playlist.id),
      write: () => putShowPlaylist(playlist.id, payload),
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

  const bindEntry = (key: string, cueId: string) => {
    setEntryCue((prev) => ({ ...prev, [key]: cueId }))
    setDirty(true)
  }

  const canEditEntries = evidence.state === 'loaded'

  return (
    <Section id="pl-editor" title={`Editing ${playlist.payload.name}`} eyebrow="FPP runner">
      <div className="sm-panel">
        <div className="sm-section__head">
          <p className="sm-eyebrow sm-flat">
            Imported from FPP
          </p>
          {superseding !== null && <StatusPair tone="warn" label="Hash changed" />}
        </div>
        <div className="sm-grid sm-grid--auto sm-stack-3">
          <Field label="Instance">
            {(props) => (
              <Select
                {...props}
                value={binding.instanceUuid}
                disabled
                title="Rebinding this playlist to a different FPP instance or playlist needs a reconciliation flow the mock does not fully specify; see docs/ui-rebuild/OPEN-DECISIONS.md D-013."
              >
                <option value={binding.instanceUuid}>{instanceLabel}</option>
              </Select>
            )}
          </Field>
          <Field label="FPP playlist">
            {(props) => (
              <Select
                {...props}
                value={binding.playlistName}
                disabled
                title="Rebinding this playlist to a different FPP instance or playlist needs a reconciliation flow the mock does not fully specify; see docs/ui-rebuild/OPEN-DECISIONS.md D-013."
              >
                <option value={binding.playlistName}>{binding.playlistName}</option>
              </Select>
            )}
          </Field>
        </div>
        <DefinitionStrip
          items={[
            { term: 'Instance', value: instanceRoute !== null ? <Link to={instanceRoute}>{instanceLabel}</Link> : <span className="sm-data">{instanceLabel}</span> },
            { term: 'FPP playlist', value: <span className="sm-data">{binding.playlistName}</span> },
            {
              term: 'Playlist hash',
              value: (
                <span className="sm-data">
                  {binding.playlistHash.slice(0, 8)}&hellip;{binding.playlistHash.slice(-4)}
                </span>
              ),
            },
          ]}
        />
        <Button
          title="Reconciling a re-imported definition against existing cue bindings needs a flow the mock does not fully specify; see docs/ui-rebuild/OPEN-DECISIONS.md D-013."
          disabled
        >
          Re-import
        </Button>
        {superseding !== null && (
          <p className="sm-small sm-muted sm-stack-3">
            FPP&rsquo;s definition changed at {formatClock(superseding.capturedAt) ?? 'an unrecorded time'}, so the
            bound hash no longer matches the latest captured definition. Every binding below should be treated as
            held until this is reconciled.
          </p>
        )}
      </div>

      <div className="sm-attn sm-stack-5">
        <span className="sm-strip__label">Authority</span>
        <div>
          <p className="sm-strip__fact">FPP decides what plays next</p>
          <p className="sm-small sm-muted">
            This list mirrors FPP&rsquo;s own playlist; entries cannot be reordered here. Bind each imported entry to
            a cue below.
          </p>
        </div>
      </div>

      {evidence.state === 'loading' && <RuledStrip absence="loading" label="Reading" fact="Fetching this playlist's imported definition entries." />}
      {evidence.state === 'failed' && <RuledStrip absence="failed" label="Read failed" fact={evidence.reason ?? 'Could not read the imported definition.'} />}
      {evidence.state === 'loaded' && (
        <TableWrap label="Imported FPP entries, scrollable">
          <Table minWidth={520}>
            <thead>
              <tr>
                <th scope="col">FPP entry</th>
                <th scope="col">Bound cue</th>
                <th scope="col">Verdict</th>
              </tr>
            </thead>
            <tbody>
              {evidence.entries.length === 0 ? (
                <tr>
                  <td colSpan={3}>
                    <RuledStrip absence="empty" label="None" fact="The imported definition has no entries." />
                  </td>
                </tr>
              ) : (
                evidence.entries.map((entry, index) => {
                  const key = `${entry.section}:${entry.position}`
                  const boundCue = entryCue[key] ?? ''
                  return (
                    <tr key={`${key}:${index}`}>
                      <td>
                        <span className="sm-data sm-small sm-faint">
                          {entry.section} · {entry.position}
                        </span>
                        <br />
                        {entry.sequenceName !== '' ? entry.sequenceName : entry.mediaName !== '' ? entry.mediaName : '(no filename)'}
                      </td>
                      <td>
                        <Select
                          aria-label={`Bound cue for ${entry.section} · ${entry.position}`}
                          value={boundCue}
                          onChange={(e) => bindEntry(key, e.target.value)}
                        >
                          <option value="">No cue bound</option>
                          {cues.map((cue) => (
                            <option key={cue.id} value={cue.id}>
                              {cue.label}
                            </option>
                          ))}
                        </Select>
                      </td>
                      <td>
                        {boundCue !== '' ? <StatusPair tone="good" label="Bound" /> : <StatusPair tone="pending" label="Unbound" />}
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </Table>
        </TableWrap>
      )}
      <p className="sm-section__footnote">
        {evidence.entries.length} imported {evidence.entries.length === 1 ? 'entry' : 'entries'} ·{' '}
        {Object.values(entryCue).filter((v) => v !== '').length} bound.
      </p>

      <div className="sm-section sm-stack-5">
        <h3 className="sm-subsection__title">Playlist readiness</h3>
        <p className="sm-small sm-muted">
          An authoring-time verdict for this playlist, from <span className="sm-data">GET /integrations/fpp/playlists/{'{playlistId}'}/readiness</span>.
        </p>
        <Button onClick={readiness.check} disabled={readiness.state.state === 'loading'}>
          {readiness.state.state === 'loading' ? 'Checking…' : 'Check readiness'}
        </Button>
        {readiness.state.state === 'failed' && (
          <RuledStrip absence="failed" label="Read failed" fact={readiness.state.reason ?? 'Could not read readiness.'} />
        )}
        {readiness.state.state === 'loaded' && readiness.state.response !== null && (
          <p className="sm-verdict">
            <StatusPair
              tone={readiness.state.response.ready ? 'good' : 'bad'}
              label={readiness.state.response.ready ? 'Ready' : (readiness.state.response.failingCondition?.replace(/-/g, ' ') ?? 'Not ready')}
            />
            {readiness.state.response.reason !== undefined && readiness.state.response.reason !== '' && (
              <span className="sm-verdict__detail">{readiness.state.response.reason}</span>
            )}
            {readiness.state.response.warning !== undefined && readiness.state.response.warning !== '' && (
              <span className="sm-verdict__detail">{readiness.state.response.warning}</span>
            )}
          </p>
        )}
      </div>

      <div className="sm-attn sm-stack-5">
        <span className="sm-strip__label">On mismatch</span>
        <div>
          <Segmented label="On mismatch" value={storedMismatch ?? 'hold'} options={MISMATCH_OPTIONS} onChange={() => {}} disabled />
          <p className="sm-small sm-muted sm-stack-2">
            <strong>Not settable here yet:</strong> mismatch handling is expected to follow Show versus Program mode rather than
            being set per playlist. The stored value is shown and written back unchanged, so nothing set by showmeshctl is lost.
          </p>
          <p className="sm-small sm-muted sm-stack-2">
            {storedMismatch === undefined
              ? 'No policy is stored for this playlist, so the coordinator applies its own default.'
              : 'If FPP plays something this playlist cannot resolve, this is how ShowMesh answers.'}
            {safeCueRef !== '' && ` Safe cue: ${cueLabel(cues, safeCueRef)}.`}
          </p>
        </div>
      </div>

      <ButtonRow>
        <Button
          variant="primary"
          onClick={save}
          disabled={!dirty || saving || !saveGate.allowed || !canEditEntries}
          title={!saveGate.allowed ? saveGate.reason : !canEditEntries ? 'The imported definition has not finished reading.' : undefined}
        >
          {saving ? 'Saving…' : 'Save playlist'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving || !saveGate.allowed} title={!saveGate.allowed ? saveGate.reason : undefined}>
          Discard changes
        </Button>
        <div className="sm-push-end">
          <RevisionHistory id="pl-rev-fpp" fetch={() => getShowPlaylistRevisions(playlist.id)} reloadKey={`${playlist.id}:${playlist.revision}`} />
        </div>
      </ButtonRow>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            getShowPlaylist(playlist.id).then(onSaved).catch((err: unknown) => setSaveError(describeApiError(err)))
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
    </Section>
  )
}

type EntryDraft = { id: string; cue: string }

function AudioPlaylistEditor({
  playlist,
  cues,
  model,
  onSaved,
}: {
  playlist: Playlist
  cues: ConfigObjectSummary[]
  model: ReturnType<typeof useModelContext>
  onSaved: (response: Playlist) => void
}) {
  const [entries, setEntries] = useState<EntryDraft[]>([])
  const [repeat, setRepeat] = useState<'none' | 'all'>('none')
  const [addCueId, setAddCueId] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<Playlist>, { kind: 'stale' }> | null>(null)
  useEffect(() => {
    setEntries(playlist.payload.entries.map((e) => ({ id: e.id, cue: e.cue })))
    setRepeat(playlist.payload.showmeshAudio?.repeat ?? 'none')
    setAddCueId('')
    setDirty(false)
    setSaveError(null)
    setStale(null)
  }, [playlist])

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const discard = () => {
    setEntries(playlist.payload.entries.map((e) => ({ id: e.id, cue: e.cue })))
    setRepeat(playlist.payload.showmeshAudio?.repeat ?? 'none')
    setAddCueId('')
    setDirty(false)
    setSaveError(null)
  }

  const addEntry = () => {
    if (addCueId === '') return
    setEntries((prev) => [...prev, { id: randomUUIDv4(), cue: addCueId }])
    setAddCueId('')
    setDirty(true)
  }

  const removeEntry = (id: string) => {
    setEntries((prev) => prev.filter((e) => e.id !== id))
    setDirty(true)
  }

  const moveEntry = (index: number, direction: -1 | 1) => {
    setEntries((prev) => {
      const target = index + direction
      if (target < 0 || target >= prev.length) return prev
      const next = [...prev]
      const [item] = next.splice(index, 1)
      if (item === undefined) return prev
      next.splice(target, 0, item)
      return next
    })
    setDirty(true)
  }

  const save = () => {
    if (entries.length === 0) {
      setSaveError('At least one entry is required; a playlist cannot save empty.')
      return
    }
    const payload: ConfigShowPlaylist = {
      show: playlist.payload.show,
      name: playlist.payload.name,
      runner: 'showmesh-audio',
      showmeshAudio: { repeat },
      entries: entries.map((e) => ({ id: e.id, cue: e.cue })),
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: playlist,
      read: () => getShowPlaylist(playlist.id),
      write: () => putShowPlaylist(playlist.id, payload),
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

  return (
    <Section id="pl-editor" title={`Editing ${playlist.payload.name}`} eyebrow="ShowMesh audio">
      <div className="sm-attn">
        <span className="sm-strip__label">Authority</span>
        <div>
          <p className="sm-strip__fact">ShowMesh decides what plays next</p>
          <p className="sm-small sm-muted">
            This order is authored here. ShowMesh owns progression, repeat, and the audio playhead. No LTC is emitted
            unless a cue declares it.
          </p>
        </div>
      </div>

      <div className="sm-inline-row sm-stack-4">
        <Segmented
          label="Repeat"
          value={repeat}
          options={[
            { value: 'none', label: 'None' },
            { value: 'all', label: 'All' },
          ]}
          onChange={(value) => {
            setRepeat(value)
            setDirty(true)
          }}
        />
      </div>

      <div className="sm-inline-row sm-stack-4">
        <Select aria-label="Cue to add" value={addCueId} onChange={(e) => setAddCueId(e.target.value)}>
          <option value="">Select a cue</option>
          {cues.map((cue) => (
            <option key={cue.id} value={cue.id}>
              {cue.label}
            </option>
          ))}
        </Select>
        <Button onClick={addEntry} disabled={addCueId === ''}>
          Add cue
        </Button>
      </div>

      <TableWrap label="Playlist entries, scrollable">
        <Table minWidth={480}>
          <thead>
            <tr>
              <th scope="col">Ord</th>
              <th scope="col">Cue</th>
              <th scope="col">Length</th>
              <th scope="col">Reorder</th>
              <th scope="col">Remove</th>
            </tr>
          </thead>
          <tbody>
            {entries.length === 0 ? (
              <tr>
                <td colSpan={5}>
                  <RuledStrip absence="empty" label="None" fact="This playlist has no entries." />
                </td>
              </tr>
            ) : (
              entries.map((entry, index) => (
                <tr key={entry.id}>
                  <td className="sm-data">{index + 1}</td>
                  <td>{cueLabel(cues, entry.cue)}</td>
                  <td className="sm-data sm-faint">not reported</td>
                  <td>
                    <Button
                      variant="quiet"
                      size="compact"
                      onClick={() => moveEntry(index, -1)}
                      disabled={index === 0}
                      title="Move earlier in the playlist"
                    >
                      Move up
                    </Button>
                    <Button
                      variant="quiet"
                      size="compact"
                      onClick={() => moveEntry(index, 1)}
                      disabled={index === entries.length - 1}
                      title="Move later in the playlist"
                    >
                      Move down
                    </Button>
                  </td>
                  <td>
                    <Button variant="quiet" size="compact" onClick={() => removeEntry(entry.id)}>
                      Remove
                    </Button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </Table>
      </TableWrap>
      <p className="sm-section__footnote">
        {entries.length} {entries.length === 1 ? 'entry' : 'entries'}. Entry duration is not a field the coordinator
        reports; each cue's own length is not shown here.
      </p>

      <ButtonRow>
        <Button
          variant="primary"
          onClick={save}
          disabled={!dirty || saving || !saveGate.allowed}
          title={saveGate.allowed ? undefined : saveGate.reason}
        >
          {saving ? 'Saving…' : 'Save playlist'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving || !saveGate.allowed} title={saveGate.allowed ? undefined : saveGate.reason}>
          Discard changes
        </Button>
        <div className="sm-push-end">
          <RevisionHistory id="pl-rev-audio" fetch={() => getShowPlaylistRevisions(playlist.id)} reloadKey={`${playlist.id}:${playlist.revision}`} />
        </div>
      </ButtonRow>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            getShowPlaylist(playlist.id).then(onSaved).catch((err: unknown) => setSaveError(describeApiError(err)))
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
    </Section>
  )
}

/** The creation pattern's gate case: nothing below the runner renders until it is answered. */
type Runner = 'fpp' | 'showmesh-audio'

const RUNNER_OPTIONS: readonly { value: Runner; label: string }[] = [
  { value: 'fpp', label: 'FPP' },
  { value: 'showmesh-audio', label: 'ShowMesh audio' },
]

/** The newest definition per playlist name, for one instance: what "the FPP playlist" picker offers. */
function latestDefinitionsByName(definitions: readonly FPPPlaylistDefinitionMetadata[], instanceUuid: string): FPPPlaylistDefinitionMetadata[] {
  const byName = new Map<string, FPPPlaylistDefinitionMetadata>()
  for (const definition of definitions) {
    if (definition.instanceUuid !== instanceUuid) continue
    const existing = byName.get(definition.playlistName)
    if (existing === undefined || definition.capturedAt > existing.capturedAt) byName.set(definition.playlistName, definition)
  }
  return Array.from(byName.values()).sort((a, b) => a.playlistName.localeCompare(b.playlistName))
}

function PlaylistDraft({
  showId,
  cues,
  model,
  onCreated,
  onDiscard,
  onOpenExisting,
}: {
  showId: string
  cues: ConfigObjectSummary[]
  model: ReturnType<typeof useModelContext>
  onCreated: (response: Playlist) => void
  onDiscard: () => void
  onOpenExisting: (id: string) => void
}) {
  const [runner, setRunner] = useState<Runner | ''>('')
  const [name, setName] = useState('')
  const [id, setId] = useState('')
  const [idTouched, setIdTouched] = useState(false)
  const [repeat, setRepeat] = useState<'none' | 'all'>('none')
  const [instanceUuid, setInstanceUuid] = useState('')
  const [definitions, setDefinitions] = useState<{ state: 'idle' | 'loading' | 'loaded' | 'failed'; items: FPPPlaylistDefinitionMetadata[] }>({
    state: 'idle',
    items: [],
  })
  const [playlistHash, setPlaylistHash] = useState('')
  const [entries, setEntries] = useState<{ state: 'idle' | 'loading' | 'loaded' | 'failed'; items: FPPPlaylistDefinitionEntry[] }>({
    state: 'idle',
    items: [],
  })
  const [boundCue, setBoundCue] = useState('')
  const [audioCue, setAudioCue] = useState('')
  const [creating, setCreating] = useState(false)
  const [taken, setTaken] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  useEffect(() => {
    if (runner !== 'fpp') return
    let cancelled = false
    setDefinitions({ state: 'loading', items: [] })
    listFPPPlaylistDefinitions()
      .then((response) => {
        if (!cancelled) setDefinitions({ state: 'loaded', items: response.definitions })
      })
      .catch(() => {
        if (!cancelled) setDefinitions({ state: 'failed', items: [] })
      })
    return () => {
      cancelled = true
    }
  }, [runner])

  useEffect(() => {
    if (runner !== 'fpp' || instanceUuid === '' || playlistHash === '') {
      setEntries({ state: 'idle', items: [] })
      return
    }
    let cancelled = false
    setEntries({ state: 'loading', items: [] })
    getFPPPlaylistDefinitionEntries(instanceUuid, playlistHash)
      .then((response) => {
        if (!cancelled) setEntries({ state: 'loaded', items: response.entries })
      })
      .catch(() => {
        if (!cancelled) setEntries({ state: 'failed', items: [] })
      })
    return () => {
      cancelled = true
    }
  }, [runner, instanceUuid, playlistHash])

  const createGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const onNameChange = (value: string) => {
    setName(value)
    if (!idTouched) setId(slugify(value))
  }
  const onIdChange = (value: string) => {
    setId(value)
    setIdTouched(true)
  }

  const fppInstances = model.fpp.filter((instance): instance is typeof instance & { instanceUuid: string } => instance.instanceUuid !== null)
  const availableDefinitions = instanceUuid === '' ? [] : latestDefinitionsByName(definitions.items, instanceUuid)
  const chosenDefinition = availableDefinitions.find((d) => d.playlistHash === playlistHash) ?? null
  const firstEntry = entries.items[0] ?? null

  const canCreate =
    runner !== '' &&
    id !== '' &&
    (runner === 'showmesh-audio'
      ? audioCue !== ''
      : chosenDefinition !== null && firstEntry !== null && boundCue !== '')

  const create = () => {
    const activeRunner: Runner | '' = runner
    if (!canCreate || activeRunner === '') return
    const payload: ConfigShowPlaylist =
      activeRunner === 'fpp'
        ? {
            show: showId,
            name,
            runner: 'fpp',
            fpp: { instanceUuid: chosenDefinition!.instanceUuid, playlistName: chosenDefinition!.playlistName, playlistHash: chosenDefinition!.playlistHash },
            entries: [{ id: randomUUIDv4(), cue: boundCue, fpp: { section: firstEntry!.section, position: firstEntry!.position } }],
          }
        : {
            show: showId,
            name,
            runner: 'showmesh-audio',
            showmeshAudio: { repeat },
            entries: [{ id: randomUUIDv4(), cue: audioCue }],
          }
    setCreating(true)
    setTaken(false)
    setCreateError(null)
    guardedCreate({
      read: () => getShowPlaylist(id),
      write: () => putShowPlaylist(id, payload),
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
    <Section id="pl-editor" title="New playlist" eyebrow={runner === '' ? 'Draft · gate unanswered' : runner === 'fpp' ? 'Draft · FPP' : 'Draft · ShowMesh audio'}>
      <Segmented<Runner | ''>
        label="Runner"
        value={runner}
        options={RUNNER_OPTIONS}
        onChange={(value) => {
          setRunner(value as Runner)
          setInstanceUuid('')
          setPlaylistHash('')
          setBoundCue('')
          setAudioCue('')
        }}
      />
      <p className="sm-small sm-faint">
        Stored as <span className="sm-data">fpp</span> or <span className="sm-data">showmesh-audio</span>, and immutable once
        created, like the id: an FPP playlist and an audio playlist are different objects with different bindings.
      </p>

      {runner === '' && (
        <p className="sm-small sm-muted">
          The rest of the form appears once a runner is picked. It is not shown disabled: half of it would be fields this playlist can never have.
        </p>
      )}

      {runner !== '' && (
        <div className="sm-grid sm-form-column">
          <Field label="Name">{(props) => <Input {...props} value={name} onChange={(e) => onNameChange(e.target.value)} />}</Field>
          <Field label="Id" help="From the name, editable until created. Night session definitions bind playlists by id.">
            {(props) => <Input {...props} className="sm-data" value={id} onChange={(e) => onIdChange(e.target.value)} />}
          </Field>

          {runner === 'fpp' ? (
            <div>
              <Segmented label="If the FPP playlist does not match" value="hold" options={MISMATCH_OPTIONS} onChange={() => {}} disabled />
              <p className="sm-small sm-muted">
                fpp only. What the coordinator does when the captured definition&rsquo;s hash is not the one this playlist binds.{' '}
                <strong>Not settable here yet:</strong> mismatch handling is expected to follow Show versus Program mode rather than
                being set per playlist, so a draft is created with no stored policy and the coordinator applies its own default.
              </p>
            </div>
          ) : (
            <div>
              <Segmented
                label="Repeat"
                value={repeat}
                options={[
                  { value: 'none', label: 'None' },
                  { value: 'all', label: 'All' },
                ]}
                onChange={setRepeat}
              />
              <p className="sm-small sm-muted">An audio playlist carries its own order rather than binding an FPP capture.</p>
            </div>
          )}

          <div>
            <h3 className="sm-subsection__title">First entry</h3>
            <p className="sm-small sm-muted">
              The coordinator refuses a playlist with no entries, the same way it refuses a macro with no steps, so the first entry is part of creation.
            </p>
            {runner === 'fpp' ? (
              <div className="sm-grid sm-grid--auto sm-stack-3">
                {fppInstances.length === 0 ? (
                  <RuledStrip absence="empty" label="None" fact="No FPP instance is configured." />
                ) : (
                  <Field label="Instance">
                    {(props) => (
                      <Select
                        {...props}
                        value={instanceUuid}
                        onChange={(e) => {
                          setInstanceUuid(e.target.value)
                          setPlaylistHash('')
                          setBoundCue('')
                        }}
                      >
                        <option value="">Select an instance</option>
                        {fppInstances.map((instance) => (
                          <option key={instance.instanceUuid} value={instance.instanceUuid}>
                            {instance.instanceId}
                          </option>
                        ))}
                      </Select>
                    )}
                  </Field>
                )}
                {instanceUuid !== '' &&
                  (definitions.state === 'loading' ? (
                    <RuledStrip absence="loading" label="Reading" fact="Fetching this instance's imported FPP playlist definitions." />
                  ) : definitions.state === 'failed' ? (
                    <RuledStrip absence="failed" label="Read failed" fact="Could not read this instance's imported FPP playlist definitions." />
                  ) : availableDefinitions.length === 0 ? (
                    <RuledStrip absence="empty" label="None" fact="This instance has no imported FPP playlist definition." />
                  ) : (
                    <Field label="FPP playlist">
                      {(props) => (
                        <Select
                          {...props}
                          value={playlistHash}
                          onChange={(e) => {
                            setPlaylistHash(e.target.value)
                            setBoundCue('')
                          }}
                        >
                          <option value="">Select an FPP playlist</option>
                          {availableDefinitions.map((definition) => (
                            <option key={definition.playlistHash} value={definition.playlistHash}>
                              {definition.playlistName}
                            </option>
                          ))}
                        </Select>
                      )}
                    </Field>
                  ))}
                {playlistHash !== '' &&
                  (entries.state === 'loading' ? (
                    <RuledStrip absence="loading" label="Reading" fact="Fetching the chosen definition's entries." />
                  ) : entries.state === 'failed' ? (
                    <RuledStrip absence="failed" label="Read failed" fact="Could not read the chosen definition's entries." />
                  ) : firstEntry === null ? (
                    <RuledStrip absence="empty" label="None" fact="The chosen definition has no entries." />
                  ) : (
                    <Field label="Bound cue">
                      {(props) => (
                        <Select {...props} value={boundCue} onChange={(e) => setBoundCue(e.target.value)}>
                          <option value="">Select a cue</option>
                          {cues.map((cue) => (
                            <option key={cue.id} value={cue.id}>
                              {cue.label}
                            </option>
                          ))}
                        </Select>
                      )}
                    </Field>
                  ))}
              </div>
            ) : (
              <Field label="Cue">
                {(props) => (
                  <Select {...props} value={audioCue} onChange={(e) => setAudioCue(e.target.value)}>
                    <option value="">Select a cue</option>
                    {cues.map((cue) => (
                      <option key={cue.id} value={cue.id}>
                        {cue.label}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            )}
          </div>
        </div>
      )}

      {taken && (
        <RuledStrip
          absence="failed"
          label="Id taken"
          fact={
            <>
              <span className="sm-data">{id}</span> already names a playlist.{' '}
              <button type="button" className="sm-linkbutton" onClick={() => onOpenExisting(id)}>
                Open it
              </button>
            </>
          }
        />
      )}
      {createError !== null && <RuledStrip absence="failed" label="Save failed" fact={createError} />}

      <ButtonRow>
        <Button
          variant="primary"
          onClick={create}
          disabled={!canCreate || creating || !createGate.allowed}
          title={!createGate.allowed ? createGate.reason : undefined}
        >
          {creating ? 'Creating…' : 'Create playlist'}
        </Button>
        <Button variant="quiet" onClick={onDiscard} disabled={creating}>
          Discard
        </Button>
        <span className="sm-small sm-muted sm-push-end">
          {runner === '' ? (
            'Runner required'
          ) : (
            <>
              Creates {id === '' ? 'nothing until an id is entered' : <span className="sm-data">{id}</span>}
            </>
          )}
        </span>
      </ButtonRow>
    </Section>
  )
}
