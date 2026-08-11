// Referenced by vitest.config.ts's `test.setupFiles`. Registers jest-dom's
// DOM matchers (toBeInTheDocument, etc.) globally so seam B and C test
// files don't each have to import this themselves.
import '@testing-library/jest-dom/vitest'
