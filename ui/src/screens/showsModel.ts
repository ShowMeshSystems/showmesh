import type {
  ActionBinding,
  Asset,
  ConfigObjectSummary,
  ConfigShowActionTarget,
  ConfigShowCue,
  ConfigShowMacroStep,
  ConfigShowPlaylist,
  FPPInstance,
  FPPPlaylistDefinitionMetadata,
  MacroRunSummary,
  Model,
  Node,
  ShowCueConfigResponse,
} from '../api'
import type { Tone } from '../kit'
import { EVIDENCE_ABSENCE, EVIDENCE_LABEL, EVIDENCE_TONE } from '../domain/evidence'
import { ageMs, formatClock, formatDuration, millisToTimecode } from '../domain/time'

/** The id of the show `currentRuns` reports as active, or null when unknown. */
export function activeShowId(model: Model): string | null {
  const activeShow = model.currentRuns?.activeShow
  if (activeShow === undefined || activeShow === null || !activeShow.configured) return null
  return activeShow.show
}

export type ShowRow = {
  id: string
  label: string
  revision: number
  updatedAt: string
  active: boolean
}

export function showRows(objects: readonly ConfigObjectSummary[], model: Model): ShowRow[] {
  const active = activeShowId(model)
  return objects.map((object) => ({
    id: object.id,
    label: object.label,
    revision: object.currentRevision,
    updatedAt: object.updatedAt,
    active: active !== null && active === object.id,
  }))
}

export function showSavedLabel(updatedAt: string): string {
  return formatClock(updatedAt) ?? 'an unrecorded time'
}

/**
 * What one show contains, per its own configured objects. A fetch that fails
 * leaves the whole bundle unfetched rather than substituting zero for one part.
 */
export type ShowContents = {
  playlists: ConfigObjectSummary[]
  cues: ConfigObjectSummary[]
  surfaces: ConfigObjectSummary[]
  actions: ConfigObjectSummary[]
  macros: ConfigObjectSummary[]
  assets: Asset[]
}

export type ShowContentsCounts = {
  playlists: number
  cues: number
  surfaces: number
  assets: number
  /** The workspace's Automation tab covers both action and macro objects. */
  automation: number
}

/** A show the lists returned nothing for holds nothing, which the lists did determine. */
export const EMPTY_CONTENTS: ShowContents = {
  playlists: [],
  cues: [],
  surfaces: [],
  actions: [],
  macros: [],
  assets: [],
}

export function contentsCounts(contents: ShowContents): ShowContentsCounts {
  return {
    playlists: contents.playlists.length,
    cues: contents.cues.length,
    surfaces: contents.surfaces.length,
    assets: contents.assets.length,
    automation: contents.actions.length + contents.macros.length,
  }
}

export function contentsSummary(counts: ShowContentsCounts): string {
  if (counts.playlists === 0 && counts.cues === 0 && counts.surfaces === 0 && counts.assets === 0) return 'Empty'
  return `${counts.playlists} ${counts.playlists === 1 ? 'playlist' : 'playlists'} · ${counts.cues} ${counts.cues === 1 ? 'cue' : 'cues'} · ${counts.surfaces} ${counts.surfaces === 1 ? 'surface' : 'surfaces'} · ${counts.assets} ${counts.assets === 1 ? 'asset' : 'assets'}`
}

/** A cue id resolved to the label its own config object carries, never invented. */
export function cueLabel(cues: readonly ConfigObjectSummary[], cueId: string): string {
  return cues.find((cue) => cue.id === cueId)?.label ?? cueId
}

export type PlaylistRow = {
  id: string
  label: string
  runner: ConfigShowPlaylist['runner']
  runnerLabel: string
  entriesBound: number
  detail: string
}

export function playlistRows(playlists: readonly { id: string; payload: ConfigShowPlaylist }[]): PlaylistRow[] {
  return playlists.map(({ id, payload }) => ({
    id,
    label: payload.name,
    runner: payload.runner,
    runnerLabel: payload.runner === 'fpp' ? 'FPP runner' : 'ShowMesh audio',
    entriesBound: payload.entries.length,
    detail:
      payload.runner === 'fpp'
        ? `FPP owns order and progression · ${payload.entries.length} ${payload.entries.length === 1 ? 'entry' : 'entries'} bound`
        : `ShowMesh owns order and progression · ${payload.entries.length} ${payload.entries.length === 1 ? 'entry' : 'entries'}${payload.showmeshAudio !== undefined && payload.showmeshAudio.repeat === 'all' ? ' · repeat all' : ''}`,
  }))
}

