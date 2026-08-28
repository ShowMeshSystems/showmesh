import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Configuration } from './Configuration'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'
import { ApiError } from '../api/errors'

// Track G seam G-3 (ADR-039): the fpp.mqtt section's own behavior,
// mirroring Configuration.resolume.test.tsx's identical shape for the
// resolume.instances section (load, 404-as-not-configured, error, save,
// 409) plus this kind's own credential-specific case: the password field
// must never be pre-filled, and leaving it blank must PUT no "password"
// key at all.
const {
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  getResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
  getFPPMQTTConfig,
  putFPPMQTTConfig,
  getFPPMQTTConfigRevisions,
} = vi.hoisted(() => ({
  getFPPEndpointsConfig: vi.fn(),
  getFPPEndpointsConfigRevisions: vi.fn(),
  getResolumeInstancesConfig: vi.fn(),
  getResolumeInstancesConfigRevisions: vi.fn(),
  getFPPMQTTConfig: vi.fn(),
  putFPPMQTTConfig: vi.fn(),
  getFPPMQTTConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getFPPEndpointsConfig,
    getFPPEndpointsConfigRevisions,
    getResolumeInstancesConfig,
    getResolumeInstancesConfigRevisions,
    getFPPMQTTConfig,
    putFPPMQTTConfig,
    getFPPMQTTConfigRevisions,
  }
})

const emptyFPPRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'fpp.endpoints', revisions: [] }
const emptyResolumeRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'resolume.instances', revisions: [] }
const emptyFPPMQTTRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'fpp.mqtt', revisions: [] }

const activeFPPMQTTConfig = {
  serverTime: '2026-08-17T00:00:00Z',
  kind: 'fpp.mqtt',
  revision: 1,
  payload: {
    brokerURL: 'tcp://10.0.1.5:1883',
    username: 'showmesh',
    topicPrefix: 'falcon/player',
    hosts: { 'player-01': 'FPP-Player' },
    passwordSet: true,
  },
  updatedAt: '2026-08-17T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  restartRequired: false,
  restartRequiredReason:
    'this change is already in effect: the FPP MQTT collector follows this configuration within about ten seconds. No restart is needed.',
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read', 'config:write'],
  scopesState: 'current',
})

function renderConfiguration(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <Configuration />
    </ModelContext.Provider>,
  )
}

async function fppMQTTSection() {
  // Queried as a heading, not plain text: the section index at the top of
  // the page also links to a "FPP MQTT" text node.
  const heading = await screen.findByRole('heading', { name: 'FPP MQTT' })
  return heading.closest('section')!
}

beforeEach(() => {
  getFPPEndpointsConfig.mockRejectedValue(
    new ApiError('no fpp.endpoints configuration has been created yet; PUT one to create it', 404,
      'https://showmesh.dev/problems/resource-not-found'),
  )
  getFPPEndpointsConfigRevisions.mockResolvedValue(emptyFPPRevisions)
  getResolumeInstancesConfig.mockRejectedValue(
    new ApiError('no resolume.instances configuration has been created yet; PUT one to create it', 404,
      'https://showmesh.dev/problems/resource-not-found'),
  )
  getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
})

afterEach(() => {
  cleanup()
  getFPPEndpointsConfig.mockReset()
  getFPPEndpointsConfigRevisions.mockReset()
  getResolumeInstancesConfig.mockReset()
  getResolumeInstancesConfigRevisions.mockReset()
  getFPPMQTTConfig.mockReset()
  putFPPMQTTConfig.mockReset()
  getFPPMQTTConfigRevisions.mockReset()
})

