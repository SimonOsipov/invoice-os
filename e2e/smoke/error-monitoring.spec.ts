import { expect, type Page } from '@playwright/test'
import { browserToken, signInAs } from '../personaSession'
import { CONSOLE_SESSION_KEY, seedStaffStorage, test, type ConsoleTarget } from '../staffSession'
import { resolveTarget } from '../targets'
import { APPS } from './apps'
import { enclosesRect, gaps, WIDE_WIDTHS } from '../topology/layout'
import { isProductionHost, isSentryHost } from './sentryHost'

// A PR environment runs with Sentry off, so no SPA may send anything to a Sentry host.
// Assertions read the request sink only; the safety-net route just keeps a live SDK's
// traffic from leaving the browser.

const OBSERVE_MS = 5000
const CONTROL_URL = 'https://o1.ingest.de.sentry.io/api/1/envelope/'

function mainViewOf(name: string): (page: Page) => Promise<void> {
  const app = APPS.find((a) => a.name === name)
  if (!app) throw new Error(`no APPS entry named ${name}`)
  return app.assertMainView
}

const TARGETS: {
  name: string
  url: () => string
  console?: ConsoleTarget
  signIn?: (page: Page) => Promise<void>
  mainView: (page: Page) => Promise<void>
}[] = [
  { name: 'landing', url: () => resolveTarget('LANDING_URL'), mainView: mainViewOf('landing') },
  {
    name: 'app',
    // A bare APP_URL bounces to landing; the stored session from signIn mounts the workspace.
    url: () => resolveTarget('APP_URL'),
    signIn: (page) => signInAs(page, 'firm'),
    mainView: async (page) => {
      await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
    },
  },
  { name: 'ops-console', url: () => resolveTarget('OPS_CONSOLE_URL'), console: 'ops', mainView: mainViewOf('ops-console') },
  { name: 'support-console', url: () => resolveTarget('SUPPORT_CONSOLE_URL'), console: 'support', mainView: mainViewOf('support-console') },
]

for (const target of TARGETS) {
  test(`${target.name}: no request reaches a Sentry host`, async ({ page, staffAccount }) => {
    const url = target.url()
    test.skip(isProductionHost(url), `${new URL(url).hostname} is a production host, where Sentry may be on`)
    const allRequests: { url: string; type: string }[] = []
    const sentryRequests: string[] = []
    page.on('request', (req) => {
      allRequests.push({ url: req.url(), type: req.resourceType() })
      if (isSentryHost(req.url())) sentryRequests.push(req.url())
    })
    await page.route(
      (u) => isSentryHost(u.toString()),
      (route) => route.fulfill({ status: 200, body: '{}', headers: { 'access-control-allow-origin': '*' } }),
    )

    await target.signIn?.(page)
    if (target.console) await seedStaffStorage(page, target.console, staffAccount)
    await page.goto(url)
    await target.mainView(page)
    // A fixed window, not networkidle: an SDK flushes on a timer, and a signed-in SPA may never go idle.
    await page.waitForTimeout(OBSERVE_MS)
    const seen = [...sentryRequests]

    const origin = new URL(url).origin
    expect(
      allRequests.some((r) => r.type === 'script' && new URL(r.url).origin === origin),
      `the sink recorded no same-origin script request from ${origin}, so "no Sentry request" proves nothing`,
    ).toBe(true)
    expect(seen, `${target.name} sent requests to a Sentry host:\n${seen.join('\n')}`).toEqual([])

    // Control needle: the predicate and the sink must catch a real ingest request.
    await page.evaluate(
      (u) => fetch(u, { method: 'POST', mode: 'no-cors', body: '{}' }).catch(() => {}),
      CONTROL_URL,
    )
    await expect.poll(() => sentryRequests).toContain(CONTROL_URL)
  })
}

const CRASH_MESSAGE = 'e2e: induced render crash'
// internal/gateway/cors.go corsAllowHeaders grants Authorization, Content-Type, sentry-trace, baggage.
const TRACE_HEADERS = {
  'sentry-trace': '0123456789abcdef0123456789abcdef-0123456789abcdef-1',
  baggage: 'sentry-environment=production',
}

