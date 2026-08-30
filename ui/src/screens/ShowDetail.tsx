import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { getShow, putShow, type ShowConfigResponse } from '../api'
import { Button, ButtonRow, Field, Input, NotWired, PageTitle, RuledStrip, Section, StatTile, StatusPair, Textarea, Tiles } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { fetchShowContents } from './showsData'
import { activeShowId, contentsCounts, type ShowContentsCounts } from './showsModel'

type DetailState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: ShowConfigResponse }
  | { kind: 'failed'; reason: string; response: ShowConfigResponse | null }

function useShowDetail(id: string): { state: DetailState; reload: () => void; setResponse: (r: ShowConfigResponse) => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<DetailState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState((prev) => (prev.kind === 'loaded' ? prev : { kind: 'loading' }))
    getShow(id)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => ({ kind: 'failed', reason: describeApiError(err), response: prev.kind === 'loading' ? null : prev.response }))
      })
    return () => {
      cancelled = true
    }
  }, [id, attempt])

  return {
    state,
    reload: () => setAttempt((n) => n + 1),
    setResponse: (r) => setState({ kind: 'loaded', response: r }),
  }
}

function useContents(id: string): ShowContentsCounts | 'loading' | 'failed' {
  const [counts, setCounts] = useState<ShowContentsCounts | 'loading' | 'failed'>('loading')
  useEffect(() => {
    let cancelled = false
    setCounts('loading')
    fetchShowContents(id)
      .then((contents) => {
        if (!cancelled) setCounts(contentsCounts(contents))
      })
      .catch(() => {
        if (!cancelled) setCounts('failed')
      })
    return () => {
      cancelled = true
    }
  }, [id])
  return counts
}

