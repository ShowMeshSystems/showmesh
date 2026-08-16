import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResolumeCompositionUpload } from './ResolumeCompositionUpload'
import { ApiError, ForbiddenError, UnauthorizedError } from '../api/errors'
import type { ResolumeCompositionSummary, ResolumeCompositionUploadResponse } from '../api'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeSessionResponse } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Review finding 5: this control lost its config:write scope gate when it
// moved from Configuration.tsx (which only ever mounted it inside that
// scope's own gate) to ResolumeView.tsx (which renders with no session at
// all). Every test below now renders inside a ModelContext carrying an
// authenticated, config:write-holding session by default, matching what
// this component could always assume before the move — the "no scope"
// case gets its own describe block further down.
const writeSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function renderUpload(model: Model = makeModel({ session: writeSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <ResolumeCompositionUpload />
    </ModelContext.Provider>,
  )
}

// Mirrors Configuration.test.tsx's own pattern exactly (that file's own
// header comment explains why): the two API functions this component
// calls are mocked here to isolate ITS OWN branching (which state renders
// what, and which calls fire when, including the double-submit guard),
// not the network transport itself. resolumeCompositionUpload.test.ts is
// where the "real request, not a stub" half of the task spec's test list
// (item 2: multipart/form-data, part named "file", real bytes) is
// actually proven, against a real node:http server — this file proves
// the component calls that real function with the right File and renders
// what it reports, which is the wiring this component itself owns.
const { getResolumeComposition, uploadResolumeComposition } = vi.hoisted(() => ({
  getResolumeComposition: vi.fn(),
  uploadResolumeComposition: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getResolumeComposition, uploadResolumeComposition }
})

afterEach(() => {
  cleanup()
  getResolumeComposition.mockReset()
  uploadResolumeComposition.mockReset()
})

const summary: ResolumeCompositionSummary = {
  name: 'Christmas 25',
  sourceFilename: 'Christmas 25.avc',
  contentHash: 'sha256:abc123',
  sizeBytes: 407_552,
  writtenBy: { product: 'Arena', major: 7, minor: 23, micro: 2, revision: 0 },
  canvas: { width: 1920, height: 1080 },
  decks: [
    { id: 'deck-1', name: 'Main', nameGenerated: false, closed: false, clipCount: 30 },
    { id: 'deck-2', name: 'Backup', nameGenerated: false, closed: true, clipCount: 12 },
  ],
  layerCount: 18,
  layerGroupCount: 3,
  columnCount: 30,
  clipCount: 42,
  persistentClipCount: 4,
}

const storedResponse = {
  serverTime: '2026-08-14T00:00:00Z',
  revision: 3,
  activatedAt: '2026-08-14T00:00:00Z',
  composition: summary,
  decks: summary.decks,
  layerGroups: [],
  layers: [],
  columns: [],
  clips: [],
  persistentClips: [],
}

const uploadResponse: ResolumeCompositionUploadResponse = {
  serverTime: '2026-08-14T01:00:00Z',
  revision: 4,
  activatedAt: '2026-08-14T01:00:00Z',
  composition: summary,
}

function makeAvcFile(name = 'Christmas 25.avc'): File {
  return new File(['<Composition/>'], name, { type: 'application/octet-stream' })
}

describe('ResolumeCompositionUpload', () => {
  it('renders the empty state and says what to do when nothing has been uploaded yet', async () => {
    getResolumeComposition.mockRejectedValue(
      new ApiError('no Resolume composition has been uploaded yet; upload a composition file to create one', 404),
    )
    renderUpload()

    expect(await screen.findByText(/no Resolume composition has been uploaded yet/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/composition file/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /upload composition/i })).toBeInTheDocument()
  })

  it('renders a 403 on the initial read as a permissions state, not a connectivity or server failure', async () => {
    getResolumeComposition.mockRejectedValue(new ForbiddenError('this principal’s role does not include "config:write"'))
    renderUpload()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/config:write/)
    expect(alert.className).toContain('panel--warning')
    expect(alert.className).not.toContain('panel--error')
  })

  it('renders a 401 on the initial read distinctly from a 403 or a transport failure', async () => {
    getResolumeComposition.mockRejectedValue(new UnauthorizedError(false, 'no valid credential was presented'))
    renderUpload()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/no valid credential/i)
  })

  it('renders the stored composition: name, Arena version, and every deck name', async () => {
    getResolumeComposition.mockResolvedValue(storedResponse)
    renderUpload()

    expect(await screen.findByText('Christmas 25')).toBeInTheDocument()
    expect(screen.getByText(/Arena 7\.23\.2\.0/)).toBeInTheDocument()
    expect(screen.getByText(/Main/)).toBeInTheDocument()
    expect(screen.getByText(/Backup/)).toBeInTheDocument()
  })

  it('choosing a file and uploading calls uploadResolumeComposition with the selected File', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    uploadResolumeComposition.mockResolvedValue(uploadResponse)
    const user = userEvent.setup()
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })

    const file = makeAvcFile()
    const input = screen.getByLabelText(/composition file/i) as HTMLInputElement
    await user.upload(input, file)
    expect(input.files?.[0]).toBe(file)

    await user.click(screen.getByRole('button', { name: /upload composition/i }))

    await waitFor(() => expect(uploadResolumeComposition).toHaveBeenCalledTimes(1))
    expect(uploadResolumeComposition.mock.calls[0]?.[0]).toBe(file)
    expect(typeof uploadResolumeComposition.mock.calls[0]?.[1]).toBe('function')
  })

  it('renders the newly uploaded composition on success, replacing the empty state', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    uploadResolumeComposition.mockResolvedValue(uploadResponse)
    const user = userEvent.setup()
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })

    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile())
    await user.click(screen.getByRole('button', { name: /upload composition/i }))

    expect(await screen.findByText('Christmas 25')).toBeInTheDocument()
    expect(screen.queryByText(/no Resolume composition has been uploaded yet/i)).not.toBeInTheDocument()
  })

  it('a rejected upload renders the server reason and leaves the previously stored composition unchanged', async () => {
    getResolumeComposition.mockResolvedValue(storedResponse)
    uploadResolumeComposition.mockRejectedValue(
      new ApiError('the uploaded file is not a valid Resolume composition (.avc) file', 400),
    )
    const user = userEvent.setup()
    renderUpload()
    await screen.findByText('Christmas 25')

    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile('not-a-composition.avc'))
    await user.click(screen.getByRole('button', { name: /upload composition/i }))

    expect(await screen.findByText(/not a valid Resolume composition/i)).toBeInTheDocument()
    // The previously stored composition is untouched — still "Christmas
    // 25" at revision 3, not replaced or blanked by the failed attempt.
    expect(screen.getByText('Christmas 25')).toBeInTheDocument()
    expect(screen.getByText('Stored revision').closest('dl')).toHaveTextContent('3, activated')
  })

  // Regression guard for the defect the owner found by loading the real
  // Operator UI: this endpoint's problem detail used to be built from
  // pkg/resolumecomp's own sentinel-error text, so a rejected upload
  // rendered "...: resolumecomp: root element is not <Composition>: found
  // <NotAComposition>" — a Go package name — directly to the operator.
  // The fix moved the translation server-side
  // (internal/coordinator/api/resolumecomposition.go now maps the
  // sentinel to an operator sentence before it ever reaches the wire), so
  // this component's own job is unchanged: render server.message verbatim
  // (fppCommandCopyGuard.test.ts, this file's sibling, is what stops a
  // literal Go package name from ever being hardcoded INTO this
  // component). This test proves the two halves compose end to end: fed
  // the real post-fix server sentence, nothing this component does to
  // that string introduces a package name of its own, and what renders is
  // exactly the sentence an operator can act on.
  it('a rejected upload renders no Go package name, only the server’s operator-facing reason', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    uploadResolumeComposition.mockRejectedValue(
      new ApiError('This does not look like a Resolume composition file: its root element is not <Composition>.', 400),
    )
    const user = userEvent.setup()
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })

    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile('not-a-composition.avc'))
    await user.click(screen.getByRole('button', { name: /upload composition/i }))

    const alert = await screen.findByText(/does not look like a Resolume composition/i)
    expect(alert.textContent).not.toMatch(/resolumecomp/i)
  })

  it('distinguishes a 413 (too large), a 403, and a 401 upload failure from each other and from a rejected file', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    const user = userEvent.setup()

    uploadResolumeComposition.mockRejectedValue(new ApiError('the uploaded file exceeds this coordinator’s upload limit', 413))
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })
    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile())
    await user.click(screen.getByRole('button', { name: /upload composition/i }))
    expect(await screen.findByText(/exceeds this coordinator/i)).toBeInTheDocument()
    cleanup()

    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    uploadResolumeComposition.mockRejectedValue(new ForbiddenError('this principal’s role does not include "config:write"'))
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })
    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile())
    await user.click(screen.getByRole('button', { name: /upload composition/i }))
    const forbiddenAlert = await screen.findByText(/config:write/i)
    expect(forbiddenAlert.className).toContain('panel--warning')
    cleanup()

    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    uploadResolumeComposition.mockRejectedValue(new UnauthorizedError(true, 'the supplied credential was rejected'))
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })
    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile())
    await user.click(screen.getByRole('button', { name: /upload composition/i }))
    expect(await screen.findByText(/credential was rejected/i)).toBeInTheDocument()
  })

  it('renders a transport failure distinctly, never as though the coordinator refused the request', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    uploadResolumeComposition.mockRejectedValue(new ApiError('network error uploading to /config/resolume/composition'))
    const user = userEvent.setup()
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })

    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile())
    await user.click(screen.getByRole('button', { name: /upload composition/i }))

    const alert = await screen.findByText(/network error/i)
    // Never the permissions-warning treatment — a transport failure keeps
    // the plain error styling, distinguishing it from classifyUploadError's
    // forbidden/unauthorized branches.
    expect(alert.className).not.toContain('panel--warning')
  })

  // Same defect class as Configuration.tsx's own savingRef guard (that
  // component's own comment: `fireEvent.click` synchronously flushes
  // React's state update via Testing Library's own act() wrapper, so two
  // SEPARATE fireEvent calls never reproduce the race — both clicks must
  // land before the first one's state update commits, which requires
  // wrapping both in one outer act()).
  it('does not submit a second upload while one is already in flight', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    let resolveUpload: (value: ResolumeCompositionUploadResponse) => void = () => {}
    uploadResolumeComposition.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveUpload = resolve
        }),
    )
    renderUpload()
    await screen.findByRole('button', { name: /upload composition/i })

    const input = screen.getByLabelText(/composition file/i) as HTMLInputElement
    const file = makeAvcFile()
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    fireEvent.change(input)

    const button = screen.getByRole('button', { name: /upload composition|uploading/i })
    act(() => {
      fireEvent.click(button)
      fireEvent.click(button)
    })

    resolveUpload(uploadResponse)
    await waitFor(() => expect(screen.getByText('Christmas 25')).toBeInTheDocument())

    expect(uploadResolumeComposition).toHaveBeenCalledTimes(1)
  })
})

