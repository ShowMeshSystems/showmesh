import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// A separate config file rather than folding into vite.config.ts (spec
// section 3 allows either): keeps the production build config free of
// test-only concerns like `environment` and `setupFiles`.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    // Seam B's tests spin a real node:http server (spec section 5.7) and
    // seam C's component tests render real DOM through Testing Library;
    // neither needs a browser, but both need real timers/network by
    // default, so nothing here mocks globals.
    restoreMocks: true,
  },
})
