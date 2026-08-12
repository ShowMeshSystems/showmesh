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
            <Route path="config" element={<Configuration />} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </ModelContext.Provider>
  )
}
