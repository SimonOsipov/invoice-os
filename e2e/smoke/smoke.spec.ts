import { test, expect } from '@playwright/test'
import { APPS } from './apps'
import { resolveTarget } from '../targets'

// One smoke test per deployed SPA: the main mock view renders and the page logs
// no console errors or uncaught exceptions during load.
for (const app of APPS) {
  test(`${app.name}: main view renders with no console errors`, async ({ page }) => {
    const errors: string[] = []
    // Attach listeners before navigating so load-time errors are captured.
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => {
      errors.push(`pageerror: ${err.message}`)
    })

    const response = await page.goto(app.url)
    expect(response, `no response from ${app.url}`).toBeTruthy()
    expect(response!.ok(), `${app.url} returned HTTP ${response!.status()}`).toBeTruthy()

    // Auto-waits for the signature element, so client-side render has completed
    // (and any load-time console errors have fired) by the time this resolves.
    await app.assertMainView(page)

    expect(errors, `console errors on ${app.name}:\n${errors.join('\n')}`).toEqual([])
  })
}

// The sign-in gate. The Ops Console used to render for anyone who had the URL — it had no
// session concept at all, so there was nothing to be signed out OF. It now refuses to draw
// without the landing page's hand-off and sends a bare visit back to the front door.
//
// Pinned as its own spec because the APPS entry above deliberately arrives WITH the
// hand-off: without this, the console could quietly become open again and every smoke test
// would still pass. Playwright gives each test a fresh context, so the session the entry
// above persists cannot leak in here.
//
// Not a security assertion — a fabricated localStorage entry still gets in, and there is no
// backend behind this console to protect (M7/M8 own that). This pins ROUTING.
// The developer console's org card became a real switcher, matching the Platform app's
// company switcher rather than being a static label. Pinned here because the repo has no
// component-test harness (every frontend vitest project runs in `node`, with no DOM), so a
// browser check is the only place this control can be exercised at all. Asserting the menu
// OPENS, not merely that a chevron is drawn — the whole point of the change is that the
// affordance is honest.
test('ops-console: the org card is a switcher whose menu opens', async ({ page }) => {
  await page.goto(`${resolveTarget('OPS_CONSOLE_URL')}?persona=developer`)

  const switcher = page.getByRole('button', { expanded: false }).filter({ hasText: 'Zephyr Pay' })
  await expect(switcher).toBeVisible()

  const menu = page.getByRole('menu')
  await expect(menu).toBeHidden()
  await switcher.click()
  await expect(menu).toBeVisible()
  await expect(menu.getByText('Switch organisation')).toBeVisible()
  await expect(menu.getByRole('menuitem')).toHaveCount(1)
})

for (const [name, target] of [
  ['ops-console', 'OPS_CONSOLE_URL'],
  ['support-console', 'SUPPORT_CONSOLE_URL'],
] as const) {
  test(`${name}: a visit with no session redirects to the landing page`, async ({ page }) => {
    const consoleUrl = resolveTarget(target)
    const landingUrl = resolveTarget('LANDING_URL')

    await page.goto(consoleUrl)
    await page.waitForURL((url) => url.href.startsWith(landingUrl), { timeout: 20_000 })

    expect(page.url(), `expected a redirect from ${consoleUrl} to ${landingUrl}`).toContain(landingUrl)
  })
}
