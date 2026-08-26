import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowAction, getShowActionRevisions, putShowAction, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import {
  clipOptions,
  columnOptions,
  deckOptions,
  layerOptions,
  type ClipPickerOption,
} from '../app/resolumeComposition'
import { resolumeCompositionOrNull, useResolumeComposition } from '../app/useResolumeComposition'
import { ScopedButton } from '../components/ScopedButton'
import { ActionBindingCheck } from '../components/ActionBindingCheck'
import { ActionInvokeButton } from '../components/ActionInvokeButton'
import type { ActionIntegration, ConfigShowAction, SafetyClass, ShowActionConfigResponse } from '../app/types'

// Authoring surface #1 of 2 for this wave (STEP-9-SPEC.md section 5.3):
// "the owner declined the cut [of the show.action authoring surface] and
// it ships." Every rule this form enforces client-side is a MIRROR of a
// server-side rule the coordinator enforces independently (ADR-030: "the
// UI holds no authoring logic; validation is server-side and the browser
// may only mirror it") — this component never substitutes its own
// judgement for a `PUT` rejection; it renders whatever the server says
// via describeApiError, exactly like Configuration.tsx.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

const SAFETY_CLASSES: SafetyClass[] = ['none', 'blackout', 'stop', 'powerOff']
// The eight primitives Step 8 registered (docs/bench/fpp-command-vocabulary.md
// section 4's registry, as also enumerated in FPPCommandOutcome's own
// sibling controls) — this form does not invent a ninth, and the
// coordinator resolves whatever is picked through its own registry
// (STEP-9-SPEC.md section 5.3: "primitive must be one of the eight wire
// actions Step 8 registered, resolved through the existing registry
// rather than a second copy of the list"), so a stale copy here fails
// loudly at PUT time rather than silently drifting.
const FPP_PRIMITIVES = [
  'startPlaylist',
  'stopPlaylist',
  'stopPlaylistGracefully',
  'pausePlaylist',
  'resumePlaylist',
  'nextPlaylistItem',
  'prevPlaylistItem',
  'setVolume',
]
const MQTT_EXPECT_KINDS = ['none', 'boolean', 'number', 'text', 'match'] as const

// The fixed seven-action vocabulary Track D D-3 registered
// (GET /resolume/actions' own registry) — same "hardcoded here, resolved
// through the server's own registry rather than a second copy" posture as
// FPP_PRIMITIVES above, for the identical reason.
const RESOLUME_ACTIONS = [
  'launchClip',
  'clearLayer',
  'blackout',
  'launchColumn',
  'selectDeck',
  'setLayerBypass',
  'setLayerMaster',
]

interface FormState {
  show: string
  label: string
  description: string
  safetyClass: SafetyClass | ''
  integration: ActionIntegration
  // fpp fields
  instanceId: string
  primitive: string
  paramsJson: string
  // mqtt fields
  broker: string
  publishTopic: string
  publishPayload: string
  publishQos: '' | '0' | '1' | '2'
  publishRetain: boolean
  expectKind: (typeof MQTT_EXPECT_KINDS)[number]
  expectTopic: string
  expectValue: string
  expectDeadlineSeconds: string
  // resolume fields (ADR-037 decision 8, build contract §2.4): every
  // reference is a NAME, never an object id — the same picker vocabulary
  // as the controller page (ResolumeActionController.tsx).
  resolumeAction: string
  resolumeDeck: string
  resolumeClip: string
  resolumePersistent: boolean
  resolumeLayer: string
  resolumeColumn: string
  resolumeBypassed: boolean
  resolumeMaster: string
}

function emptyForm(): FormState {
  return {
    show: '',
    label: '',
    description: '',
    safetyClass: '',
    integration: 'fpp',
    instanceId: '',
    primitive: '',
    paramsJson: '',
    broker: '',
    publishTopic: '',
    publishPayload: '',
    publishQos: '',
    publishRetain: false,
    expectKind: 'none',
    expectTopic: '',
    expectValue: '',
    expectDeadlineSeconds: '',
    resolumeAction: '',
    resolumeDeck: '',
    resolumeClip: '',
    resolumePersistent: false,
    resolumeLayer: '',
    resolumeColumn: '',
    resolumeBypassed: false,
    resolumeMaster: '',
  }
}

