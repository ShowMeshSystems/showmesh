import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeModel } from './test-support/fixtures'
import App from './App'

const { useModelMock, submitTokenMock } = vi.hoisted(() => ({
  useModelMock: vi.fn(),
  submitTokenMock: vi.fn(),
}))

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, useModel: useModelMock, submitToken: submitTokenMock }
})

vi.mock('./Layout', async () => {
  const { Outlet } = await import('react-router-dom')
  return { Layout: () => <Outlet /> }
})

vi.mock('../views/Configuration', () => ({
  Configuration: () => <div data-testid="settings-directory">Settings directory</div>,
}))

vi.mock('../views/SettingsPages', () => ({
  ConnectionsSettings: () => <div data-testid="settings-connections">Connections</div>,
  ContentDeliverySettings: () => <div data-testid="settings-content-delivery">Content delivery</div>,
  RenderRecoverySettings: () => <div data-testid="settings-render-recovery">Render recovery</div>,
  AccessSettings: () => <div data-testid="settings-access">Access</div>,
  AppearanceSettings: () => <div data-testid="settings-appearance">Appearance</div>,
  AudioSettingsDirectory: () => <div data-testid="settings-audio">Audio</div>,
  ModeSettings: () => <div data-testid="settings-mode">Mode</div>,
}))

afterEach(() => {
  cleanup()
  useModelMock.mockReset()
  submitTokenMock.mockReset()
  vi.restoreAllMocks()
})

function renderAt(path: string) {
  window.history.pushState({}, '', path)
  useModelMock.mockReturnValue(makeModel())
  return render(<App />)
}

describe('Settings routes', () => {
  it.each([
    ['/config/connections', 'settings-connections'],
    ['/config/content-delivery', 'settings-content-delivery'],
    ['/config/render-recovery', 'settings-render-recovery'],
    ['/config/access', 'settings-access'],
    ['/config/appearance', 'settings-appearance'],
    ['/config/audio', 'settings-audio'],
    ['/config/mode', 'settings-mode'],
  ])('matches the direct Settings path %s in the application router', (path, page) => {
    renderAt(path)

    expect(screen.getByTestId(page)).toBeInTheDocument()
  })

  it.each([
    ['#fpp-endpoints', 'settings-connections'],
    ['#resolume-instances', 'settings-connections'],
    ['#fpp-mqtt', 'settings-connections'],
    ['#assets-settings', 'settings-content-delivery'],
    ['#render-settings', 'settings-render-recovery'],
    ['#show-mode', 'settings-mode'],
  ])('redirects the legacy /config%s fragment through the application router', async (fragment, page) => {
    renderAt(`/config${fragment}`)

    await waitFor(() => expect(screen.getByTestId(page)).toBeInTheDocument())
  })
})
