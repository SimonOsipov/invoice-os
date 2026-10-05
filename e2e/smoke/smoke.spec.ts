import { expect } from '@playwright/test'
import { APPS } from './apps'
import { seedStaffSession, test } from '../staffSession'
import { resolveTarget } from '../targets'

// One smoke test per deployed SPA: the main mock view renders and the page logs
// no console errors or uncaught exceptions during load. A console opens on a seeded real staff session.
for (const app of APPS) {
  test(`${app.name}: main view renders with no console errors`, async ({ page, staffAccount }) => {
    const errors: string[] = []
    // Attach listeners before navigating so load-time errors are captured.
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => {
      errors.push(`pageerror: ${err.message}`)
    })

    if (app.console) {
      await seedStaffSession(page, app.console, staffAccount)
    } else {
      const response = await page.goto(app.url)
      expect(response, `no response from ${app.url}`).toBeTruthy()
      expect(response!.ok(), `${app.url} returned HTTP ${response!.status()}`).toBeTruthy()
    }

    // Auto-waits for the signature element, so client-side render has completed
    // (and any load-time console errors have fired) by the time this resolves.
    await app.assertMainView(page)

    expect(errors, `console errors on ${app.name}:\n${errors.join('\n')}`).toEqual([])
  })
}

// The developer console's org card became a real switcher, matching the Platform app's
// company switcher rather than being a static label. Pinned here because the repo has no
// component-test harness (every frontend vitest project runs in `node`, with no DOM), so a
// browser check is the only place this control can be exercised at all. Asserting the menu
// OPENS, not merely that a chevron is drawn — the whole point of the change is that the
// affordance is honest.
test('ops-console: the org card is a switcher whose menu opens', async ({ page, staffAccount }) => {
  await seedStaffSession(page, 'ops', staffAccount)

  const switcher = page.getByRole('button', { expanded: false }).filter({ hasText: 'Zephyr Pay' })
  await expect(switcher).toBeVisible()

  const menu = page.getByRole('menu')
  await expect(menu).toBeHidden()
  await switcher.click()
  await expect(menu).toBeVisible()
  await expect(menu.getByText('Switch organisation')).toBeVisible()
  await expect(menu.getByRole('menuitem')).toHaveCount(1)
})

// The sign-in gate: a console draws only for a real staff session, and a bare visit goes
// back to the front door. Pinned as its own spec because the APPS entry above arrives WITH a
// session: without this, the console could quietly become open again and every smoke test
// would still pass. Playwright gives each test a fresh context, so the session the entry
// above seeds cannot leak in here. This pins ROUTING; the forged and customer sessions are
// refused by topology/auth.spec.ts.
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

// Each console build must hold this environment's gateway host: without it no hand-off code can
// be redeemed. The host is baked in from VITE_GATEWAY_URL at the image build.
for (const app of APPS.filter((a) => a.console)) {
  test(`${app.name}: the served main script holds this environment's gateway host`, async ({ request }) => {
    const page = await request.get(app.url)
    expect(page.ok(), `${app.url} returned HTTP ${page.status()}`).toBe(true)
    const src = (await page.text()).match(/<script\b[^>]*\btype="module"[^>]*\bsrc="([^"]+)"/)?.[1]
    expect(src, `no module script in the ${app.name} document`).toBeTruthy()
    const scriptUrl = new URL(src!, app.url).href
    const script = await request.get(scriptUrl)
    expect(script.ok(), `${scriptUrl} returned HTTP ${script.status()}`).toBe(true)
    const host = new URL(resolveTarget('GATEWAY_URL')).host
    expect(await script.text(), `${scriptUrl} does not contain ${host}`).toContain(host)
  })
}
