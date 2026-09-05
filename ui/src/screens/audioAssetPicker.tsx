/**
 * The audio-asset picker one background-audio item binds against: a
 * `Select` over the show's current, node-targeted audio assets, keyed by
 * asset id but reporting back the (sequence, target) pair the wire shape
 * actually stores. Shared by the night session's inline background-audio
 * item editor and Shows/Media Playlists' item editor (both bind an item to
 * the same asset identity, ADR-028).
 */
import { Select } from '../kit'
import type { AudioAssetOption } from './showsModel'

export function AudioAssetPicker({
  assets,
  sequence,
  target,
  onPick,
  ...selectProps
}: {
  assets: readonly AudioAssetOption[]
  sequence: string
  target: string
  onPick: (asset: AudioAssetOption | null) => void
} & Omit<Parameters<typeof Select>[0], 'value' | 'onChange' | 'onSelect' | 'children'>) {
  const selectedId = assets.find((asset) => asset.sequence === sequence && asset.target === target)?.id ?? ''
  return (
    <Select
      {...selectProps}
      value={selectedId}
      onChange={(e) => onPick(assets.find((asset) => asset.id === e.target.value) ?? null)}
    >
      <option value="">Select an audio asset…</option>
      {assets.map((asset) => (
        <option key={asset.id} value={asset.id}>
          {asset.sequence} · {asset.target}
        </option>
      ))}
    </Select>
  )
}