/** The FPP instance's own label for a binding's instanceUuid, or the raw uuid when it names nothing this coordinator has heard from. */
export function fppInstanceLabel(fpp: readonly FPPInstance[], instanceUuid: string): string {
  return fpp.find((instance) => instance.instanceUuid === instanceUuid)?.instanceId ?? instanceUuid
}

export function fppInstanceRoute(fpp: readonly FPPInstance[], instanceUuid: string): string | null {
  const instance = fpp.find((i) => i.instanceUuid === instanceUuid)
  return instance === undefined ? null : `/monitor/fleet/fpp/${instance.instanceId}`
}

export type CueOutputKind = 'render' | 'audio' | 'ltc' | 'announcement'

export const CUE_OUTPUT_CHIP: Record<CueOutputKind, string> = {
  render: 'RND',
  audio: 'AUD',
  ltc: 'LTC',
  announcement: 'ANN',
}

export function cueOutputKinds(outputs: ConfigShowCue['outputs']): CueOutputKind[] {
  const kinds: CueOutputKind[] = []
  if (outputs.render !== undefined) kinds.push('render')
  if (outputs.audio !== undefined) kinds.push('audio')
  if (outputs.ltc !== undefined) kinds.push('ltc')
  if (outputs.announcement !== undefined) kinds.push('announcement')
  return kinds
}

export function describeAnnouncementPolicy(announcement: NonNullable<ConfigShowCue['outputs']['announcement']>): string {
  if (announcement.policy === 'duck') {
    return announcement.duckGainDb === undefined ? 'Duck' : `Duck to ${announcement.duckGainDb} dB`
  }
  if (announcement.policy === 'mix') return 'Mix'
  return 'Interrupt'
}

export type CueGroup = 'announcement' | 'playlist' | 'unreachable'

export type CueRow = {
  id: string
  label: string
  revision: number
  updatedAt: string
  kinds: CueOutputKind[]
  group: CueGroup
  usedByPlaylists: string[]
  announcementPolicy: string | null
  /** This cue's audio asset (audio or announcement output) is not among the show's current assets. */
  assetMissing: boolean
}

/** True when a cue's audio output names a sequence not among this show's current assets. Announcement cues always carry an audio output (ADR-043), so this covers both. */
export function cueAssetMissing(outputs: ConfigShowCue['outputs'], assets: readonly Asset[]): boolean {
  if (outputs.audio === undefined) return false
  return !assets.some((asset) => asset.current && asset.sequence === outputs.audio?.asset)
}

/**
 * A cue with an announcement output is directly activatable and is grouped
 * there regardless of playlist membership (ADR-043: announcements are not
 * playlist entries). Everything else is grouped by whether any playlist in
 * this show binds it to an entry.
 */
export function cueRows(
  cues: readonly ShowCueConfigResponse[],
  playlists: readonly { payload: ConfigShowPlaylist }[],
  assets: readonly Asset[] = [],
): CueRow[] {
  const usedBy = new Map<string, string[]>()
  for (const playlist of playlists) {
    for (const entry of playlist.payload.entries) {
      const names = usedBy.get(entry.cue) ?? []
      if (!names.includes(playlist.payload.name)) names.push(playlist.payload.name)
      usedBy.set(entry.cue, names)
    }
  }

  return cues.map((cue) => {
    const kinds = cueOutputKinds(cue.payload.outputs)
    const names = usedBy.get(cue.id) ?? []
    const group: CueGroup = cue.payload.outputs.announcement !== undefined ? 'announcement' : names.length > 0 ? 'playlist' : 'unreachable'
    return {
      id: cue.id,
      label: cue.payload.name,
      revision: cue.revision,
      updatedAt: cue.updatedAt,
      kinds,
      group,
      usedByPlaylists: names,
      announcementPolicy: cue.payload.outputs.announcement !== undefined ? describeAnnouncementPolicy(cue.payload.outputs.announcement) : null,
      assetMissing: cueAssetMissing(cue.payload.outputs, assets),
    }
  })
}

