/**
 * The audio-track picker one background-audio item binds against: one
 * entry per track in the show, saved with no node so the bed's own nodes
 * decide where it plays. An item already pinned to one node's upload keeps
 * that entry so it still shows. Shared by the night session's inline
 * background-audio editor and Shows/Media Playlists' item editor.
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
  const visible = assets.filter((asset) => asset.target === '' || (asset.sequence === sequence && asset.target === target))
  const selectedId = visible.find((asset) => asset.sequence === sequence && asset.target === target)?.id ?? ''
  return (
    <Select
      {...selectProps}
      value={selectedId}
      onChange={(e) => onPick(visible.find((asset) => asset.id === e.target.value) ?? null)}
    >
      <option value="">Select an audio track…</option>
      {visible.map((asset) => (
        <option key={asset.id} value={asset.id}>
          {asset.target === '' ? asset.sequence : `${asset.sequence} · ${asset.target} only`}
        </option>
      ))}
    </Select>
  )
}
