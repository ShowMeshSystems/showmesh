import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { RenderSettingsPanel } from '../components/RenderSettingsPanel'
import { ShowModePanel } from '../components/ShowModePanel'
import { OperatorPageHeader } from '../components/SharedLayouts'
import { Access } from './Access'
import { AssetsSettingsSection, FPPMQTTSection, FPPEndpointsSection, ResolumeInstancesSection } from './Configuration'
import './settings-pages.css'

const CONFIG_WRITE_SCOPE = 'config:write'

function ConfigEditorPage({ title, lede, children }: { title: string; lede: ReactNode; children: ReactNode }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  return (
    <div className="settings-direct-page">
      <OperatorPageHeader title={title} eyebrow="Settings" lede={lede} />
      {!gate.allowed ? (
        <p className="panel panel--error" role="status">{gate.reason}</p>
      ) : (
        <section className="settings-direct-page__editor">
          <h2 className="visually-hidden">{title} settings editor</h2>
          {children}
        </section>
      )}
    </div>
  )
}

export function ConnectionsSettings() {
  return (
    <ConfigEditorPage title="Connections" lede="Coordinator connections to FPP, Resolume, and the FPP MQTT collector.">
      <FPPEndpointsSection />
      <ResolumeInstancesSection />
      <FPPMQTTSection />
    </ConfigEditorPage>
  )
}

export function ContentDeliverySettings() {
  return (
    <ConfigEditorPage title="Content delivery" lede="Asset-store settings used to make content available to nodes.">
      <AssetsSettingsSection />
    </ConfigEditorPage>
  )
}

export function RenderRecoverySettings() {
  return (
    <ConfigEditorPage title="Render recovery" lede="Idle output and bounded render-pipeline restart behavior.">
      <div id="render-settings">
        <RenderSettingsPanel />
      </div>
    </ConfigEditorPage>
  )
}

export function ModeSettings() {
  return (
    <ConfigEditorPage title="Mode" lede="Choose the installation-wide operating mode.">
      <div id="show-mode">
        <ShowModePanel />
      </div>
    </ConfigEditorPage>
  )
}

export function AudioSettingsDirectory() {
  return (
    <div className="settings-direct-page">
      <OperatorPageHeader title="Audio" eyebrow="Settings" lede="Open the existing coordinator-backed editor for installation defaults or per-node routing." />
      <nav aria-label="Audio settings" className="config-index">
        <ul className="config-index__list">
          <li><Link to="/config/audio.settings">Audio defaults</Link></li>
          <li><Link to="/config/audio.node">Audio routing</Link></li>
        </ul>
      </nav>
    </div>
  )
}

export function AppearanceSettings() {
  return (
    <div className="settings-direct-page">
      <OperatorPageHeader title="Appearance" eyebrow="Settings" lede="Appearance is local to this browser and does not create a coordinator revision." />
      <section className="panel settings-direct-page__appearance" aria-labelledby="contrast-heading">
        <h2 id="contrast-heading" className="panel__title">High contrast</h2>
        <p className="text-muted">Use the persistent High contrast control in the sidebar footer. The preference applies only to this browser and does not change coordinator configuration.</p>
      </section>
    </div>
  )
}

export function AccessSettings() {
  return (
    <div className="settings-direct-page">
      <OperatorPageHeader title="Access" eyebrow="Settings" lede="Principals and credentials are managed in the existing audited access editor." />
      <Access />
    </div>
  )
}
