import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigate, useNavigationType } from 'react-router-dom'
import { UnsavedChangesProvider } from './UnsavedChanges'
// Seam B's public surface (spec sections 5.4-5.6): the `useModel()` hook
// and a way to submit an operator-supplied API token. `ui/src/api` does
// not exist in this working tree yet -- seam B is building it
// concurrently -- so this is the one import in this seam expected to fail
// to resolve until it lands (spec section 6: "Your code will not
// typecheck until seam B lands; write against the declared interface and
// say so in your report"). `submitToken` is this seam's assumed name for
// the "way to submit a token" the spec asks for without fixing a name;
// see this builder's report for the alternative names to try if seam B
// exports something else.
import { useModel, submitToken } from '../api'
import { ModelContext } from './ModelContext'
import { Layout } from './Layout'
import { Dashboard } from '../views/Dashboard'
import { Monitor } from '../views/Monitor'
import { Observations } from '../views/Observations'
import { NodeDetail } from '../views/NodeDetail'
import { FPPDetail } from '../views/FPPDetail'
import { Capabilities } from '../views/Capabilities'
import { Events } from '../views/Events'
import { Configuration } from '../views/Configuration'
import {
  AppearanceSettings,
  ConnectionsSettings,
  AudioDefaultsSettings,
  ContentDeliverySettings,
  ModeSettings,
  RenderRecoverySettings,
} from '../views/SettingsPages'
import { NodeRoutingSettings } from '../views/settings/NodeRoutingSettings'
// ADR-039/ADR-018: the audio.settings engine-wide singleton and audio.node
// per-node object, the last two configuration kinds that shipped with a
// full API path/showmeshctl coverage and no Operator UI control.
import { AudioNodeDetail } from '../views/AudioNodeDetail'
import { MacroDetail } from '../views/MacroDetail'
import { MacroRunView } from '../views/MacroRunView'
import { LiveControl } from '../views/LiveControl'
import { ShowActions } from '../views/ShowActions'
import { ShowActionDetail } from '../views/ShowActionDetail'
import { ResolumeView } from '../views/ResolumeView'
import { Access } from '../views/Access'
// Track G seam G-8 (TRACK-G-surface-parity.md "G-8"): routes for Track E's
// previously UI-less surface — shows, surfaces, active-show activation,
// the asset browser, the per-node manifest, and the audit log.
import { Shows } from '../views/Shows'
import { ShowDetail } from '../views/ShowDetail'
import { ShowSurfaces } from '../views/ShowSurfaces'
import { ShowSurfaceDetail } from '../views/ShowSurfaceDetail'
// Track H seam H6 (TRACK-H-cues-and-playlists.md "H6"): the show.cue
// authoring surface, previously reachable only from showmeshctl.
import { ShowCues } from '../views/ShowCues'
import { ShowCueDetail } from '../views/ShowCueDetail'

// Track H seam H6 (TRACK-H-cues-and-playlists.md "H6"): show.playlist
// authoring, one level down from the show it belongs to, same posture as
// the Shows/Surfaces routes just above.
import { ShowPlaylists } from '../views/ShowPlaylists'
import { ShowPlaylistDetail } from '../views/ShowPlaylistDetail'
import { Assets } from '../views/Assets'
import { AssetManifest } from '../views/AssetManifest'
// Track F seam F2 (UI half): the night-session lifecycle operating view
// and its night.session/night.session.active configuration screens.
import { NightSession } from '../views/NightSession'
import { NightSessions } from '../views/NightSessions'
import { NightSessionDetail } from '../views/NightSessionDetail'
// TRACK-H-H2-SPEC.md §5/§6: the show-night Playlist readiness and FPP
// instance reconciliation verdicts, previously reachable only from
// `showmeshctl fpp`.
import { Readiness } from '../views/PlaylistReadiness'
// TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition import
// evidence, an authoring surface (unlike PlaylistReadiness above), so it
// routes under Configure rather than Show night.
import { FPPPlaylistDefinitionDetail } from '../views/FPPPlaylistDefinitionDetail'
import { NotFound } from '../views/NotFound'
import '../styles/index.css'

