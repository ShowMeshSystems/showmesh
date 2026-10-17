import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { ConfigRevisionMeta } from '../api'
import { RevisionHistory } from './RevisionHistory'

const meta = (revision: number, active: boolean): ConfigRevisionMeta =>
  ({ revision, createdAt: '2026-08-30T18:00:00Z', createdByPrincipalId: 'p', createdByPrincipalName: 'erbartos', source: 'api', note: '', active }) as ConfigRevisionMeta

const payloads: Record<number, unknown> = { 1: { label: 'One', port: 80 }, 2: { label: 'Two', port: 80, extra: true } }

function renderList() {
  return render(
    <RevisionHistory
      mode="list"
      reloadKey="k"
      fetch={() => Promise.resolve({ serverTime: '', kind: 'x', revisions: [meta(2, true), meta(1, false)] } as never)}
      fetchRevision={(n) => Promise.resolve({ payload: payloads[n] })}
    />,
  )
}

describe('RevisionHistory View version', () => {
  afterEach(cleanup)

  it('shows the fields that changed from the version before', async () => {
    renderList()
    const buttons = await screen.findAllByRole('button', { name: 'View version' })
    fireEvent.click(buttons[0]!)
    expect(await screen.findByLabelText('Changes in version 2')).toBeInTheDocument()
    expect(screen.getByText('label')).toBeInTheDocument()
    expect(screen.getByText('extra')).toBeInTheDocument()
    expect(screen.queryByText('port')).not.toBeInTheDocument()
  })

  it('says version 1 is the first version', async () => {
    renderList()
    const buttons = await screen.findAllByRole('button', { name: 'View version' })
    fireEvent.click(buttons[1]!)
    expect(await screen.findByText(/Version 1 is the first version/)).toBeInTheDocument()
  })

  it('hides the changes on a second click', async () => {
    renderList()
    const buttons = await screen.findAllByRole('button', { name: 'View version' })
    fireEvent.click(buttons[1]!)
    fireEvent.click(await screen.findByRole('button', { name: 'Hide version' }))
    expect(screen.queryByText(/first version/)).not.toBeInTheDocument()
  })
})
