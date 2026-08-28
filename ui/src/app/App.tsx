import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigate, useNavigationType, useParams } from 'react-router-dom'
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
import { NodesList } from '../views/NodesList'
import { NodeDetail } from '../views/NodeDetail'
import { FPPList } from '../views/FPPList'
import { FPPDetail } from '../views/FPPDetail'
import { Capabilities } from '../views/Capabilities'
import { Events } from '../views/Events'
import { Configuration } from '../views/Configuration'
import {
  AccessSettings,
  AppearanceSettings,
  AudioSettingsDirectory,
  ConnectionsSettings,
  ContentDeliverySettings,
  ModeSettings,
  RenderRecoverySettings,
} from '../views/SettingsPages'
// ADR-039/ADR-018: the audio.settings engine-wide singleton and audio.node
// per-node object, the last two configuration kinds that shipped with a
// full API path/showmeshctl coverage and no Operator UI control.
import { AudioSettings } from '../views/AudioSettings'
import { AudioNodes } from '../views/AudioNodes'
import { AudioNodeDetail } from '../views/AudioNodeDetail'
import { Macros } from '../views/Macros'
import { MacroDetail } from '../views/MacroDetail'
import { MacroRunView } from '../views/MacroRunView'
import { LiveControl } from '../views/LiveControl'
import { ShowWorkspaceOverview } from '../components/ShowWorkspace'
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
import { ShowActive } from '../views/ShowActive'
import { Assets } from '../views/Assets'
import { AssetManifest } from '../views/AssetManifest'
import { Audit } from '../views/Audit'
// Track F seam F2 (UI half): the night-session lifecycle operating view
// and its night.session/night.session.active configuration screens.
import { NightSession } from '../views/NightSession'
import { NightSessions } from '../views/NightSessions'
import { NightSessionDetail } from '../views/NightSessionDetail'
import { NightSessionActive } from '../views/NightSessionActive'
// TRACK-H-H2-SPEC.md §5/§6: the show-night Playlist readiness and FPP
// instance reconciliation verdicts, previously reachable only from
// `showmeshctl fpp`.
import { PlaylistReadiness } from '../views/PlaylistReadiness'
// TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition import
// evidence, an authoring surface (unlike PlaylistReadiness above), so it
// routes under Configure rather than Show night.
import { FPPPlaylistDefinitions } from '../views/FPPPlaylistDefinitions'
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
  ['/macros', 'Macros'],
  ['/config/show', 'Shows'],
  ['/assets', 'Assets'],
  ['/monitor', 'Monitor'],
  ['/observations', 'Observations'],
  ['/nodes', 'Monitor'],
  ['/config', 'Settings'],
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

function ShowWorkspaceOverviewRoute() {
  const { id = '' } = useParams()
  return <ShowWorkspaceOverview showId={id} />
}

export function ConfigurationRoute() {
  const { hash } = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (hash === '#show-mode') navigate('/config/mode', { replace: true })
  }, [hash, navigate])

  return <Configuration />
}

function ShowWorkspaceRedirect({ destination }: { destination: 'playlists' | 'cues' | 'assets' | 'actions' | 'surfaces' | 'night' | 'readiness' }) {
  const { id = '' } = useParams()
  const query = `?show=${encodeURIComponent(id)}`
  const targets = {
    playlists: `/config/show.playlist${query}`,
    cues: `/config/show.cue${query}`,
    assets: `/assets${query}`,
    actions: `/actions${query}`,
    surfaces: `/config/show.surface${query}`,
    night: '/night',
    readiness: `/playlists/readiness${query}`,
  }
  return <Navigate replace to={targets[destination]} />
}

