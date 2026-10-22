import { useEffect, useState } from 'react'
import { getEventsMQTTConfig, putEventsMQTTConfig, type ConfigEventsMQTTPayload } from '../api'
import { Button, Choice, CopyButton, Field, FieldGrid, Input, RuledStrip, Section } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'

const BLANK: ConfigEventsMQTTPayload = { enabled: false, broker: '', topicPrefix: 'showmesh' }

/** Settings > Home Assistant: where the coordinator publishes its events and the night state (ADR-068). */
export function SettingsHomeAssistant() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [loaded, setLoaded] = useState<ConfigEventsMQTTPayload | null>(null)
  const [draft, setDraft] = useState<ConfigEventsMQTTPayload>(BLANK)
  const [revision, setRevision] = useState(0)
  const [readError, setReadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    let cancelled = false
    getEventsMQTTConfig()
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
  const problem = draft.enabled && draft.broker.trim() === '' ? 'Publishing is on but no broker is named. Enter the broker id the installer set up.' : null

  const save = () => {
    setSaving(true)
    setSaveError(null)
    setSaved(false)
    putEventsMQTTConfig({ enabled: draft.enabled, broker: draft.broker.trim(), topicPrefix: draft.topicPrefix.trim() })
      .then((cfg) => {
        setLoaded(cfg.payload)
        setDraft(cfg.payload)
        setRevision(cfg.revision)
        setSaved(true)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  const eventsTopic = `${draft.topicPrefix}/events/#`
  const stateTopic = `${draft.topicPrefix}/night/state`

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Home Assistant</p>
      <h2 className="sm-section__title">Send events to Home Assistant</h2>
      {loaded === null ? (
        readError !== null ? (
          <RuledStrip absence="failed" label="Read failed" fact={readError} />
        ) : (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the Home Assistant settings." />
        )
      ) : (
        <>
          <Section
            id="st-ha-publish"
            title="Publishing"
            detail="Every event the coordinator records is sent to the broker, and the night state is kept up to date there. Choose which ones matter in Home Assistant."
          >
            <Choice
              type="checkbox"
              checked={draft.enabled}
              disabled={editReason !== undefined}
              onChange={(e) => setDraft((d) => ({ ...d, enabled: e.target.checked }))}
              label="Send events to the broker"
            />
            <FieldGrid>
              <Field label="Broker" help="The id of a broker the installer set up for integrations.">
                {(props) => <Input {...props} value={draft.broker} disabled={editReason !== undefined} onChange={(e) => setDraft((d) => ({ ...d, broker: e.target.value }))} />}
              </Field>
              <Field label="Topic prefix" help="The first part of every topic. Plain words separated by /.">
                {(props) => <Input {...props} value={draft.topicPrefix} disabled={editReason !== undefined} onChange={(e) => setDraft((d) => ({ ...d, topicPrefix: e.target.value }))} />}
              </Field>
            </FieldGrid>
          </Section>
          <Section id="st-ha-topics" title="Topics to subscribe to" detail="Use these in Home Assistant. They follow the topic prefix above.">
            <p className="sm-small">Every event: <code>{eventsTopic}</code> <CopyButton value={eventsTopic} label="Copy" /></p>
            <p className="sm-small">Night state: <code>{stateTopic}</code> <CopyButton value={stateTopic} label="Copy" /></p>
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
