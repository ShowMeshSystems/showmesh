import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { AttentionList, AttentionListItem, CommandGroup, ConfigurationSection, EmptyBlock, EvidenceTable, FailedBlock, LoadingBlock, OperatorPageHeader, OperatorSection, OverviewDetailWorkspace, StaleBlock, StatusStrip, StatusStripItem, UnavailableBlock, UnobservedBlock } from './SharedLayouts'

afterEach(cleanup)

describe('SharedLayouts', () => {
  it('provides labelled header, section, status, attention, command, and configuration landmarks without deriving their truth', () => {
    render(<><OperatorPageHeader title="Show Night" eyebrow="Run of Show" lede="Transition Step evidence." actions={<button>Open editor</button>} /><OperatorSection title="Presentation" aria-labelledby="presentation"><p>Observed path</p></OperatorSection><StatusStrip label="Current status"><StatusStripItem label="Coordinator" detail="Reported now">Connected</StatusStripItem></StatusStrip><AttentionList><AttentionListItem><a href="#detail">Review failed Transition Step</a></AttentionListItem></AttentionList><CommandGroup title="Lifecycle commands"><button>Prepare site</button></CommandGroup><ConfigurationSection title="Configuration"><label>Show name<input defaultValue="Halloween" /></label></ConfigurationSection></>)
    expect(screen.getByRole('heading', { name: 'Show Night' })).toBeVisible()
    expect(screen.getByText('Run of Show')).toBeVisible()
    expect(screen.getByRole('region', { name: 'Presentation' })).toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Current status' })).toHaveTextContent('CoordinatorConnectedReported now')
    expect(screen.getByRole('list', { name: 'Needs attention' })).toHaveTextContent('Review failed Transition Step')
    expect(screen.getByRole('region', { name: 'Lifecycle commands' })).toHaveTextContent('Prepare site')
    expect(screen.getByRole('region', { name: 'Configuration' })).toHaveTextContent('Show name')
  })

  it('preserves composition order and gives wide evidence a focusable local overflow region', async () => {
    const user = userEvent.setup()
    render(<OverviewDetailWorkspace overview={<button>Overview first</button>} detail={<EvidenceTable label="Signal evidence"><table><tbody><tr><td>very-wide operational value</td></tr></tbody></table></EvidenceTable>} />)
    await user.tab()
    expect(screen.getByRole('button', { name: 'Overview first' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('region', { name: 'Signal evidence' })).toHaveFocus()
  })

  it('renders each supplied state and unavailable reason explicitly', () => {
    render(<><LoadingBlock title="Loading Run of Show" reason="Waiting for the coordinator." /><EmptyBlock title="No Transition Steps" reason="Add a step to the Run of Show." /><UnavailableBlock title="FPP controls unavailable" reason="No FPP instance is configured." /><FailedBlock title="Presentation failed" reason="The coordinator reported a failed render." /><StaleBlock title="Evidence is stale" reason="Showing last known state." /><UnobservedBlock title="Audio unobserved" reason="No report has arrived." /></>)
    expect(screen.getByRole('status', { name: 'Loading Run of Show: loading' })).toHaveTextContent('Waiting for the coordinator.')
    expect(screen.getByText('No Transition Steps')).toBeVisible()
    expect(screen.getByText('No FPP instance is configured.')).toBeVisible()
    expect(screen.getByRole('alert', { name: 'Presentation failed: failed' })).toHaveTextContent('The coordinator reported a failed render.')
    expect(screen.getByText('Evidence is stale')).toBeVisible()
    expect(screen.getByText('Audio unobserved')).toBeVisible()
  })
})
