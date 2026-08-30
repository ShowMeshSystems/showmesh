import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigationType } from 'react-router-dom'
import { useModel } from '../api'
import { ModelContext } from './ModelContext'
import { Layout } from './Layout'
import { useDensity, useTheme } from './useTheme'
import { Dashboard } from '../screens/Dashboard'
import { LiveControl } from '../screens/LiveControl'
import { Monitor } from '../screens/Monitor'
import { MonitorSignals } from '../screens/MonitorSignals'
import { MonitorActivity } from '../screens/MonitorActivity'
import { MonitorCapabilities } from '../screens/MonitorCapabilities'
import { MonitorManifest } from '../screens/MonitorManifest'
import { ShowNight } from '../screens/ShowNight'
import { Shows } from '../screens/Shows'
import { ShowDetail } from '../screens/ShowDetail'
import { ShowsWorkspace, ShowsTabPlaceholder } from '../screens/ShowsWorkspace'
import { ShowsPlaylists } from '../screens/ShowsPlaylists'
import { NotRebuilt } from '../screens/NotRebuilt'
import { NotFound } from '../screens/NotFound'
import { Specimen } from '../kit/Specimen'
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

/**
 * Every route the rebuild has not reached. Each one names the mock it is
 * waiting on, so the queue is readable from the running app.
 */
const QUEUE: readonly { path: string; title: string; mock: string }[] = [
  { path: '/assets/*', title: 'Assets', mock: 'Show Assets.dc.html' },
  { path: '/monitor/fleet/node/:nodeId', title: 'Node', mock: 'Node.dc.html' },
  { path: '/monitor/fleet/fpp/:instanceId', title: 'FPP instance', mock: 'Node.dc.html' },
  { path: '/monitor/fleet/resolume/*', title: 'Resolume Config', mock: 'Resolume Config.dc.html' },
  { path: '/settings/*', title: 'Settings', mock: 'Settings.dc.html' },
  { path: '/access', title: 'Access', mock: 'Access.dc.html' },
]

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
            <Route path="settings" element={<Navigate replace to="/settings/connections" />} />
            <Route index element={<Dashboard />} />
            <Route path="control" element={<LiveControl />} />
            <Route path="night" element={<ShowNight />} />
            <Route path="monitor/fleet" element={<Monitor />} />
            <Route path="monitor/signals" element={<MonitorSignals />} />
            <Route path="monitor/activity" element={<MonitorActivity />} />
            <Route path="monitor/capabilities" element={<MonitorCapabilities />} />
            <Route path="monitor/manifest" element={<MonitorManifest />} />
            <Route path="shows" element={<Shows />} />
            <Route path="shows/:id" element={<ShowDetail />} />
            <Route path="shows/:id" element={<ShowsWorkspace />}>
              <Route path="playlists" element={<ShowsPlaylists />} />
              <Route path="cues" element={<ShowsTabPlaceholder tab="Cues" />} />
              <Route path="assets" element={<ShowsTabPlaceholder tab="Assets" />} />
              <Route path="presentation" element={<ShowsTabPlaceholder tab="Presentation" />} />
              <Route path="automation" element={<ShowsTabPlaceholder tab="Automation" />} />
            </Route>
            {QUEUE.map((entry) => (
              <Route key={entry.path} path={entry.path} element={<NotRebuilt title={entry.title} mock={entry.mock} />} />
            ))}
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </ModelContext.Provider>
  )
}
