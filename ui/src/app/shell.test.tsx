import { readdirSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import type { Model, SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from './ModelContext'
import { Layout } from './Layout'
import { NotFound } from '../screens/NotFound'

function session(overrides: Partial<SessionResponse>): SessionResponse {
  return {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: false,
    principal: null,
    session: null,
    credentialForm: null,
    scopes: [],
    scopesState: 'not_applicable',
    bootstrapRequired: false,
    ...overrides,
  }
}

function renderShell(model: Partial<Model>, route = '/') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[route]}>
        <Routes>
          <Route path="/" element={<Layout />}>
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('app shell', () => {
  afterEach(cleanup)

  it('renders the seven rail destinations and no eighth', () => {
    renderShell({})
    const rail = screen.getByRole('navigation', { name: 'Primary' })
    const labels = [...rail.querySelectorAll('a')].map((link) => link.textContent)
    expect(labels).toEqual([
      'Dashboard',
      'Show Night',
      'Live Control',
      'Shows',
      'Assets',
      'Monitor',
      'Settings',
    ])
  })

  it('shows no rail badge before anything has been read', () => {
    renderShell({})
    const rail = screen.getByRole('navigation', { name: 'Primary' })
    expect(rail.querySelectorAll('.sm-rail__badge')).toHaveLength(0)
  })

  it('renders signed out as a band with the rail still present', () => {
    renderShell({ session: session({ authenticated: false }) })
    expect(screen.getByText('Signed out on this device')).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
  })

  it('lets bootstrap outrank signed out', () => {
    renderShell({ session: session({ authenticated: false, bootstrapRequired: true }) })
    expect(screen.getByText('No administrator exists on this coordinator')).toBeInTheDocument()
    expect(screen.queryByText('Signed out on this device')).not.toBeInTheDocument()
  })

  it('shows neither band while the first session response is outstanding', () => {
    renderShell({ session: null })
    expect(screen.queryByText('Signed out on this device')).not.toBeInTheDocument()
    expect(screen.queryByText('No administrator exists on this coordinator')).not.toBeInTheDocument()
  })

  it('says nothing is playing rather than inventing a title', () => {
    renderShell({})
    expect(screen.getByText('Nothing playing')).toBeInTheDocument()
  })

  it('maps an old address to its new home instead of redirecting', () => {
    renderShell({}, '/events')
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('No page at this address')
    expect(screen.getByRole('link', { name: 'Go to Monitor › Activity' })).toBeInTheDocument()
  })

  it('shows no clock skew strip when the skew has not been observed', () => {
    renderShell({ clockSkewMs: null })
    expect(screen.queryByText('Clock skew', { exact: false })).not.toBeInTheDocument()
  })

  it('shows no clock skew strip when the skew is under the threshold', () => {
    renderShell({ clockSkewMs: 4_999 })
    expect(screen.queryByText('Clock skew', { exact: false })).not.toBeInTheDocument()
  })

  it('shows the strip when the skew is exactly at the threshold', () => {
    renderShell({ clockSkewMs: 5_000 })
    expect(screen.getByRole('status')).toHaveTextContent('Clock skew')
  })

  it('names the coordinator as ahead when clockSkewMs is positive', () => {
    renderShell({ clockSkewMs: 90_000 })
    const strip = screen.getByRole('status')
    expect(strip).toHaveTextContent('Clock skew')
    expect(strip).toHaveTextContent('behind the coordinator')
  })

  it('names the browser as ahead when clockSkewMs is negative', () => {
    renderShell({ clockSkewMs: -90_000 })
    const strip = screen.getByRole('status')
    expect(strip).toHaveTextContent('Clock skew')
    expect(strip).toHaveTextContent('ahead of the coordinator')
  })
})

/* The old design is gone only when it is unreachable and a test says so. */
function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = path.join(dir, name)
    if (statSync(full).isDirectory()) return name === 'generated' ? [] : sourceFiles(full)
    return /\.(tsx?|css)$/.test(name) ? [full] : []
  })
}

describe('the old design system', () => {
  const src = path.join(__dirname, '..')
  const files = sourceFiles(src)

  it('has no views, components or styles directory left', () => {
    const dirs = readdirSync(src).filter((name) => statSync(path.join(src, name)).isDirectory())
    expect(dirs).not.toContain('views')
    expect(dirs).not.toContain('components')
    expect(dirs).not.toContain('styles')
  })

  it('is imported by no file: every stylesheet import points into the kit', () => {
    const offenders = files.filter((file) => {
      const source = readFileSync(file, 'utf8')
      return [...source.matchAll(/(?:@import\s+|from\s+)['"]([^'"]+\.css)['"]/g)].some(
        (match) => !(match[1] ?? '').includes('kit/styles') && !(match[1] ?? '').startsWith('./'),
      )
    })
    expect(offenders).toEqual([])
  })
})
