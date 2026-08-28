import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { RenderSettingsPanel } from '../components/RenderSettingsPanel'
import { ShowModePanel } from '../components/ShowModePanel'
import { LoadingBlock, OperatorPageHeader, StaleBlock, UnavailableBlock } from '../components/SharedLayouts'
import { Access } from './Access'
import { AssetsSettingsSection, FPPMQTTSection, FPPEndpointsSection, ResolumeInstancesSection } from './Configuration'
import './settings-pages.css'

const CONFIG_WRITE_SCOPE = 'config:write'

function ConfigEditorPage({ title, lede, children }: { title: string; lede: ReactNode; children: ReactNode }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const permissionState = permissionStateBlock(model.session, model.sessionFetchFailed, gate.allowed ? null : gate.reason)

  return (
    <div className="settings-direct-page">
      <OperatorPageHeader title={title} eyebrow="Settings" lede={lede} />
      {permissionState ? (
        permissionState
      ) : (
        <section className="settings-direct-page__editor">
          <h2 className="visually-hidden">{title} settings editor</h2>
          {children}
        </section>
      )}
    </div>
  )
}

function permissionStateBlock(
  session: ReturnType<typeof useModelContext>['session'],
  sessionFetchFailed: boolean,
  insufficientPermissionReason: string | null,
) {
  if (session === null) {
    return <LoadingBlock title="Loading permissions" reason="Waiting for the coordinator to report what this device may do." />
  }
  if (!session.authenticated) {
    return <UnavailableBlock title="Signed out" reason="This device is not signed in, so it cannot edit these settings." />
  }
  if (sessionFetchFailed || session.scopesState !== 'current') {
    return <StaleBlock title="Stale permission evidence" reason="Settings remain unavailable until the coordinator can confirm this device’s current permissions." />
  }
  if (insufficientPermissionReason) {
    return <UnavailableBlock title="Insufficient permission" reason={insufficientPermissionReason} />
  }
  return null
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
      <ShowModePanel />
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
