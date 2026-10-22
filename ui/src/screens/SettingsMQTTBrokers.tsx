import { useEffect, useState } from 'react'
import { deleteIntegrationBroker, listIntegrationBrokers, putIntegrationBroker, type IntegrationBroker, type IntegrationBrokersListResponse } from '../api'
import { Button, ButtonRow, Choice, ConfirmDialog, DefinitionStrip, Field, FieldGrid, Input, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { brokerDraftDirty, brokerDraftError, brokerRequest, draftFrom, emptyDraft, topicRule, type BrokerDraft } from './mqttBrokersModel'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: IntegrationBrokersListResponse }
  | { kind: 'failed'; reason: string }

/** Settings › MQTT brokers: where an MQTT action can publish. */
export function SettingsMQTTBrokers() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<LoadState>({ kind: 'loading' })
  const [drafts, setDrafts] = useState<BrokerDraft[]>([])
  const [adding, setAdding] = useState<BrokerDraft | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<IntegrationBroker | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoad({ kind: 'loading' })
    listIntegrationBrokers()
      .then((response) => {
        if (cancelled) return
        setLoad({ kind: 'loaded', response })
        setDrafts(response.brokers.filter((b) => !b.builtIn).map(draftFrom))
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoad({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const blocked = load.kind === 'loaded' && load.response.editsBlocked !== ''
  const canEdit = gate.allowed && !blocked
  const editTitle = !gate.allowed ? gate.reason : blocked && load.kind === 'loaded' ? load.response.editsBlocked : undefined
  const taken = load.kind === 'loaded' ? load.response.brokers.map((b) => b.id) : []

  const save = (draft: BrokerDraft, done: () => void) => {
    const id = draft.id.trim()
    setBusy(id)
    setError(null)
    putIntegrationBroker(id, brokerRequest(draft))
      .then(() => {
        done()
        setAttempt((n) => n + 1)
      })
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => setBusy(null))
  }

  const remove = (broker: IntegrationBroker) => {
    setBusy(broker.id)
    setError(null)
    deleteIntegrationBroker(broker.id)
      .then(() => setAttempt((n) => n + 1))
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => {
        setBusy(null)
        setRemoving(null)
      })
  }

  const patch = (index: number, change: Partial<BrokerDraft>) => {
    setDrafts(drafts.map((d, i) => (i === index ? { ...d, ...change } : d)))
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> MQTT brokers</p>
      <h2 className="sm-section__title">Where MQTT actions publish</h2>

      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the MQTT brokers." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <>
          {blocked && <RuledStrip absence="stale" label="Set on the coordinator" fact={load.response.editsBlocked} />}

          {load.response.brokers.filter((b) => b.builtIn).map((b) => (
            <Section key={b.id} id="st-mqtt-builtin" title="Built-in broker" detail="Always listed. ShowMesh logs in to it with its own login, which can only use the topics below.">
              <DefinitionStrip
                items={[
                  { term: 'Id', value: b.id },
                  { term: 'Address', value: b.url },
                  { term: 'Topics', value: topicRule(b) },
                ]}
              />
            </Section>
          ))}

          <Section id="st-mqtt-brokers" title="Other brokers" detail="A password is stored and never shown again.">
            {drafts.length === 0 && adding === null ? (
              <RuledStrip absence="empty" label="No other brokers" fact="Only the built-in broker is set. Add another broker to publish somewhere else." />
            ) : (
              <div className="sm-stack-4">
                {drafts.map((draft, index) => {
                  const problem = brokerDraftError(draft, [])
                  const dirty = brokerDraftDirty(draft)
                  return (
                    <div key={draft.id} className="sm-panel sm-stack-4">
                      <h3 className="sm-subsection__title">{draft.id}</h3>
                      <BrokerFields draft={draft} disabled={!canEdit} onChange={(change) => patch(index, change)} />
                      <ButtonRow>
                        <Button
                          variant="primary"
                          disabled={!canEdit || !dirty || problem !== null || busy !== null}
                          title={editTitle ?? problem ?? undefined}
                          onClick={() => save(draft, () => undefined)}
                        >
                          {busy === draft.id ? 'Saving…' : 'Save'}
                        </Button>
                        <Button variant="quiet" disabled={!canEdit || busy !== null} title={editTitle} onClick={() => draft.loaded !== null && setRemoving(draft.loaded)}>
                          Remove
                        </Button>
                        {dirty && <StatusPair tone="warn" label="Unsaved" />}
                        {problem !== null && dirty && <span className="sm-small sm-muted">{problem}</span>}
                      </ButtonRow>
                    </div>
                  )
                })}
                {adding !== null && (
                  <div className="sm-panel sm-stack-4">
                    <h3 className="sm-subsection__title">New broker</h3>
                    <BrokerFields draft={adding} disabled={!canEdit} onChange={(change) => setAdding({ ...adding, ...change })} />
                    <ButtonRow>
                      <Button
                        variant="primary"
                        disabled={!canEdit || brokerDraftError(adding, taken) !== null || busy !== null}
                        title={editTitle ?? brokerDraftError(adding, taken) ?? undefined}
                        onClick={() => save(adding, () => setAdding(null))}
                      >
                        {busy === adding.id.trim() ? 'Adding…' : 'Add broker'}
                      </Button>
                      <Button variant="quiet" onClick={() => setAdding(null)}>
                        Cancel
                      </Button>
                      {adding.id !== '' && brokerDraftError(adding, taken) !== null && <span className="sm-small sm-muted">{brokerDraftError(adding, taken)}</span>}
                    </ButtonRow>
                  </div>
                )}
              </div>
            )}
            {adding === null && (
              <Button disabled={!canEdit} title={editTitle} onClick={() => setAdding(emptyDraft())}>
                Add a broker
              </Button>
            )}
          </Section>
        </>
      )}

      {error !== null && <RuledStrip absence="failed" label="Not saved" fact={error} />}

      <ConfirmDialog
        open={removing !== null}
        title={removing === null ? '' : `Remove the ${removing.id} broker?`}
        detail="Actions that use it must move to another broker first. Its saved password is deleted."
        confirmLabel="Remove broker"
        busy={busy !== null}
        onConfirm={() => removing !== null && remove(removing)}
        onCancel={() => setRemoving(null)}
      />
    </>
  )
}

