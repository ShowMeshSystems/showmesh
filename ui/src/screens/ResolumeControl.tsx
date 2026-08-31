import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ApiError,
  blackoutResolume,
  clearResolumeLayer,
  getResolumeComposition,
  launchResolumeClip,
  launchResolumeColumn,
  selectResolumeDeck,
  setResolumeLayerBypass,
  setResolumeLayerMaster,
  type ResolumeActionResult,
  type ResolumeCompositionResponse,
} from '../api'
import { Button, ButtonRow, Field, Input, RuledStrip, Section, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso } from '../domain/time'
import { findSignal, lastAnswerAgeMs, RESOLUME_HEALTH_LABEL, RESOLUME_HEALTH_TONE } from './resolumeModel'

type CompositionState =
  | { kind: 'loading' }
  | { kind: 'none' }
  | { kind: 'failed'; reason: string }
  | { kind: 'loaded'; response: ResolumeCompositionResponse }

function outcomeTone(result: ResolumeActionResult): 'good' | 'warn' | 'bad' | 'unknown' {
  if (result.outcome === 'confirmed') return 'good'
  if (result.outcome === 'unconfirmed' || result.outcome === 'unconfirmable') return 'warn'
  if (result.outcome === 'refused' || result.outcome === 'failed') return 'bad'
  return 'unknown'
}

function outcomeLabel(result: ResolumeActionResult): string {
  if (result.outcome === '') return result.replay ? 'Replay pending' : 'Outcome pending'
  return result.outcome.charAt(0).toUpperCase() + result.outcome.slice(1)
}

function observationValue(value: boolean | string | number | null | undefined): string {
  if (value === null || value === undefined || value === '') return 'Not reported'
  return String(value)
}

