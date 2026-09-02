// Referenced by vitest.config.ts's `test.setupFiles`. Registers jest-dom's
// DOM matchers (toBeInTheDocument, etc.) globally so seam B and C test
// files don't each have to import this themselves.
import '@testing-library/jest-dom/vitest'
import { configure } from '@testing-library/react'

// A workspace screen chains several reads before its first paint, which is
// tight against Testing Library's 1000ms default on a loaded CI runner.
configure({ asyncUtilTimeout: 5000 })
