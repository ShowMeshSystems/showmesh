import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigationType, useParams } from 'react-router-dom'
import { useModel } from '../api'
import { ModelContext } from './ModelContext'
import { useModelContext } from './ModelContext'
import { Layout } from './Layout'
import { useDensity, useTheme } from './useTheme'
import { Assets } from '../screens/Assets'
import { Dashboard } from '../screens/Dashboard'
import { LiveControl } from '../screens/LiveControl'
import { Monitor } from '../screens/Monitor'
import { MonitorSignals } from '../screens/MonitorSignals'
import { MonitorActivity } from '../screens/MonitorActivity'
import { MonitorCapabilities } from '../screens/MonitorCapabilities'
import { MonitorManifest } from '../screens/MonitorManifest'
import { NodeDetail } from '../screens/NodeDetail'
import { ResolumeConfig } from '../screens/ResolumeConfig'
import { ResolumeControl } from '../screens/ResolumeControl'
import { ShowNight } from '../screens/ShowNight'
import { Shows } from '../screens/Shows'
import { ShowDraft } from '../screens/ShowDraft'
import { ShowDetail } from '../screens/ShowDetail'
import { ShowsWorkspace } from '../screens/ShowsWorkspace'
import { ShowsPlaylists } from '../screens/ShowsPlaylists'
import { ShowsCues } from '../screens/ShowsCues'
import { ShowsAssets } from '../screens/ShowsAssets'
import { ShowsPresentation } from '../screens/ShowsPresentation'
import { ShowsAutomation } from '../screens/ShowsAutomation'
import { Access } from '../screens/Access'
import { Settings } from '../screens/Settings'
import { SettingsConnections } from '../screens/SettingsConnections'
import { SettingsDelivery } from '../screens/SettingsDelivery'
import { SettingsRecovery } from '../screens/SettingsRecovery'
import { SettingsAppearance } from '../screens/SettingsAppearance'
import { SettingsAudioDefaults } from '../screens/SettingsAudioDefaults'
import { SettingsNodeRouting } from '../screens/SettingsNodeRouting'
import { SettingsMode } from '../screens/SettingsMode'
import { NotFound } from '../screens/NotFound'
import { Specimen } from '../kit/Specimen'
import { BlankingPlate } from '../kit'
import '../kit/styles/index.css'

/** A new page starts at the top, except on a browser back or forward. */
function ScrollToTop() {
  const { pathname } = useLocation()
  const navigationType = useNavigationType()

  useEffect(() => {
    if (navigationType !== 'POP') window.scrollTo(0, 0)
  }, [pathname, navigationType])

  return null
}

/** Preserve old FPP detail links while the approved Monitor inspector owns FPP evidence. */
function FppFleetRedirect() {
  const { instanceId = '' } = useParams<{ instanceId: string }>()
  return <Navigate replace to={`/monitor/fleet?resource=fpp:${encodeURIComponent(instanceId)}`} />
}

/** Resolume is configured under Settings; Fleet remains an evidence-only view. */
function ResolumeFleetRedirect() {
  const { instanceId = '' } = useParams<{ instanceId: string }>()
  return <Navigate replace to={`/settings/resolume/${encodeURIComponent(instanceId)}`} />
}

function ResolumeSettingsIndex() {
  const model = useModelContext()
  const first = model.resolume[0]
  if (first !== undefined) return <Navigate replace to={`/settings/resolume/${encodeURIComponent(first.instanceId)}`} />
  return <BlankingPlate absence="empty" stamp="None" eyebrow="Resolume · no instance" title="No Resolume instance is configured" detail="Add an instance in Settings › Connections before configuring its stored composition or recovery." />
}

function Shell() {
  useTheme()
  useDensity()
  return <Layout />
}

export default function App() {
  const model = useModel()

  return (
    <ModelContext.Provider value={model}>
      <BrowserRouter>
        <ScrollToTop />
        <Routes>
          <Route path="/" element={<Shell />}>
            <Route path="_specimen" element={<Specimen />} />
            <Route path="monitor" element={<Navigate replace to="/monitor/fleet" />} />
            <Route index element={<Dashboard />} />
            <Route path="control" element={<LiveControl />} />
            <Route path="control/resolume" element={<ResolumeControl />} />
            <Route path="night" element={<ShowNight />} />
            <Route path="monitor/fleet" element={<Monitor />} />
            <Route path="monitor/signals" element={<MonitorSignals />} />
            <Route path="monitor/activity" element={<MonitorActivity />} />
            <Route path="monitor/capabilities" element={<MonitorCapabilities />} />
            <Route path="monitor/manifest" element={<MonitorManifest />} />
            <Route path="monitor/fleet/node/:nodeId" element={<NodeDetail />} />
            <Route path="monitor/fleet/fpp/:instanceId" element={<FppFleetRedirect />} />
            <Route path="monitor/fleet/resolume" element={<Navigate replace to="/settings/resolume" />} />
            <Route path="monitor/fleet/resolume/:instanceId" element={<ResolumeFleetRedirect />} />
            <Route path="shows" element={<Shows />} />
            <Route path="shows/new" element={<ShowDraft />} />
            <Route path="shows/:id" element={<ShowDetail />} />
            <Route path="shows/:id" element={<ShowsWorkspace />}>
              <Route path="playlists" element={<ShowsPlaylists />} />
              <Route path="cues" element={<ShowsCues />} />
              <Route path="assets" element={<ShowsAssets />} />
              <Route path="presentation" element={<ShowsPresentation />} />
              <Route path="automation" element={<ShowsAutomation />} />
            </Route>
            <Route path="settings" element={<Settings />}>
              <Route index element={<Navigate replace to="/settings/connections" />} />
              <Route path="connections" element={<SettingsConnections />} />
              <Route path="delivery" element={<SettingsDelivery />} />
              <Route path="recovery" element={<SettingsRecovery />} />
              <Route path="appearance" element={<SettingsAppearance />} />
              <Route path="audio-defaults" element={<SettingsAudioDefaults />} />
              <Route path="node-routing" element={<SettingsNodeRouting />} />
              <Route path="mode" element={<SettingsMode />} />
              <Route path="resolume" element={<ResolumeSettingsIndex />} />
              <Route path="resolume/:instanceId" element={<ResolumeConfig />} />
            </Route>
            <Route path="assets" element={<Assets />} />
            <Route path="access" element={<Access />} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </ModelContext.Provider>
  )
}
