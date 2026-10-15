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
