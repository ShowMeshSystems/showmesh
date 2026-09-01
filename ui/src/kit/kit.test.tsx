import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useRef, useState } from 'react'
import { BlankingPlate, Button, ClockSkewStrip, Field, Input, NotWired, Popover, RuledStrip, Segmented, SelectableRow, StatusPair, Table } from './index'

afterEach(cleanup)

describe('StatusPair', () => {
  it('renders a state word, so colour is never the only signal', () => {
    render(<StatusPair tone="bad" label="Offline" />)
    expect(screen.getByText('Offline')).toBeTruthy()
  })

  it('renders unknown as a labelled state without claiming it is unobserved', () => {
    const { container, unmount } = render(<StatusPair tone="unknown" label="Unobserved" />)
    expect(container.querySelector('.sm-status--unknown')).not.toBeNull()
    expect(container.querySelector('.sm-status--unknown')).not.toHaveStyle({ borderStyle: 'dashed' })
    unmount()
    for (const tone of ['good', 'warn', 'bad'] as const) {
      const settled = render(<StatusPair tone={tone} label="Settled" />)
      expect(settled.container.querySelector('.sm-status--unknown')).toBeNull()
      settled.unmount()
    }
  })
})

describe('RuledStrip', () => {
  it('marks never-collected evidence and nothing else', () => {
    const { container, unmount } = render(<RuledStrip absence="unobserved" label="Unobserved" fact="Never returned" />)
    expect(container.querySelector('.sm-strip__label--unobserved')).not.toBeNull()
    unmount()

    // Empty, stale and unavailable are settled facts: they keep a solid edge.
    for (const absence of ['empty', 'stale', 'unavailable'] as const) {
      const settled = render(<RuledStrip absence={absence} label={absence} fact="Settled" />)
      expect(settled.container.querySelector('.sm-strip__label--unobserved')).toBeNull()
      settled.unmount()
    }
  })

  it('puts the fact above the caveat', () => {
    render(<RuledStrip absence="stale" label="Stale · 4 m" fact="Pipeline state is old" detail="Stale is unknown, never healthy." />)
    const fact = screen.getByText('Pipeline state is old')
    const detail = screen.getByText('Stale is unknown, never healthy.')
    expect(fact.compareDocumentPosition(detail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

describe('ClockSkewStrip', () => {
  it('is a status region carrying a state word and a glyph, not colour alone', () => {
    render(<ClockSkewStrip>The clock is off.</ClockSkewStrip>)
    const strip = screen.getByRole('status')
    expect(strip).toHaveTextContent('⚠')
    expect(strip).toHaveTextContent('Clock skew')
    expect(strip).toHaveTextContent('The clock is off.')
  })
})

describe('NotWired', () => {
  it('makes a Segmented actually inert, not just tagged', async () => {
    const onChange = vi.fn()
    render(
      <NotWired>
        <Segmented label="Policy" value="a" options={[{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }]} onChange={onChange} />
      </NotWired>,
    )
    const b = screen.getByRole('button', { name: 'B' })
    expect(b).toBeDisabled()
    await userEvent.click(b)
    expect(onChange).not.toHaveBeenCalled()
  })
})

describe('BlankingPlate', () => {
  it('carries the absence class on the stamp for never-collected', () => {
    const { container } = render(<BlankingPlate absence="unobserved" stamp="No sig" eyebrow="Audio · unobserved" title="Never reported" />)
    expect(container.querySelector('.sm-plate--unobserved')).not.toBeNull()
  })

  it('reads a refusal as a permission state, not a failure', () => {
    const { container } = render(<BlankingPlate absence="noPermission" stamp="Perm" eyebrow="Access" title="Missing scope" />)
    expect(container.querySelector('.sm-plate--warn')).not.toBeNull()
    expect(container.querySelector('.sm-plate--bad')).toBeNull()
  })

  it('renders the title as an h2 by default, keeping the plate class', () => {
    render(<BlankingPlate absence="empty" stamp="Empty" eyebrow="Audio · empty" title="Nothing here" />)
    const heading = screen.getByRole('heading', { level: 2, name: 'Nothing here' })
    expect(heading.tagName).toBe('H2')
    expect(heading.className).toBe('sm-plate__title')
  })

  it('renders the title as an h3, same class, when headingLevel is 3 for a plate inside a Section', () => {
    render(<BlankingPlate absence="empty" stamp="Empty" eyebrow="Audio · empty" title="Nothing here" headingLevel={3} />)
    const heading = screen.getByRole('heading', { level: 3, name: 'Nothing here' })
    expect(heading.tagName).toBe('H3')
    expect(heading.className).toBe('sm-plate__title')
  })
})

describe('Button', () => {
  it('is actually inert when disabled, not merely styled as unavailable', async () => {
    const onClick = vi.fn()
    render(<Button disabled={true} onClick={onClick}>Apply · needs config:write</Button>)
    const button = screen.getByRole('button')
    expect((button as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(button, { pointerEventsCheck: 0 })
    expect(onClick).not.toHaveBeenCalled()
  })

  it('defaults to type button so it never submits a form by accident', () => {
    render(<Button>Run discovery</Button>)
    expect(screen.getByRole('button').getAttribute('type')).toBe('button')
  })
})

describe('SelectableRow', () => {
  it('exposes each table minimum width without changing its accessible structure', () => {
    render(
      <Table minWidth={680}>
        <tbody><tr><td>Layer</td></tr></tbody>
      </Table>,
    )
    const table = screen.getByRole('table')
    expect(table).toHaveStyle({ '--sm-table-min-width': '680px' })
  })

  it('activates from pointer, Enter, and Space without stealing nested controls', async () => {
    const onActivate = vi.fn()
    const nested = vi.fn()
    render(
      <Table>
        <tbody>
          <SelectableRow selected onActivate={onActivate} ariaLabel="Edit Alpha">
            <td>Alpha</td>
            <td><button type="button" onClick={nested}>Independent action</button></td>
          </SelectableRow>
        </tbody>
      </Table>,
    )
    const row = screen.getByRole('row', { name: 'Edit Alpha' })
    expect(row).toHaveAttribute('tabindex', '0')
    expect(row).toHaveAttribute('aria-current', 'true')
    await userEvent.click(row)
    fireEvent.keyDown(row, { key: 'Enter' })
    fireEvent.keyDown(row, { key: ' ' })
    expect(onActivate).toHaveBeenCalledTimes(3)
    await userEvent.click(screen.getByRole('button', { name: 'Independent action' }))
    expect(nested).toHaveBeenCalledOnce()
    expect(onActivate).toHaveBeenCalledTimes(3)
  })
})

describe('Field', () => {
  it('labels the control and describes it with the error and the help', () => {
    render(
      <Field label="Idle output" help="Reachable from the coordinator." error="The coordinator rejected an empty value.">
        {(props) => <Input defaultValue="" {...props} />}
      </Field>,
    )
    const input = screen.getByLabelText('Idle output')
    expect(input.getAttribute('aria-invalid')).toBe('true')
    const described = (input.getAttribute('aria-describedby') ?? '').split(' ')
    expect(described.length).toBe(2)
    for (const id of described) expect(document.getElementById(id)).not.toBeNull()
  })

  it('leaves a healthy field unmarked', () => {
    render(
      <Field label="FPP endpoint address">
        {(props) => <Input defaultValue="10.20.0.14" {...props} />}
      </Field>,
    )
    const input = screen.getByLabelText('FPP endpoint address')
    expect(input.getAttribute('aria-invalid')).toBeNull()
    expect(input.getAttribute('aria-describedby')).toBeNull()
  })
})

describe('Segmented', () => {
  it('announces the selection with aria-pressed rather than colour', async () => {
    function Harness() {
      const [value, setValue] = useState<'dark' | 'light'>('dark')
      return <Segmented label="Theme" value={value} options={[{ value: 'dark', label: 'Dark' }, { value: 'light', label: 'Light' }]} onChange={setValue} />
    }
    render(<Harness />)
    expect(screen.getByRole('button', { name: 'Dark' }).getAttribute('aria-pressed')).toBe('true')
    await userEvent.click(screen.getByRole('button', { name: 'Light' }))
    expect(screen.getByRole('button', { name: 'Light' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'Dark' }).getAttribute('aria-pressed')).toBe('false')
  })
})

describe('Popover', () => {
  function Harness() {
    const anchorRef = useRef<HTMLButtonElement>(null)
    const [open, setOpen] = useState(false)
    return (
      <div>
        <button ref={anchorRef} type="button" onClick={() => setOpen(true)}>
          Anchor
        </button>
        <Popover open={open} title="Choose one" anchorRef={anchorRef} onClose={() => setOpen(false)}>
          <button type="button">First option</button>
          <button type="button">Second option</button>
        </Popover>
      </div>
    )
  }

  it('renders nothing until open', () => {
    render(<Harness />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('is a labelled dialog, moves focus in on open and returns it to the anchor on close', () => {
    render(<Harness />)
    const anchor = screen.getByRole('button', { name: 'Anchor' })
    fireEvent.click(anchor)

    const dialog = screen.getByRole('dialog', { name: 'Choose one' })
    expect(dialog).toBeInTheDocument()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'First option' }))

    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(anchor)
  })

  it('closes on an outside click but not on a click inside the panel', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Anchor' }))
    screen.getByRole('dialog', { name: 'Choose one' })

    fireEvent.mouseDown(screen.getByRole('button', { name: 'Second option' }))
    expect(screen.getByRole('dialog', { name: 'Choose one' })).toBeInTheDocument()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
