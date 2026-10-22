import type { IntegrationBroker, IntegrationBrokerPutRequest } from '../api'

/** One editable broker card. `loaded` is null for a broker not yet saved. */
export type BrokerDraft = {
  id: string
  url: string
  username: string
  password: string
  clearPassword: boolean
  loaded: IntegrationBroker | null
}

const BROKER_ID = /^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$/
const BROKER_URL = /^(tcp|ssl|tls|mqtt|mqtts|ws|wss):\/\/[^/@\s]+/

export function draftFrom(broker: IntegrationBroker): BrokerDraft {
  return { id: broker.id, url: broker.url, username: broker.username, password: '', clearPassword: false, loaded: broker }
}

export function emptyDraft(): BrokerDraft {
  return { id: '', url: '', username: '', password: '', clearPassword: false, loaded: null }
}

/** The first thing stopping the draft from saving, or null. `taken` lists ids already in use. */
export function brokerDraftError(draft: BrokerDraft, taken: readonly string[]): string | null {
  const id = draft.id.trim()
  if (draft.loaded === null) {
    if (id === 'built-in') return 'The built-in broker is already listed. Pick another id.'
    if (!BROKER_ID.test(id)) return 'The id must be lowercase letters, digits or hyphens, and cannot start or end with a hyphen.'
    if (taken.includes(id)) return `A broker with the id ${id} already exists. Pick another id.`
  }
  const url = draft.url.trim()
  if (url.includes('@')) return 'The address cannot include a login. Put the username and password in their own fields.'
  if (!BROKER_URL.test(url)) return 'The address must look like tcp://10.0.0.5:1883.'
  return null
}

export function brokerDraftDirty(draft: BrokerDraft): boolean {
  if (draft.loaded === null) return true
  return (
    draft.url.trim() !== draft.loaded.url ||
    draft.username.trim() !== draft.loaded.username ||
    draft.password !== '' ||
    draft.clearPassword
  )
}

/** Sends only what changed, so a saved password is never cleared by accident. */
export function brokerRequest(draft: BrokerDraft): IntegrationBrokerPutRequest {
  const request: IntegrationBrokerPutRequest = {}
  if (draft.loaded === null || draft.url.trim() !== draft.loaded.url) request.url = draft.url.trim()
  if (draft.loaded === null || draft.username.trim() !== draft.loaded.username) request.username = draft.username.trim()
  if (draft.clearPassword) request.password = null
  else if (draft.password !== '') request.password = draft.password
  return request
}

/** The topics line shown for a broker. */
export function topicRule(broker: IntegrationBroker): string {
  return broker.topicPrefix === '' ? 'Any topic' : `Topics must start with ${broker.topicPrefix}`
}
