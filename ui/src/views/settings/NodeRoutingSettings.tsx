import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useModelContext } from '../../app/ModelContext'
import type { Node } from '../../app/types'
import { EmptyBlock } from '../../components/SharedLayouts'
import { AudioNodeDetail } from '../AudioNodeDetail'
import { SettingsShell } from './SettingsShell'

// UI-DESIGN-GUIDE.md section 7 / DESIGN-DECISIONS §6 "Audio node": routes
// come from the capability's `routes` attribute, and the agent advertises
// routes only, never channel inventories -- both facts already live in
// AudioNodeDetail.tsx, which owns the whole cross-checked form. This file
// adds only the one thing Settings.dc.html's Node routing tab has that the
// routed `/config/audio.node/:id` editor does not: a node picker, because
// the tab itself carries no node id in its URL (ROUTE-MAP.md).
const PROGRAM_CAPABILITY = 'audio.output.local'
const LTC_CAPABILITY = 'audio.output.ltc'

function isEligible(node: Node): boolean {
  return node.capabilities.some((c) => c.id === PROGRAM_CAPABILITY || c.id === LTC_CAPABILITY)
}

export function NodeRoutingSettings() {
  const model = useModelContext()
  const eligibleNodes = model.nodes.filter(isEligible)
  const ineligibleNodes = model.nodes.filter((node) => !isEligible(node))
  const [selected, setSelected] = useState<string>(eligibleNodes[0]?.nodeId ?? '')
  const currentIsEligible = selected !== '' && eligibleNodes.some((node) => node.nodeId === selected)

  return (
    <SettingsShell active="audio">
      <p className="t-small text-muted settings-breadcrumb">
        <Link to="/settings/connections">Settings</Link> / Audio / Node routing
      </p>
      <h2 className="t-heading">Where this node&rsquo;s audio leaves the building</h2>
      <p className="t-small text-muted" style={{ maxWidth: '74ch' }}>
        Program and LTC leave through one interface in one clock domain: the coordinator refuses a
        split. A route this node has not advertised is refused on save.
      </p>

      <section aria-labelledby="node-routing-node-heading" style={{ marginTop: 'var(--space-24px)', maxWidth: '640px' }}>
        <h3 id="node-routing-node-heading" className="t-meta settings-shell__section-label">
          Node
        </h3>
        <label className="form-field">
          Audio node
          <select aria-label="Audio node" value={selected} onChange={(e) => setSelected(e.target.value)}>
            <option value="" disabled>
              Choose a node
            </option>
            {eligibleNodes.map((node) => (
              <option key={node.nodeId} value={node.nodeId}>
                {node.nodeId} &mdash; {node.controlPlane.state}
              </option>
            ))}
            {ineligibleNodes.map((node) => (
              <option key={node.nodeId} value={node.nodeId} disabled>
                {node.nodeId} &mdash; no audio capability advertised
              </option>
            ))}
          </select>
          <span className="t-small text-muted">
            Nodes advertising <code>{PROGRAM_CAPABILITY}</code> or <code>{LTC_CAPABILITY}</code>.
          </span>
        </label>
      </section>

      {selected === '' ? (
        <EmptyBlock
          title="No eligible audio node observed"
          reason="No node in this coordinator's current evidence advertises audio.output.local or audio.output.ltc."
        >
          {' '}
          <Link to="/config/audio.node/new">Create an audio node manually</Link>.
        </EmptyBlock>
      ) : currentIsEligible ? (
        <AudioNodeDetail nodeIdOverride={selected} />
      ) : (
        <EmptyBlock
          title="Node has no audio capability"
          reason="This node has not advertised audio.output.local or audio.output.ltc, so routing cannot be edited from here."
        />
      )}

      {/* AudioNodes.tsx's list-and-create controls: the mock's Node routing
          tab draws neither, and the owner has not ruled on where they
          belong (BUILDER-BRIEF.md), so both stay reachable rather than
          disappearing. */}
      <p className="t-small text-muted" style={{ marginTop: 'var(--space-24px)' }}>
        This picker only lists nodes observed live. Every configured audio node, including one this
        coordinator has never observed, is in <Link to="/config/audio.node">all configured audio
        nodes</Link>, which is also where <Link to="/config/audio.node/new">a new audio node</Link>{' '}
        is created.
      </p>
    </SettingsShell>
  )
}
