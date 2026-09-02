import { mkdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { chromium } from '@playwright/test'

const baseUrl = process.env.SHOWMESH_VISUAL_BASE_URL ?? 'http://127.0.0.1:18101'
const outputDir = path.resolve(process.cwd(), 'visual-artifacts')
const routes = [
  ['dashboard', '/'],
  ['show-night', '/night'],
  ['live-control', '/control'],
  ['monitor-fleet', '/monitor/fleet'],
  ['monitor-signals', '/monitor/signals'],
  ['monitor-activity', '/monitor/activity'],
  ['monitor-capabilities', '/monitor/capabilities'],
  ['monitor-manifest', '/monitor/manifest'],
  ['node-detail', '/monitor/fleet/node/barn-controller'],
  ['shows', '/shows'],
  ['show-detail', '/shows/winter-ridge-2026'],
  ['show-playlists', '/shows/winter-ridge-2026/playlists'],
  ['show-cues', '/shows/winter-ridge-2026/cues'],
  ['show-assets', '/shows/winter-ridge-2026/assets'],
  ['show-presentation', '/shows/winter-ridge-2026/presentation'],
  ['show-automation', '/shows/winter-ridge-2026/automation'],
  ['assets', '/assets'],
  ['settings-connections', '/settings/connections'],
  ['settings-delivery', '/settings/delivery'],
  ['settings-recovery', '/settings/recovery'],
  ['settings-appearance', '/settings/appearance'],
  ['settings-audio-defaults', '/settings/audio-defaults'],
  ['settings-node-routing', '/settings/node-routing'],
  ['settings-mode', '/settings/mode'],
  ['resolume-config', '/settings/resolume/arena-main'],
  ['access', '/access'],
  ['resolume-control', '/control/resolume'],
]
const viewports = [
  ['desktop', { width: 1440, height: 1000 }],
  ['breakpoint-1100', { width: 1100, height: 900 }],
]

await mkdir(outputDir, { recursive: true })
// Signs the page in when SHOWMESH_VISUAL_TOKEN_FILE names a file holding an API
// token: the client reads sessionStorage['showmesh.apiToken'] (ui/src/api/token.ts).
async function signedInContext(browser, options) {
  const context = await browser.newContext(options)
  const tokenFile = process.env.SHOWMESH_VISUAL_TOKEN_FILE
  if (tokenFile !== undefined) {
    const token = (await readFile(tokenFile, 'utf8')).trim()
    await context.addInitScript((t) => sessionStorage.setItem('showmesh.apiToken', t), token)
  }
  return context
}

const browser = await chromium.launch()

try {
  for (const [viewportName, viewport] of viewports) {
    const context = await signedInContext(browser, { viewport })
    const page = await context.newPage()
    for (const [name, route] of routes) {
      await page.goto(new URL(route, baseUrl).toString(), { waitUntil: 'load' })
      await page.waitForTimeout(1200)
      await page.screenshot({ path: path.join(outputDir, `${name}-${viewportName}.png`), fullPage: true })
    }
    await page.close()
  }
} finally {
  await browser.close()
}

console.log(`Captured ${routes.length * viewports.length} screenshots in ${outputDir}`)
