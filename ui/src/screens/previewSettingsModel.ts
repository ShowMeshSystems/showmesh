export type CameraDraft = { id: string; name: string; enabled: boolean; url: string }

export const GATEWAY_URL_ERROR = 'The gateway address must start with http:// or https://. Fix it and save again.'
export const CAMERA_URL_ERROR = 'The camera address must start with rtsp:// or rtsps:// and name a host. Correct it and save again.'

export function gatewayUrlError(url: string): string | null {
  const trimmed = url.trim()
  if (trimmed === '') return null
  return /^https?:\/\/\S+$/i.test(trimmed) ? null : GATEWAY_URL_ERROR
}

/** The write body for the gateway: an empty token field leaves the stored token alone. */
export function gatewayRequest(url: string, token: string): { url: string; token?: string } {
  return token === '' ? { url: url.trim() } : { url: url.trim(), token }
}

/**
 * The first problem with a camera form, or null. A new camera needs an address;
 * an edit may leave it blank to keep the stored one.
 */
export function cameraDraftError(draft: CameraDraft, mode: 'new' | 'edit', takenIds: readonly string[]): string | null {
  const id = draft.id.trim()
  if (mode === 'new') {
    if (id === '') return 'Give the camera an id. It cannot be changed later.'
    if (/[\s/]/.test(id)) return 'The camera id cannot contain spaces or slashes. Use letters, numbers and hyphens.'
    if (takenIds.includes(id)) return `A camera with the id ${id} already exists. Choose another id.`
  }
  if (draft.name.trim() === '') return 'Give the camera a name. The Dashboard tile shows it.'
  const url = draft.url.trim()
  if (url === '') return mode === 'new' ? 'Enter the camera address. It is stored and never shown again.' : null
  return /^rtsps?:\/\/[^\s#/?]+[^\s#]*$/i.test(url) ? null : CAMERA_URL_ERROR
}

/** The write body for a camera: an empty address field leaves the stored address alone. */
export function cameraRequest(draft: CameraDraft): { name: string; enabled: boolean; url?: string } {
  const url = draft.url.trim()
  const base = { name: draft.name.trim(), enabled: draft.enabled }
  return url === '' ? base : { ...base, url }
}

export type NDIDraft = { id: string; label: string; ndiName: string; address: string; enabled: boolean }

export const NDI_ADDRESS_ERROR = 'The NDI address must be host:port, such as 10.0.20.15:5961.'

/** The first problem with an NDI source form, or null. The address is optional. */
export function ndiDraftError(draft: NDIDraft, mode: 'new' | 'edit', takenIds: readonly string[]): string | null {
  const id = draft.id.trim()
  if (mode === 'new') {
    if (id === '') return 'Give the NDI source an id. It cannot be changed later.'
    if (/[\s/]/.test(id)) return 'The NDI source id cannot contain spaces or slashes. Use letters, numbers and hyphens.'
    if (takenIds.includes(id)) return `An NDI source with the id ${id} already exists. Choose another id.`
  }
  const name = draft.ndiName.trim()
  if (name === '') return 'Enter the NDI name the sender announces, such as ARENA (Composition).'
  if (/["\\]/.test(name)) return 'The NDI name has a quote or backslash. Rename the NDI source.'
  const address = draft.address.trim()
  if (address === '') return null
  const match = /^(\[[^\]\s]+\]|[^\s"\\:[\]]+):(\d{1,5})$/.exec(address)
  const port = match === null ? 0 : Number(match[2])
  return port >= 1 && port <= 65535 ? null : NDI_ADDRESS_ERROR
}

/** The write body for an NDI source: every field, so a cleared address is cleared. */
export function ndiRequest(draft: NDIDraft): { label: string; ndiName: string; address: string; enabled: boolean } {
  return { label: draft.label.trim(), ndiName: draft.ndiName.trim(), address: draft.address.trim(), enabled: draft.enabled }
}
