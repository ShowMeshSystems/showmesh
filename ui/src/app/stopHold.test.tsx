import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Model } from '../api'
import { initialModel } from '../api/domain'
import { StopHoldShellBanner } from './StopHoldShellBanner'

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getCurrentNightSession: () => new Promise(() => {}) }
})

const BANNER = 'The show is stopped. Press Resume on Live Control to start the show playlist from its first song.'

function renderBanner(model: Partial<Model>, authenticated = true) {
  return render(<StopHoldShellBanner model={{ ...initialModel(), ...model }} authenticated={authenticated} />)
}

describe('StopHoldShellBanner', () => {
  afterEach(cleanup)
  const held = { state: 'live', stopHold: { reason: 'The show was stopped with Stop.', at: '2026-09-23T20:05:00Z', principal: 'bench' } } as never

  it('shows the exact alert while Stop holds the night', () => {
    renderBanner({ nightSession: held })
    expect(screen.getByRole('alert')).toHaveTextContent(BANNER)
    expect(screen.getByRole('region', { name: 'Show stopped' })).toHaveTextContent(/by bench/)
  })

  it('shows nothing when no hold stands', () => {
    renderBanner({ nightSession: { state: 'live' } as never })
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument()
  })

  it('shows nothing to a signed-out device', () => {
    renderBanner({ nightSession: held }, false)
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument()
  })
})