// ---------------------------------------------------------------------
// Cue activation summary ("On activation this cue will...")
// ---------------------------------------------------------------------

export type CueActivationDraft = {
  render: { sequence: string } | null
  audio: { asset: string; startOffsetMillis: number; target: string } | null
  ltc: { startOffsetMillis: number; target: string } | null
  announcement: { policy: 'duck' | 'mix' | 'interrupt'; duckGainDb: number; fadeMillis: number; target: string } | null
  /** The installation's LTC frame rate, for formatting audio/ltc start offsets as timecode. Null when it has not been read. */
  ltcFps: number | null
}

function joinFacts(parts: readonly string[]): string {
  if (parts.length === 0) return ''
  if (parts.length === 1) return parts[0] ?? ''
  if (parts.length === 2) return `${parts[0]} and ${parts[1]}`
  return `${parts.slice(0, -1).join(', ')}, and ${parts[parts.length - 1]}`
}

/**
 * States only facts the form itself holds: no output picked yet, an
 * unnamed sequence, an unselected asset and an unset target all say so
 * literally rather than being silently skipped.
 */
export function cueActivationSummary(draft: CueActivationDraft): string {
  if (draft.render === null && draft.audio === null && draft.ltc === null && draft.announcement === null) {
    return 'Pick at least one output to see what this cue will do.'
  }

  const parts: string[] = []

  if (draft.render !== null) {
    parts.push(draft.render.sequence.trim() === '' ? 'render an unnamed sequence' : `render sequence ${draft.render.sequence.trim()}`)
  }

  if (draft.audio !== null) {
    const asset = draft.audio.asset.trim() === '' ? 'an unselected asset' : draft.audio.asset
    const offset = draft.audio.startOffsetMillis > 0 ? ` at ${millisToTimecode(draft.audio.startOffsetMillis, draft.ltcFps)}` : ''
    const target = draft.audio.target !== '' ? ` on ${draft.audio.target}` : ''
    parts.push(`play ${asset}${offset}${target}`)
  }

  if (draft.ltc !== null) {
    const target = draft.ltc.target !== '' ? ` on ${draft.ltc.target}` : ''
    parts.push(`emit LTC from ${millisToTimecode(draft.ltc.startOffsetMillis, draft.ltcFps)}${target}`)
  }

  if (draft.announcement !== null) {
    const target = draft.announcement.target !== '' ? ` on ${draft.announcement.target}` : ''
    if (draft.announcement.policy === 'duck') {
      parts.push(`duck the background bed to ${draft.announcement.duckGainDb} dB over ${draft.announcement.fadeMillis} ms${target}`)
    } else if (draft.announcement.policy === 'mix') {
      parts.push(`mix into the background bed over ${draft.announcement.fadeMillis} ms${target}`)
    } else {
      parts.push(`interrupt the background bed${target}`)
    }
  }

  if (draft.render === null) parts.push('leave FPP untouched')

  return `On activation this cue will ${joinFacts(parts)}.`
}

/** Slug rule for a client-derived config object id: lowercase, hyphenated, ascii. */
export function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

export type AudioAssetOption = { id: string; sequence: string; target: string }

/** `assets` filtered current/audio/node: what an audio-asset picker offers. Shared by the night session's background-audio item editor and Shows/Media Playlists' item editor (both bind an item to the same asset identity, ADR-028). */
export function audioAssetOptions(assets: readonly { id: string; current: boolean; mediaType: string; targetKind: string; sequence: string; target: string }[]): AudioAssetOption[] {
  return assets
    .filter((asset) => asset.current && asset.mediaType === 'audio' && asset.targetKind === 'node')
    .map((asset) => ({ id: asset.id, sequence: asset.sequence, target: asset.target }))
}

export type AssetIdentityKey = string

/** The identity ADR-028 defines: (show, sequence, targetKind, target). Never the runtime filename. */
export function assetIdentityKey(asset: Pick<Asset, 'show' | 'sequence' | 'targetKind' | 'target'>): AssetIdentityKey {
  return `${asset.show}\u0000${asset.sequence}\u0000${asset.targetKind}\u0000${asset.target}`
}

