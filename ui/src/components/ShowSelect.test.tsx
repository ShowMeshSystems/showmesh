import { useState } from 'react'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowSelect } from './ShowSelect'
import { makeShowList } from '../app/test-support/fixtures'

/** Keeps its own state, like every real caller (setForm), so typing into
 * the text-input fallback accumulates characters instead of each
 * keystroke overwriting a fixed prop value. */
function ControlledShowSelect({ initialValue }: { initialValue: string }) {
  const [value, setValue] = useState(initialValue)
  return <ShowSelect value={value} onChange={setValue} />
}

const { listConfigObjects } = vi.hoisted(() => ({
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

function renderShowSelect(value: string, onChange = vi.fn()) {
  render(
    <MemoryRouter>
      <ShowSelect value={value} onChange={onChange} />
    </MemoryRouter>,
  )
  return onChange
}

describe('ShowSelect', () => {
  // SM operator report: hand-typing a show is a namespace and every
  // cross-object reference has to stay inside it, so a typo produces an
  // object that is either refused or silently belongs to a namespace that
  // does not exist. This proves the replacement dropdown is actually
  // populated from the show list (GET /config/show), not a hand-typed
  // field.
  it('lists the shows from the API as select options', async () => {
    listConfigObjects.mockResolvedValue(makeShowList(['halloween-2026', 'christmas-2026']))
    renderShowSelect('')

    const select = await screen.findByRole('combobox')
    await waitFor(() => {
      expect(screen.getByRole('option', { name: 'halloween-2026' })).toBeInTheDocument()
      expect(screen.getByRole('option', { name: 'christmas-2026' })).toBeInTheDocument()
    })
    expect(listConfigObjects).toHaveBeenCalledWith('show')
    expect(select).toHaveValue('')
  })

  it('calls onChange with the picked show id when an option is selected', async () => {
    listConfigObjects.mockResolvedValue(makeShowList(['halloween-2026']))
    const user = userEvent.setup()
    const onChange = renderShowSelect('', vi.fn())
    const select = await screen.findByRole('combobox')

    await user.selectOptions(select, 'halloween-2026')

    expect(onChange).toHaveBeenCalledWith('halloween-2026')
  })

  // Case 1 from the defect report: a show can be removed from the list
  // while an object still references it (the local dev coordinator has
  // exactly this: objects in a show no longer in the list). The current
  // value must stay selectable and visibly marked, never silently dropped
  // or swapped for something else.
  it('keeps an existing value that is not in the show list, marked as such, rather than dropping it', async () => {
    listConfigObjects.mockResolvedValue(makeShowList(['halloween-2026']))
    renderShowSelect('whydoesthishavetobelowercase')

    const select = await screen.findByRole('combobox')
    await waitFor(() => expect(select).toHaveValue('whydoesthishavetobelowercase'))
    expect(screen.getByText(/not in the current show list/)).toBeVisible()
    expect(screen.getByRole('option', { name: /whydoesthishavetobelowercase/ })).toBeInTheDocument()
    // The real show is still offered alongside it.
    expect(screen.getByRole('option', { name: 'halloween-2026' })).toBeInTheDocument()
  })

  // Case 2: the show list read fails. This must read as "could not load",
  // never as "there are no shows": falls back to the plain text input,
  // with the reason stated, so the operator can still type a value and
  // is told why the dropdown is not there.
  it('falls back to the text input with the failure reason when the show list cannot be read', async () => {
    listConfigObjects.mockRejectedValue(new Error('network unreachable'))
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <ControlledShowSelect initialValue="" />
      </MemoryRouter>,
    )

    const textbox = await screen.findByRole('textbox')
    await waitFor(() => expect(screen.getByText(/Could not load the show list/)).toBeVisible())
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()

    await user.type(textbox, 'halloween-2026')
    expect(textbox).toHaveValue('halloween-2026')
  })

  // Case 3: the list loads but is genuinely empty and there is no current
  // value (a brand-new object, nothing yet configured at all). Must say
  // so plainly and point at where a show is created, never render an
  // empty dropdown that could be mistaken for a load failure or for
  // "nothing matches".
  it('says plainly that no shows are configured yet, for a new object, rather than an empty dropdown', async () => {
    listConfigObjects.mockResolvedValue(makeShowList([]))
    renderShowSelect('')

    await waitFor(() => expect(screen.getByText(/No shows are configured yet/)).toBeVisible())
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Create a show' })).toHaveAttribute('href', '/config/show/new')
  })

  // The empty-list case combined with an existing value: the list is
  // genuinely empty (not a read failure), but the object already carries
  // a show. That value must still be visible and editable, not vanish
  // just because nothing else exists to pick from.
  it('keeps the current value editable when the list is empty but the object already has one', async () => {
    listConfigObjects.mockResolvedValue(makeShowList([]))
    renderShowSelect('halloween-2026')

    const textbox = await screen.findByRole('textbox')
    expect(textbox).toHaveValue('halloween-2026')
    expect(screen.getByText(/No shows are currently configured/)).toBeVisible()
  })
})
