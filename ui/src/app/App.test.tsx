import { cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RouteTitle, ScrollToTop } from './App'

// Operator-reported: clicking the show-mode badge (a hash link to
// /config#show-mode) from a long, scrolled page left the page at its old
// scroll offset, and clicking it while already on /config did nothing at
// all. `ScrollToTop`'s own comment used to claim "the browser handles the
// anchor itself" for a hash change, which is false under this app's
// `<BrowserRouter>` (a non-data router): there is no `ScrollRestoration`
// mounted anywhere, and `history.pushState` neither scrolls nor fires
// `hashchange` on its own. jsdom does no layout, so scroll POSITION is not
// observable here -- these tests assert what is: that the handler calls
// `scrollIntoView` on the hash target for a hash navigation, does nothing
// for a `POP` (browser back/forward, which must keep its own restored
// position), and falls back to `window.scrollTo(0, 0)` for a plain,
// non-hash navigation.
const { useLocationMock, useNavigationTypeMock } = vi.hoisted(() => ({
  useLocationMock: vi.fn(),
  useNavigationTypeMock: vi.fn(),
}))

vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return {
    ...actual,
    useLocation: useLocationMock,
    useNavigationType: useNavigationTypeMock,
  }
})

afterEach(() => {
  cleanup()
  useLocationMock.mockReset()
  useNavigationTypeMock.mockReset()
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('ScrollToTop', () => {
  it('scrolls the hash target into view on a hash navigation, even on the same page', async () => {
    const target = document.createElement('div')
    target.id = 'show-mode'
    document.body.appendChild(target)
    const scrollIntoView = vi.fn()
    target.scrollIntoView = scrollIntoView

    useLocationMock.mockReturnValue({ pathname: '/config', hash: '#show-mode' })
    useNavigationTypeMock.mockReturnValue('PUSH')
    const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})

    render(<ScrollToTop />)

    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledTimes(1))
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('waits for the target to render before scrolling to it, without a fixed delay', async () => {
    useLocationMock.mockReturnValue({ pathname: '/config', hash: '#show-mode' })
    useNavigationTypeMock.mockReturnValue('PUSH')

    render(<ScrollToTop />)

    // The target does not exist yet at render time -- this mirrors a hash
    // link landing on the same navigation that renders its own target
    // section, where the section is not in the DOM on ScrollToTop's first
    // effect run.
    const target = document.createElement('div')
    target.id = 'show-mode'
    const scrollIntoView = vi.fn()
    target.scrollIntoView = scrollIntoView
    document.body.appendChild(target)

    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledTimes(1))
  })

  it('does nothing on a POP navigation, preserving the browser-restored position', () => {
    const target = document.createElement('div')
    target.id = 'show-mode'
    document.body.appendChild(target)
    const scrollIntoView = vi.fn()
    target.scrollIntoView = scrollIntoView

    useLocationMock.mockReturnValue({ pathname: '/config', hash: '#show-mode' })
    useNavigationTypeMock.mockReturnValue('POP')
    const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})

    render(<ScrollToTop />)

    expect(scrollIntoView).not.toHaveBeenCalled()
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('scrolls to the top on a plain navigation with no hash', () => {
    useLocationMock.mockReturnValue({ pathname: '/nodes', hash: '' })
    useNavigationTypeMock.mockReturnValue('PUSH')
    const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})

    render(<ScrollToTop />)

    expect(scrollTo).toHaveBeenCalledWith(0, 0)
  })
})

describe('RouteTitle', () => {
  it('gives deep-linked routes a useful document title without changing routing', () => {
    useLocationMock.mockReturnValue({ pathname: '/config/show/holiday', hash: '' })

    render(<RouteTitle />)

    expect(document.title).toBe('Shows · ShowMesh Operator')
  })

  it('uses a stable product title for legacy routes without a dedicated label', () => {
    useLocationMock.mockReturnValue({ pathname: '/audit', hash: '' })

    render(<RouteTitle />)

    expect(document.title).toBe('ShowMesh Operator')
  })
})
