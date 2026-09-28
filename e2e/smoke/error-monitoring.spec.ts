import { expect, test, type Page } from '@playwright/test'
import { signInUrl } from '../personas'
import { resolveTarget } from '../targets'
import { APPS } from './apps'
import { isSentryHost } from './sentryHost'

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

const TARGETS: { name: string; url: () => string; mainView: (page: Page) => Promise<void> }[] = [
  { name: 'landing', url: () => resolveTarget('LANDING_URL'), mainView: mainViewOf('landing') },
  {
    name: 'app',
    // A bare APP_URL bounces to landing; the persona hand-off mounts the signed-in workspace.
    url: () => signInUrl('firm'),
    mainView: async (page) => {
      await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
    },
  },
  { name: 'ops-console', url: () => signInUrl('developer'), mainView: mainViewOf('ops-console') },
  { name: 'support-console', url: () => signInUrl('support'), mainView: mainViewOf('support-console') },
]

for (const target of TARGETS) {
  test(`${target.name}: no request reaches a Sentry host`, async ({ page }) => {
    const url = target.url()
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
