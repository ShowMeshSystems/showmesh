import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  getFPPPlaylistDefinitionEntries,
  getFPPPlaylistReadiness,
  listFPPPlaylistDefinitions,
  type ConfigObjectSummary,
  type ConfigShowPlaylist,
  type FPPPlaylistDefinitionEntry,
  type FPPPlaylistDefinitionMetadata,
  type FPPPlaylistReadinessResponse,
} from '../api'
import { Button, Callout, DefinitionStrip, RuledStrip, Section, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import { fetchShowContents, fetchShowPlaylists } from './showsData'
import { cueLabel, fppInstanceLabel, fppInstanceRoute, newerDefinition, playlistRows } from './showsModel'

type Playlist = { id: string; payload: ConfigShowPlaylist }

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; cues: ConfigObjectSummary[]; playlists: Playlist[] }
  | { kind: 'failed'; reason: string }

function usePlaylists(showId: string): { state: ListState; reload: () => void } {
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

  return { state, reload: () => setAttempt((n) => n + 1) }
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
  const { state, reload } = usePlaylists(showId)
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
        <FPPPlaylistEditor playlist={selected} cues={state.kind === 'loaded' ? state.cues : []} evidence={evidence} readiness={readiness} model={model} />
      )}

      {selected !== null && selected.payload.runner === 'showmesh-audio' && (
        <AudioPlaylistEditor playlist={selected} cues={state.kind === 'loaded' ? state.cues : []} />
      )}
    </>
  )
}

function FPPPlaylistEditor({
  playlist,
  cues,
  evidence,
  readiness,
  model,
}: {
  playlist: Playlist
  cues: ConfigObjectSummary[]
  evidence: FPPEvidence
  readiness: { state: ReadinessState; check: () => void }
  model: ReturnType<typeof useModelContext>
}) {
  const binding = playlist.payload.fpp
  if (binding === undefined) {
    return <RuledStrip absence="unavailable" label="No binding" fact="This FPP-runner playlist has no stored FPP binding." />
  }

  const instanceLabel = fppInstanceLabel(model.fpp, binding.instanceUuid)
  const instanceRoute = fppInstanceRoute(model.fpp, binding.instanceUuid)
  const superseding = newerDefinition(evidence.definitions, binding.instanceUuid, binding.playlistName, binding.playlistHash)

  const bound = new Set(playlist.payload.entries.flatMap((e) => (e.fpp !== undefined ? [`${e.fpp.section}:${e.fpp.position}`] : [])))
  const entryForKey = (key: string) => playlist.payload.entries.find((e) => e.fpp !== undefined && `${e.fpp.section}:${e.fpp.position}` === key)

  return (
    <Section id="pl-fpp" title={`Editing ${playlist.payload.name}`} eyebrow="FPP runner">
      <div className="sm-panel">
        <div className="sm-section__head">
          <p className="sm-eyebrow sm-flat">
            Imported from FPP
          </p>
          {superseding !== null && <StatusPair tone="warn" label="Hash changed" />}
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
            This list mirrors FPP&rsquo;s own playlist; entries cannot be reordered here. Each entry is bound to a
            cue.
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
                  const boundEntry = entryForKey(key)
                  return (
                    <tr key={`${key}:${index}`}>
                      <td>
                        <span className="sm-data sm-small sm-faint">
                          {entry.section} · {entry.position}
                        </span>
                        <br />
                        {entry.sequenceName !== '' ? entry.sequenceName : entry.mediaName !== '' ? entry.mediaName : '(no filename)'}
                      </td>
                      <td>{boundEntry !== undefined ? cueLabel(cues, boundEntry.cue) : <span className="sm-faint">No cue bound</span>}</td>
                      <td>
                        {bound.has(key) ? <StatusPair tone="good" label="Bound" /> : <StatusPair tone="pending" label="Unbound" />}
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
        {evidence.entries.length} imported {evidence.entries.length === 1 ? 'entry' : 'entries'} · {bound.size} bound.
        Reordering, adding, and removing entries is not built in this change.
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
          <StatusPair
            tone="pending"
            label={playlist.payload.mismatchPolicy === 'blackAndSilence' ? 'Black & silence' : playlist.payload.mismatchPolicy === 'safeCue' ? 'Safe cue' : 'Hold'}
          />
          <p className="sm-small sm-muted sm-stack-2">
            If FPP plays something this playlist cannot resolve, this is how ShowMesh answers. Editing this policy is
            not built in this change.
          </p>
        </div>
      </div>
    </Section>
  )
}

function AudioPlaylistEditor({ playlist, cues }: { playlist: Playlist; cues: ConfigObjectSummary[] }) {
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

      <p className="sm-small sm-muted sm-stack-4">
        Repeat: <StatusPair tone="pending" label={playlist.payload.showmeshAudio?.repeat === 'all' ? 'All' : 'None'} />
      </p>

      <TableWrap label="Playlist entries, scrollable">
        <Table>
          <thead>
            <tr>
              <th scope="col">Ord</th>
              <th scope="col">Cue</th>
              <th scope="col">Length</th>
            </tr>
          </thead>
          <tbody>
            {playlist.payload.entries.length === 0 ? (
              <tr>
                <td colSpan={3}>
                  <RuledStrip absence="empty" label="None" fact="This playlist has no entries." />
                </td>
              </tr>
            ) : (
              playlist.payload.entries.map((entry, index) => (
                <tr key={entry.id}>
                  <td className="sm-data">{index + 1}</td>
                  <td>{cueLabel(cues, entry.cue)}</td>
                  <td className="sm-data sm-faint">not reported</td>
                </tr>
              ))
            )}
          </tbody>
        </Table>
      </TableWrap>
      <p className="sm-section__footnote">
        {playlist.payload.entries.length} {playlist.payload.entries.length === 1 ? 'entry' : 'entries'}. Entry
        duration is not a field the coordinator reports; each cue's own length is not shown here. Reordering,
        adding, and removing entries is not built in this change.
      </p>
    </Section>
  )
}
