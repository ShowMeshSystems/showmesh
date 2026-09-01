import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  getShowCue,
  getShowCueRevisions,
  listConfigObjects,
  putShowCue,
  type Asset,
  type ConfigObjectSummary,
  type ConfigShowCue,
  type ConfigShowCueOutputs,
  type ShowCueConfigResponse,
  type ShowPlaylistConfigResponse,
} from '../api'
import { Button, Callout, Field, Input, Panes, RevisionHistory, RuledStrip, Section, Segmented, Select, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { fetchShowContents, fetchShowCues, fetchShowPlaylists } from './showsData'
import { CUE_OUTPUT_CHIP, type CueActivationDraft, type CueOutputKind, type CueRow, cueActivationSummary, cueRows, formatBytes, slugify } from './showsModel'

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; cues: ShowCueConfigResponse[]; playlists: ShowPlaylistConfigResponse[]; assets: Asset[] }
  | { kind: 'failed'; reason: string }

function useCues(showId: string): { state: ListState; reload: () => void; upsertCue: (response: ShowCueConfigResponse) => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    fetchShowContents(showId)
      .then(async (contents) => {
        const [cues, playlists] = await Promise.all([fetchShowCues(contents.cues), fetchShowPlaylists(contents.playlists)])
        if (!cancelled) setState({ kind: 'loaded', cues, playlists, assets: contents.assets })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, attempt])

  const upsertCue = (response: ShowCueConfigResponse) => {
    setState((prev) => {
      if (prev.kind !== 'loaded') return prev
      const exists = prev.cues.some((c) => c.id === response.id)
      return { ...prev, cues: exists ? prev.cues.map((c) => (c.id === response.id ? response : c)) : [...prev.cues, response] }
    })
  }

  return { state, reload: () => setAttempt((n) => n + 1), upsertCue }
}

const KIND_FILTERS: readonly { value: 'all' | CueOutputKind | 'unused'; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'render', label: 'Render' },
  { value: 'audio', label: 'Audio' },
  { value: 'unused', label: 'Unused' },
]