function refString(ref: Record<string, unknown> | undefined, key: string): string {
  const v = ref?.[key]
  return typeof v === 'string' ? v : ''
}

function refBoolean(ref: Record<string, unknown> | undefined, key: string): boolean {
  return ref?.[key] === true
}

function refNumber(ref: Record<string, unknown> | undefined, key: string): string {
  const v = ref?.[key]
  return typeof v === 'number' ? String(v) : ''
}

function formFromPayload(payload: ConfigShowAction): FormState {
  const target = payload.target
  const ref = target.ref
  return {
    show: payload.show,
    label: payload.label,
    description: payload.description,
    safetyClass: payload.safetyClass,
    integration: target.integration,
    instanceId: target.instanceId ?? '',
    primitive: target.primitive ?? '',
    paramsJson: target.params === undefined ? '' : JSON.stringify(target.params, null, 2),
    broker: target.broker ?? '',
    publishTopic: target.publish?.topic ?? '',
    publishPayload: target.publish?.payload ?? '',
    publishQos: target.publish === undefined ? '' : (String(target.publish.qos) as '0' | '1' | '2'),
    publishRetain: target.publish?.retain ?? false,
    expectKind: target.expect?.kind ?? 'none',
    expectTopic: target.expect?.topic ?? '',
    expectValue: target.expect?.value ?? '',
    expectDeadlineSeconds: target.expect?.deadlineSeconds === undefined ? '' : String(target.expect.deadlineSeconds),
    resolumeAction: target.action ?? '',
    resolumeDeck: refString(ref, 'deck'),
    resolumeClip: refString(ref, 'clip'),
    resolumePersistent: refBoolean(ref, 'persistent'),
    resolumeLayer: refString(ref, 'layer'),
    resolumeColumn: refString(ref, 'column'),
    resolumeBypassed: refBoolean(ref, 'bypassed'),
    resolumeMaster: refNumber(ref, 'master'),
  }
}

/**
 * Builds the wire payload from form state, OR returns a client-side
 * validation message. This is a MIRROR, not the authority (see this
 * file's own header comment) — every check here also exists server-side;
 * this only saves a round trip for the common typo/omission case.
 *
 * `resolumeClips` (review finding 6): the SAME clip option list the form
 * rendered — needed here only to recover `duplicateName`/`layerName` for
 * the clip actually picked, exactly like ResolumeActionController.tsx's
 * own `handleGo` does. Without it, a clip disambiguated as "Snow (on
 * Layer B)" in the picker saved a bare `clip: "Snow"` with no `layer`,
 * so the picker showed the disambiguation and the saved macro discarded
 * it — ambiguous again the moment it runs.
 *
 * `resolumeClipId` (review finding B4): the picker's own selected clip
 * ID, looked up here by `key`, never by re-matching `form.resolumeClip`
 * (the NAME) against `resolumeClips`. Two clips can share a name, and a
 * name-based lookup always resolves to whichever of them happens to come
 * first in the list — silently attaching the FIRST duplicate's
 * `layerName` even when the operator picked the second, which persists a
 * `show.action` revision that disambiguates to the wrong clip.
 */