export type AssetGroup = {
  show: string
  sequence: string
  mediaType: Asset['mediaType']
  runtimeFilename: string
  current: Asset[]
}

/**
 * The current asset for every distinct (sequence, targetKind, target) this
 * show holds, grouped by logical sequence for display, because one
 * sequence produces a different file per target and xLights gives every
 * one of them the same runtime filename (ADR-028 decision 1). The key
 * includes the show so a caller spanning every show never merges two
 * shows' same-named sequences into one group.
 */
export function assetGroups(assets: readonly Asset[]): AssetGroup[] {
  const byKey = new Map<string, AssetGroup>()
  for (const asset of assets) {
    if (!asset.current) continue
    const key = `${asset.show}\u0000${asset.sequence}\u0000${asset.mediaType}`
    const existing = byKey.get(key)
    if (existing !== undefined) {
      existing.current.push(asset)
      continue
    }
    byKey.set(key, { show: asset.show, sequence: asset.sequence, mediaType: asset.mediaType, runtimeFilename: asset.runtimeFilename, current: [asset] })
  }
  return Array.from(byKey.values()).sort((a, b) => a.show.localeCompare(b.show) || a.sequence.localeCompare(b.sequence))
}

/** Every asset row (current and superseded) sharing one identity, newest first. */
export function assetHistory(assets: readonly Asset[], identity: AssetIdentityKey): Asset[] {
  return assets.filter((a) => assetIdentityKey(a) === identity).sort((a, b) => b.createdAt.localeCompare(a.createdAt))
}

export function targetLabel(asset: Pick<Asset, 'targetKind' | 'target'>): string {
  return asset.targetKind === 'show' ? 'Show-wide' : asset.target
}

/**
 * A newer definition, captured under a different hash for the same instance
 * and playlist name (TRACK-H-H2-SPEC.md §3.6), is the coordinator's own
 * evidence that FPP's definition moved, not a guess from comparing timestamps.
 */
export function newerDefinition(
  definitions: readonly FPPPlaylistDefinitionMetadata[],
  instanceUuid: string,
  playlistName: string,
  boundHash: string,
): FPPPlaylistDefinitionMetadata | null {
  const candidates = definitions.filter((d) => d.instanceUuid === instanceUuid && d.playlistName === playlistName && d.playlistHash !== boundHash)
  if (candidates.length === 0) return null
  return candidates.reduce((latest, entry) => (entry.capturedAt > latest.capturedAt ? entry : latest))
}

// ---------------------------------------------------------------------
// Presentation (show.surface)
// ---------------------------------------------------------------------

/** width * height * channels-per-pixel must equal channelRange.channelCount exactly (PUT /config/show.surface/{id}). */
export function channelsPerPixel(pixelFormat: 'rgb' | 'rgbw'): number {
  return pixelFormat === 'rgbw' ? 4 : 3
}

/** Render capability ids (pkg/capability/id.go) a node needs to be a plausible surface target. */
const RENDER_CAPABILITIES = new Set(['matrix.render', 'display.hdmi', 'transport.ndi.send'])

export function renderCapableNodes(nodes: readonly Node[]): Node[] {
  return nodes.filter((node) => node.capabilities.some((capability) => RENDER_CAPABILITIES.has(capability.id)))
}

export type SurfaceRenderStatus = {
  tone: Tone
  label: string
  detail: string | null
  /** `true` only when nothing has ever reported rendering this surface at all - distinct from a report that is stale or failing. */
  unclaimed: boolean
}

/**
 * A surface's own render evidence, read from its assigned node's `render`
 * observations (never a per-surface fetch - the node already carries this in
 * the live model). No matching observation is a real, distinct state from a
 * stale or failed one: nothing has ever claimed to be rendering this surface.
 */
