import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './app/App'

// SCRATCH: deliberate lint failure to prove later ui job steps still
// report their own outcome instead of being skipped. Remove before merge.
const scratchUnusedVar = 'deliberate-red-check'

const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('#root element not found in index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