export default function App() {
  const model = useModel()

  return (
    <ModelContext.Provider value={model}>
      <BrowserRouter>
        <ScrollToTop />
        <RouteTitle />
        <Routes>
          <Route element={<Layout onSubmitToken={submitToken} />}>
            <Route index element={<Dashboard />} />
            <Route path="monitor" element={<Monitor />} />
            <Route path="observations" element={<Observations />} />
            <Route path="nodes" element={<NodesList />} />
            <Route path="nodes/:nodeId" element={<NodeDetail />} />
            <Route path="fpp" element={<FPPList />} />
            <Route path="fpp/:instanceId" element={<FPPDetail />} />
            <Route path="capabilities" element={<Capabilities />} />
            <Route path="events" element={<Events />} />
            {/* Track D seam D-4: the Resolume observability/control view. */}
            <Route path="resolume" element={<ResolumeView />} />
            <Route path="config" element={<ConfigurationRoute />} />
            <Route path="config/connections" element={<ConnectionsSettings />} />
            <Route path="config/content-delivery" element={<ContentDeliverySettings />} />
            <Route path="config/render-recovery" element={<RenderRecoverySettings />} />
            <Route path="config/access" element={<AccessSettings />} />
            <Route path="config/appearance" element={<AppearanceSettings />} />
            <Route path="config/audio" element={<AudioSettingsDirectory />} />
            <Route path="config/mode" element={<ModeSettings />} />
            {/* Step 9 (STEP-9-SPEC.md section 9): the macro list/run/run-view
                surfaces plus authoring for both show.macro and show.action.
                "/macros/new" and "/actions/new" are listed BEFORE their
                ":id" siblings for readability; react-router-dom v6 ranks
                routes by specificity regardless of declaration order, so
                this ordering is not load-bearing, only easier to read. */}
            <Route path="macros" element={<Macros />} />
            <Route path="control" element={<LiveControl />} />
            <Route path="macros/new" element={<MacroDetail isNew />} />
            <Route path="macros/:id" element={<MacroDetail />} />
            <Route path="macros/:id/runs/:runId" element={<MacroRunView />} />
            <Route path="actions" element={<ShowActions />} />
            <Route path="actions/new" element={<ShowActionDetail isNew />} />
            <Route path="actions/:id" element={<ShowActionDetail />} />
            {/* Track G seam G-5: identity administration's own view. */}
            <Route path="access" element={<Access />} />
            {/* Track G seam G-8: "new" is listed before its ":id" sibling
                for readability only, same non-load-bearing note as the
                macro/action routes above. */}
            <Route path="config/show" element={<Shows />} />
            <Route path="config/show/new" element={<ShowDetail isNew />} />
            <Route path="config/show/:id" element={<ShowDetail />} />
            <Route path="config/show/:id/workspace" element={<ShowWorkspaceOverviewRoute />} />
            <Route path="config/show/:id/workspace/run-of-show" element={<ShowWorkspaceRedirect destination="playlists" />} />
            <Route path="config/show/:id/workspace/cues" element={<ShowWorkspaceRedirect destination="cues" />} />
            <Route path="config/show/:id/workspace/assets" element={<ShowWorkspaceRedirect destination="assets" />} />
            <Route path="config/show/:id/workspace/automation" element={<ShowWorkspaceRedirect destination="actions" />} />
            <Route path="config/show/:id/workspace/presentation" element={<ShowWorkspaceRedirect destination="surfaces" />} />
            <Route path="config/show/:id/workspace/show-night" element={<ShowWorkspaceRedirect destination="night" />} />
            <Route path="config/show/:id/workspace/readiness" element={<ShowWorkspaceRedirect destination="readiness" />} />
            <Route path="config/show.surface" element={<ShowSurfaces />} />
            <Route path="config/show.surface/new" element={<ShowSurfaceDetail isNew />} />
            <Route path="config/show.surface/:id" element={<ShowSurfaceDetail />} />
            <Route path="config/show.cue" element={<ShowCues />} />
            <Route path="config/show.cue/new" element={<ShowCueDetail isNew />} />
            <Route path="config/show.cue/:id" element={<ShowCueDetail />} />

            {/* ADR-039/ADR-018: audio.settings/audio.node, "new" listed
                before its ":id" sibling for the same readability reason as
                every other pair above. */}
            <Route path="config/audio.settings" element={<AudioSettings />} />
            <Route path="config/audio.node" element={<AudioNodes />} />
            <Route path="config/audio.node/new" element={<AudioNodeDetail isNew />} />
            <Route path="config/audio.node/:id" element={<AudioNodeDetail />} />

            <Route path="config/show.playlist" element={<ShowPlaylists />} />
            <Route path="config/show.playlist/new" element={<ShowPlaylistDetail isNew />} />
            <Route path="config/show.playlist/:id" element={<ShowPlaylistDetail />} />
            {/* TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition
                import evidence -- an authoring question (what has FPP
                actually reported, and does its hash still match a
                binding), not the show-night readiness question
                /playlists/readiness answers, so it sits beside the other
                Configure authoring routes instead. */}
            <Route path="config/fpp-playlist-definitions" element={<FPPPlaylistDefinitions />} />
            <Route
              path="config/fpp-playlist-definitions/:instanceUuid/:playlistHash"
              element={<FPPPlaylistDefinitionDetail />}
            />
            <Route path="config/show.active" element={<ShowActive />} />
            {/* Track F seam F2 (UI half): the night-session lifecycle
                operating view lives under Monitor/Control (Layout.tsx),
                not under /config — it observes and commands the RUNNING
                controller, never the authored definition. "new" is listed
                before its ":id" sibling for readability only, same
                non-load-bearing note as the macro/action/show routes
                above. */}
            <Route path="night" element={<NightSession />} />
            {/* TRACK-H-H2-SPEC.md §5/§6: the Playlist readiness and FPP
                instance reconciliation verdicts. Lives in the Show night
                nav group (Layout.tsx), same reasoning as /night just
                above: this is a question an operator asks BEFORE a show
                runs, never a configuration authoring surface. */}
            <Route path="playlists/readiness" element={<PlaylistReadiness />} />
            <Route path="config/night.session" element={<NightSessions />} />
            <Route path="config/night.session/new" element={<NightSessionDetail isNew />} />
            <Route path="config/night.session/:id" element={<NightSessionDetail />} />
            <Route path="config/night.session.active" element={<NightSessionActive />} />
            <Route path="assets" element={<Assets />} />
            <Route path="assets/manifest" element={<AssetManifest />} />
            <Route path="audit" element={<Audit />} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </ModelContext.Provider>
  )
}