export function surfaceRenderStatus(nodes: readonly Node[], nodeId: string, surfaceId: string, nowIso: string | null): SurfaceRenderStatus {
  const node = nodes.find((n) => n.nodeId === nodeId)
  if (node === undefined) {
    return { tone: 'unknown', label: 'Node not seen', detail: 'This coordinator has never heard from the assigned node.', unclaimed: true }
  }
  const entries = node.render.filter((entry) => entry.resource.kind === 'surface' && entry.resource.id === surfaceId)
  const stateEntry = entries.find((entry) => entry.signal === 'surface.pipeline.state')
  if (stateEntry === undefined) {
    return {
      tone: 'unknown',
      label: 'Not claimed',
      detail: `${node.label ?? node.nodeId} has never reported rendering this surface. The configuration is stored; nothing confirms a node picked it up.`,
      unclaimed: true,
    }
  }
  const tone = EVIDENCE_TONE[stateEntry.state]
  const age = ageMs(stateEntry.observedAt, nowIso)
  const ageLabel = age === null ? null : `${formatDuration(age)} ago`
  if (stateEntry.state === 'current') {
    const rate = entries.find((entry) => entry.signal === 'surface.frames.rate')
    const rateLabel = typeof rate?.value === 'number' ? ` · ${rate.value} fps` : ''
    return { tone, label: `${String(stateEntry.value ?? 'running')}${rateLabel}`, detail: ageLabel, unclaimed: false }
  }
  return {
    tone,
    label: `${EVIDENCE_LABEL[stateEntry.state]}${ageLabel === null ? '' : ` · ${ageLabel}`}`,
    detail: stateEntry.reason ?? (EVIDENCE_ABSENCE[stateEntry.state] === 'unobserved' ? 'Never reported.' : null),
    unclaimed: false,
  }
}

export type ChannelSpan = { id: string; label: string; start: number; end: number }

/** Sorted spans plus the id set of every span overlapping another - the coordinator refuses this at write time; this only flags it for display. */
export function channelSpans(surfaces: readonly { id: string; label: string; startChannel: number; channelCount: number }[]): {
  spans: ChannelSpan[]
  overlapping: Set<string>
} {
  const spans = surfaces
    .map((s) => ({ id: s.id, label: s.label, start: s.startChannel, end: s.startChannel + s.channelCount - 1 }))
    .sort((a, b) => a.start - b.start)
  const overlapping = new Set<string>()
  for (let i = 1; i < spans.length; i += 1) {
    const prev = spans[i - 1]
    const cur = spans[i]
    if (prev !== undefined && cur !== undefined && cur.start <= prev.end) {
      overlapping.add(prev.id)
      overlapping.add(cur.id)
    }
  }
  return { spans, overlapping }
}

// ---------------------------------------------------------------------
// Automation (show.action, show.macro)
// ---------------------------------------------------------------------

export function bindingsByAction(bindings: readonly ActionBinding[]): Map<string, ActionBinding> {
  return new Map(bindings.map((binding) => [binding.actionId, binding]))
}

export function bindingTone(state: ActionBinding['state'] | undefined): Tone {
  if (state === 'ok') return 'good'
  if (state === 'broken') return 'bad'
  return 'unknown'
}

export function bindingLabel(state: ActionBinding['state'] | undefined): string {
  if (state === undefined) return 'not swept'
  return state
}

const INTEGRATION_LABEL: Record<ConfigShowActionTarget['integration'], string> = {
  fpp: 'FPP',
  mqtt: 'MQTT',
  resolume: 'Resolume',
  audio: 'Audio',
}

export function actionIntegrationLabel(integration: ConfigShowActionTarget['integration']): string {
  return INTEGRATION_LABEL[integration]
}

/** A one-line summary of what an action's target names, in the mock's own vocabulary. */
export function actionTargetSummary(target: ConfigShowActionTarget): string {
  switch (target.integration) {
    case 'fpp':
      return [target.primitive, target.instanceId].filter((v) => v !== undefined && v !== '').join(' · ') || 'fpp primitive not set'
    case 'mqtt':
      if (target.publish !== undefined) {
        return `${target.publish.topic} · qos ${target.publish.qos}${target.publish.retain ? ' · retained' : ''}`
      }
      return target.broker !== undefined ? `broker ${target.broker}` : 'mqtt target not set'
    case 'resolume':
      return target.action ?? 'resolume action not set'
    case 'audio':
      return [target.audioAction, target.audioNodeId].filter((v) => v !== undefined && v !== '').join(' · ') || 'audio target not set'
  }
}

/** The macros (by label) whose steps reference this action, in this show. */
export function macrosUsingAction(macros: readonly { payload: { label: string; steps: readonly ConfigShowMacroStep[] } }[], actionId: string): string[] {
  return macros.filter((macro) => macro.payload.steps.some((step) => step.action === actionId)).map((macro) => macro.payload.label)
}

