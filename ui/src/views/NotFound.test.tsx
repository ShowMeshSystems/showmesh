import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { NotFound } from './NotFound'

afterEach(cleanup)

describe('NotFound', () => {
  it('renders a way back to the dashboard', () => {
    render(
      <MemoryRouter>
        <NotFound />
      </MemoryRouter>,
    )
    expect(screen.getByText('Page not found')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'Return to the dashboard' })
    expect(link).toHaveAttribute('href', '/')
  })
})