// The app reads URLSearchParams in a render-time initializer, so throwing there crashes its first render.
// A console reads it in the async gate, so its crash is armed when the gate stores the renewed session:
// the console's first render then calls Array.prototype.filter (jobs.filter in each console's App).
for (const target of TARGETS.filter((t) => t.name !== 'landing')) {
  test(`${target.name}: an induced render crash shows the recovery screen`, async ({ page, staffAccount }, testInfo) => {
    const url = target.url()
    test.skip(isProductionHost(url), `${new URL(url).hostname} is a production host, where Sentry may be on`)
    const consoleErrors: string[] = []
    const pageErrors: string[] = []
    const sentryRequests: string[] = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(msg.text())
    })
    page.on('pageerror', (err) => pageErrors.push(err.message))
    page.on('request', (req) => {
      if (isSentryHost(req.url())) sentryRequests.push(req.url())
    })
    // The crash init script comes after sign-in, so the sign-in itself is not crashed.
    await target.signIn?.(page)
    // Added after the seed, so the seed's own setItem is the original.
    if (target.console) await seedStaffStorage(page, target.console, staffAccount)
    await page.addInitScript(
      ({ message, key }) => {
        if (key === null) {
          ;(window as unknown as { URLSearchParams: unknown }).URLSearchParams = function () {
            throw new Error(message)
          }
          return
        }
        const setItem = Storage.prototype.setItem
        Storage.prototype.setItem = function (k: string, v: string) {
          setItem.call(this, k, v)
          if (k === key) {
            Array.prototype.filter = function () {
              throw new Error(message)
            }
          }
        }
      },
      { message: CRASH_MESSAGE, key: target.console ? CONSOLE_SESSION_KEY[target.console] : null },
    )

    await page.goto(url)
    const region = page.getByRole('region', { name: 'Something went wrong' })
    const reload = page.getByRole('button', { name: 'Reload page' })
    await expect(region).toBeVisible()
    await expect(reload).toBeVisible()
    await expect(reload).toBeEnabled()

    // React's own crash log stays on the console, so the no-error gate still fails on a real crash.
    expect(consoleErrors.some((e) => e.includes(CRASH_MESSAGE)), `no console error carried the crash:\n${consoleErrors.join('\n')}`).toBe(true)
    expect(pageErrors, 'the boundary should catch the crash').toEqual([])
    expect(sentryRequests, 'the DSN is blank in a PR environment').toEqual([])

    const card = region.locator('xpath=..')
    const entry = page.viewportSize()
    const sweep: unknown[] = []
    try {
      for (const width of WIDE_WIDTHS) {
        await page.setViewportSize({ width, height: 1080 })
        const viewport = { x: 0, y: 0, width, height: 1080 }
        const cardBox = await card.boundingBox()
        const buttonBox = await reload.boundingBox()
        expect(cardBox, `${target.name} @${width}: the card has no box`).not.toBeNull()
        expect(buttonBox, `${target.name} @${width}: the button has no box`).not.toBeNull()
        const gap = gaps(cardBox!, viewport)
        const scroll = await page.evaluate(() => {
          const el = document.scrollingElement!
          return el.scrollWidth - el.clientWidth
        })
        sweep.push({ width, card: cardBox, button: buttonBox, gap, scroll })
        expect(enclosesRect(viewport, cardBox!, 1), `${target.name} @${width}: the card leaves the viewport`).toBe(true)
        expect(Math.abs(gap.left - gap.right), `${target.name} @${width}: the card is off-centre (${gap.left} vs ${gap.right})`).toBeLessThanOrEqual(1)
        expect(enclosesRect(cardBox!, buttonBox!, 1), `${target.name} @${width}: the button leaves the card`).toBe(true)
        expect(scroll, `${target.name} @${width}: the page scrolls sideways`).toBeLessThanOrEqual(1)
      }
    } finally {
      await testInfo.attach(`${target.name}-recovery-layout`, { body: JSON.stringify(sweep, null, 2), contentType: 'application/json' })
      if (entry) await page.setViewportSize(entry)
    }

    // The init script persists across the reload, so the crash recurs.
    await Promise.all([page.waitForEvent('load'), reload.click()])
    await expect(region).toBeVisible()
  })
}

test('app: a traced gateway call passes the CORS preflight', async ({ page }) => {
  const url = resolveTarget('APP_URL')
  test.skip(isProductionHost(url), `${new URL(url).hostname} is a production host`)
  const gateway = resolveTarget('GATEWAY_URL')
  await signInAs(page, 'firm')
  const token = await browserToken(page)

  const call = (extra: Record<string, string>) =>
    page.evaluate(
      async ({ endpoint, headers }) => {
        try {
          return (await fetch(endpoint, { headers })).status
        } catch {
          return 'blocked'
        }
      },
      { endpoint: `${gateway}/api/tenancy/v1/me`, headers: { Authorization: `Bearer ${token}`, ...TRACE_HEADERS, ...extra } },
    )
  expect(await call({})).toBe(200)
  // Control: an ungranted header fails the preflight, so the 200 above was a real preflight pass.
  expect(await call({ 'x-sentry06-control': '1' })).toBe('blocked')
})