export function ShowsCues() {
  const { id: showId = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const { state, reload, upsertCue } = useCues(showId)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [filterText, setFilterText] = useState('')
  const [filterKind, setFilterKind] = useState<'all' | CueOutputKind | 'unused'>('all')

  if (state.kind === 'loading') {
    return (
      <Section id="cue-list" title="Cues in this show">
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's cues." />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="cue-list" title="Cues in this show">
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

  const rows = cueRows(state.cues, state.playlists, state.assets)
  const matches = (row: CueRow) => {
    if (filterText !== '' && !row.label.toLowerCase().includes(filterText.toLowerCase()) && !row.id.toLowerCase().includes(filterText.toLowerCase())) {
      return false
    }
    if (filterKind === 'all') return true
    if (filterKind === 'unused') return row.group === 'unreachable'
    return row.kinds.includes(filterKind)
  }

  const inPlaylist = rows.filter((r) => r.group === 'playlist' && matches(r))
  const unreachable = rows.filter((r) => r.group === 'unreachable' && matches(r))
  const announcements = rows.filter((r) => r.group === 'announcement' && matches(r))

  const selected = selectedId === null ? null : state.cues.find((c) => c.id === selectedId) ?? null
  const audioAssets = state.assets.filter((a) => a.mediaType === 'audio' && a.current)
  const existingIds = state.cues.map((c) => c.id)

  return (
    <Panes>
      <div>
        <Section
          id="cue-list"
          title="Cues in this show"
          aside={
            <Button
              variant="primary"
              onClick={() => {
                setCreating(true)
                setSelectedId(null)
              }}
            >
              New cue
            </Button>
          }
        >
          <p className="sm-small sm-muted">Select a cue to edit it in the panel on the right. Editing a cue changes every playlist that uses it.</p>
          <div className="sm-inline-row sm-stack-3">
            <Input aria-label="Filter cues" placeholder="Filter cues…" value={filterText} onChange={(e) => setFilterText(e.target.value)} />
            <Segmented label="Filter by output" value={filterKind} options={KIND_FILTERS} onChange={setFilterKind} />
          </div>

          <CueTable
            title="In a playlist"
            rows={inPlaylist}
            selectedId={selectedId}
            usedByColumn
            onSelect={(id) => {
              setSelectedId(id)
              setCreating(false)
            }}
          />

          <div className="sm-subsection">
            <h3 className="sm-subsection__title">
              Not in any playlist <span className="sm-small sm-muted">· {unreachable.length}</span>
            </h3>
            <p className="sm-small sm-muted">Authored but unreachable. Bind it to a playlist entry, or leave it as a safe-cue target.</p>
            <CueTable
              title="Not in any playlist"
              hideTitle
              rows={unreachable}
              selectedId={selectedId}
              stateColumn
              onSelect={(id) => {
                setSelectedId(id)
                setCreating(false)
              }}
            />
          </div>

          <div className="sm-subsection">
            <h3 className="sm-subsection__title">
              Directly activatable <span className="sm-small sm-muted">· {announcements.length}</span>
            </h3>
            <p className="sm-small sm-muted">
              Announcements are not playlist entries. An operator fires them from Live Control, and they duck the background bed without touching FPP.
            </p>
            <CueTable
              title="Directly activatable"
              hideTitle
              rows={announcements}
              selectedId={selectedId}
              policyColumn
              onSelect={(id) => {
                setSelectedId(id)
                setCreating(false)
              }}
            />
          </div>
        </Section>
      </div>

      <aside>
        {(creating || selected !== null) && (
          <CueEditor
            key={selected?.id ?? 'new'}
            showId={showId}
            cue={selected}
            existingIds={existingIds}
            audioAssets={audioAssets}
            model={model}
            onSaved={(response) => {
              upsertCue(response)
              setSelectedId(response.id)
              setCreating(false)
            }}
            onCancel={() => {
              setCreating(false)
              setSelectedId(null)
            }}
          />
        )}
      </aside>
    </Panes>
  )
}

function CueTable({
  rows,
  selectedId,
  onSelect,
  usedByColumn = false,
  stateColumn = false,
  policyColumn = false,
  hideTitle = false,
  title,
}: {
  rows: CueRow[]
  selectedId: string | null
  onSelect: (id: string) => void
  usedByColumn?: boolean
  stateColumn?: boolean
  policyColumn?: boolean
  hideTitle?: boolean
  title: string
}) {
  return (
    <>
      {!hideTitle && (
        <h3 className="sm-subsection__title sm-stack-4">
          {title} <span className="sm-small sm-muted">· {rows.length}</span>
        </h3>
      )}
      <TableWrap label={`${title}, scrollable`}>
        <Table>
          <thead>
            <tr>
              <th scope="col">Cue</th>
              <th scope="col">Outputs</th>
              {usedByColumn && <th scope="col">Used by</th>}
              {stateColumn && <th scope="col">State</th>}
              {policyColumn && <th scope="col">Policy</th>}
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={3}>
                  <RuledStrip absence="empty" label="None" fact="No cue matches here." />
                </td>
              </tr>
            ) : (
              rows.map((row) => (
                <tr key={row.id} aria-current={selectedId === row.id ? 'true' : undefined} className={selectedId === row.id ? 'sm-table__row--current' : undefined}>
                  <td>
                    <button type="button" className="sm-linkbutton" onClick={() => onSelect(row.id)} aria-pressed={selectedId === row.id}>
                      {row.label}
                    </button>
                    {selectedId === row.id && <span className="sm-viewing">Editing</span>}
                    <br />
                    {row.assetMissing ? (
                      <StatusPair tone="bad" appearance="word" label="Asset not uploaded" />
                    ) : (
                      <span className="sm-data sm-small sm-faint">{row.id}</span>
                    )}
                  </td>
                  <td>
                    <span className="sm-chip-row">
                      {row.kinds.length === 0 ? (
                        <span className="sm-small sm-faint">none</span>
                      ) : (
                        row.kinds.map((kind) => (
                          <span key={kind} className="sm-chip">
                            {CUE_OUTPUT_CHIP[kind]}
                          </span>
                        ))
                      )}
                    </span>
                  </td>
                  {usedByColumn && <td className="sm-small sm-muted">{row.usedByPlaylists.join(', ')}</td>}
                  {stateColumn && (
                    <td>
                      <span className="sm-small sm-faint">Unreachable</span>
                    </td>
                  )}
                  {policyColumn && <td className="sm-small sm-muted">{row.announcementPolicy}</td>}
                </tr>
              ))
            )}
          </tbody>
        </Table>
      </TableWrap>
    </>
  )
}

type AudioNodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: ConfigObjectSummary[] } | { kind: 'failed'; reason: string }

