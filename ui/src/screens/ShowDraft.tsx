import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { getShow, putShow } from '../api'
import { Button, ButtonRow, Field, Input, RuledStrip, Section, Textarea } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate } from '../domain/save'
import { slugify } from './showsModel'

export function ShowDraft() {
  const model = useModelContext()
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [id, setId] = useState('')
  const [idTouched, setIdTouched] = useState(false)
  const [notes, setNotes] = useState('')
  const [creating, setCreating] = useState(false)
  const [taken, setTaken] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  const createGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const onNameChange = (value: string) => {
    setName(value)
    if (!idTouched) setId(slugify(value))
  }

  const onIdChange = (value: string) => {
    setId(value)
    setIdTouched(true)
  }

  const create = () => {
    if (id === '') return
    setCreating(true)
    setTaken(false)
    setCreateError(null)
    guardedCreate({
      read: () => getShow(id),
      write: () => putShow(id, { name, notes }),
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
        navigate(`/shows/${id}`)
      })
      .catch((err: unknown) => setCreateError(describeApiError(err)))
      .finally(() => setCreating(false))
  }

  return (
    <>
      <p className="sm-small sm-muted">
        <Link to="/shows" className="sm-muted">
          Shows
        </Link>{' '}
        <span className="sm-faint">/</span> New show
      </p>

      <div className="sm-page__head sm-stack-2">
        <div>
          <h1 className="sm-page__title">New show</h1>
          <p className="sm-page__lede">Identity only. What a show contains is authored in its workspace tabs once it exists.</p>
        </div>
      </div>

      <Section id="sd-ident" title="Identity" eyebrow="Draft · new show">
        <div className="sm-grid sm-form-column">
          <Field label="Name" help="What the show picker and every heading shows. Editable forever.">
            {(props) => <Input {...props} value={name} onChange={(e) => onNameChange(e.target.value)} />}
          </Field>
          <Field
            label="Id"
            help="Immutable once created. Its cues, playlists, surfaces and assets are keyed by it, and nothing outside the show can reference them."
          >
            {(props) => <Input {...props} className="sm-data" value={id} onChange={(e) => onIdChange(e.target.value)} />}
          </Field>
          <Field label="Notes · optional">
            {(props) => <Textarea {...props} value={notes} onChange={(e) => setNotes(e.target.value)} rows={3} />}
          </Field>
        </div>
        <p className="sm-small sm-faint">
          Nothing else. A show is a namespace: playlists, cues and macros are authored in its workspace tabs once it exists.
        </p>
      </Section>

      {taken && (
        <RuledStrip
          absence="failed"
          label="Id taken"
          fact={
            <>
              <span className="sm-data">{id}</span> already names a show.{' '}
              <Link to={`/shows/${id}`}>Open it</Link> instead.
            </>
          }
        />
      )}
      {createError !== null && <RuledStrip absence="failed" label="Save failed" fact={createError} />}

      <ButtonRow>
        <Button
          variant="primary"
          onClick={create}
          disabled={id === '' || creating || !createGate.allowed}
          title={!createGate.allowed ? createGate.reason : undefined}
        >
          {creating ? 'Creating…' : 'Create show'}
        </Button>
        <Button variant="quiet" onClick={() => navigate('/shows')} disabled={creating}>
          Discard
        </Button>
        <span className="sm-small sm-muted sm-push-end">
          {id === '' ? (
            'An id is required.'
          ) : (
            <>
              Creates <span className="sm-data">{id}</span> at revision <span className="sm-data">1</span>
            </>
          )}
        </span>
      </ButtonRow>
    </>
  )
}
