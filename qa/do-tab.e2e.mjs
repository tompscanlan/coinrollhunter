// Stateful browser regression suite for the workflow-first UI. The scenarios run
// in order against one throwaway database; each module owns one coherent surface.
import { chromium } from 'playwright'
import { createHarness } from './e2e/support.mjs'
import { runWorkflows } from './e2e/workflows.mjs'
import { runReportingAndPhotos } from './e2e/reporting-photos.mjs'
import { runGridPersistence } from './e2e/grid-persistence.mjs'
import { runGridBehavior } from './e2e/grid-behavior.mjs'

const base = process.env.BASE_URL || 'http://127.0.0.1:8799'
const screenshotDir = process.env.SHOT_DIR || '.'
const browser = await chromium.launch()
const page = await browser.newPage()
const consoleErrors = []
const pageErrors = []

page.on('console', (message) => {
  if (message.type() === 'error') consoleErrors.push(message.text())
})
page.on('pageerror', (error) => pageErrors.push(error.message))

const harness = createHarness(page, base)

const modules = [
  ['workflows', () => runWorkflows(harness)],
  ['reporting and photos', () => runReportingAndPhotos(harness, screenshotDir)],
  ['grid persistence', () => runGridPersistence(harness)],
  ['grid behavior', () => runGridBehavior(harness)],
]

try {
  for (const [name, run] of modules) {
    try {
      await run()
    } catch (error) {
      harness.ok(`${name}: UNCAUGHT`, false, error.message)
      await page
        .screenshot({ path: `${screenshotDir}/do-error-${name.replaceAll(' ', '-')}.png`, fullPage: true })
        .catch(() => {})
    }
  }
} finally {
  harness.ok('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 5).join(' | '))
  harness.ok('no page errors', pageErrors.length === 0, pageErrors.slice(0, 5).join(' | '))
  const passed = harness.results.filter((result) => result.pass).length
  console.log(`\n${passed}/${harness.results.length} checks passed`)
  await browser.close()
  process.exit(passed === harness.results.length ? 0 : 1)
}
