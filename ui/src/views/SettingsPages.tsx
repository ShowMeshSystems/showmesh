import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { describeSignInState, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { useTheme, type Theme } from '../app/useTheme'
import { CoordinatorBuildNotice } from '../app/Layout'
import { RenderSettingsPanel } from '../components/RenderSettingsPanel'
import { ResolumeRecoveryToggle } from '../components/ResolumeRecoveryToggle'
import { ShowModePanel } from '../components/ShowModePanel'
import { LoadingBlock, OperatorPageHeader, PlannedFeature, StaleBlock, UnavailableBlock } from '../components/SharedLayouts'
import { Access } from './Access'
import { AssetsSettingsSection, FPPMQTTSection, FPPEndpointsSection, ResolumeInstancesSection } from './Configuration'
import { AudioSettings } from './AudioSettings'
import { SettingsShell, type SettingsTabId } from './settings/SettingsShell'
import { NodeRoutingSettings } from './settings/NodeRoutingSettings'
import './settings-pages.css'

const CONFIG_WRITE_SCOPE = 'config:write'

// Every /settings/* tab (Settings.dc.html) needs the same coordinator-write
// permission gate ConfigEditorPage already established, now wrapped in the
// persistent SettingsShell tab strip (UI-DESIGN-GUIDE.md section 3:
// "the same horizontal tab language as everywhere else"). tabId selects
// which strip item reads aria-current="page".
function ConfigEditorPage({
  tabId,
  title,
  lede,
  children,
}: {
  tabId: SettingsTabId
  title: string
  lede: ReactNode
  children: ReactNode
}) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const permissionState = permissionStateBlock(model.session, model.sessionFetchFailed, gate.allowed ? null : gate.reason)

  return (
    <SettingsShell active={tabId}>
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
    </SettingsShell>
  )
}

function permissionStateBlock(
  session: ReturnType<typeof useModelContext>['session'],
  sessionFetchFailed: boolean,
  insufficientPermissionReason: string | null,
) {
  const signInState = describeSignInState(session)

  if (signInState.kind === 'loading') {
    return <LoadingBlock title="Loading permissions" reason="Waiting for the coordinator to report what this device may do." />
  }
  if (signInState.kind === 'bootstrap_required') {
    return <UnavailableBlock title="Setup required" reason="No administrator exists on this coordinator. Claim the bootstrap code from its data volume to create one before editing settings." />
  }
  if (signInState.kind === 'signed_out') {
    return <UnavailableBlock title="Signed out" reason="This device is not signed in, so it cannot edit these settings." />
  }
  if (sessionFetchFailed || signInState.session.scopesState !== 'current') {
    return <StaleBlock title="Stale permission evidence" reason="Settings remain unavailable until the coordinator can confirm this device’s current permissions." />
  }
  if (insufficientPermissionReason) {
    return <UnavailableBlock title="Insufficient permission" reason={insufficientPermissionReason} />
  }
  return null
}

export function ConnectionsSettings() {
  return (
    <ConfigEditorPage
      tabId="connections"
      title="Connections"
      lede="Addresses must be reachable from the coordinator, not from this browser. Saving does not disturb a running show: the coordinator re-polls on the next cycle."
    >
      <FPPEndpointsSection />
      <ResolumeInstancesSection />
      <FPPMQTTSection />
    </ConfigEditorPage>
  )
}

// Settings.dc.html's Content delivery tab also drew a Backend chooser
// (coordinator volume / mounted filesystem / SMB share) and a Disk usage
// readout. Neither exists on this checkout's assets.settings
// (config.ConfigAssetsSettingsPayload / AssetsSettingsSection above only
// has contentBaseUrl/maxUploadBytes/syncIntervalSeconds/
// inventoryIntervalSeconds -- no backend field to write and no disk
// telemetry to read). OWNER RULING 2026-08-29: keep drawing the idea, but
// stamp it as not built rather than dropping it silently.
const STORAGE_BACKEND_PREVIEW = (
  <div className="planned-preview-form">
    <label>
      Backend
      <select disabled>
        <option>Coordinator volume</option>
        <option>Mounted filesystem</option>
        <option>SMB share</option>
      </select>
    </label>
    <label>
      Path
      <input type="text" disabled placeholder="/var/lib/showmesh/assets" />
    </label>
  </div>
)

const DISK_USAGE_PREVIEW = (
  <div className="planned-preview-form">
    <span className="t-data">&mdash; of &mdash; used by &mdash; assets</span>
    <div className="planned-preview-bar" />
  </div>
)