// Review finding 5: this control used to be mounted only inside
// Configuration.tsx's own config:write gate; on ResolumeView.tsx, which
// renders with no session at all, that protection was silently lost — a
// plain <button> submitted a real upload for a session that would only
// learn it lacked the scope from the resulting 403. ADR-024 decision 12
// requires a disabled control with a stated reason instead.
describe('ResolumeCompositionUpload (config:write scope gate)', () => {
  it('disables the upload button with a stated reason when there is no session at all', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    renderUpload(makeModel({ session: null }))

    const button = await screen.findByRole('button', { name: /upload composition/i })
    expect(button).toBeDisabled()
    expect(screen.getByText(/coordinator what this device may do/i)).toBeInTheDocument()
  })

  it('disables the upload button with a stated reason for a signed-out session', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    renderUpload(makeModel({ session: makeSessionResponse() }))

    const button = await screen.findByRole('button', { name: /upload composition/i })
    expect(button).toBeDisabled()
    expect(screen.getByText(/sign in to use this control/i)).toBeInTheDocument()
  })

  it('disables the upload button for a session that lacks config:write, and never dispatches on click', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    const user = userEvent.setup()
    renderUpload(makeModel({ session: makeAuthenticatedSession({ scopes: [] }) }))

    const button = await screen.findByRole('button', { name: /upload composition/i })
    expect(button).toBeDisabled()
    expect(screen.getByText(/does not include "config:write"/i)).toBeInTheDocument()

    await user.upload(screen.getByLabelText(/composition file/i), makeAvcFile())
    await user.click(button)
    expect(uploadResolumeComposition).not.toHaveBeenCalled()
  })
})
