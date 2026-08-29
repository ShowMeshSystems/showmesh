import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AssetUpload } from './AssetUpload'
import type { Asset, AssetResponse } from '../api'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// This file is the UI half of ADR-028 decision 10's own coverage: the
// store and API rollback paths have store- and API-level tests
// (assets_test.go, both packages); this proves the ONE remaining client
// surface — the Operator UI's upload control — states a rollback as its
// own state rather than folding it into the reloaded asset list, matching
// AssetUpload.tsx's own doc comment.

const writeSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['asset:write'],
})

function renderUpload(onUploaded: (asset: Asset, rolledBack: boolean) => void, model: Model = makeModel({ session: writeSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <AssetUpload onUploaded={onUploaded} />
    </ModelContext.Provider>,
  )
}

const { uploadAsset } = vi.hoisted(() => ({ uploadAsset: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, uploadAsset }
})

afterEach(() => {
  cleanup()
  uploadAsset.mockReset()
})

function makeFseqFile(name = 'Thriller.fseq'): File {
  return new File(['fseq bytes'], name, { type: 'application/octet-stream' })
}

const baseAsset: Asset = {
  id: 'asset-1',
  show: 'halloween-2026',
  sequence: 'opening',
  targetKind: 'show',
  target: '',
  mediaType: 'fseq',
  contentHash: 'sha256:abc',
  runtimeFilename: 'Thriller.fseq',
  sizeBytes: 4096,
  createdAt: '2026-08-18T20:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  supersededAt: null,
  current: true,
}

// Fills the show/sequence/mediaType/targetKind fields and selects a file,
// stopping short of clicking upload — every test below shares this setup
// and only "show"-targets, to stay clear of the node-target select's own
// branching (that path belongs to AssetUpload.tsx's other tests, not this
// file's rollback-specific coverage).
async function fillFormAndChooseFile(user: ReturnType<typeof userEvent.setup>, file = makeFseqFile()) {
  await user.type(screen.getByLabelText(/^show$/i), 'halloween-2026')
  await user.type(screen.getByLabelText(/^sequence$/i), 'opening')
  await user.selectOptions(screen.getByLabelText(/media type/i), 'fseq')
  await user.selectOptions(screen.getByLabelText(/^target$/i), 'show')
  await user.upload(screen.getByLabelText(/^file$/i), file)
}

describe('AssetUpload', () => {
  it('renders the rollback banner when the coordinator reports rolledBack=true', async () => {
    uploadAsset.mockResolvedValue({
      serverTime: '2026-08-18T20:00:00Z',
      asset: baseAsset,
      rolledBack: true,
    } satisfies AssetResponse)
    const onUploaded = vi.fn()
    const user = userEvent.setup()
    renderUpload(onUploaded)

    await fillFormAndChooseFile(user)
    await user.click(screen.getByRole('button', { name: /upload asset/i }))

    const banner = await screen.findByText(/rollback/i)
    expect(banner).toHaveTextContent(/asset-1/)
    expect(banner.className).toContain('asset-upload__rollback-banner')
  })

  it('does NOT render the rollback banner on an ordinary upload', async () => {
    uploadAsset.mockResolvedValue({
      serverTime: '2026-08-18T20:00:00Z',
      asset: baseAsset,
      rolledBack: false,
    } satisfies AssetResponse)
    const onUploaded = vi.fn()
    const user = userEvent.setup()
    renderUpload(onUploaded)

    await fillFormAndChooseFile(user)
    await user.click(screen.getByRole('button', { name: /upload asset/i }))

    await waitFor(() => expect(onUploaded).toHaveBeenCalledTimes(1))
    expect(screen.queryByText(/rollback/i)).not.toBeInTheDocument()
  })

  it('passes rolledBack through to onUploaded for both outcomes', async () => {
    const onUploaded = vi.fn()
    const user = userEvent.setup()
    renderUpload(onUploaded)

    uploadAsset.mockResolvedValueOnce({
      serverTime: '2026-08-18T20:00:00Z',
      asset: baseAsset,
      rolledBack: true,
    } satisfies AssetResponse)
    await fillFormAndChooseFile(user)
    await user.click(screen.getByRole('button', { name: /upload asset/i }))
    await waitFor(() => expect(onUploaded).toHaveBeenCalledTimes(1))
    expect(onUploaded).toHaveBeenNthCalledWith(1, baseAsset, true)

    uploadAsset.mockResolvedValueOnce({
      serverTime: '2026-08-18T20:01:00Z',
      asset: { ...baseAsset, id: 'asset-2' },
      rolledBack: false,
    } satisfies AssetResponse)
    await fillFormAndChooseFile(user, makeFseqFile('Other.fseq'))
    await user.click(screen.getByRole('button', { name: /upload asset/i }))
    await waitFor(() => expect(onUploaded).toHaveBeenCalledTimes(2))
    expect(onUploaded).toHaveBeenNthCalledWith(2, { ...baseAsset, id: 'asset-2' }, false)
  })
})
