import { useEffect, useState } from 'react'
import { getEmergencyStopConfig, listConfigObjects, putEmergencyStopConfig, type ConfigEmergencyStopPayload } from '../api'
import { ActionList, Button, RuledStrip, Section, type ActionOption } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'

type Level = 'stop' | 'stopPowerDown' | 'hardStop'

const LEVELS: readonly { key: Level; title: string; detail: string }[] = [
  { key: 'stop', title: 'Stop', detail: 'Runs after the show stops and the night is held.' },
  { key: 'stopPowerDown', title: 'Stop and power down', detail: 'Runs after the show stops and the night starts its normal power down.' },
  { key: 'hardStop', title: 'Hard stop', detail: 'Runs after the show stops and the night ends with no waiting.' },
]

const emptyPayload = (): ConfigEmergencyStopPayload => ({ stop: { actions: [] }, stopPowerDown: { actions: [] }, hardStop: { actions: [] } })

/** Settings > Emergency stop: the actions each emergency stop level runs after the stop itself. */
export function SettingsEmergencyStop() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [loaded, setLoaded] = useState<ConfigEmergencyStopPayload | null>(null)
  const [draft, setDraft] = useState<ConfigEmergencyStopPayload>(emptyPayload)
  const [revision, setRevision] = useState(0)
  const [options, setOptions] = useState<ActionOption[]>([])
  const [readError, setReadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState<number | null>(null)

  useEffect(() => {
    let cancelled = false
    Promise.all([getEmergencyStopConfig(), listConfigObjects('show.action')])
      .then(([cfg, actions]) => {
        if (cancelled) return
        setLoaded(cfg.payload)
        setDraft(cfg.payload)
        setRevision(cfg.revision)
        setOptions(actions.objects.map((a) => ({ id: a.id, label: a.show === '' ? a.label : `${a.label} (${a.show})` })))
      })
      .catch((err: unknown) => {
        if (!cancelled) setReadError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [])

  const blank = (Object.keys(draft) as Level[]).some((k) => draft[k].actions.some((id) => id === ''))
  const dirty = loaded !== null && JSON.stringify(loaded) !== JSON.stringify(draft)
  const editReason = gate.allowed ? undefined : gate.reason

  const save = () => {
    setSaving(true)
    setSaveError(null)
    putEmergencyStopConfig(draft)
      .then((cfg) => {
        setLoaded(cfg.payload)
        setDraft(cfg.payload)
        setRevision(cfg.revision)
        setSavedAt(Date.now())
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Emergency stop</p>
      <h2 className="sm-section__title">What each emergency stop runs after it stops the show</h2>
      {loaded === null ? (
        readError !== null ? (
          <RuledStrip absence="failed" label="Read failed" fact={readError} />
        ) : (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the emergency stop settings." />
        )
      ) : (
        <>
          <p className="sm-small sm-muted">
            Every level stops FPP, silences audio and blacks out projection first. These actions run after that, in order. A failed action is reported and the rest still run.
          </p>
          {LEVELS.map((level) => (
            <Section key={level.key} id={`st-estop-${level.key}`} title={level.title} detail={level.detail}>
              <ActionList
                label="Follow-up actions"
                value={draft[level.key].actions}
                options={options}
                disabledReason={editReason}
                onChange={(next) => setDraft((d) => ({ ...d, [level.key]: { ...d[level.key], actions: next } }))}
              />
            </Section>
          ))}
          {blank && <RuledStrip absence="empty" label="Incomplete" fact="Pick an action for every row, or remove the empty row." />}
          {saveError !== null && <RuledStrip absence="failed" label="Not saved" fact={saveError} />}
          <div className="sm-btn-row">
            <span className="sm-small sm-muted">{savedAt !== null && !dirty ? `Saved as version ${revision}.` : revision === 0 ? 'Not saved yet.' : `Version ${revision}.`}</span>
            <Button variant="quiet" disabled={!dirty || saving} onClick={() => setDraft(loaded)}>
              Discard changes
            </Button>
            <Button variant="primary" disabled={editReason !== undefined || !dirty || blank || saving} title={editReason} onClick={save}>
              {saving ? 'Saving…' : 'Save'}
            </Button>
          </div>
        </>
      )}
    </>
  )
}
