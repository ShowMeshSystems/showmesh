import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ContentDeliverySettings } from './SettingsPages'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeSessionResponse } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderSettings(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <ContentDeliverySettings />
    </ModelContext.Provider>,
  )
}

describe('ConfigEditorPage permission evidence', () => {
  it.each([
    ['Loading permissions', makeModel({ session: null }), 'shared-state-block--loading'],
    ['Setup required', makeModel({ session: makeSessionResponse({ bootstrapRequired: true }) }), 'shared-state-block--unavailable'],
    ['Signed out', makeModel({ session: makeSessionResponse() }), 'shared-state-block--unavailable'],
    ['Stale permission evidence', makeModel({ session: makeAuthenticatedSession({ scopes: ['config:write'] }), sessionFetchFailed: true }), 'shared-state-block--stale'],
    ['Stale permission evidence', makeModel({ session: makeAuthenticatedSession({ scopes: ['config:write'], scopesState: 'unknown' }) }), 'shared-state-block--stale'],
    ['Insufficient permission', makeModel({ session: makeAuthenticatedSession({ scopes: [] }) }), 'shared-state-block--unavailable'],
  ])('renders %s as a literal status label', (title, model, stateClass) => {
    renderSettings(model)

    expect(screen.getByRole('heading', { name: title })).toBeVisible()
    expect(document.querySelector(`.${stateClass}`)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Asset store settings' })).not.toBeInTheDocument()
    if (title === 'Setup required') {
      expect(screen.queryByRole('heading', { name: 'Signed out' })).not.toBeInTheDocument()
    }
  })
})
