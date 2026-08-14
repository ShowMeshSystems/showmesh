import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowActionDetail } from './ShowActionDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const { putShowAction } = vi.hoisted(() => ({ putShowAction: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, putShowAction }
})

afterEach(() => {
  cleanup()
  putShowAction.mockReset()
})

function renderNewAction(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/actions/new']}>
        <ShowActionDetail isNew />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

describe('ShowActionDetail (new action authoring)', () => {
  // STEP-9-SPEC.md section 5.3: "safetyClass is required and is never
  // defaulted in the UI." The select's first option is a disabled
  // placeholder, so nothing is pre-selected — an operator who never
  // touches this field cannot submit a "none" they never chose.
  it('never pre-selects a safety class — the select starts on the disabled placeholder', () => {
    renderNewAction(makeModel({ session: adminSession }))
    const select = screen.getByLabelText('Safety class') as HTMLSelectElement
    expect(select.value).toBe('')
  })

  it('refuses to submit with no safety class chosen, even with every other field filled in', async () => {
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.type(screen.getByLabelText('FPP instance id'), 'fpp-main')
    await user.selectOptions(screen.getByLabelText('Primitive'), 'startPlaylist')
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/is required and is never defaulted/)
    expect(putShowAction).not.toHaveBeenCalled()
  })

  it('requires a broker with no default for an MQTT action', async () => {
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'powerOff')
    await user.selectOptions(screen.getByLabelText('Integration'), 'mqtt')
    await user.type(screen.getByLabelText(/Publish topic/), 'home/projectors/set')
    await user.selectOptions(screen.getByLabelText(/QoS/), '1')
    // Broker left blank on purpose.
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(await screen.findByText(/Broker is required/)).toBeVisible()
    expect(putShowAction).not.toHaveBeenCalled()
  })

  it('submits a valid MQTT action with an explicit safety class and broker', async () => {
    putShowAction.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      kind: 'show.action',
      id: 'projectors-on',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Projectors on',
        description: '',
        safetyClass: 'powerOff',
        target: { integration: 'mqtt', broker: 'home-automation', publish: { topic: 't', payload: '', qos: 1, retain: false }, expect: { kind: 'none' } },
      },
      updatedAt: '2026-08-14T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'powerOff')
    await user.selectOptions(screen.getByLabelText('Integration'), 'mqtt')
    await user.type(screen.getByLabelText(/Broker/), 'home-automation')
    await user.type(screen.getByLabelText(/Publish topic/), 'home/projectors/set')
    await user.selectOptions(screen.getByLabelText(/QoS/), '1')
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(putShowAction).toHaveBeenCalledWith(
      'projectors-on',
      expect.objectContaining({
        safetyClass: 'powerOff',
        target: expect.objectContaining({ integration: 'mqtt', broker: 'home-automation' }),
      }),
    )
  })

  // This task's finding 4: the server (decodeMQTTExpect,
  // internal/coordinator/config/showaction.go) requires "match"'s value
  // KEY present but explicitly allows it to be an empty string — an
  // empty match target is a real, valid value, not "no value". This form
  // used to refuse to submit with the match value left blank, which was
  // stricter than the server it must only mirror (ADR-030) and made a
  // stored revision with an empty match value impossible to re-save.
  it('accepts an empty "match" value — the server allows it, so this form must not refuse it', async () => {
    putShowAction.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      kind: 'show.action',
      id: 'projectors-on',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Projectors on',
        description: '',
        safetyClass: 'powerOff',
        target: {
          integration: 'mqtt',
          broker: 'home-automation',
          publish: { topic: 't', payload: '', qos: 1, retain: false },
          expect: { kind: 'match', topic: 'home/projectors/state', value: '', deadlineSeconds: 5 },
        },
      },
      updatedAt: '2026-08-14T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'powerOff')
    await user.selectOptions(screen.getByLabelText('Integration'), 'mqtt')
    await user.type(screen.getByLabelText(/Broker/), 'home-automation')
    await user.type(screen.getByLabelText(/Publish topic/), 'home/projectors/set')
    await user.selectOptions(screen.getByLabelText(/QoS/), '1')
    await user.selectOptions(screen.getByLabelText('Expected response'), 'match')
    await user.type(screen.getByLabelText(/Response topic/), 'home/projectors/state')
    await user.type(screen.getByLabelText(/seconds/i), '5')
    // "Exact value to match" left blank on purpose — the case under test.
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(screen.queryByRole('alert')).toBeNull()
    expect(putShowAction).toHaveBeenCalledWith(
      'projectors-on',
      expect.objectContaining({
        target: expect.objectContaining({
          expect: { kind: 'match', topic: 'home/projectors/state', value: '', deadlineSeconds: 5 },
        }),
      }),
    )
  })
})
