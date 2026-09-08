import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Static-asset build only (ADR-015): no dev-time proxy target is baked in
// here, because the built output never talks to a coordinator address
// decided at build time. Runtime routing to the coordinator is the nginx
// container's job (nginx.conf, ADR-022), not Vite's — `npm run dev`
// against a real coordinator, when that's needed, is a local convenience
// outside this step's scope, not part of the shipped contract.
// A local fixture coordinator for the rebuild's visual gate, opt-in and
// dev-only: `SHOWMESH_DEV_API=http://localhost:8099 npx vite`. The built
// output still routes to the coordinator through nginx, never through this.
const devApi = process.env.SHOWMESH_DEV_API

export default defineConfig({
  ...(devApi === undefined ? {} : { server: { proxy: { '/api': devApi } } }),
  plugins: [react()],
  build: {
    // Hashed filenames (Vite's default) are what let nginx.conf cache
    // /assets/ immutably while index.html stays no-store.
    outDir: 'dist',
  },
})