function buildPayload(
  form: FormState,
  resolumeClips: readonly ClipPickerOption[],
  resolumeClipId: string,
): { payload: ConfigShowAction } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.label.trim() === '') return { error: 'Label is required.' }
  if (form.safetyClass === '') {
    return {
      error:
        'Safety class is required and is never defaulted; pick the one that matches what this action actually does.',
    }
  }

  if (form.integration === 'fpp') {
    if (form.instanceId.trim() === '') return { error: 'FPP instance id is required.' }
    if (form.primitive === '') return { error: 'Primitive is required.' }
    let params: Record<string, unknown> | undefined
    if (form.paramsJson.trim() !== '') {
      try {
        const parsed: unknown = JSON.parse(form.paramsJson)
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          return { error: 'Params must be a JSON object, e.g. {"playlist": "Halloween Main"}.' }
        }
        params = parsed as Record<string, unknown>
      } catch {
        return { error: 'Params is not valid JSON.' }
      }
    }
    return {
      payload: {
        show: form.show.trim(),
        label: form.label.trim(),
        description: form.description,
        safetyClass: form.safetyClass,
        target: {
          integration: 'fpp',
          instanceId: form.instanceId.trim(),
          primitive: form.primitive,
          // `exactOptionalPropertyTypes`: an optional key must be OMITTED,
          // not set to `undefined`, to mean "absent" — this mirrors the
          // wire's own absent/null/present distinction (STEP-9-SPEC.md
          // section 5.3) at the TypeScript layer, so the spread is
          // conditional rather than always including a `params: undefined` key.
          ...(params !== undefined ? { params } : {}),
        },
      },
    }
  }

  if (form.integration === 'resolume') {
    if (form.resolumeAction === '') return { error: 'Resolume action is required.' }
    const ref: Record<string, unknown> = {}
    switch (form.resolumeAction) {
      case 'launchClip': {
        if (form.resolumeClip.trim() === '') return { error: 'Clip is required for launchClip.' }
        if (!form.resolumePersistent && form.resolumeDeck.trim() === '') {
          return { error: 'Deck is required for launchClip unless the clip is persistent.' }
        }
        ref.clip = form.resolumeClip.trim()
        if (form.resolumePersistent) {
          ref.persistent = true
        } else {
          ref.deck = form.resolumeDeck.trim()
        }
        // `ref.layer` disambiguates a clip name shared by more than one
        // clip in this scope (ADR-037) — sent automatically whenever the
        // selected clip's own name is a duplicate, matching
        // ResolumeActionController.tsx's identical rule. Looked up by id
        // (see this function's own doc comment on `resolumeClipId`), never
        // by name.
        const selectedClip = resolumeClips.find((c) => c.key === resolumeClipId)
        if (selectedClip?.duplicateName) ref.layer = selectedClip.layerName
        break
      }
      case 'clearLayer':
      case 'setLayerBypass':
      case 'setLayerMaster':
        if (form.resolumeLayer.trim() === '') return { error: `Layer is required for ${form.resolumeAction}.` }
        ref.layer = form.resolumeLayer.trim()
        if (form.resolumeAction === 'setLayerBypass') ref.bypassed = form.resolumeBypassed
        if (form.resolumeAction === 'setLayerMaster') {
          if (form.resolumeMaster.trim() === '') return { error: 'Master value is required for setLayerMaster.' }
          const master = Number(form.resolumeMaster)
          if (!Number.isFinite(master)) return { error: 'Master value must be a number.' }
          ref.master = master
        }
        break
      case 'launchColumn':
        if (form.resolumeColumn.trim() === '') return { error: 'Column is required for launchColumn.' }
        if (form.resolumeDeck.trim() === '') return { error: 'Deck is required for launchColumn.' }
        ref.column = form.resolumeColumn.trim()
        ref.deck = form.resolumeDeck.trim()
        break
      case 'selectDeck':
        if (form.resolumeDeck.trim() === '') return { error: 'Deck is required for selectDeck.' }
        ref.deck = form.resolumeDeck.trim()
        break
      case 'blackout':
        // No parameters — ref stays empty.
        break
      default:
        return { error: `Unrecognized Resolume action "${form.resolumeAction}".` }
    }
    return {
      payload: {
        show: form.show.trim(),
        label: form.label.trim(),
        description: form.description,
        safetyClass: form.safetyClass,
        target: {
          integration: 'resolume',
          action: form.resolumeAction,
          // Every Resolume action is coordinator-required (there is no
          // local fallback for a coordinator-hosted adapter) — this
          // target carries no localFallback field itself (that lives on
          // the MACRO STEP, not the action — see MacroDetail.tsx), but the
          // ref shape is exactly what that step's own coordinator-required
          // constraint assumes exists.
          ...(Object.keys(ref).length > 0 ? { ref } : {}),
        },
      },
    }
  }

  // mqtt
  if (form.broker.trim() === '') {
    return { error: 'Broker is required for an MQTT action and has no default; say which one this publishes on.' }
  }
  if (form.publishTopic.trim() === '') return { error: 'Publish topic is required.' }
  if (form.publishQos === '') return { error: 'QoS is required and has no default; pick 0, 1, or 2.' }
  if (form.expectKind !== 'none') {
    if (form.expectTopic.trim() === '') return { error: 'Expected response topic is required unless the response kind is "none".' }
    if (form.expectDeadlineSeconds.trim() === '') {
      return { error: 'A deadline (in seconds, up to 120) is required unless the response kind is "none".' }
    }
    const deadline = Number(form.expectDeadlineSeconds)
    if (!Number.isInteger(deadline) || deadline <= 0 || deadline > 120) {
      return { error: 'Deadline must be a whole number of seconds from 1 to 120.' }
    }
    // This task's finding 4: the server (decodeMQTTExpect,
    // internal/coordinator/config/showaction.go) requires "match"'s value
    // KEY to be present but explicitly allows it to be an empty string
    // (decodeRequiredStringAllowEmpty — matching against an empty payload
    // is a real, valid target). A client-side refusal of an empty match
    // value was stricter than the server it is supposed to only MIRROR
    // (ADR-030), and it made a stored revision with "" as its match value
    // — which the server had already accepted once — impossible to
    // re-save unchanged. Removed rather than narrowed: there is no client
    // check left to make here that the server does not already make.
  }
  return {
    payload: {
      show: form.show.trim(),
      label: form.label.trim(),
      description: form.description,
      safetyClass: form.safetyClass,
      target: {
        integration: 'mqtt',
        broker: form.broker.trim(),
        publish: {
          topic: form.publishTopic.trim(),
          payload: form.publishPayload,
          qos: Number(form.publishQos) as 0 | 1 | 2,
          retain: form.publishRetain,
        },
        expect:
          form.expectKind === 'none'
            ? { kind: 'none' }
            : {
                kind: form.expectKind,
                topic: form.expectTopic.trim(),
                // Same `exactOptionalPropertyTypes` rule as `params`
                // above: omit `value` entirely rather than set it to
                // `undefined` when this response kind carries none.
                //
                // "match" and "number" are NOT symmetric here (this
                // task's finding 4): the server requires "match"'s value
                // KEY present always, even when it is the empty string —
                // an empty match target is a real, distinct, valid value,
                // not a stand-in for "no value" (decodeMQTTExpect,
                // internal/coordinator/config/showaction.go, via
                // decodeRequiredStringAllowEmpty). "number"'s value stays
                // genuinely optional (an omitted key means "accept receipt
                // with no equality check" server-side), so it is still
                // sent only when the operator actually typed one.
                ...(form.expectKind === 'match'
                  ? { value: form.expectValue }
                  : form.expectKind === 'number' && form.expectValue !== ''
                    ? { value: form.expectValue }
                    : {}),
                deadlineSeconds: Number(form.expectDeadlineSeconds),
              },
      },
    },
  }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowActionConfigResponse; revisions: ConfigRevisionMeta[] }

