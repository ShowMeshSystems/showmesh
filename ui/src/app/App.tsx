import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigationType } from 'react-router-dom'
import { useModel } from '../api'
import { ModelContext } from './ModelContext'
import { Layout } from './Layout'
import { useDensity, useTheme } from './useTheme'
import { Dashboard } from '../screens/Dashboard'
import { LiveControl } from '../screens/LiveControl'
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
  { path: '/night', title: 'Show Night', mock: 'Show Night.dc.html' },
  { path: '/shows/*', title: 'Shows', mock: 'Shows.dc.html' },
  { path: '/assets/*', title: 'Assets', mock: 'Show Assets.dc.html' },
  { path: '/monitor/fleet/*', title: 'Monitor · Fleet', mock: 'Monitor.dc.html' },
  { path: '/monitor/signals', title: 'Monitor · Signals', mock: 'Monitor.dc.html' },
  { path: '/monitor/activity', title: 'Monitor · Activity', mock: 'Monitor.dc.html' },
  { path: '/monitor/capabilities', title: 'Monitor · Capabilities', mock: 'Monitor.dc.html' },
  { path: '/monitor/manifest', title: 'Monitor · Manifest', mock: 'the kit' },
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
