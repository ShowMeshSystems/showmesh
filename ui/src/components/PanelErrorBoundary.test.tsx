import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PanelErrorBoundary } from './PanelErrorBoundary'

function Throws(): never {
  throw new Error('synthetic panel failure')
}

describe('PanelErrorBoundary', () => {
  // React logs the caught error to the console itself (in addition to
  // this component's own componentDidCatch), which is expected noise for
  // this specific test and not something to assert on.
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })
  afterEach(() => {
    vi.restoreAllMocks()
    // See EvidenceValue.test.tsx for why this is registered explicitly
    // here rather than relying on @testing-library/react's automatic
    // afterEach(cleanup), which needs `test.globals: true`.
    cleanup()
  })

  it('renders a fallback instead of the thrown panel, without losing the rest of the page', () => {
    render(
      <div>
        <PanelErrorBoundary panelLabel="Broken panel">
          <Throws />
        </PanelErrorBoundary>
        <PanelErrorBoundary panelLabel="Healthy sibling panel">
          <p>sibling content still rendered</p>
        </PanelErrorBoundary>
      </div>,
    )

    expect(screen.getByText('Broken panel failed to render')).toBeInTheDocument()
    expect(screen.getByText('sibling content still rendered')).toBeInTheDocument()
  })

  it('renders children normally when nothing throws', () => {
    render(
      <PanelErrorBoundary panelLabel="Fine panel">
        <p>ordinary content</p>
      </PanelErrorBoundary>,
    )
    expect(screen.getByText('ordinary content')).toBeInTheDocument()
    expect(screen.queryByText(/failed to render/)).not.toBeInTheDocument()
  })
})