export function ShowDetail() {
  const { id = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const navigate = useNavigate()
  const { state, reload, setResponse } = useShowDetail(id)
  const counts = useContents(id)
  const active = activeShowId(model) === id

  const [name, setName] = useState('')
  const [notes, setNotes] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    if (state.kind === 'loaded') {
      setName(state.response.payload.name)
      setNotes(state.response.payload.notes)
      setDirty(false)
    }
  }, [state])

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const discard = () => {
    if (state.kind !== 'loaded') return
    setName(state.response.payload.name)
    setNotes(state.response.payload.notes)
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    if (state.kind === 'loading' || (state.kind === 'failed' && state.response === null)) return
    const loaded = state.response as ShowConfigResponse
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded,
      read: () => getShow(id),
      write: () => putShow(id, { name, notes }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setResponse(outcome.response)
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

  if (state.kind === 'loading') {
    return (
      <>
        <PageTitle title="Show" />
        <RuledStrip absence="loading" label="Reading" fact={`Asking the coordinator for ${id}.`} />
      </>
    )
  }

  if (state.kind === 'failed' && state.response === null) {
    return (
      <>
        <PageTitle title="Show" />
        <RuledStrip
          absence="failed"
          label="Read failed"
          fact={state.reason}
          detail={
            <>
              No revision of <span className="sm-data">{id}</span> has ever been read on this device.{' '}
              <button type="button" className="sm-linkbutton" onClick={reload}>
                Try again
              </button>
            </>
          }
        />
      </>
    )
  }

  // Both remaining branches (loaded, or failed with a retained copy) carry a
  // response; only the "failed with nothing ever read" branch above does not.
  const response = state.response as ShowConfigResponse
  const { payload, revision, updatedAt, createdByPrincipalName: updatedBy } = response

  return (
    <>
      <p className="sm-small sm-muted">
        <Link to="/shows" className="sm-muted">
          Shows
        </Link>{' '}
        <span className="sm-faint">/</span> {payload.name}
      </p>

      <div className="sm-page__head sm-stack-2">
        <div>
          <div className="sm-inline-row">
            <h1 className="sm-page__title">{payload.name}</h1>
            {active && <StatusPair tone="good" label="Active" />}
          </div>
          <p className="sm-page__lede">Identity and notes only. What the show contains lives in its workspace tabs.</p>
        </div>
        <Button onClick={() => navigate(`/shows/${id}/playlists`)}>Open workspace</Button>
      </div>

      {state.kind === 'failed' && (
        <RuledStrip
          absence="stale"
          label="Stale"
          fact={state.reason}
          detail={`Showing the copy last read at revision ${revision}.`}
        />
      )}

      <Section id="sh-ident" title="Identity">
        <div className="sm-grid sm-form-column">
          <Field label="Name">
            {(props) => (
              <Input
                {...props}
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  setDirty(true)
                }}
              />
            )}
          </Field>
          <div className="sm-field">
            <span className="sm-field__label">Id</span>
            <p className="sm-input sm-data sm-muted">{id}</p>
            <span className="sm-field__help">
              Fixed at creation. Every cue, surface and asset in this show is keyed on it, so it cannot change.
            </span>
          </div>
          <Field label="Notes" help="For whoever operates this next, including you in eleven months.">
            {(props) => (
              <Textarea
                {...props}
                value={notes}
                onChange={(e) => {
                  setNotes(e.target.value)
                  setDirty(true)
                }}
                rows={4}
              />
            )}
          </Field>
        </div>
      </Section>

      <Section id="sh-contents" title="What this show contains">
        {counts === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Counting this show's playlists, cues, surfaces and assets." />
        ) : counts === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact="Could not count this show's contents just now." />
        ) : (
          <Tiles>
            <StatTile label="Playlists" value={counts.playlists} detail="show.playlist objects" to={`/shows/${id}/playlists`} />
            <StatTile label="Cues" value={counts.cues} detail="show.cue objects" to={`/shows/${id}/cues`} />
            <StatTile label="Surfaces" value={counts.surfaces} detail="show.surface objects" to={`/shows/${id}/presentation`} />
            <StatTile label="Assets" value={counts.assets} detail="uploaded content" to={`/shows/${id}/assets`} />
          </Tiles>
        )}
        <p className="sm-section__footnote">
          Each of these is its own revisioned object, so editing a cue creates a cue revision, not an opaque revision
          of the whole show.
        </p>
      </Section>

      <ButtonRow>
        <Button
          variant="primary"
          onClick={save}
          disabled={!dirty || saving || !saveGate.allowed}
          title={saveGate.allowed ? undefined : saveGate.reason}
        >
          {saving ? 'Saving…' : 'Save show'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
          Discard changes
        </Button>
        <span className="sm-small sm-muted sm-push-end">
          Active revision <span className="sm-data">{revision}</span> · {updatedBy ?? 'unknown principal'}{' '}
          {formatClock(updatedAt) ?? 'at an unrecorded time'}
        </span>
      </ButtonRow>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            reload()
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}

      <Section id="sh-danger" title="Delete this show">
        <div className="sm-panel">
          <p className="sm-small sm-muted">
            {active ? (
              <>
                This is the <strong>active</strong> show, the one this installation's authority is currently set to.
                That is not the same as a show running right now, which Show Night reports. Deleting it would orphan{' '}
                {counts === 'loading' || counts === 'failed'
                  ? 'this show’s configured objects'
                  : `${counts.cues} cues, ${counts.playlists} playlists and ${counts.surfaces} surfaces`}
                , and leave the installation with no authority for tonight.
              </>
            ) : (
              'Deleting a show removes its own configuration objects; its assets are content, not configuration, and are not removed either way.'
            )}
          </p>
          <ButtonRow>
            <NotWired label="No delete endpoint">
              <Button variant="danger">Delete show</Button>
            </NotWired>
          </ButtonRow>
          <p className="sm-small sm-faint">
            The coordinator has no endpoint to delete a show config object today. This control is drawn to its final
            shape and is inert.
          </p>
        </div>
      </Section>
    </>
  )
}
