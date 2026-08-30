import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  getFPPPlaylistDefinitionEntries,
  getFPPPlaylistReadiness,
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
import { Button, ButtonRow, Callout, Choice, DefinitionStrip, Field, NotWired, NotWiredBanner, RuledStrip, Section, Segmented, Select, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { fetchShowContents, fetchShowPlaylists } from './showsData'
import { cueLabel, fppInstanceLabel, fppInstanceRoute, newerDefinition, playlistRows } from './showsModel'

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

  const playlists = state.kind === 'loaded' ? state.playlists : []
  const selected = playlists.find((p) => p.id === selectedId) ?? playlists[0] ?? null
  const evidence = useFPPEvidence(selected !== null && selected.payload.runner === 'fpp' ? selected : null)
  const readiness = useReadiness(selected?.id ?? null)

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

  return (
    <>
      <Section
        id="pl-list"
        title="Playlists in this show"
        aside={
          <Button title="Playlist creation needs a runner-selection form the mock does not draw; see docs/ui-rebuild/OPEN-DECISIONS.md D-011." disabled>
            New playlist
          </Button>
        }
      >
        {rows.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="This show has no playlist configured." />
        ) : (
          <ul className="sm-plain-list">
            {rows.map((row) => (
              <li key={row.id}>
                <button
                  type="button"
                  className="sm-linkbutton"
                  aria-pressed={selected?.id === row.id}
                  onClick={() => setSelectedId(row.id)}
                >
                  {row.label}
                </button>{' '}
                <span className="sm-small sm-faint">{row.runnerLabel}</span>
                {selected?.id === row.id && <span className="sm-viewing">Editing</span>}
                <br />
                <span className="sm-small sm-muted">{row.detail}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      {playlists.length > 1 && (
        <Callout>
          Multiple playlists in the same show run concurrently. FPP advances the lighting sequence while ShowMesh
          independently advances the music bed; two runners are never authoritative for the same playlist.
        </Callout>
      )}

      {selected !== null && selected.payload.runner === 'fpp' && (
        <FPPPlaylistEditor playlist={selected} cues={state.kind === 'loaded' ? state.cues : []} evidence={evidence} readiness={readiness} model={model} onSaved={updatePlaylist} />
      )}

      {selected !== null && selected.payload.runner === 'showmesh-audio' && (
        <AudioPlaylistEditor playlist={selected} cues={state.kind === 'loaded' ? state.cues : []} model={model} onSaved={updatePlaylist} />
      )}
    </>
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

  useEffect(() => {
    const map: Record<string, string> = {}
    for (const entry of playlist.payload.entries) {
      if (entry.fpp !== undefined) map[`${entry.fpp.section}:${entry.fpp.position}`] = entry.cue
    }
    setEntryCue(map)
    setSafeCueRef(playlist.payload.safeCueRef ?? '')
    setDirty(false)
    setSaveError(null)
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
      entries.push({ id: existing?.id ?? randomUUIDv4(), cue, fpp: { section: entry.section, position: entry.position } })
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
    putShowPlaylist(playlist.id, payload)
      .then((response) => {
        onSaved(response)
        setDirty(false)
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
    <Section id="pl-fpp" title={`Editing ${playlist.payload.name}`} eyebrow="FPP runner">
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
          <Table>
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
          <NotWiredBanner
            what="Setting the mismatch policy here"
            missing="a per-playlist setting the design does not want"
            detail="The mock draws this control and says it is expected to follow Show vs Program mode rather than being set per playlist. The stored value is shown and is written back unchanged, so nothing set by showmeshctl is lost."
          />
          <NotWired>
            <Segmented label="On mismatch" value={storedMismatch ?? 'hold'} options={MISMATCH_OPTIONS} onChange={() => {}} />
          </NotWired>
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
        <span className="sm-small sm-muted sm-push-end">
          Active revision <span className="sm-data">{playlist.revision}</span> · {playlist.createdByPrincipalName ?? 'unknown principal'}{' '}
          {formatClock(playlist.updatedAt) ?? 'at an unrecorded time'}
        </span>
      </ButtonRow>
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
  useEffect(() => {
    setEntries(playlist.payload.entries.map((e) => ({ id: e.id, cue: e.cue })))
    setRepeat(playlist.payload.showmeshAudio?.repeat ?? 'none')
    setAddCueId('')
    setDirty(false)
    setSaveError(null)
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
    putShowPlaylist(playlist.id, payload)
      .then((response) => {
        onSaved(response)
        setDirty(false)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <Section id="pl-audio" title={`Editing ${playlist.payload.name}`} eyebrow="ShowMesh audio">
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
        <NotWired label="No stored field">
          <Choice type="checkbox" label="Resume where it left off" />
        </NotWired>
        <span className="sm-small sm-muted">
          Resume where it left off has no home in <span className="sm-data">show.playlist.showmeshAudio</span>; see
          docs/ui-rebuild/OPEN-DECISIONS.md D-012.
        </span>
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
        <Table>
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
        <span className="sm-small sm-muted sm-push-end">
          Active revision <span className="sm-data">{playlist.revision}</span> · {playlist.createdByPrincipalName ?? 'unknown principal'}{' '}
          {formatClock(playlist.updatedAt) ?? 'at an unrecorded time'}
        </span>
      </ButtonRow>
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
    </Section>
  )
}
