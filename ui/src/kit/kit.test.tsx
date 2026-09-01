import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useRef, useState } from 'react'
import { BlankingPlate, Button, ClockSkewStrip, Drawer, Field, Input, LifecycleCommands, NotWired, Panes, Popover, RuledStrip, Segmented, SelectableRow, StatusPair, Table } from './index'

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

  it('puts help below the control and error below help, in both DOM order and aria-describedby', () => {
    const { container } = render(
      <Field label="Idle output" help="Reachable from the coordinator." error="Rejected an empty value.">
        {(props) => <Input defaultValue="" {...props} />}
      </Field>,
    )
    const help = screen.getByText('Reachable from the coordinator.')
    const error = screen.getByText('Rejected an empty value.')
    expect(help.compareDocumentPosition(error) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const input = screen.getByLabelText('Idle output')
    const [firstId = '', secondId = ''] = (input.getAttribute('aria-describedby') ?? '').split(' ')
    expect(document.getElementById(firstId)).toBe(help)
    expect(document.getElementById(secondId)).toBe(error)
    // Label, control, help, error: never side by side.
    expect(container.querySelector('.sm-field')?.children.length).toBe(4)
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

  it('portals the panel onto document.body, clear of the anchor’s stacking context', () => {
    const { container } = render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Anchor' }))

    // Queried on document.body, not the render container: the chrome bar
    // this anchors under is a sticky container with its own stacking
    // context, so the panel must render as a direct child of body.
    const dialog = document.body.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog?.parentElement).toBe(document.body)
    expect(container.contains(dialog)).toBe(false)
  })

  it('closes on an outside click but not on a click inside the portaled panel', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Anchor' }))
    screen.getByRole('dialog', { name: 'Choose one' })

    fireEvent.mouseDown(screen.getByRole('button', { name: 'Second option' }))
    expect(screen.getByRole('dialog', { name: 'Choose one' })).toBeInTheDocument()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('Drawer', () => {
  function Harness({ width = 'content' }: { width?: 'content' | 'wide' | number }) {
    const openerRef = useRef<HTMLButtonElement>(null)
    const [open, setOpen] = useState(false)
    return (
      <div>
        <button ref={openerRef} type="button" onClick={() => setOpen(true)}>
          Inspect
        </button>
        <Drawer open={open} onClose={() => setOpen(false)} labelledBy="drawer-heading" width={width}>
          <h2 id="drawer-heading">Node detail</h2>
          <button type="button">Inner action</button>
        </Drawer>
      </div>
    )
  }

  it('renders nothing until open, then portals into the body', () => {
    render(<Harness />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    const dialog = screen.getByRole('dialog', { name: 'Node detail' })
    expect(dialog).toBeInTheDocument()
    expect(dialog.closest('body')).toBe(document.body)
    expect(dialog.parentElement).toBe(document.body)
  })

  it('is labelled by the heading inside it and stays non-modal', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    const dialog = screen.getByRole('dialog', { name: 'Node detail' })
    expect(dialog).toHaveAttribute('aria-modal', 'false')
  })

  it('moves focus in on open and returns it to the opener on close', async () => {
    render(<Harness />)
    const opener = screen.getByRole('button', { name: 'Inspect' })
    // userEvent, not fireEvent: only userEvent's click also moves focus, the
    // way a real pointer click does, so the drawer has a real opener to
    // capture and return to.
    await userEvent.click(opener)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }))

    await userEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(opener)
  })

  it('closes on Escape', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    const dialog = screen.getByRole('dialog', { name: 'Node detail' })
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('closes on a scrim click but not on a click inside the panel', () => {
    const { container } = render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    screen.getByRole('dialog', { name: 'Node detail' })

    fireEvent.click(screen.getByRole('button', { name: 'Inner action' }))
    expect(screen.getByRole('dialog', { name: 'Node detail' })).toBeInTheDocument()

    const scrim = container.ownerDocument.querySelector('.sm-drawer-scrim')
    expect(scrim).not.toBeNull()
    fireEvent.click(scrim as Element)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('applies a width class for wide and an inline width for a pixel value', () => {
    render(<Harness width="wide" />)
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    expect(document.querySelector('.sm-drawer--wide')).not.toBeNull()

    cleanup()
    render(<Harness width={500} />)
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    const dialog = screen.getByRole('dialog', { name: 'Node detail' })
    expect(dialog).toHaveStyle({ width: '500px' })
  })
})

describe('Panes', () => {
  function Harness() {
    const [selected, setSelected] = useState<string | null>(null)
    return (
      <Panes inspectorOpen={selected !== null} onInspectorClose={() => setSelected(null)} inspectorLabelledBy="row-heading">
        <ul>
          <li>
            <button type="button" onClick={() => setSelected('a')}>Row A</button>
          </li>
        </ul>
        <aside>
          <h2 id="row-heading">Row {selected}</h2>
        </aside>
      </Panes>
    )
  }

  it('renders the list as the page body and keeps it full width with nothing selected', () => {
    const { container } = render(<Harness />)
    expect(screen.getByRole('button', { name: 'Row A' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(container.querySelector('aside')).toBeNull()
  })

  it('opens the aside content in a dialog portaled into document.body on selection', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Row A' }))
    const dialog = screen.getByRole('dialog', { name: 'Row a' })
    expect(dialog.parentElement).toBe(document.body)
  })

  it('clears the selection on Escape', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: 'Row A' }))
    screen.getByRole('dialog', { name: 'Row a' })
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('LifecycleCommands', () => {
  it('renders an untitled group as a flat grid with no subsection heading', () => {
    const { container } = render(
      <LifecycleCommands
        groups={[
          {
            id: 'flat',
            commands: [
              { command: 'prepare-site', label: 'Prepare site', detail: 'Opens a preparation epoch.', onRun: vi.fn() },
            ],
          },
        ]}
      />,
    )
    expect(container.querySelector('.sm-lifecycle-commands')).not.toBeNull()
    expect(container.querySelector('h3')).toBeNull()
    expect(screen.getByRole('button', { name: 'Prepare site' })).toBeInTheDocument()
  })

  it('renders a titled group as a labelled subsection with the control grid', () => {
    render(
      <LifecycleCommands
        groups={[
          {
            id: 'lc-prepare',
            title: 'Prepare',
            commands: [
              { command: 'run-readiness', label: 'Run readiness', detail: 'Re-runs every readiness check.', onRun: vi.fn() },
            ],
          },
        ]}
      />,
    )
    expect(screen.getByRole('heading', { level: 3, name: 'Prepare' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Prepare' })).toBeInTheDocument()
  })

  it('calls onRun with no arguments when its button is pressed', async () => {
    const onRun = vi.fn()
    render(
      <LifecycleCommands
        groups={[{ id: 'flat', commands: [{ command: 'end-session', label: 'End session', detail: 'Abandons the session.', onRun }] }]}
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(onRun).toHaveBeenCalledTimes(1)
  })

  it('disables the button and swaps the consequence line for the reason', () => {
    render(
      <LifecycleCommands
        groups={[
          {
            id: 'flat',
            commands: [
              {
                command: 'end-session',
                label: 'End session',
                detail: 'Abandons the session.',
                disabled: true,
                disabledReason: 'Requires night:command.',
                onRun: vi.fn(),
              },
            ],
          },
        ]}
      />,
    )
    const button = screen.getByRole('button', { name: 'End session' })
    expect(button).toBeDisabled()
    expect(button.getAttribute('title')).toBe('Requires night:command.')
    expect(screen.getByText('Requires night:command.')).toBeInTheDocument()
    expect(screen.queryByText('Abandons the session.')).not.toBeInTheDocument()
  })

  it('renders a command’s options inside its own cell, under the consequence line, not beside the button', () => {
    render(
      <LifecycleCommands
        groups={[
          {
            id: 'flat',
            commands: [
              {
                command: 'start-night',
                label: 'Start night',
                detail: 'Commits the armed show and starts the first cycle.',
                onRun: vi.fn(),
                options: <span data-testid="skip-option">Skip the enter-show lead.</span>,
              },
              { command: 'prepare-site', label: 'Prepare site', detail: 'Opens a preparation epoch.', onRun: vi.fn() },
            ],
          },
        ]}
      />,
    )
    const option = screen.getByTestId('skip-option')
    const cell = option.closest('.sm-lifecycle-command--start-night')
    expect(cell).not.toBeNull()
    expect(cell?.querySelector('.sm-btn')).not.toBeNull()
    // The consequence line precedes the option within the same cell.
    const detail = screen.getByText('Commits the armed show and starts the first cycle.')
    expect(cell?.contains(detail)).toBe(true)
    expect(detail.compareDocumentPosition(option) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // It never lands in the neighbouring command's cell.
    expect(cell?.querySelector('[data-testid="skip-option"]')?.closest('.sm-lifecycle-command--prepare-site')).toBeFalsy()
  })
})
