import { mkdir, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { chromium } from '@playwright/test'

const appBaseUrl = process.env.SHOWMESH_VISUAL_BASE_URL ?? 'http://127.0.0.1:18102'
const mockBaseUrl = process.env.SHOWMESH_MOCK_BASE_URL ?? 'http://127.0.0.1:18103'
const outputDir = path.resolve(process.cwd(), 'visual-artifacts', 'comparison')
const viewport = { width: 1440, height: 1000 }

// A mock may cover more than one route where the current product has a facet
// or direct settings page. Each route still receives an independent render.
const pages = [
  ['dashboard', '/', 'Dashboard.dc.html'],
  ['show-night', '/night', 'Show Night.dc.html'],
  ['live-control', '/control', 'Live Control.dc.html'],
  ['resolume-control', '/control/resolume', 'Resolume Control.dc.html'],
  ['monitor-fleet', '/monitor/fleet', 'Monitor.dc.html'],
  ['monitor-signals', '/monitor/signals', 'Monitor.dc.html'],
  ['monitor-activity', '/monitor/activity', 'Monitor.dc.html'],
  ['monitor-capabilities', '/monitor/capabilities', 'Monitor.dc.html'],
  ['monitor-manifest', '/monitor/manifest', 'Monitor.dc.html'],
  ['node-detail', '/monitor/fleet/node/barn-controller', 'Node.dc.html'],
  ['shows', '/shows', 'Shows.dc.html'],
  ['show-detail', '/shows/winter-ridge-2026', 'Show Authoring.dc.html'],
  ['show-playlists', '/shows/winter-ridge-2026/playlists', 'Show Authoring.dc.html'],
  ['show-cues', '/shows/winter-ridge-2026/cues', 'Show Cues.dc.html'],
  ['show-assets', '/shows/winter-ridge-2026/assets', 'Show Assets.dc.html'],
  ['show-presentation', '/shows/winter-ridge-2026/presentation', 'Show Presentation.dc.html'],
  ['show-automation', '/shows/winter-ridge-2026/automation', 'Show Automation.dc.html'],
  ['new-show', '/shows/new', 'Object Creation.dc.html'],
  ['assets', '/assets', 'Show Assets.dc.html'],
  ['settings-connections', '/settings/connections', 'Settings.dc.html'],
  ['settings-delivery', '/settings/delivery', 'Settings.dc.html'],
  ['settings-recovery', '/settings/recovery', 'Settings.dc.html'],
  ['settings-appearance', '/settings/appearance', 'Settings.dc.html'],
  ['settings-audio-defaults', '/settings/audio-defaults', 'Settings.dc.html'],
  ['settings-node-routing', '/settings/node-routing', 'Settings.dc.html'],
  ['settings-mode', '/settings/mode', 'Settings.dc.html'],
  ['resolume-config', '/settings/resolume/arena-main', 'Resolume Config.dc.html'],
  ['access', '/access', 'Access.dc.html'],
]

const requested = process.argv.find((argument) => argument.startsWith('--page='))?.slice('--page='.length)
const selectedPages = requested === undefined ? pages : pages.filter(([name]) => name === requested)
if (selectedPages.length === 0) throw new Error(`No comparison page matched ${requested}`)

async function settle(page) {
  await page.evaluate(async () => {
    await document.fonts?.ready
  })
  await page.waitForTimeout(500)
}

async function measurements(page) {
  return page.evaluate(() => {
    const rect = (element) => {
      const box = element.getBoundingClientRect()
      const style = getComputedStyle(element)
      return {
        tag: element.tagName.toLowerCase(),
        text: (element.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 80),
        x: Math.round(box.x * 10) / 10,
        y: Math.round(box.y * 10) / 10,
        width: Math.round(box.width * 10) / 10,
        height: Math.round(box.height * 10) / 10,
        background: style.backgroundColor,
        color: style.color,
        font: style.font,
        border: style.border,
        borderRadius: style.borderRadius,
      }
    }
    const all = (selector, limit = 24) => [...document.querySelectorAll(selector)].slice(0, limit).map(rect)
    return {
      viewport: { width: innerWidth, height: innerHeight },
      scroll: { width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight },
      body: rect(document.body),
      chrome: all('[data-chrome], .sm-chrome', 1),
      rail: all('[data-rail], .sm-rail', 1),
      main: all('main', 1),
      headings: all('main h1, main h2, main h3'),
      sections: all('main section'),
      controls: all('main button, main input, main select, main textarea'),
      tables: all('main table'),
    }
  })
}

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
const results = []

try {
  for (const [name, route, mock] of selectedPages) {
    const appContext = await signedInContext(browser, { viewport, deviceScaleFactor: 1 })
    const appPage = await appContext.newPage()
    const mockPage = await browser.newPage({ viewport, deviceScaleFactor: 1 })
    const appUrl = new URL(route, appBaseUrl).toString()
    const mockUrl = new URL(encodeURIComponent(mock), `${mockBaseUrl}/`).toString()
    await Promise.all([
      appPage.goto(appUrl, { waitUntil: 'load' }),
      mockPage.goto(mockUrl, { waitUntil: 'load' }),
    ])
    await Promise.all([settle(appPage), settle(mockPage)])

    const prefix = path.join(outputDir, name)
    const [appMetrics, mockMetrics] = await Promise.all([measurements(appPage), measurements(mockPage)])
    await Promise.all([
      appPage.screenshot({ path: `${prefix}-app.png`, fullPage: true }),
      mockPage.screenshot({ path: `${prefix}-mock.png`, fullPage: true }),
    ])
    const difference = spawnSync('python3', ['scripts/visual-image-diff.py', `${prefix}-mock.png`, `${prefix}-app.png`, prefix], {
      cwd: process.cwd(),
      encoding: 'utf8',
    })
    if (difference.status !== 0) throw new Error(difference.stderr || `Image diff failed for ${name}`)
    results.push({ name, route, mock, appMetrics, mockMetrics, imageDifference: JSON.parse(difference.stdout) })
    await Promise.all([appPage.close(), mockPage.close()])
  }
} finally {
  await browser.close()
}

await writeFile(path.join(outputDir, 'measurements.json'), `${JSON.stringify({ viewport, results }, null, 2)}\n`)
console.log(`Compared ${results.length} routes at ${viewport.width}×${viewport.height}; results in ${outputDir}`)