// Operator-reported: navigating from a long page to another one left the
// new page scrolled partway down, at wherever the previous page had been
// scrolled. Keyed on pathname only, not the full location (a search-string
// or hash change on the SAME page must not yank the reader back to the
// top), and skipped on a browser back/forward navigation (`POP`) so this
// does not fight the browser's own scroll restoration for that case.
//
// A link carrying a hash is asking for a specific section rather than the
// top of the page. Under `<BrowserRouter>` (a non-data router) there is no
// `ScrollRestoration`/`useScrollRestoration` mounted anywhere in this app,
// and `history.pushState` neither scrolls the page nor fires `hashchange`
// on its own, so the browser does NOT handle the anchor itself here. This
// has to scroll to the target element by hand, including when only the
// hash changes and the pathname does not (e.g. a link on `/config` to
// `#show-mode`), and the target may not exist yet on the effect's first
// run if it renders as part of the same navigation, so this retries with
// `requestAnimationFrame` for a bounded number of frames rather than a
// fixed timeout guessing at render latency.
export function ScrollToTop() {
  const { pathname, hash } = useLocation()
  const navigationType = useNavigationType()

  useEffect(() => {
    if (navigationType === 'POP') return

    if (hash === '') {
      window.scrollTo(0, 0)
      return
    }

    const id = hash.slice(1)
    let frame = 0
    let cancelled = false
    const maxAttempts = 20 // ~a third of a second at 60fps; the target
    // renders as part of the same navigation, not from a network fetch, so
    // this is a generous bound rather than a tuned wait.

    const tryScroll = (attempt: number) => {
      if (cancelled) return
      const target = document.getElementById(id)
      if (target) {
        target.scrollIntoView()
        return
      }
      if (attempt >= maxAttempts) return
      frame = requestAnimationFrame(() => tryScroll(attempt + 1))
    }

    tryScroll(0)

    return () => {
      cancelled = true
      cancelAnimationFrame(frame)
    }
  }, [pathname, hash, navigationType])

  return null
}

const ROUTE_TITLES: Array<[string, string]> = [
  ['/', 'Dashboard'],
  ['/night', 'Show Night'],
  ['/control', 'Live Control'],
  ['/shows', 'Shows'],
  ['/assets', 'Assets'],
  ['/monitor', 'Monitor'],
  ['/settings', 'Settings'],
  ['/access', 'Access'],
]

export function RouteTitle() {
  const { pathname } = useLocation()

  useEffect(() => {
    const title = ROUTE_TITLES.find(([prefix]) =>
      prefix === '/' ? pathname === '/' : pathname === prefix || pathname.startsWith(`${prefix}/`),
    )?.[1]
    document.title = title === undefined ? 'ShowMesh Operator' : `${title} · ShowMesh Operator`
  }, [pathname])

  return null
}

// The Settings directory replaced the former single long Configuration page.
// Keep its established fragment links working, including the in-page target
// when the destination contains more than one editor.
export const LEGACY_CONFIGURATION_HASH_DESTINATIONS: Record<string, string> = {
  '#fpp-endpoints': '/config/connections#fpp-endpoints',
  '#resolume-instances': '/config/connections#resolume-instances',
  '#fpp-mqtt': '/config/connections#fpp-mqtt',
  '#assets-settings': '/config/content-delivery#assets-settings',
  '#render-settings': '/config/render-recovery#render-settings',
  '#show-mode': '/config/mode#show-mode',
}

export function ConfigurationRoute() {
  const { hash } = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    const destination = LEGACY_CONFIGURATION_HASH_DESTINATIONS[hash]
    if (destination) navigate(destination, { replace: true })
  }, [hash, navigate])

  return <Configuration />
}