function useAudioNodes(): AudioNodesState {
  const [state, setState] = useState<AudioNodesState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('audio.node')
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', nodes: response.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
}

function TargetNodeField({
  label,
  value,
  onChange,
  nodesState,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  nodesState: AudioNodesState
}) {
  if (nodesState.kind === 'loading') return <RuledStrip absence="loading" label="Reading" fact="Fetching this deployment's declared audio nodes." />
  if (nodesState.kind === 'failed') return <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />
  if (nodesState.nodes.length === 0) {
    return (
      <>
        <RuledStrip absence="empty" label="None" fact="No audio node is declared." />
        {value !== '' && (
          <p className="sm-small sm-faint">
            Stored target: <span className="sm-data">{value}</span>
          </p>
        )}
      </>
    )
  }
  const notDeclared = value !== '' && !nodesState.nodes.some((node) => node.id === value)
  return (
    <Field
      label={label}
      help={notDeclared ? `${value} is not declared; readiness will report it as unbound.` : "Empty resolves to the installation's single program+ltc node."}
    >
      {(props) => (
        <Select {...props} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Resolve to the program+ltc node</option>
          {nodesState.nodes.map((node) => (
            <option key={node.id} value={node.id}>
              {node.label}
            </option>
          ))}
          {notDeclared && <option value={value}>{value} (not declared)</option>}
        </Select>
      )}
    </Field>
  )
}

const OUTPUT_OPTIONS: readonly { kind: CueOutputKind; title: string; description: string }[] = [
  { kind: 'render', title: 'Render', description: 'Drive lighting and video from a sequence' },
  { kind: 'audio', title: 'Audience audio', description: 'Play an audio asset on the program bus' },
  { kind: 'ltc', title: 'LTC', description: 'Emit timecode for Resolume' },
  { kind: 'announcement', title: 'Announcement', description: 'Speak over the background bed' },
]

type Kinds = Set<CueOutputKind>

function initialKinds(cue: ShowCueConfigResponse | null): Kinds {
  const set = new Set<CueOutputKind>()
  if (cue === null) return set
  if (cue.payload.outputs.render !== undefined) set.add('render')
  if (cue.payload.outputs.audio !== undefined) set.add('audio')
  if (cue.payload.outputs.ltc !== undefined) set.add('ltc')
  if (cue.payload.outputs.announcement !== undefined) set.add('announcement')
  return set
}

