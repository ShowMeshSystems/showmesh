import type { Evidence, Node } from '../api'

/** Operator label of each audio setting, shared by the Settings audio form and the substitution wording. */
export const AUDIO_SETTING_LABELS = {
  fadeCurve: { name: 'Default fade curve' },
  fadeDuration: { name: 'Default fade duration', unit: 'ms' },
  maxBackgroundGain: { name: 'Max background gain', unit: 'dB' },
  duckTargetGain: { name: 'Duck target gain', unit: 'dB' },
  duckFadeDuration: { name: 'Duck fade duration', unit: 'ms' },
  duckRestoreFadeDuration: { name: 'Duck restore fade duration', unit: 'ms' },
  ltcFrameRate: { name: 'LTC frame rate' },
  ltcDefaultStartOffset: { name: 'LTC default start offset' },
  multisyncStartLead: { name: 'MultiSync start lead', unit: 'ms' },
} as const satisfies Record<string, { name: string; unit?: string }>

type SettingKey = keyof typeof AUDIO_SETTING_LABELS

/** The form's field label: the name, with its unit when the value has one. */
export function audioSettingFieldLabel(key: SettingKey): string {
  const label: { name: string; unit?: string } = AUDIO_SETTING_LABELS[key]
  return label.unit === undefined ? label.name : `${label.name} (${label.unit})`
}

const NODE_FIELD_KEYS: Record<string, SettingKey> = {
  DefaultFadeCurve: 'fadeCurve',
  DefaultFadeDurationMs: 'fadeDuration',
  DefaultMaxBackgroundGain: 'maxBackgroundGain',
  DuckTargetGain: 'duckTargetGain',
  DuckFadeDurationMs: 'duckFadeDuration',
  DuckRestoreFadeDurationMs: 'duckRestoreFadeDuration',
  LTCFrameRate: 'ltcFrameRate',
  LTCDefaultStartOffset: 'ltcDefaultStartOffset',
  MultisyncStartLeadMs: 'multisyncStartLead',
}

export function audioSettingName(nodeField: string): string {
  const key = NODE_FIELD_KEYS[nodeField.trim()]
  return key === undefined ? 'an audio setting' : AUDIO_SETTING_LABELS[key].name
}

export type AudioSettingSubstitution = {
  fields: string[]
  /** The node's own words, which may name internals, so it is never the headline. */
  reason: string | null
}

const STATE_SIGNAL = 'node.audio.settings.state'
const FIELDS_SIGNAL = 'node.audio.settings.substituted_fields'
const REASON_SIGNAL = 'node.audio.settings.reason'

export const SUBSTITUTION_DETAIL_SIGNALS: readonly string[] = [FIELDS_SIGNAL, REASON_SIGNAL]

export function isSubstitutedState(entry: Evidence): boolean {
  return entry.signal === STATE_SIGNAL && entry.state === 'current' && entry.value === 'substituted'
}

/** What a node currently reports it refused, or null while it applies every audio setting as given. */
export function audioSettingSubstitution(node: Pick<Node, 'audio'>): AudioSettingSubstitution | null {
  if (!node.audio.some(isSubstitutedState)) return null
  const valueOf = (signal: string) => {
    const value = node.audio.find((entry) => entry.signal === signal)?.value
    return typeof value === 'string' && value.trim() !== '' ? value.trim() : null
  }
  const named = (valueOf(FIELDS_SIGNAL) ?? '').split(';').filter((part) => part.trim() !== '')
  const fields = [...new Set(named.map(audioSettingName))]
  return { fields: fields.length === 0 ? ['an audio setting'] : fields, reason: valueOf(REASON_SIGNAL) }
}

function joinNames(names: readonly string[]): string {
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

/** Fact first, then the action. */
export function audioSettingSubstitutionSentence(nodeLabel: string, substitution: AudioSettingSubstitution): string {
  return `${nodeLabel} could not apply ${joinNames(substitution.fields)} and is using its own value. Change the setting to one the node accepts, then save again.`
}
