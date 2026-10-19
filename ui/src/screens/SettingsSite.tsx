import { useEffect, useState } from 'react'
import { getSitePowerConfig, putSitePowerConfig, type ConfigSitePowerPayload } from '../api'
import { ActionList, Button, RuledStrip, Section } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { ShutdownSequenceEditor } from './ShutdownSequenceEditor'
import { sequencesProblem } from './shutdownSequenceModel'
import { useAllActionOptions } from './useAllActionOptions'

/** Settings > Site settings: the shutdown sequences and power-on actions for the whole site (ADR-065). */
export function SettingsSite() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const options = useAllActionOptions()
  const [loaded, setLoaded] = useState<ConfigSitePowerPayload | null>(null)
  const [draft, setDraft] = useState<ConfigSitePowerPayload>({ shutdownSequences: [], powerOnActions: [] })
  const [revision, setRevision] = useState(0)
  const [readError, setReadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    let cancelled = false
    getSitePowerConfig()
      .then((cfg) => {
        if (cancelled) return
        setLoaded(cfg.payload)
        setDraft(cfg.payload)
        setRevision(cfg.revision)
      })
      .catch((err: unknown) => {
        if (!cancelled) setReadError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [])

  const editReason = gate.allowed ? undefined : gate.reason
  const dirty = loaded !== null && JSON.stringify(loaded) !== JSON.stringify(draft)
  const problem = sequencesProblem(draft.shutdownSequences) ?? (draft.powerOnActions.includes('') ? 'A power-on row has no action. Pick one or remove the row.' : null)

  const save = () => {
    setSaving(true)
    setSaveError(null)
    putSitePowerConfig(draft)
      .then((cfg) => {
        setLoaded(cfg.payload)
        setDraft(cfg.payload)
        setRevision(cfg.revision)
        setSaved(true)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Site settings</p>
      <h2 className="sm-section__title">Power for the whole site</h2>
      {loaded === null ? (
        readError !== null ? (
          <RuledStrip absence="failed" label="Read failed" fact={readError} />
        ) : (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the site settings." />
        )
      ) : (
        <>
          <Section
            id="st-site-shutdown"
            title="Shutdown sequences"
            detail="Every sequence runs at power down, side by side. Steps in a sequence run in order. A step that fails, or does not confirm when it must, stops that sequence so no more power is cut. A night can leave a sequence out."
          >
            <ShutdownSequenceEditor sequences={draft.shutdownSequences} savedIds={loaded.shutdownSequences.map((s) => s.id)} options={options} disabledReason={editReason} onChange={(next) => setDraft((d) => ({ ...d, shutdownSequences: next }))} />
          </Section>
          <Section id="st-site-power-on" title="Power on" detail="Runs in order when someone presses System power on under Testing. Never part of a night.">
            <ActionList label="Power-on actions" value={draft.powerOnActions} options={options} disabledReason={editReason} onChange={(next) => setDraft((d) => ({ ...d, powerOnActions: next }))} />
          </Section>
          {problem !== null && <RuledStrip absence="empty" label="Incomplete" fact={problem} />}
          {saveError !== null && <RuledStrip absence="failed" label="Not saved" fact={saveError} />}
          <div className="sm-btn-row">
            <span className="sm-small sm-muted">{saved && !dirty ? `Saved as version ${revision}.` : revision === 0 ? 'Not saved yet.' : `Version ${revision}.`}</span>
            <Button variant="quiet" disabled={!dirty || saving} onClick={() => setDraft(loaded)}>
              Discard changes
            </Button>
            <Button variant="primary" disabled={editReason !== undefined || !dirty || problem !== null || saving} title={editReason} onClick={save}>
              {saving ? 'Saving…' : 'Save'}
            </Button>
          </div>
        </>
      )}
    </>
  )
}
