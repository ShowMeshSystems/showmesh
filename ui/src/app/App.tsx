import { BrowserRouter, Route, Routes } from 'react-router-dom'
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
import { NodesList } from '../views/NodesList'
import { NodeDetail } from '../views/NodeDetail'
import { FPPList } from '../views/FPPList'
import { FPPDetail } from '../views/FPPDetail'
import { Capabilities } from '../views/Capabilities'
import { Events } from '../views/Events'
import { Configuration } from '../views/Configuration'
import { Macros } from '../views/Macros'
import { MacroDetail } from '../views/MacroDetail'
import { MacroRunView } from '../views/MacroRunView'
import { ShowActions } from '../views/ShowActions'
import { ShowActionDetail } from '../views/ShowActionDetail'
import { ResolumeView } from '../views/ResolumeView'
// Track G seam G-8 (TRACK-G-surface-parity.md "G-8"): routes for Track E's
// previously UI-less surface — shows, surfaces, active-show activation,
// the asset browser, the per-node manifest, and the audit log.
import { Shows } from '../views/Shows'
import { ShowDetail } from '../views/ShowDetail'
import { ShowSurfaces } from '../views/ShowSurfaces'
import { ShowSurfaceDetail } from '../views/ShowSurfaceDetail'
import { ShowActive } from '../views/ShowActive'
import { Assets } from '../views/Assets'
import { AssetManifest } from '../views/AssetManifest'
import { Audit } from '../views/Audit'
import { NotFound } from '../views/NotFound'
import '../styles/index.css'

export default function App() {
  const model = useModel()

  return (
    <ModelContext.Provider value={model}>
      <BrowserRouter>
        <Routes>
          <Route element={<Layout onSubmitToken={submitToken} />}>
            <Route index element={<Dashboard />} />
            <Route path="nodes" element={<NodesList />} />
            <Route path="nodes/:nodeId" element={<NodeDetail />} />
            <Route path="fpp" element={<FPPList />} />
            <Route path="fpp/:instanceId" element={<FPPDetail />} />
            <Route path="capabilities" element={<Capabilities />} />
            <Route path="events" element={<Events />} />
            {/* Track D seam D-4: the Resolume observability/control view. */}
            <Route path="resolume" element={<ResolumeView />} />
            <Route path="config" element={<Configuration />} />
            {/* Step 9 (STEP-9-SPEC.md section 9): the macro list/run/run-view
                surfaces plus authoring for both show.macro and show.action.
                "/macros/new" and "/actions/new" are listed BEFORE their
                ":id" siblings for readability; react-router-dom v6 ranks
                routes by specificity regardless of declaration order, so
                this ordering is not load-bearing, only easier to read. */}
            <Route path="macros" element={<Macros />} />
            <Route path="macros/new" element={<MacroDetail isNew />} />
            <Route path="macros/:id" element={<MacroDetail />} />
            <Route path="macros/:id/runs/:runId" element={<MacroRunView />} />
            <Route path="actions" element={<ShowActions />} />
            <Route path="actions/new" element={<ShowActionDetail isNew />} />
            <Route path="actions/:id" element={<ShowActionDetail />} />
            {/* Track G seam G-8: "new" is listed before its ":id" sibling
                for readability only, same non-load-bearing note as the
                macro/action routes above. */}
            <Route path="config/show" element={<Shows />} />
            <Route path="config/show/new" element={<ShowDetail isNew />} />
            <Route path="config/show/:id" element={<ShowDetail />} />
            <Route path="config/show.surface" element={<ShowSurfaces />} />
            <Route path="config/show.surface/new" element={<ShowSurfaceDetail isNew />} />
            <Route path="config/show.surface/:id" element={<ShowSurfaceDetail />} />
            <Route path="config/show.active" element={<ShowActive />} />
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
