import { useState } from 'react'
import {
  blackoutResolume,
  clearResolumeLayer,
  launchResolumeClip,
  launchResolumeColumn,
  selectResolumeDeck,
  setResolumeLayerBypass,
  setResolumeLayerMaster,
} from '../api'
import { useResolumeActionCall } from '../app/useResolumeAction'
import {
  clipOptions,
  columnOptions,
  deckOptions,
  layerOptions,
  type PickerOption,
} from '../app/resolumeComposition'
import type { ResolumeAction, ResolumeCompositionResponse } from '../app/types'
import { ScopedButton } from './ScopedButton'
import { ResolumeActionOutcome } from './ResolumeActionOutcome'

// Build contract §2.3 (ADR-037 decision 8): "a dropdown plus a Go button",
// populated from the stored composition, sending names — never a Resolume
// object id — for every reference. One control drives all seven actions;
// the reference pickers shown change with the selected action, matching
// each action's own params (GET /resolume/actions' own vocabulary).

export interface ResolumeActionControllerProps {
  actions: readonly ResolumeAction[]
  composition: ResolumeCompositionResponse | null
}

// Each <option> below appends "(generated)" itself when nameGenerated is
// set, matching build contract §2.2/§2.3's "a generated label presented
// as though it were authored defeats the point of the flag existing."
function Picker({
  label,
  options,
  value,
  onChange,
  disabled = false,
}: {
  label: string
  options: readonly PickerOption[]
  value: string
  onChange: (value: string) => void
  disabled?: boolean
}) {
  return (
    <label className="form-field">
      {label}
      <select value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled}>
        <option value="" disabled>
          Choose one
        </option>
        {options.map((o) => (
          <option key={o.key} value={o.value}>
            {o.label}
            {o.nameGenerated ? ' (generated)' : ''}
          </option>
        ))}
      </select>
    </label>
  )
}