export interface ShowActionDetailProps {
  isNew?: boolean
}

export function ShowActionDetail({ isNew = false }: ShowActionDetailProps) {
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false) // see Configuration.tsx's own savingRef for why this guards instead of `saving` state

  // Track D seam D-4 (build contract §2.4): the same composition-driven
  // pickers the controller page uses (ResolumeActionController.tsx),
  // reused here so an authored action's references are names too.
  // Review finding 7: fetched ONLY while the operator has the `resolume`
  // integration selected — this is a `config:write`-gated request, and
  // visiting an ordinary `fpp` or `mqtt` action used to issue it anyway,
  // 403ing for most sessions for no reason the form ever needed.
  const resolumeCompositionState = useResolumeComposition('show-action-detail', form.integration === 'resolume')
  const resolumeComposition = resolumeCompositionOrNull(resolumeCompositionState)
  const resolumeDecks = deckOptions(resolumeComposition)
  const resolumeLayers = layerOptions(resolumeComposition)

  // Review finding 8: the deck picker's own <select> must key on the
  // deck's id, never its (possibly ambiguous) name — see
  // ResolumeActionController.tsx's identical `Picker` comment for why an
  // HTML <select> cannot otherwise distinguish two same-named decks.
  // `form.resolumeDeck` keeps holding the NAME (what buildPayload sends
  // and what an existing action's stored ref carries); `resolumeDeckId`
  // is UI-only state driving the picker and the clip/column scoping
  // below, kept in sync by the effect after it.
  const [resolumeDeckId, setResolumeDeckId] = useState('')

  // Review finding B4: the SAME id-keyed pattern as resolumeDeckId, one
  // level down — the clip picker's own <select> must key on the clip's
  // id, never its (possibly duplicate, within this scope) name, or the
  // second of two same-named clips is never independently selectable.
  // `form.resolumeClip` keeps holding the NAME (what buildPayload sends
  // and what an existing action's stored ref carries); `resolumeClipId`
  // is UI-only state driving the picker, kept in sync by the effect
  // after it and threaded into buildPayload so the SAVED `layer`
  // disambiguator comes from the clip actually picked, not from
  // whichever same-named clip a name lookup happens to find first.
  const [resolumeClipId, setResolumeClipId] = useState('')

  const resolumeColumns = columnOptions(resolumeComposition, resolumeDeckId === '' ? null : resolumeDeckId)
  // useMemo (not a plain const): resolumeClipId's own resolution effect
  // below depends on this array, and a fresh array reference on every
  // render (the plain-const shape every other derived list here uses)
  // would re-run that effect on every render rather than only when the
  // scope actually changes.
  const resolumeClips = useMemo(
    () =>
      form.resolumePersistent
        ? clipOptions(resolumeComposition, { persistent: true })
        : resolumeDeckId === ''
          ? []
          : clipOptions(resolumeComposition, { deckId: resolumeDeckId }),
    [resolumeComposition, form.resolumePersistent, resolumeDeckId],
  )

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    // A newly-navigated-to action's deck and clip ids are unknown until
    // resolved (below) against ITS OWN composition state — clearing them
    // here stops the previously-viewed action's ids from leaking into
    // this one while that resolution is pending.
    setResolumeDeckId('')
    setResolumeClipId('')
    Promise.all([getShowAction(existingId), getShowActionRevisions(existingId)])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setForm(formFromPayload(config.payload))
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [existingId, readGate.allowed, isNew])

  // Resolves `resolumeDeckId` from the stored deck NAME once the
  // composition is loaded. Best-effort only when that name is itself
  // ambiguous (two decks sharing it): this picks the first match, purely
  // for what the picker shows pre-populated — buildPayload always sends
  // `form.resolumeDeck` (the name actually stored) untouched, so loading
  // an existing action and saving it again without touching this field
  // changes nothing. An explicit pick through the picker's own onChange
  // always sets both the id and the name together and is never
  // overwritten here (the `current !== ''` guard).
  useEffect(() => {
    if (resolumeComposition === null) return
    if (form.resolumeDeck === '') return
    setResolumeDeckId((current) => {
      if (current !== '') return current
      return deckOptions(resolumeComposition).find((d) => d.value === form.resolumeDeck)?.key ?? current
    })
  }, [resolumeComposition, form.resolumeDeck])

  // Resolves `resolumeClipId` from the stored clip NAME the identical
  // way the effect above resolves `resolumeDeckId` — best-effort only
  // when that name is itself ambiguous within this scope (two clips
  // sharing it): this picks the first match, purely for what the picker
  // shows pre-populated. An explicit pick through the picker's own
  // onChange always sets both the id and the name together and is never
  // overwritten here (the `current !== ''` guard) — that explicit pick is
  // what makes selecting the SECOND of two same-named clips actually
  // save the right one; this effect only pre-populates a valid-looking
  // selection for an action nobody has touched yet.
  useEffect(() => {
    if (form.resolumeClip === '') return
    setResolumeClipId((current) => {
      if (current !== '') return current
      return resolumeClips.find((c) => c.value === form.resolumeClip)?.key ?? current
    })
  }, [resolumeClips, form.resolumeClip])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('An action id is required.')
      return
    }
    const built = buildPayload(form, resolumeClips, resolumeClipId)
    if ('error' in built) {
      setSaveError(built.error)
      return
    }
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      const resp = await putShowAction(id, built.payload)
      if (isNew) {
        navigate(`/actions/${encodeURIComponent(id)}`)
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getShowActionRevisions(id)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions } : prev))
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  const pageGate = isNew ? writeGate : readGate
  if (!pageGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">{isNew ? 'New show action' : 'Show action'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading action…</p>
  }
  if (!isNew && state.kind === 'error') {
    return (
      <p className="panel panel--error" role="alert">
        {state.message}
      </p>
    )
  }

  const editable = writeGate.allowed

  return (
    <div>
      <h2 className="panel__title">{isNew ? 'New show action' : form.label || existingId}</h2>

      {!isNew && existingId && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '1rem', flexWrap: 'wrap' }}>
          <ActionBindingCheck actionId={existingId} />
          <ActionInvokeButton actionId={existingId} label="Invoke now" />
        </div>
      )}

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Action id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable} className="show-action-form">
        <label className="form-field">
          Show
          <input type="text" value={form.show} onChange={(e) => setForm({ ...form, show: e.target.value })} />
        </label>
        <label className="form-field">
          Label
          <input type="text" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} />
        </label>
        <label className="form-field">
          Description
          <input
            type="text"
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
        </label>
        <label className="form-field">
          Safety class
          <select
            value={form.safetyClass}
            onChange={(e) => setForm({ ...form, safetyClass: e.target.value as SafetyClass })}
          >
            <option value="" disabled>
              Choose one, never defaulted
            </option>
            {SAFETY_CLASSES.map((cls) => (
              <option key={cls} value={cls}>
                {cls}
              </option>
            ))}
          </select>
        </label>

        <label className="form-field">
          Integration
          <select
            value={form.integration}
            onChange={(e) => setForm({ ...form, integration: e.target.value as ActionIntegration })}
          >
            <option value="fpp">FPP primitive</option>
            <option value="mqtt">External MQTT command</option>
            <option value="resolume">Resolume action</option>
          </select>
        </label>

        {form.integration === 'fpp' ? (
          <>
            <label className="form-field">
              FPP instance id
              <input
                type="text"
                value={form.instanceId}
                onChange={(e) => setForm({ ...form, instanceId: e.target.value })}
              />
            </label>
            <label className="form-field">
              Primitive
              <select value={form.primitive} onChange={(e) => setForm({ ...form, primitive: e.target.value })}>
                <option value="" disabled>
                  Choose one
                </option>
                {FPP_PRIMITIVES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
            <label className="form-field">
              Params (JSON object, optional; shape depends on the primitive chosen above)
              <textarea
                rows={4}
                value={form.paramsJson}
                onChange={(e) => setForm({ ...form, paramsJson: e.target.value })}
              />
            </label>
          </>
        ) : form.integration === 'mqtt' ? (
          <>
            <label className="form-field">
              Broker (required; this deployment&rsquo;s declared broker identifier, never defaulted)
              <input type="text" value={form.broker} onChange={(e) => setForm({ ...form, broker: e.target.value })} />
            </label>
            <label className="form-field">
              Publish topic
              <input
                type="text"
                value={form.publishTopic}
                onChange={(e) => setForm({ ...form, publishTopic: e.target.value })}
              />
            </label>
            <label className="form-field">
              Publish payload
              <input
                type="text"
                value={form.publishPayload}
                onChange={(e) => setForm({ ...form, publishPayload: e.target.value })}
              />
            </label>
            <label className="form-field">
              QoS (required, no default)
              <select
                value={form.publishQos}
                onChange={(e) => setForm({ ...form, publishQos: e.target.value as FormState['publishQos'] })}
              >
                <option value="" disabled>
                  Choose one
                </option>
                <option value="0">0</option>
                <option value="1">1</option>
                <option value="2">2</option>
              </select>
            </label>
            <label className="form-field form-field--checkbox">
              <input
                type="checkbox"
                checked={form.publishRetain}
                onChange={(e) => setForm({ ...form, publishRetain: e.target.checked })}
              />
              Retain (defaults to off when left unchecked)
            </label>

            <label className="form-field">
              Expected response
              <select
                value={form.expectKind}
                onChange={(e) => setForm({ ...form, expectKind: e.target.value as FormState['expectKind'] })}
              >
                {MQTT_EXPECT_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </select>
            </label>
            {form.expectKind === 'none' ? (
              <p className="text-muted">
                No response is expected. This step will report as unconfirmable, on every run, by
                design: that is honest, not a defect.
              </p>
            ) : (
              <>
                <label className="form-field">
                  Response topic
                  <input
                    type="text"
                    value={form.expectTopic}
                    onChange={(e) => setForm({ ...form, expectTopic: e.target.value })}
                  />
                </label>
                {(form.expectKind === 'match' || form.expectKind === 'number') && (
                  <label className="form-field">
                    {form.expectKind === 'match' ? 'Exact value to match' : 'Expected value (optional)'}
                    <input
                      type="text"
                      value={form.expectValue}
                      onChange={(e) => setForm({ ...form, expectValue: e.target.value })}
                    />
                  </label>
                )}
                <label className="form-field">
                  Deadline, in seconds (1&ndash;120)
                  <input
                    type="number"
                    min={1}
                    max={120}
                    value={form.expectDeadlineSeconds}
                    onChange={(e) => setForm({ ...form, expectDeadlineSeconds: e.target.value })}
                  />
                </label>
              </>
            )}
          </>
        ) : (
          <>
            <p className="text-muted">
              Every Resolume action is coordinator-required: Resolume holds no local fallback for
              a coordinator-hosted adapter. This macro step must use the
              &ldquo;coordinator-required&rdquo; local fallback class; see the macro editor.
            </p>
            {/* Review finding 7: these pickers render from
                resolumeCompositionOrNull alone, which is null in EVERY one
                of the states below — a non-admin used to see four empty
                "Choose one" dropdowns with no reason at all. Follows the
                same branches ResolumeView.tsx's own inventory panel
                handles. */}
            {resolumeCompositionState.kind === 'loading' && (
              <p className="text-muted">Loading the stored composition…</p>
            )}
            {resolumeCompositionState.kind === 'not_stored' && (
              <p className="text-muted" role="status">
                {resolumeCompositionState.reason}
              </p>
            )}
            {resolumeCompositionState.kind === 'forbidden' && (
              <p className="panel panel--warning" role="alert">
                {resolumeCompositionState.reason}
              </p>
            )}
            {resolumeCompositionState.kind === 'unauthorized' && (
              <p className="panel panel--warning" role="alert">
                {resolumeCompositionState.reason}
              </p>
            )}
            {resolumeCompositionState.kind === 'error' && (
              <p className="panel panel--error" role="alert">
                {resolumeCompositionState.message}
              </p>
            )}
            <label className="form-field">
              Action
              <select
                value={form.resolumeAction}
                onChange={(e) => setForm({ ...form, resolumeAction: e.target.value })}
              >
                <option value="" disabled>
                  Choose one
                </option>
                {RESOLUME_ACTIONS.map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </select>
            </label>

            {(form.resolumeAction === 'launchClip' ||
              form.resolumeAction === 'launchColumn' ||
              form.resolumeAction === 'selectDeck') && (
              <label className="form-field">
                Deck
                <select
                  value={resolumeDeckId}
                  onChange={(e) => {
                    const id = e.target.value
                    const name = resolumeDecks.find((d) => d.key === id)?.value ?? ''
                    setResolumeDeckId(id)
                    setForm({ ...form, resolumeDeck: name })
                  }}
                  disabled={form.resolumeAction === 'launchClip' && form.resolumePersistent}
                >
                  <option value="" disabled>
                    Choose one
                  </option>
                  {resolumeDecks.map((d) => (
                    <option key={d.key} value={d.key}>
                      {d.label}
                      {d.nameGenerated ? ' (generated)' : ''}
                    </option>
                  ))}
                </select>
              </label>
            )}

            {form.resolumeAction === 'launchClip' && (
              <>
                <label className="form-field form-field--checkbox">
                  <input
                    type="checkbox"
                    checked={form.resolumePersistent}
                    onChange={(e) => {
                      setForm({ ...form, resolumePersistent: e.target.checked, resolumeClip: '' })
                      setResolumeClipId('')
                    }}
                  />
                  Persistent clip (lives outside any deck)
                </label>
                <label className="form-field">
                  Clip
                  <select
                    value={resolumeClipId}
                    onChange={(e) => {
                      const id = e.target.value
                      const name = resolumeClips.find((c) => c.key === id)?.value ?? ''
                      setResolumeClipId(id)
                      setForm({ ...form, resolumeClip: name })
                    }}
                  >
                    <option value="" disabled>
                      Choose one
                    </option>
                    {resolumeClips.map((c) => (
                      <option key={c.key} value={c.key}>
                        {c.label}
                        {c.nameGenerated ? ' (generated)' : ''}
                      </option>
                    ))}
                  </select>
                </label>
                {resolumeClips.some((c) => c.ambiguous) && (
                  <p className="text-muted" role="status">
                    One or more clips in this list are ambiguous in Resolume itself; see the
                    ambiguous clips list on the Resolume view for what to rename.
                  </p>
                )}
              </>
            )}

            {(form.resolumeAction === 'clearLayer' ||
              form.resolumeAction === 'setLayerBypass' ||
              form.resolumeAction === 'setLayerMaster') && (
              <label className="form-field">
                Layer
                <select
                  value={form.resolumeLayer}
                  onChange={(e) => setForm({ ...form, resolumeLayer: e.target.value })}
                >
                  <option value="" disabled>
                    Choose one
                  </option>
                  {resolumeLayers.map((l) => (
                    <option key={l.key} value={l.value}>
                      {l.label}
                      {l.nameGenerated ? ' (generated)' : ''}
                    </option>
                  ))}
                </select>
              </label>
            )}

            {form.resolumeAction === 'launchColumn' && (
              <label className="form-field">
                Column
                <select
                  value={form.resolumeColumn}
                  onChange={(e) => setForm({ ...form, resolumeColumn: e.target.value })}
                >
                  <option value="" disabled>
                    Choose one
                  </option>
                  {resolumeColumns.map((c) => (
                    <option key={c.key} value={c.value}>
                      {c.label}
                      {c.nameGenerated ? ' (generated)' : ''}
                    </option>
                  ))}
                </select>
              </label>
            )}

            {form.resolumeAction === 'setLayerBypass' && (
              <label className="form-field form-field--checkbox">
                <input
                  type="checkbox"
                  checked={form.resolumeBypassed}
                  onChange={(e) => setForm({ ...form, resolumeBypassed: e.target.checked })}
                />
                Bypassed
              </label>
            )}

            {form.resolumeAction === 'setLayerMaster' && (
              <label className="form-field">
                Master value
                <input
                  type="number"
                  step="any"
                  value={form.resolumeMaster}
                  onChange={(e) => setForm({ ...form, resolumeMaster: e.target.value })}
                />
              </label>
            )}
          </>
        )}
      </fieldset>

      {editable && (
        <div style={{ marginTop: '1rem' }}>
          {saveError !== null && (
            <p role="alert" className="session-form__error">
              {saveError}
            </p>
          )}
          <ScopedButton
            requiredScope={CONFIG_WRITE_SCOPE}
            onClick={() => void handleSave()}
            busy={saving}
            busyReason="Saving this action revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create action' : 'Save action'}
          </ScopedButton>
        </div>
      )}

      {/* Status: what the coordinator currently reports about this
          action's stored configuration, kept apart from the authoring
          fieldset above (ActionBindingCheck/ActionInvokeButton at the top
          of this page are the OTHER live-status widgets, already outside
          the fieldset and untouched here). The active-revision line stays
          outside any <details>: it is short and always relevant. Revision
          history is a long, rarely-consulted list, a reasonable thing to
          start collapsed (nothing in it is stale/failed evidence rendered
          through EvidenceValue; it is a plain fetched list). */}
      {!isNew && state.kind === 'loaded' && (
        <section aria-label="Status">
          <p className="panel" role="status">
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>
          {state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Revision</th>
                    <th>Active</th>
                    <th>Created at</th>
                    <th>Created by</th>
                  </tr>
                </thead>
                <tbody>
                  {state.revisions.map((rev) => (
                    <tr key={rev.revision}>
                      <td>{rev.revision}</td>
                      <td>{rev.active ? 'active' : ''}</td>
                      <td>{formatAbsolute(rev.createdAt)}</td>
                      <td>{rev.createdByPrincipalName ?? '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </details>
          )}
        </section>
      )}
    </div>
  )
}

