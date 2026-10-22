import { describe, expect, it } from 'vitest'
import type { IntegrationBroker } from '../api'
import { brokerDraftDirty, brokerDraftError, brokerRequest, draftFrom, emptyDraft, topicRule } from './mqttBrokersModel'

const saved: IntegrationBroker = {
  id: 'ha', url: 'tcp://10.0.0.5:1883', username: 'showmesh', passwordSet: true, builtIn: false, topicPrefix: '',
  revision: 2, updatedAt: '2026-10-22T18:00:00Z', createdByPrincipalName: 'erbartos', source: 'api',
}

describe('mqttBrokersModel', () => {
  it('refuses bad ids and addresses on a new broker', () => {
    const draft = { ...emptyDraft(), id: 'ha', url: 'tcp://10.0.0.5:1883' }
    expect(brokerDraftError(draft, [])).toBeNull()
    expect(brokerDraftError(draft, ['ha'])).toMatch(/already exists/)
    expect(brokerDraftError({ ...draft, id: 'built-in' }, [])).toMatch(/built-in broker/)
    expect(brokerDraftError({ ...draft, id: 'Bad_Id' }, [])).toMatch(/lowercase/)
    expect(brokerDraftError({ ...draft, url: '10.0.0.5:1883' }, [])).toMatch(/tcp:\/\//)
    expect(brokerDraftError({ ...draft, url: 'tcp://u:p@10.0.0.5:1883' }, [])).toMatch(/login/)
  })

  it('sends only what changed, and never clears a password by leaving it empty', () => {
    const draft = draftFrom(saved)
    expect(brokerDraftDirty(draft)).toBe(false)
    expect(brokerRequest({ ...draft, username: 'other' })).toEqual({ username: 'other' })
    expect(brokerRequest({ ...draft, password: 'new' })).toEqual({ password: 'new' })
    expect(brokerRequest({ ...draft, clearPassword: true })).toEqual({ password: null })
    expect(brokerRequest({ ...emptyDraft(), id: 'x', url: ' tcp://h:1883 ' })).toEqual({ url: 'tcp://h:1883', username: '' })
  })

  it('describes the topic rule', () => {
    expect(topicRule(saved)).toBe('Any topic')
    expect(topicRule({ ...saved, topicPrefix: 'showmesh-actions/' })).toBe('Topics must start with showmesh-actions/')
  })
})