function BrokerFields({ draft, disabled, onChange }: { draft: BrokerDraft; disabled: boolean; onChange: (change: Partial<BrokerDraft>) => void }) {
  const isNew = draft.loaded === null
  const passwordHelp = isNew ? undefined : draft.loaded?.passwordSet ? 'A password is saved. Leave this empty to keep it.' : 'No password is saved.'
  return (
    <>
      <FieldGrid>
        {isNew && (
          <Field label="Id" help="Actions name the broker by this id. It cannot be changed later.">
            {(props) => <Input {...props} value={draft.id} disabled={disabled} onChange={(e) => onChange({ id: e.target.value })} className="sm-input--id" />}
          </Field>
        )}
        <Field label="Address" help="For example tcp://10.0.0.5:1883.">
          {(props) => <Input {...props} value={draft.url} disabled={disabled} onChange={(e) => onChange({ url: e.target.value })} className="sm-input--address" />}
        </Field>
        <Field label="Username">
          {(props) => <Input {...props} value={draft.username} disabled={disabled} autoComplete="off" onChange={(e) => onChange({ username: e.target.value })} />}
        </Field>
        <Field label={isNew ? 'Password' : 'New password'} help={passwordHelp}>
          {(props) => (
            <Input
              {...props}
              type="password"
              value={draft.password}
              disabled={disabled || draft.clearPassword}
              autoComplete="new-password"
              onChange={(e) => onChange({ password: e.target.value })}
            />
          )}
        </Field>
      </FieldGrid>
      {!isNew && draft.loaded?.passwordSet === true && (
        <Choice type="checkbox" checked={draft.clearPassword} disabled={disabled} onChange={(e) => onChange({ clearPassword: e.target.checked, password: '' })} label="Delete the saved password" />
      )}
    </>
  )
}