describe('Configuration: FPP MQTT section', () => {
  it('fetches and renders the active configuration, including passwordSet, never the password', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    await waitFor(() => expect(getFPPMQTTConfig).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('tcp://10.0.1.5:1883')).toBeInTheDocument()
    expect(screen.getByDisplayValue('showmesh')).toBeInTheDocument()
    expect(screen.getByDisplayValue('falcon/player')).toBeInTheDocument()
    expect(screen.getByDisplayValue('player-01')).toBeInTheDocument()
    expect(screen.getByDisplayValue('FPP-Player')).toBeInTheDocument()
    expect(screen.getByText(/currently set/i)).toBeInTheDocument()

    const passwordInput = screen.getByLabelText(/^password/i) as HTMLInputElement
    expect(passwordInput.value).toBe('')
  })

  // Defect follow-up to #116 (controls-before-prose): the broker password
  // note is safety-relevant, not ordinary explanation -- a blank password
  // field means "unchanged" only because of this sentence, and an operator
  // who saves without reading past the button cannot otherwise tell a
  // blank field apart from a cleared one. It must sit at or above the
  // password control, not below Save where #116 moved every other note.
  it('renders the broker password note before the password field, not below Save', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await fppMQTTSection()
    const passwordInput = within(section).getByLabelText(/^password/i)
    const note = within(section).getByText(/leave the password field blank to keep it unchanged/i)
    const saveButton = within(section).getByRole('button', { name: /save fpp mqtt configuration/i })

    expect(
      note.compareDocumentPosition(passwordInput) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(note.compareDocumentPosition(saveButton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it("renders the coordinator's own 404 reason with an empty editor, not as an error", async () => {
    getFPPMQTTConfig.mockRejectedValue(
      new ApiError('no fpp.mqtt configuration has been created yet; PUT one to create it', 404,
        'https://showmesh.dev/problems/resource-not-found'),
    )
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await fppMQTTSection()
    expect(await within(section).findByText(/configuration has been created yet/i)).toBeInTheDocument()
    expect(within(section).queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a real fetch failure as an error, distinct from 404', async () => {
    getFPPMQTTConfig.mockRejectedValue(new ApiError('the store is unreachable', 500))
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await fppMQTTSection()
    expect(await within(section).findByRole('alert')).toHaveTextContent(/unreachable/i)
  })

  // The load-bearing test: ADR-039 decision 7's own "GET then PUT must
  // not erase the password" rule, proven at the UI layer. Editing a field
  // that has nothing to do with the password and saving must not send a
  // "password" key at all.
  it('leaving the password field blank omits "password" from the PUT request entirely', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    putFPPMQTTConfig.mockResolvedValue({ ...activeFPPMQTTConfig, revision: 2 })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    const topicPrefixInput = await screen.findByDisplayValue('falcon/player')
    await user.clear(topicPrefixInput)
    await user.type(topicPrefixInput, 'custom/prefix')

    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    await waitFor(() =>
      expect(putFPPMQTTConfig).toHaveBeenCalledWith({
        brokerURL: 'tcp://10.0.1.5:1883',
        username: 'showmesh',
        topicPrefix: 'custom/prefix',
        hosts: { 'player-01': 'FPP-Player' },
      }),
    )
    const sentRequest = putFPPMQTTConfig.mock.calls.at(0)?.at(0)
    expect(sentRequest).not.toHaveProperty('password')
  })

  it('typing a new password sends it as an explicit "password" key', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    putFPPMQTTConfig.mockResolvedValue({ ...activeFPPMQTTConfig, revision: 2 })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('tcp://10.0.1.5:1883')
    const passwordInput = screen.getByLabelText(/^password/i)
    await user.type(passwordInput, 'new-secret')

    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    await waitFor(() =>
      expect(putFPPMQTTConfig).toHaveBeenCalledWith(expect.objectContaining({ password: 'new-secret' })),
    )
  })

  it('checking "clear the stored password" sends an explicit null', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    putFPPMQTTConfig.mockResolvedValue({
      ...activeFPPMQTTConfig, revision: 2,
      payload: { ...activeFPPMQTTConfig.payload, passwordSet: false },
    })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('tcp://10.0.1.5:1883')
    await user.click(screen.getByRole('checkbox', { name: /clear the stored password/i }))
    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    await waitFor(() =>
      expect(putFPPMQTTConfig).toHaveBeenCalledWith(expect.objectContaining({ password: null })),
    )
  })

  // A host row with only one of (instance id, HostName) filled cannot be
  // represented in the map-shaped payload — the old behavior silently
  // dropped it, the save "succeeded", and the reload erased the
  // operator's half-entered row. The save is blocked instead, naming the
  // row; the server still owns validation of what is sent (ADR-030).
  it('blocks the save with an inline error naming a half-filled host row, sending no PUT', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('tcp://10.0.1.5:1883')
    await user.click(screen.getByRole('button', { name: /add host/i }))
    await user.type(screen.getByLabelText('Host 2 instance id'), 'player-02')

    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    const section = await fppMQTTSection()
    const alert = await within(section).findByRole('alert')
    expect(alert).toHaveTextContent(/host 2/i)
    expect(alert).toHaveTextContent(/both an instance id and a hostname/i)
    expect(putFPPMQTTConfig).not.toHaveBeenCalled()
    // The half-entered row is still on screen, not erased.
    expect(screen.getByDisplayValue('player-02')).toBeInTheDocument()
  })

  it('blocks the save with an inline error on duplicate trimmed instance ids, never collapsing last-wins', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('tcp://10.0.1.5:1883')
    await user.click(screen.getByRole('button', { name: /add host/i }))
    // Trims to the same id as the existing 'player-01' row.
    await user.type(screen.getByLabelText('Host 2 instance id'), ' player-01 ')
    await user.type(screen.getByLabelText('Host 2 HostName'), 'FPP-Other')

    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    const section = await fppMQTTSection()
    const alert = await within(section).findByRole('alert')
    expect(alert).toHaveTextContent(/host 2/i)
    expect(alert).toHaveTextContent(/player-01/)
    expect(alert).toHaveTextContent(/unique/i)
    expect(putFPPMQTTConfig).not.toHaveBeenCalled()
  })

  it('skips a fully blank host row (no operator input) rather than refusing the save', async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    putFPPMQTTConfig.mockResolvedValue({ ...activeFPPMQTTConfig, revision: 2 })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('tcp://10.0.1.5:1883')
    await user.click(screen.getByRole('button', { name: /add host/i }))

    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    await waitFor(() =>
      expect(putFPPMQTTConfig).toHaveBeenCalledWith({
        brokerURL: 'tcp://10.0.1.5:1883',
        username: 'showmesh',
        topicPrefix: 'falcon/player',
        hosts: { 'player-01': 'FPP-Player' },
      }),
    )
  })

  it("renders the coordinator's 409 refusal (SHOWMESH_FPP_MQTT_BROKER_URL still set) as an actionable message", async () => {
    getFPPMQTTConfig.mockResolvedValue(activeFPPMQTTConfig)
    getFPPMQTTConfigRevisions.mockResolvedValue(emptyFPPMQTTRevisions)
    putFPPMQTTConfig.mockRejectedValue(
      new ApiError(
        'This write is refused because SHOWMESH_FPP_MQTT_BROKER_URL is still set in this coordinator\'s ' +
          'environment — accepting it now would conflict with that variable on the next restart. Remove ' +
          'SHOWMESH_FPP_MQTT_BROKER_URL, SHOWMESH_FPP_MQTT_USERNAME, SHOWMESH_FPP_MQTT_PASSWORD, ' +
          'SHOWMESH_FPP_MQTT_TOPIC_PREFIX, and SHOWMESH_FPP_MQTT_HOSTS and restart this coordinator once, then retry.',
        409,
        'https://showmesh.dev/problems/conflict',
      ),
    )
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('tcp://10.0.1.5:1883')
    await user.click(screen.getByRole('button', { name: /save fpp mqtt configuration/i }))

    const section = await fppMQTTSection()
    const alert = await within(section).findByRole('alert')
    expect(alert).toHaveTextContent(/SHOWMESH_FPP_MQTT_BROKER_URL/)
    expect(alert).toHaveTextContent(/restart this coordinator once/i)
  })
})