/** The wide, named-object control surface. Arena ids never leave the stored composition map. */
export function ResolumeControl() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'resolume:action')
  const instance = model.resolume[0]
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const [composition, setComposition] = useState<CompositionState>({ kind: 'loading' })
  const [selectedDeck, setSelectedDeck] = useState('')
  const [master, setMaster] = useState<Record<string, string>>({})
  const [outcomes, setOutcomes] = useState<ResolumeActionResult[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    getResolumeComposition()
      .then((response) => {
        if (!cancelled) setComposition({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) setComposition({ kind: 'none' })
        else setComposition({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => { cancelled = true }
  }, [])

  const data = composition.kind === 'loaded' ? composition.response : null
  const reportedDeck = instance === undefined ? undefined : findSignal(instance, 'resolume.composition.selected_deck')
  const activeDeck = selectedDeck || (typeof reportedDeck?.value === 'string' ? reportedDeck.value : '') || data?.decks[0]?.name || ''
  const selected = data?.decks.find((deck) => deck.name === activeDeck)
  const columns = useMemo(() => data?.columns.filter((column) => column.deckId === selected?.id) ?? [], [data, selected?.id])
  const clips = useMemo(() => data?.clips.filter((clip) => clip.deckId === selected?.id) ?? [], [data, selected?.id])

  const run = useCallback((call: () => Promise<ResolumeActionResult>) => {
    setError(null)
    call()
      .then((result) => setOutcomes((current) => [result, ...current].slice(0, 12)))
      .catch((err: unknown) => setError(describeApiError(err)))
  }, [])

  const blockedTitle = gate.allowed ? undefined : gate.reason
  const canAddress = gate.allowed && data !== null
  const activeClip = (layerName: string) => instance === undefined ? undefined : findSignal(instance, `resolume.layer.${layerName}.active_clip`)
  const readiness = (layerName: string) => instance === undefined ? undefined : findSignal(instance, `resolume.layer.${layerName}.ready`)
  const bypassed = (layerName: string) => instance === undefined ? undefined : findSignal(instance, `resolume.layer.${layerName}.bypassed`)

  return (
    <>
      <p className="sm-small sm-faint"><Link to="/control">Live Control</Link> <span aria-hidden="true">/</span> Resolume</p>
      <h1 className="sm-page__title">Resolume Control</h1>
      <p className="sm-page__lede">Driving the wall by hand, outside any cue or macro. Everything here is named, never an id, and each dispatch reports its own evidence outcome.</p>

      <Section id="resolume-target" title={instance?.instanceId ?? 'Resolume'} aside={<Button variant="danger" size="gloved" disabled={!gate.allowed} title={blockedTitle} onClick={() => run(blackoutResolume)}>Blackout</Button>}>
        {instance === undefined ? (
          <RuledStrip absence="empty" label="None configured" fact="No Resolume instance is configured on this coordinator." detail="Settings › Connections is where an endpoint is added." />
        ) : (
          <p className="sm-small sm-muted"><StatusPair tone={RESOLUME_HEALTH_TONE[instance.health]} label={RESOLUME_HEALTH_LABEL[instance.health]} /> · answered {lastAnswerAgeMs(instance, nowIso) === null ? 'at an unrecorded time' : `${Math.round(lastAnswerAgeMs(instance, nowIso) ?? 0) / 1000} s ago`}. {instance.composition === null ? 'No composition identity reported.' : <>Composition <span className="sm-data">{instance.composition.name}</span>. <Link to={`/settings/resolume/${encodeURIComponent(instance.instanceId)}`}>Stored composition and recovery →</Link></>}</p>
        )}
      </Section>

      {composition.kind === 'loading' ? <RuledStrip absence="loading" label="Reading" fact="Reading the stored Resolume composition." /> : null}
      {composition.kind === 'failed' ? <RuledStrip absence="failed" label="Read failed" fact={composition.reason} detail="Blackout remains available because it addresses no composition object." /> : null}
      {composition.kind === 'none' ? <RuledStrip absence="unavailable" label="No composition" fact="No stored composition names are available for direct clip or layer controls." detail="Upload one in Settings › Resolume. Blackout still works because it addresses no composition object." /> : null}

      {data !== null && (
        <>
          <Section id="resolume-decks" title="Deck" aside={<span className="sm-data">selectDeck</span>}>
            <ButtonRow>
              {data.decks.map((deck) => <Button key={deck.id} variant={deck.name === activeDeck ? 'primary' : 'secondary'} aria-pressed={deck.name === activeDeck} disabled={!canAddress} title={blockedTitle} onClick={() => { setSelectedDeck(deck.name); run(() => selectResolumeDeck(deck.name)) }}>{deck.name}</Button>)}
            </ButtonRow>
            <p className="sm-small sm-muted">The grid below is this deck’s. Select a deck before launching one of its clips; ShowMesh will not silently switch decks.</p>
          </Section>

          <Section id="resolume-grid" title={activeDeck || 'Composition'} aside={<span className="sm-data">launchClip · launchColumn</span>}>
            {selected === undefined ? <RuledStrip absence="unavailable" label="Deck unavailable" fact="The selected deck is not in the stored composition." /> : (
              <TableWrap label={`${selected.name} clip grid, scrollable`}>
                <Table>
                  <thead><tr><th scope="col">Layer</th>{columns.map((column) => <th key={column.id} scope="col"><Button size="compact" disabled={!canAddress} title={blockedTitle} onClick={() => run(() => launchResolumeColumn(column.name, selected.name))}>{column.name}</Button></th>)}</tr></thead>
                  <tbody>{data.layers.map((layer) => <tr key={layer.id}><td><span className="sm-data">{layer.name}</span><br /><span className="sm-small sm-faint">{observationValue(activeClip(layer.name)?.value)}</span></td>{columns.map((column) => {
                    const clip = clips.find((candidate) => candidate.layerIndex === layer.index && candidate.columnIndex === column.index)
                    if (clip === undefined) return <td key={column.id} className="sm-faint">—</td>
                    return <td key={column.id}><Button size="compact" variant={activeClip(layer.name)?.value === clip.name ? 'primary' : 'secondary'} disabled={!canAddress || clip.ambiguous} title={clip.ambiguous ? 'This clip cannot be addressed by name because another clip shares its deck, layer, and name.' : blockedTitle} onClick={() => run(() => launchResolumeClip({ clip: clip.name, deck: selected.name, layer: layer.name }))}>{clip.name}</Button>{clip.ambiguous && <p className="sm-small sm-muted">Ambiguous name</p>}</td>
                  })}</tr>)}</tbody>
                </Table>
              </TableWrap>
            )}
          </Section>

          <Section id="resolume-layers" title="Layers" aside={<span className="sm-data">clearLayer · setLayerMaster · setLayerBypass</span>}>
            <TableWrap label="Resolume layers, scrollable"><Table><thead><tr><th scope="col">Layer</th><th scope="col">Ready</th><th scope="col">Active clip</th><th scope="col">Master</th><th scope="col">Actions</th></tr></thead><tbody>
              {data.layers.map((layer) => { const isBypassed = bypassed(layer.name)?.value === true; return <tr key={layer.id}><td><span className="sm-data">{layer.name}</span></td><td>{observationValue(readiness(layer.name)?.value)}</td><td>{observationValue(activeClip(layer.name)?.value)}</td><td><Field label={`Master for ${layer.name}`}>{(field) => <Input {...field} type="number" step="any" value={master[layer.name] ?? ''} onChange={(event) => setMaster((current) => ({ ...current, [layer.name]: event.target.value }))} />}</Field></td><td><ButtonRow><Button size="compact" disabled={!canAddress || !Number.isFinite(Number(master[layer.name])) || (master[layer.name] ?? '').trim() === ''} title={blockedTitle} onClick={() => run(() => setResolumeLayerMaster(layer.name, Number(master[layer.name])))}>Set master</Button><Button size="compact" disabled={!canAddress} title={blockedTitle} onClick={() => run(() => setResolumeLayerBypass(layer.name, !isBypassed))}>{isBypassed ? 'Restore' : 'Bypass'}</Button><Button size="compact" variant="danger" disabled={!canAddress} title={blockedTitle} onClick={() => run(() => clearResolumeLayer(layer.name))}>Clear</Button></ButtonRow></td></tr> })}
            </tbody></Table></TableWrap>
            <p className="sm-section__footnote">A layer that is not reporting can still be commanded. The result below says what happened; it never treats dispatch as proof.</p>
          </Section>
        </>
      )}

      {(outcomes.length > 0 || error !== null) && <Section id="resolume-results" title="What each dispatch did" aside={<span className="sm-data">this session only</span>}>
        {error !== null && <RuledStrip absence="failed" label="Dispatch failed" fact={error} />}
        <div className="sm-stack-3">{outcomes.map((result, index) => <div key={`${result.outcome}-${index}`} className="sm-outcome"><StatusPair tone={outcomeTone(result)} label={outcomeLabel(result)} /><p className="sm-outcome__detail">{result.outcomeReason || 'The prior dispatch is still resolving.'}{result.replay ? ' This response reuses the original dispatch.' : ''}{result.attributionDegraded ? ' Attribution is degraded because the audit record could not be written.' : ''}</p></div>)}</div>
      </Section>}
    </>
  )
}