export type MacroBindingSummary = { ok: number; broken: number; unknown: number; total: number }

export function macroBindingSummary(steps: readonly ConfigShowMacroStep[], bindings: ReadonlyMap<string, ActionBinding>): MacroBindingSummary {
  const summary: MacroBindingSummary = { ok: 0, broken: 0, unknown: 0, total: steps.length }
  for (const step of steps) {
    const state = bindings.get(step.action)?.state
    if (state === 'ok') summary.ok += 1
    else if (state === 'broken') summary.broken += 1
    else summary.unknown += 1
  }
  return summary
}

/** The most recently created run for a macro, from the live model's bounded run window - never a per-macro fetch. */
export function lastRunForMacro(macroRuns: readonly MacroRunSummary[], macroId: string): MacroRunSummary | null {
  return macroRuns
    .filter((run) => run.macroObjectId === macroId)
    .reduce<MacroRunSummary | null>((latest, run) => (latest === null || run.createdAt > latest.createdAt ? run : latest), null)
}

/** One macro's own label plus the 1-based step numbers that reference an action, for an action-edit pane's "Used by" line. */
export type MacroUsage = { label: string; stepNumbers: number[] }

export function macroUsagesForAction(macros: readonly { payload: { label: string; steps: readonly ConfigShowMacroStep[] } }[], actionId: string): MacroUsage[] {
  const usages: MacroUsage[] = []
  for (const macro of macros) {
    const stepNumbers = macro.payload.steps.reduce<number[]>((acc, step, index) => {
      if (step.action === actionId) acc.push(index + 1)
      return acc
    }, [])
    if (stepNumbers.length > 0) usages.push({ label: macro.payload.label, stepNumbers })
  }
  return usages
}

/** The coordinator refuses a safetyClass that disagrees with the target's own registered class, so three of the four integrations derive it; mqtt registers none and stays a live choice. */
export type SafetyClass = 'none' | 'blackout' | 'stop' | 'powerOff'

/** FPPCommandDecision11ClassForAction (internal/coordinator/api/fppcommand_dispatch.go): only stopPlaylist and stopPlaylistGracefully are "stop", every other fpp primitive is "none". */
const FPP_STOP_PRIMITIVES = new Set(['stopPlaylist', 'stopPlaylistGracefully'])

export function fppDerivedSafetyClass(primitive: string): SafetyClass {
  return FPP_STOP_PRIMITIVES.has(primitive) ? 'stop' : 'none'
}

/** resolumeActionDeclaredSafetyClass (internal/coordinator/config/showaction.go): blackout and clearLayer are "blackout", the other five resolume actions are "none". */
const RESOLUME_BLACKOUT_ACTIONS = new Set(['blackout', 'clearLayer'])

export function resolumeDerivedSafetyClass(action: string): SafetyClass {
  return RESOLUME_BLACKOUT_ACTIONS.has(action) ? 'blackout' : 'none'
}

/** audioActionDeclaredSafetyClass (internal/coordinator/config/showaction.go): session.stop and session.clear are "stop", output.mute is "blackout", every other audio action is "none". */
const AUDIO_STOP_ACTIONS = new Set(['audio.session.stop', 'audio.session.clear'])

export function audioDerivedSafetyClass(audioAction: string): SafetyClass {
  if (AUDIO_STOP_ACTIONS.has(audioAction)) return 'stop'
  if (audioAction === 'audio.output.mute') return 'blackout'
  return 'none'
}

/** A content hash, shortened the one way every screen shows it. */
export function hashLabel(contentHash: string): string {
  const hex = contentHash.startsWith('sha256:') ? contentHash.slice('sha256:'.length) : contentHash
  if (hex.length <= 8) return hex
  return `${hex.slice(0, 4)}…${hex.slice(-2)}`
}

/** A reported byte count, shown the one way every screen shows it. Derived, never rounded away: the exact figure stays in the title. */
export function formatBytes(bytes: number): string {
  if (bytes < 1000) return `${bytes} B`
  const units = ['kB', 'MB', 'GB', 'TB']
  let value = bytes / 1000
  let unit = 0
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000
    unit += 1
  }
  return `${value.toFixed(1)} ${units[unit]}`
}
