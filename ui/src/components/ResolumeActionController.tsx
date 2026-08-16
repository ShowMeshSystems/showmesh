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
  optionValue = 'value',
}: {
  label: string
  options: readonly PickerOption[]
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  /**
   * Review finding 8 (decks), review finding B4 (clips): two entries in
   * the SAME scope can share a name, and an HTML <select> cannot
   * distinguish two <option>s with the identical `value` — by the time
   * onChange fires, the duplicate is already collapsed to one string. The
   * deck and clip pickers key their option `value` on the object's own
   * (always-unique) id instead, so a duplicate name is still SELECTABLE
   * distinctly even though its disambiguated LABEL is what makes the
   * collision visible. Both callers then resolve that id back to the
   * name the wire actually wants (ADR-037: a reference is a name, never
   * an id) by looking the id up in the SAME options list, never by
   * re-matching on `value`, which is exactly the collapse this option
   * exists to avoid. Every other picker (layer, column) keeps sending
   * the name directly, since neither is reverse-looked-up the way deck
   * and clip are.
   */
  optionValue?: 'value' | 'key'
}) {
  return (
    <label className="form-field">
      {label}
      <select value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled}>
        <option value="" disabled>
          Choose one
        </option>
        {options.map((o) => (
          <option key={o.key} value={optionValue === 'key' ? o.key : o.value}>
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
  // Review finding 8: `deck` holds the SELECTED DECK'S ID, not its name —
  // see Picker's own comment on `optionValue`. `selectedDeckName` below is
  // the one place that id is resolved back to the name the wire actually
  // wants, and that resolution is by id (always unique), never by a name
  // that might collide.
  const [deck, setDeck] = useState('')
  // Review finding B4: `clip` holds the SELECTED CLIP'S ID, not its name —
  // see Picker's own comment on `optionValue`. Two clips in the same scope
  // (deck or persistent) can share a name, and `value` collapses that
  // collision in the <select> itself; keying on the always-unique `key`
  // (and resolving it back to a name below) is what keeps the SECOND
  // duplicate actually selectable.
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
  const selectedDeckName = decks.find((d) => d.key === deck)?.value ?? ''
  const columns = columnOptions(composition, deck === '' ? null : deck)
  const clips = persistent
    ? clipOptions(composition, { persistent: true })
    : deck === ''
      ? []
      : clipOptions(composition, { deckId: deck })

  function handleGo(): void {
    if (submitting) return
    switch (actionName) {
      case 'launchClip': {
        if (clip === '' || (!persistent && deck === '')) return
        // `params.layer` disambiguates a clip name shared by more than one
        // clip in this scope (ADR-037) — sent automatically whenever the
        // selected clip's own name is a duplicate, matching build contract
        // §2.3's "the UI mirroring server-side validation." Looked up by
        // `key` (the clip's own id), never by `value` (its name): with two
        // same-named clips in this scope, `clip` state and `c.value` both
        // read that shared name, so a `value` match always resolves to
        // whichever of the two clips happens to come first, silently
        // launching the wrong one (review finding B4).
        const selected = clips.find((c) => c.key === clip)
        // `exactOptionalPropertyTypes`: an absent key, not a `deck:
        // undefined`/`persistent: undefined` key, is what "not set"
        // means on this wire shape (ADR-037's own exclusivity rule) —
        // matching ShowActionDetail.tsx's identical conditional-spread
        // pattern for the same reason. `clip` itself is this clip's ID
        // (Picker's own optionValue="key" below), so the wire dispatch —
        // which must carry the ADR-037 NAME, never a raw object id — sends
        // `selected.value`, not the raw `clip` state.
        if (selected === undefined) return
        run(() =>
          launchResolumeClip({
            clip: selected.value,
            ...(persistent ? { persistent: true } : { deck: selectedDeckName }),
            ...(selected.duplicateName ? { layer: selected.layerName } : {}),
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
        run(() => launchResolumeColumn(column, selectedDeckName))
        return
      case 'selectDeck':
        if (deck === '') return
        run(() => selectResolumeDeck(selectedDeckName))
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
        <Picker
          label="Deck"
          options={decks}
          value={deck}
          onChange={setDeck}
          optionValue="key"
          disabled={actionName === 'launchClip' && persistent}
        />
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
          <Picker label="Clip" options={clips} value={clip} onChange={setClip} optionValue="key" />
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
      {state.kind === 'result' && <ResolumeActionOutcome result={state.result} composition={composition} />}
      {state.kind === 'error' && (
        <p role="alert" className="text-error">
          {state.message}
        </p>
      )}
    </div>
  )
}