export default function App() {
  const model = useModel()

  return (
    <ModelContext.Provider value={model}>
      <BrowserRouter>
        <UnsavedChangesProvider>
          <ScrollToTop />
          <RouteTitle />
          <Routes>
          <Route element={<Layout onSubmitToken={submitToken} />}>
            {/* Seven rail destinations, no eighth. UI-DESIGN-GUIDE.md section 3.
                Old addresses are deliberately NOT redirected: the not-found page
                maps them to their new home and says so, because eighteen routes
                collapsed into seven and a 404 here is usually an old bookmark
                rather than a typo. */}

            {/* Operate */}
            <Route index element={<Dashboard />} />
            <Route path="night" element={<NightSession />} />
            <Route path="control" element={<LiveControl />} />

            {/* Author: Shows. Every tab is a real nested route that keeps the
                tab strip and the show in the breadcrumb. No tab navigates out
                of the show, which the previous ?show= redirect scheme could not
                honour. */}
            <Route path="shows" element={<Shows />} />
            <Route path="shows/new" element={<ShowDetail isNew />} />
            <Route path="shows/:showId" element={<ShowDetail />} />
            <Route path="shows/:showId/playlists" element={<ShowPlaylists />} />
            <Route path="shows/:showId/playlists/new" element={<ShowPlaylistDetail isNew />} />
            <Route path="shows/:showId/playlists/:playlistId" element={<ShowPlaylistDetail />} />
            <Route path="shows/:showId/cues" element={<ShowCues />} />
            <Route path="shows/:showId/cues/new" element={<ShowCueDetail isNew />} />
            <Route path="shows/:showId/cues/:cueId" element={<ShowCueDetail />} />
            <Route path="shows/:showId/assets" element={<Assets />} />
            <Route path="shows/:showId/presentation" element={<ShowSurfaces />} />
            <Route path="shows/:showId/presentation/new" element={<ShowSurfaceDetail isNew />} />
            <Route path="shows/:showId/presentation/:id" element={<ShowSurfaceDetail />} />
            <Route path="shows/:showId/night-sessions" element={<NightSessions />} />
            <Route path="shows/:showId/night-sessions/new" element={<NightSessionDetail isNew />} />
            <Route path="shows/:showId/night-sessions/:id" element={<NightSessionDetail />} />
            <Route path="shows/:showId/automation" element={<ShowActions />} />
            <Route path="shows/:showId/automation/actions/new" element={<ShowActionDetail isNew />} />
            <Route path="shows/:showId/automation/actions/:actionId" element={<ShowActionDetail />} />
            <Route path="shows/:showId/automation/macros/new" element={<MacroDetail isNew />} />
            <Route path="shows/:showId/automation/macros/:macroId" element={<MacroDetail />} />
            <Route path="shows/:showId/automation/macros/:macroId/runs/:runId" element={<MacroRunView />} />

            {/* Author: Assets, cross-show. */}
            <Route path="assets" element={<Assets />} />
            <Route path="assets/manifest" element={<AssetManifest />} />

            {/* System: Monitor. Five facets replacing seven old destinations,
                organised by axis rather than by resource type. Readiness is the
                owner's addition, holding the full view of the checks. */}
            <Route path="monitor" element={<Navigate replace to="/monitor/fleet" />} />
            <Route path="monitor/fleet" element={<Monitor />} />
            <Route path="monitor/fleet/node/:nodeId" element={<NodeDetail />} />
            <Route path="monitor/fleet/fpp/:instanceId" element={<FPPDetail />} />
            <Route path="monitor/fleet/resolume" element={<ResolumeView />} />
            <Route path="monitor/fleet/resolume/:instanceId" element={<ResolumeView />} />
            <Route
              path="monitor/fleet/playlist-definitions/:instanceUuid/:playlistHash"
              element={<FPPPlaylistDefinitionDetail />}
            />
            <Route path="monitor/signals" element={<Observations />} />
            <Route path="monitor/activity" element={<Events />} />
            <Route path="monitor/capabilities" element={<Capabilities />} />
            <Route path="monitor/readiness" element={<Readiness />} />

            {/* System: Settings. Access is the one tab that leaves the screen. */}
            <Route path="settings" element={<Navigate replace to="/settings/connections" />} />
            <Route path="settings/connections" element={<ConnectionsSettings />} />
            <Route path="settings/content-delivery" element={<ContentDeliverySettings />} />
            <Route path="settings/render-recovery" element={<RenderRecoverySettings />} />
            <Route path="settings/appearance" element={<AppearanceSettings />} />
            <Route path="settings/audio-defaults" element={<AudioDefaultsSettings />} />
            <Route path="settings/node-routing" element={<NodeRoutingSettings />} />
            {/* Declaring routing for a node that has NOT advertised yet. The tab's
                own Declare action reuses an advertising agent's node id, which is
                the safe default; this is the escape hatch for a node that has not
                come up, and it is the only path that accepts a typed id. */}
            <Route path="settings/node-routing/new" element={<AudioNodeDetail isNew />} />
            <Route path="settings/mode" element={<ModeSettings />} />
            <Route path="access" element={<Access />} />

            {/* Every one of these had no home when the overhaul began. All four
                now do, so their old addresses answer with the not-found migration
                guide like every other retired route. Nothing was deleted: the
                night-session editor moved to the Shows workspace, playlist
                definitions to Monitor Fleet, and audio node routing to Settings. */}
            <Route path="*" element={<NotFound />} />
          </Route>
          </Routes>
        </UnsavedChangesProvider>
      </BrowserRouter>
    </ModelContext.Provider>
  )
}
