import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Access } from './Access'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Track G seam G-5: the Access page's token panel. The load-bearing case
// here is that issuing or revoking a token actually re-fetches the token
// list — a prior version's reload() only set tokens to null and relied on
// an effect keyed on [principalID] alone, so the panel showed "Loading
// tokens…" forever after any issue/revoke.
const { listPrincipals, listPrincipalTokens, issuePrincipalToken, revokePrincipalToken } = vi.hoisted(() => ({
  listPrincipals: vi.fn(),
  listPrincipalTokens: vi.fn(),
  issuePrincipalToken: vi.fn(),
  revokePrincipalToken: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    listPrincipals,
    listPrincipalTokens,
    issuePrincipalToken,
    revokePrincipalToken,
  }
})

const machinePrincipal = {
  id: 'p-2',
  name: 'fpp-host',
  kind: 'machine' as const,
  role: 'scheduler' as const,
  disabled: false,
  hasPassword: false,
  reserved: false,
  createdAt: '2026-08-17T00:00:00Z',
}

const tokenA = {
  id: 't-1',
  principalId: 'p-2',
  hint: 'smt_a1b2…',
  label: 'first token',
  createdAt: '2026-08-17T00:00:00Z',
  expiresAt: null,
  lastUsedAt: null,
}

const tokenB = {
  id: 't-2',
  principalId: 'p-2',
  hint: 'smt_c3d4…',
  label: 'second token',
  createdAt: '2026-08-17T00:05:00Z',
  expiresAt: null,
  lastUsedAt: null,
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['principal:read', 'principal:write'],
  scopesState: 'current',
})

function renderAccess(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <Access />
    </ModelContext.Provider>,
  )
}

afterEach(() => {
  cleanup()
  listPrincipals.mockReset()
  listPrincipalTokens.mockReset()
  issuePrincipalToken.mockReset()
  revokePrincipalToken.mockReset()
})

describe('Access: tokens panel', () => {
  it('issuing a token re-fetches and re-renders the token list', async () => {
    listPrincipals.mockResolvedValue({ serverTime: '2026-08-17T00:00:00Z', principals: [machinePrincipal] })
    listPrincipalTokens
      .mockResolvedValueOnce({ serverTime: '2026-08-17T00:00:00Z', tokens: [tokenA] })
      .mockResolvedValueOnce({ serverTime: '2026-08-17T00:05:00Z', tokens: [tokenA, tokenB] })
    issuePrincipalToken.mockResolvedValue({
      serverTime: '2026-08-17T00:05:00Z',
      token: tokenB,
      value: 'smt_plaintext-shown-once',
    })
    const user = userEvent.setup()
    renderAccess(makeModel({ session: adminSession }))

    await user.click(await screen.findByRole('button', { name: 'Tokens' }))
    expect(await screen.findByText('smt_a1b2…')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /issue token/i }))

    // The one-time plaintext renders, and the list itself re-renders with
    // the freshly fetched data rather than sticking at "Loading tokens…".
    expect(await screen.findByText('smt_plaintext-shown-once')).toBeInTheDocument()
    expect(await screen.findByText('smt_c3d4…')).toBeInTheDocument()
    expect(screen.getByText('smt_a1b2…')).toBeInTheDocument()
    expect(screen.queryByText(/loading tokens/i)).not.toBeInTheDocument()
    await waitFor(() => expect(listPrincipalTokens).toHaveBeenCalledTimes(2))
  })

  it('revoking a token re-fetches and renders the fresh (empty) list', async () => {
    listPrincipals.mockResolvedValue({ serverTime: '2026-08-17T00:00:00Z', principals: [machinePrincipal] })
    listPrincipalTokens
      .mockResolvedValueOnce({ serverTime: '2026-08-17T00:00:00Z', tokens: [tokenA] })
      .mockResolvedValueOnce({ serverTime: '2026-08-17T00:05:00Z', tokens: [] })
    revokePrincipalToken.mockResolvedValue(undefined)
    const user = userEvent.setup()
    renderAccess(makeModel({ session: adminSession }))

    await user.click(await screen.findByRole('button', { name: 'Tokens' }))
    const row = (await screen.findByText('smt_a1b2…')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /revoke/i }))

    expect(await screen.findByText('(no tokens)')).toBeInTheDocument()
    expect(screen.queryByText(/loading tokens/i)).not.toBeInTheDocument()
    expect(revokePrincipalToken).toHaveBeenCalledWith('p-2', 't-1')
    await waitFor(() => expect(listPrincipalTokens).toHaveBeenCalledTimes(2))
  })
})
