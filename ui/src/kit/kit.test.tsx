import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { BlankingPlate, Button, Field, Input, RuledStrip, Segmented, StatusPair } from './index'

afterEach(cleanup)

describe('StatusPair', () => {
  it('renders a state word, so colour is never the only signal', () => {
    render(<StatusPair tone="bad" label="Offline" />)
    expect(screen.getByText('Offline')).toBeTruthy()
  })

  it('gives only the unknown tone a dashed edge', () => {
    const { container, unmount } = render(<StatusPair tone="unknown" label="Unobserved" />)
    expect(container.querySelector('.sm-status--unknown')).not.toBeNull()
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
