import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { getSequenceTestConfig, listConfigObjects, putSequenceTestConfig, type ConfigSequenceTestEntry, type SequenceTestConfigResponse } from '../api'
import { Button, Callout, Input, RuledStrip, Section, Select, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'

type CueOption = { id: string; label: string }

/** Show workspace > Sequence testing: FPP sequence files that run one of this show's cues outside a night. */
export function ShowsSequenceTesting() {
  const { id = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [loaded, setLoaded] = useState<SequenceTestConfigResponse | null>(null)
  const [draft, setDraft] = useState<ConfigSequenceTestEntry[]>([])
  const [cues, setCues] = useState<CueOption[]>([])
  const [readError, setReadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let cancelled = false
    Promise.all([getSequenceTestConfig(id), listConfigObjects('show.cue', id)])
      .then(([cfg, cueList]) => {
        if (cancelled) return
        setLoaded(cfg)
        setDraft(cfg.payload.entries)
        setCues(cueList.objects.filter((c) => c.show === id).map((c) => ({ id: c.id, label: c.label || c.id })))
      })
      .catch((err: unknown) => {
        if (!cancelled) setReadError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [id])

  if (loaded === null) {
    return readError !== null ? (
      <RuledStrip absence="failed" label="Read failed" fact={readError} />
    ) : (
      <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's test sequences." />
    )
  }

  const editReason = gate.allowed ? undefined : gate.reason
  const dirty = JSON.stringify(loaded.payload.entries) !== JSON.stringify(draft)
  const blank = draft.some((e) => e.sequenceFilename.trim() === '' || e.cue === '')
  const setRow = (i: number, patch: Partial<ConfigSequenceTestEntry>) => setDraft((d) => d.map((e, j) => (j === i ? { ...e, ...patch } : e)))

  const save = () => {
    setSaving(true)
    setSaveError(null)
    putSequenceTestConfig(id, { show: id, entries: draft })
      .then((cfg) => {
        setLoaded(cfg)
        setDraft(cfg.payload.entries)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <Section
      id="sm-sequence-testing"
      title="Sequence testing"
      detail="When FPP plays a sequence file listed here, its cue runs in full: audio, projection, LTC and actions. A night session turns these off until it stops."
    >
      {!loaded.running && loaded.notRunningReason !== undefined && <Callout>{loaded.notRunningReason}</Callout>}
      {draft.length === 0 ? (
        <p className="sm-small sm-muted">No test sequences yet. Add one to run a cue from FPP without putting it in a playlist.</p>
      ) : (
        <TableWrap label="Test sequences">
          <Table>
            <thead>
              <tr>
                <th>FPP sequence file</th>
                <th>Cue</th>
                <th>
                  <span className="sm-sr-only">Remove</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {draft.map((entry, i) => (
                <tr key={i}>
                  <td>
                    <Input
                      aria-label={`Sequence file, row ${i + 1}`}
                      value={entry.sequenceFilename}
                      placeholder="Song.fseq"
                      disabled={editReason !== undefined}
                      onChange={(e) => setRow(i, { sequenceFilename: e.target.value })}
                    />
                  </td>
                  <td>
                    <Select aria-label={`Cue, row ${i + 1}`} value={entry.cue} disabled={editReason !== undefined} onChange={(e) => setRow(i, { cue: e.target.value })}>
                      <option value="">Pick a cue</option>
                      {cues.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.label}
                        </option>
                      ))}
                    </Select>
                  </td>
                  <td>
                    <Button variant="quiet" disabled={editReason !== undefined} onClick={() => setDraft((d) => d.filter((_, j) => j !== i))}>
                      Remove
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </TableWrap>
      )}
      {blank && <RuledStrip absence="empty" label="Incomplete" fact="Enter a sequence file and pick a cue for every row, or remove the row." />}
      {saveError !== null && <RuledStrip absence="failed" label="Not saved" fact={saveError} />}
      <div className="sm-btn-row">
        <Button disabled={editReason !== undefined || cues.length === 0} title={cues.length === 0 ? 'This show has no cues yet.' : editReason} onClick={() => setDraft((d) => [...d, { sequenceFilename: '', cue: '' }])}>
          Add test sequence
        </Button>
        <span className="sm-small sm-muted">{loaded.revision === 0 ? 'Not saved yet.' : `Version ${loaded.revision}.`}</span>
        <Button variant="quiet" disabled={!dirty || saving} onClick={() => setDraft(loaded.payload.entries)}>
          Discard changes
        </Button>
        <Button variant="primary" disabled={editReason !== undefined || !dirty || blank || saving} title={editReason} onClick={save}>
          {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </Section>
  )
}
