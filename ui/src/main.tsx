import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
// Default export: src/app/App.tsx is seam C's file and does not exist yet
// in this seam's working tree, so this import cannot resolve until it
// lands. That is the one expected typecheck/build failure for this seam
// (spec section 4.1) — see this builder's report.
import App from './app/App'

// Bootstrap only (spec section 4.1): this file mounts <App/> and owns
// nothing about what the app shows or how it talks to the API. Those
// belong to seam C (src/app, src/views, src/components) and seam B
// (src/api) respectively.
const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('#root element not found in index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