function CueEditor({
  showId,
  cue,
  existingIds,
  audioAssets,
  model,
  onSaved,
  onCancel,
}: {
  showId: string
  cue: ShowCueConfigResponse | null
  existingIds: readonly string[]
  audioAssets: readonly Asset[]
  model: ReturnType<typeof useModelContext>
  onSaved: (response: ShowCueConfigResponse) => void
  onCancel: () => void
}) {
  const isNew = cue === null
  const [name, setName] = useState(cue?.payload.name ?? '')
  const [id, setId] = useState(cue?.id ?? '')
  const [idTouched, setIdTouched] = useState(!isNew)
  const [kinds, setKinds] = useState<Kinds>(initialKinds(cue))
  const [renderSequence, setRenderSequence] = useState(cue?.payload.outputs.render?.sequence ?? '')
  const [audioAsset, setAudioAsset] = useState(cue?.payload.outputs.audio?.asset ?? '')
  const [audioStartOffsetMillis, setAudioStartOffsetMillis] = useState(String(cue?.payload.outputs.audio?.startOffsetMillis ?? 0))
  const [ltcStartOffsetMillis, setLtcStartOffsetMillis] = useState(String(cue?.payload.outputs.ltc?.startOffsetMillis ?? 0))
  const [announcementPolicy, setAnnouncementPolicy] = useState<'duck' | 'mix' | 'interrupt'>(cue?.payload.outputs.announcement?.policy ?? 'duck')
  const [duckGainDb, setDuckGainDb] = useState(String(cue?.payload.outputs.announcement?.duckGainDb ?? -18))
  const [fadeMillis, setFadeMillis] = useState(String(cue?.payload.outputs.announcement?.fadeMillis ?? 400))
  const [audioTarget, setAudioTarget] = useState(cue?.payload.outputs.audio?.target ?? '')
  const [ltcTarget, setLtcTarget] = useState(cue?.payload.outputs.ltc?.target ?? '')
  const [announcementTarget, setAnnouncementTarget] = useState(cue?.payload.outputs.announcement?.target ?? '')
  const audioNodes = useAudioNodes()
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowCueConfigResponse>, { kind: 'stale' }> | null>(null)

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const toggleKind = (kind: CueOutputKind) => {
    setKinds((prev) => {
      const next = new Set(prev)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }

  let blockReason: string | null = null
  if (kinds.size === 0) blockReason = 'Pick at least one output.'
  else if (kinds.has('ltc') && !kinds.has('audio')) blockReason = 'LTC requires Audio to also be selected.'
  else if (kinds.has('announcement') && !kinds.has('audio')) blockReason = 'Announcement requires Audio to also be selected.'
  else if (kinds.has('render') && renderSequence.trim() === '') blockReason = 'Render needs a sequence name.'
  else if (kinds.has('audio') && audioAsset === '') blockReason = 'Audio needs an asset selected.'
  else if (name.trim() === '') blockReason = 'A cue needs a name.'
  else if (id.trim() === '') blockReason = 'A cue needs an id.'
  else if (isNew && existingIds.includes(id)) blockReason = `The id "${id}" already names another cue in this show; edit that cue instead or choose a different id.`
  else if (kinds.has('announcement') && announcementPolicy === 'duck') {
    const gain = Number(duckGainDb)
    if (!Number.isFinite(gain) || gain >= 0 || gain < -60) blockReason = 'Duck level must be a negative number, at least -60 dB.'
  }
  if (blockReason === null && kinds.has('announcement')) {
    const fade = Number(fadeMillis)
    if (!Number.isInteger(fade) || fade < 0 || fade > 60000) blockReason = 'Fade must be a whole number of milliseconds, 0 to 60000.'
  }
  if (blockReason === null && kinds.has('audio')) {
    const offset = Number(audioStartOffsetMillis)
    if (!Number.isInteger(offset) || offset < 0) blockReason = 'Audio start offset must be a whole number of milliseconds, 0 or more.'
  }
  if (blockReason === null && kinds.has('ltc')) {
    const offset = Number(ltcStartOffsetMillis)
    if (!Number.isInteger(offset) || offset < 0 || offset > 86400000) blockReason = 'LTC start offset must be a whole number of milliseconds, 0 to 86400000.'
  }

  const activationDraft: CueActivationDraft = {
    render: kinds.has('render') ? { sequence: renderSequence } : null,
    audio: kinds.has('audio') ? { asset: audioAsset, startOffsetMillis: Number(audioStartOffsetMillis) || 0, target: audioTarget } : null,
    ltc: kinds.has('ltc') ? { startOffsetMillis: Number(ltcStartOffsetMillis) || 0, target: ltcTarget } : null,
    announcement: kinds.has('announcement')
      ? { policy: announcementPolicy, duckGainDb: Number(duckGainDb) || 0, fadeMillis: Number(fadeMillis) || 0, target: announcementTarget }
      : null,
  }

  const save = () => {
    if (blockReason !== null) return
    const outputs: ConfigShowCueOutputs = {
      ...(kinds.has('render') ? { render: { sequence: renderSequence.trim() } } : {}),
      ...(kinds.has('audio')
        ? {
            audio: {
              asset: audioAsset,
              startOffsetMillis: Number(audioStartOffsetMillis),
              ...(audioTarget !== '' ? { target: audioTarget } : {}),
            },
          }
        : {}),
      ...(kinds.has('ltc')
        ? {
            ltc: {
              startOffsetMillis: Number(ltcStartOffsetMillis),
              ...(ltcTarget !== '' ? { target: ltcTarget } : {}),
            },
          }
        : {}),
      ...(kinds.has('announcement')
        ? {
            announcement: {
              policy: announcementPolicy,
              fadeMillis: Number(fadeMillis),
              ...(announcementPolicy === 'duck' ? { duckGainDb: Number(duckGainDb) } : {}),
              ...(announcementTarget !== '' ? { target: announcementTarget } : {}),
            },
          }
        : {}),
    }
    const payload: ConfigShowCue = { show: showId, name: name.trim(), outputs }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    if (cue === null) {
      guardedCreate({ read: () => getShowCue(id.trim()), write: () => putShowCue(id.trim(), payload) })
        .then((outcome) => {
          if (outcome.kind === 'created') {
            onSaved(outcome.response)
            return
          }
          setSaveError(
            outcome.kind === 'taken'
              ? `${id.trim()} already names a cue in this show. Creating it here would write over that one.`
              : outcome.reason,
          )
        })
        .catch((err: unknown) => setSaveError(describeApiError(err)))
        .finally(() => setSaving(false))
      return
    }
    guardedSave({
      loaded: cue,
      read: () => getShowCue(cue.id),
      write: () => putShowCue(cue.id, payload),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onSaved(outcome.response)
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
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">{isNew ? 'New cue' : `Editing ${cue.payload.name}`}</p>
      <p className="sm-small sm-muted">In this show. Cues can only reference this show's own assets.</p>

      <div className="sm-inspector__group">
        <Field label="Name">
          {(props) => (
            <Input
              {...props}
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                if (!idTouched) setId(slugify(e.target.value))
              }}
            />
          )}
        </Field>
        {isNew && (
          <Field label="Id" help="From the name, editable.">
            {(props) => (
              <Input
                {...props}
                className="sm-data"
                value={id}
                onChange={(e) => {
                  setId(e.target.value)
                  setIdTouched(true)
                }}
              />
            )}
          </Field>
        )}
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">What does this cue do?</h3>
        <p className="sm-small sm-muted">Pick at least one.</p>
        <div className="sm-choice-list">
          {OUTPUT_OPTIONS.map((option) => (
            <label key={option.kind} className="sm-choice sm-choice--card">
              <input type="checkbox" checked={kinds.has(option.kind)} onChange={() => toggleKind(option.kind)} />
              <span className="sm-choice--card__body">
                <span className="sm-choice--card__title">{option.title}</span>
                <span className="sm-choice--card__desc">{option.description}</span>
              </span>
            </label>
          ))}
        </div>
      </div>

      {kinds.has('render') && (
        <div className="sm-inspector__group">
          <Field label="Sequence" help="The logical sequence name, not an FSEQ filename or asset id.">
            {(props) => <Input {...props} value={renderSequence} onChange={(e) => setRenderSequence(e.target.value)} />}
          </Field>
        </div>
      )}

      {kinds.has('audio') && (
        <div className="sm-inspector__group">
          <Field label="Audio asset">
            {(props) => (
              <Select {...props} value={audioAsset} onChange={(e) => setAudioAsset(e.target.value)}>
                <option value="">Choose an asset…</option>
                {audioAssets.map((asset) => (
                  <option key={asset.id} value={asset.sequence}>
                    {asset.sequence} · <span title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</span>
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <p className="sm-small sm-faint">
            This show's current audio assets, by logical sequence. A cue stores the sequence, never an asset id:
            the coordinator resolves it that way in <span className="sm-data">assetsync/cuecatalog.go</span>.
          </p>
          {audioAsset !== '' && !audioAssets.some((asset) => asset.sequence === audioAsset) && (
            <RuledStrip
              absence="failed"
              label="Asset not uploaded"
              fact={`${audioAsset} is not among this show's current assets.`}
              detail="Upload it in Assets, or choose a different one."
            />
          )}
          <Field label="Start offset (ms)" help="Milliseconds into the asset where playback begins.">
            {(props) => (
              <Input {...props} type="number" min={0} step={1} value={audioStartOffsetMillis} onChange={(e) => setAudioStartOffsetMillis(e.target.value)} />
            )}
          </Field>
          <TargetNodeField label="Audio target node" value={audioTarget} onChange={setAudioTarget} nodesState={audioNodes} />
        </div>
      )}

      {kinds.has('ltc') && (
        <div className="sm-inspector__group">
          <Field label="Start offset (ms)" help="Milliseconds of timecode to skip before it starts emitting.">
            {(props) => (
              <Input {...props} type="number" min={0} max={86400000} step={1} value={ltcStartOffsetMillis} onChange={(e) => setLtcStartOffsetMillis(e.target.value)} />
            )}
          </Field>
          <TargetNodeField label="LTC target node" value={ltcTarget} onChange={setLtcTarget} nodesState={audioNodes} />
        </div>
      )}

      {kinds.has('announcement') && (
        <div className="sm-inspector__group">
          <p className="sm-eyebrow">While it plays</p>
          <Segmented
            label="While it plays"
            value={announcementPolicy}
            onChange={setAnnouncementPolicy}
            options={[
              { value: 'duck', label: 'Duck' },
              { value: 'mix', label: 'Mix' },
              { value: 'interrupt', label: 'Interrupt' },
            ]}
          />
          <div className="sm-grid sm-grid--auto sm-stack-3">
            <Field label="Duck to (dB)" help="Applies only to the Duck policy.">
              {(props) => <Input {...props} value={duckGainDb} onChange={(e) => setDuckGainDb(e.target.value)} disabled={announcementPolicy !== 'duck'} />}
            </Field>
            <Field label="Fade (ms)">{(props) => <Input {...props} value={fadeMillis} onChange={(e) => setFadeMillis(e.target.value)} />}</Field>
          </div>
          <TargetNodeField label="Announcement target node" value={announcementTarget} onChange={setAnnouncementTarget} nodesState={audioNodes} />
        </div>
      )}

      <Callout>{cueActivationSummary(activationDraft)}</Callout>

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">{isNew ? 'Creates revision 1' : `Active revision ${cue?.revision}`}</span>
        <div className="sm-btn-row">
          <Button variant="quiet" onClick={onCancel} disabled={saving}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={save}
            disabled={saving || !saveGate.allowed || blockReason !== null}
            title={!saveGate.allowed ? saveGate.reason : (blockReason ?? undefined)}
          >
            {saving ? 'Saving…' : isNew ? 'Create cue' : 'Save cue'}
          </Button>
        </div>
      </div>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            if (cue !== null) getShowCue(cue.id).then(onSaved).catch((err: unknown) => setSaveError(describeApiError(err)))
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      {cue !== null && <RevisionHistory fetch={() => getShowCueRevisions(cue.id)} reloadKey={`${cue.id}:${cue.revision}`} />}
    </div>
  )
}