export function ContentDeliverySettings() {
  return (
    <ConfigEditorPage
      tabId="delivery"
      title="Content delivery"
      lede="Metadata lives in the coordinator's database; bytes never do. Nodes play from their own disk, so nothing here is in the playback path."
    >
      <AssetsSettingsSection />
      <PlannedFeature
        title="Storage backend"
        why="assets.settings exposes contentBaseUrl, maxUploadBytes, syncIntervalSeconds and inventoryIntervalSeconds only. There is no backend field to write (coordinator volume / mounted filesystem / SMB share) and no separate path field distinct from the content base URL above."
        preview={STORAGE_BACKEND_PREVIEW}
      />
      <PlannedFeature
        title="Disk usage"
        why="assets.settings carries no disk telemetry: no used/total bytes and no asset count. Nothing in this checkout's API reports how much of the store is in use."
        preview={DISK_USAGE_PREVIEW}
      />
    </ConfigEditorPage>
  )
}

export function RenderRecoverySettings() {
  return (
    <ConfigEditorPage
      tabId="recovery"
      title="Render recovery"
      lede="What a render node shows when nothing is driving it, and how its pipeline supervisor restarts."
    >
      <div id="render-settings">
        <RenderSettingsPanel />
      </div>
      <ResolumeRecoveryToggle />
    </ConfigEditorPage>
  )
}

export function ModeSettings() {
  return (
    <ConfigEditorPage tabId="mode" title="Mode" lede="What this installation is for right now. Installation-wide, and every screen reads it.">
      <ShowModePanel />
      <p className="text-muted" style={{ marginTop: '1rem' }}>
        Playlist mismatch handling is expected to follow this setting rather than being configured
        per playlist. That wiring does not exist yet: today the per-playlist control on Shows is
        what takes effect.
      </p>
    </ConfigEditorPage>
  )
}

// AudioDefaultsSettings is the tab Settings.dc.html calls "Audio defaults"
// (ROUTE-MAP.md: /settings/audio-defaults) -- the audio.settings singleton
// editor, unchanged (AudioSettings.tsx already carries its own permission
// gate, load/save states and field validation), now reached from the
// shared tab strip instead of the old /config/audio directory.
export function AudioDefaultsSettings() {
  return (
    <SettingsShell active="defaults">
      <AudioSettings />
    </SettingsShell>
  )
}

// Node routing (ROUTE-MAP.md: /settings/node-routing) needs a node picker
// this tab's URL has no room for -- see settings/NodeRoutingSettings.tsx's
// own header comment.
export { NodeRoutingSettings }

const THEME_OPTIONS: { value: Theme; label: string }[] = [
  { value: 'system', label: 'Follow the system' },
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
  { value: 'contrast', label: 'Contrast' },
]

export function AppearanceSettings() {
  const [theme, setTheme] = useTheme()

  return (
    <SettingsShell active="appearance">
      <div className="settings-direct-page">
        <OperatorPageHeader title="Appearance" eyebrow="Settings" lede="How this browser looks." />

        <p className="text-muted" role="status">
          Everything on this page is stored in this browser. It creates no coordinator revision, is
          not attributed to you, and no one else sees it: which is why it has no Save button.
        </p>

        <section aria-labelledby="appearance-theme-heading" className="settings-shell__appearance-section">
          <h3 id="appearance-theme-heading" className="settings-shell__section-label t-meta">
            Theme
          </h3>
          <div className="segmented" role="group" aria-label="Theme">
            {THEME_OPTIONS.map((option) => (
              <button
                key={option.value}
                type="button"
                className="segmented__option"
                aria-pressed={theme === option.value}
                onClick={() => setTheme(option.value)}
              >
                {option.label}
              </button>
            ))}
          </div>
          <p className="text-muted">Dark is the show-time default. Light is for daylight setup work.</p>
        </section>

        <section aria-labelledby="appearance-build-heading" className="settings-shell__appearance-section">
          <h3 id="appearance-build-heading" className="settings-shell__section-label t-meta">
            Coordinator build
          </h3>
          <CoordinatorBuildNotice />
        </section>
      </div>
    </SettingsShell>
  )
}

// AudioSettingsDirectory is the pre-tab-strip /config/audio directory.
// Kept for the old route (App.tsx still imports and routes it); the new
// tab strip reaches the same two destinations as "Audio defaults" and
// "Node routing" instead.
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

// AccessSettings is the pre-tab-strip /config/access embed. The new tab
// strip's Access entry LEAVES the screen to /access instead (Settings.dc.html,
// ROUTE-MAP.md: "the single tab that leaves the screen"); this export is
// kept only because App.tsx's existing /config/access route still imports
// it by this name.
export function AccessSettings() {
  return (
    <div className="settings-direct-page">
      <OperatorPageHeader title="Access" eyebrow="Settings" lede="Principals and credentials are managed in the existing audited access editor." />
      <Access />
    </div>
  )
}