export function ResolumeActionController({ actions, composition }: ResolumeActionControllerProps) {
  const [actionName, setActionName] = useState<string>('')
  const [deck, setDeck] = useState('')
  const [clip, setClip] = useState('')
  const [persistent, setPersistent] = useState(false)
  const [layer, setLayer] = useState('')
  const [column, setColumn] = useState('')
  const [bypassed, setBypassed] = useState(false)
  const [master, setMaster] = useState('')
  const { state, run } = useResolumeActionCall()
  const submitting = state.kind === 'submitting'

  const decks = deckOptions(composition)
  const layers = layerOptions(composition)
  const columns = columnOptions(composition, deck === '' ? null : (decks.find((d) => d.value === deck)?.key ?? null))
  const clips = persistent
    ? clipOptions(composition, { persistent: true })
    : deck === ''
      ? []
      : clipOptions(composition, { deckId: decks.find((d) => d.value === deck)?.key ?? '' })

  function handleGo(): void {
    if (submitting) return
    switch (actionName) {
      case 'launchClip': {
        if (clip === '' || (!persistent && deck === '')) return
        // `params.layer` disambiguates a clip name shared by more than one
        // clip in this scope (ADR-037) — sent automatically whenever the
        // selected clip's own name is a duplicate, matching build contract
        // §2.3's "the UI mirroring server-side validation."
        const selected = clips.find((c) => c.value === clip)
        // `exactOptionalPropertyTypes`: an absent key, not a `deck:
        // undefined`/`persistent: undefined` key, is what "not set"
        // means on this wire shape (ADR-037's own exclusivity rule) —
        // matching ShowActionDetail.tsx's identical conditional-spread
        // pattern for the same reason.
        run(() =>
          launchResolumeClip({
            clip,
            ...(persistent ? { persistent: true } : { deck }),
            ...(selected?.duplicateName ? { layer: selected.layerName } : {}),
          }),
        )
        return
      }
      case 'clearLayer':
        if (layer === '') return
        run(() => clearResolumeLayer(layer))
        return
      case 'launchColumn':
        if (column === '' || deck === '') return
        run(() => launchResolumeColumn(column, deck))
        return
      case 'selectDeck':
        if (deck === '') return
        run(() => selectResolumeDeck(deck))
        return
      case 'blackout':
        run(() => blackoutResolume())
        return
      case 'setLayerBypass':
        if (layer === '') return
        run(() => setResolumeLayerBypass(layer, bypassed))
        return
      case 'setLayerMaster': {
        if (layer === '') return
        const value = Number(master)
        if (!Number.isFinite(value)) return
        run(() => setResolumeLayerMaster(layer, value))
        return
      }
      default:
        return
    }
  }

  const goDisabled =
    actionName === '' ||
    (actionName === 'launchClip' && (clip === '' || (!persistent && deck === ''))) ||
    (actionName === 'clearLayer' && layer === '') ||
    (actionName === 'launchColumn' && (column === '' || deck === '')) ||
    (actionName === 'selectDeck' && deck === '') ||
    (actionName === 'setLayerBypass' && layer === '') ||
    (actionName === 'setLayerMaster' && (layer === '' || master.trim() === ''))

  return (
    <div className="resolume-action-controller">
      <label className="form-field">
        Action
        <select
          value={actionName}
          onChange={(e) => {
            setActionName(e.target.value)
            setDeck('')
            setClip('')
            setPersistent(false)
            setLayer('')
            setColumn('')
            setBypassed(false)
            setMaster('')
          }}
        >
          <option value="" disabled>
            Choose an action
          </option>
          {actions.map((a) => (
            <option key={a.name} value={a.name}>
              {a.name}
            </option>
          ))}
        </select>
      </label>

      {(actionName === 'launchClip' || actionName === 'launchColumn' || actionName === 'selectDeck') && (
        <Picker label="Deck" options={decks} value={deck} onChange={setDeck} disabled={actionName === 'launchClip' && persistent} />
      )}

      {actionName === 'launchClip' && (
        <>
          <label className="form-field form-field--checkbox">
            <input
              type="checkbox"
              checked={persistent}
              onChange={(e) => {
                setPersistent(e.target.checked)
                setClip('')
              }}
            />
            Persistent clip (lives outside any deck)
          </label>
          <Picker label="Clip" options={clips} value={clip} onChange={setClip} />
          {clips.some((c) => c.ambiguous) && (
            <p className="text-muted" role="status">
              One or more clips in this list are ambiguous — Resolume has more than one clip
              with the same name on the same layer, so a pick here may still be refused. See
              the ambiguous clips list below.
            </p>
          )}
        </>
      )}

      {actionName === 'clearLayer' && <Picker label="Layer" options={layers} value={layer} onChange={setLayer} />}

      {actionName === 'launchColumn' && (
        <Picker label="Column" options={columns} value={column} onChange={setColumn} />
      )}

      {actionName === 'setLayerBypass' && (
        <>
          <Picker label="Layer" options={layers} value={layer} onChange={setLayer} />
          <label className="form-field form-field--checkbox">
            <input type="checkbox" checked={bypassed} onChange={(e) => setBypassed(e.target.checked)} />
            Bypassed
          </label>
        </>
      )}

      {actionName === 'setLayerMaster' && (
        <>
          <Picker label="Layer" options={layers} value={layer} onChange={setLayer} />
          <label className="form-field">
            Master value
            <input type="number" step="any" value={master} onChange={(e) => setMaster(e.target.value)} />
          </label>
        </>
      )}

      <ScopedButton requiredScope="resolume:action" busy={submitting} onClick={handleGo}>
        {submitting ? 'Dispatching…' : 'Go'}
      </ScopedButton>
      {goDisabled && actionName !== '' && !submitting && (
        <p className="text-muted">Choose every field above before dispatching.</p>
      )}

      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm this action actually happened — this can take
          up to a minute.
        </p>
      )}
      {state.kind === 'result' && <ResolumeActionOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="text-error">
          {state.message}
        </p>
      )}
    </div>
  )
}
