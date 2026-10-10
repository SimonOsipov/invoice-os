// The cookie answer a landing spec arrives with. Shared because more than one spec needs it
// and the record has to be written before the first navigation.
//
// Imports only Playwright's types and ./sentryHost on purpose: test:unit runs with no deploy URLs, so
// pulling in a module that resolves a target at import time would break it (e2e/README.md).
import type { BrowserContext, Page, Route } from '@playwright/test'
import { isProductionHost, isSentryHost } from './sentryHost'

const CONSENT_KEY = 'asc_consent' // retyped from frontend/landing/src/consent.ts
export const STUB_HEADER = 'x-asc-stub'

/**
 * Seeds a cookie answer before the first navigation, so the notice never renders.
 *
 * The consent default is denied, so with no stored record the tag never loads and
 * landing-demo's EXPECT_TAG biconditional would be false on production. addInitScript rather
 * than evaluate: there is no origin to write to before the first navigation, and
 * bootAnalytics() runs at module scope, so the record must land before the bundle executes.
 */
export async function seedConsent(page: Page, analytics: boolean): Promise<void> {
  // The answer travels as an addInitScript ARGUMENT, never as a closure: the function is
  // serialised into the page, where a closed-over `analytics` arrives undefined.
  await page.addInitScript((analytics: boolean) => {
    // `asc_consent` / `v: 1` retyped from frontend/landing/src/consent.ts: e2e pins what the
    // deployed build serves.
    try {
      window.localStorage.setItem('asc_consent', JSON.stringify({ analytics, ts: new Date().toISOString(), v: 1 }))
    } catch {
      // about:blank has an opaque origin; the real navigation re-runs this. A seed that never
      // landed cannot pass silently — the biconditional goes red on the production target.
    }
  }, analytics)
}

/** Attach before navigating; returns the sink to assert on. */
export function consoleGate(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

/** The hosts the routed tests must never reach: the GA tag, GA collection and Sentry. */
export function isStubbedHost(rawUrl: string): boolean {
  let host: string
  try {
    host = new URL(rawUrl).hostname.toLowerCase()
  } catch {
    return false
  }
  return host === 'www.googletagmanager.com' || host === 'google-analytics.com' || host.endsWith('.google-analytics.com') || isSentryHost(rawUrl)
}

/**
 * Serves the production hostnames from the fork, so Chromium applies real cookie rules to the
 * deployed bundles, and answers every GA and Sentry request with an empty 200. Needs no
 * local server; on a production target the forward is the identity.
 */
export async function routeProductionHosts(
  context: BrowserContext,
  targets: { landing: string; library: string },
): Promise<void> {
  const forward = (origin: string) => async (route: Route) => {
    const { pathname, search } = new URL(route.request().url())
    await route.fulfill({ response: await route.fetch({ url: origin + pathname + search }) })
  }
  await context.route('https://www.ascomply.com/**', forward(targets.landing))
  await context.route('https://library.ascomply.com/**', forward(targets.library))
  // CORS headers: a cross-origin Sentry POST answered without them logs a console error.
  await context.route((url) => isStubbedHost(url.href), (route) =>
    route.fulfill({
      status: 200,
      body: '',
      contentType: 'application/javascript',
      headers: {
        [STUB_HEADER]: '1',
        'access-control-allow-origin': '*',
        'access-control-allow-headers': '*',
        'access-control-allow-methods': '*',
      },
    }),
  )
}

/** The stored consent record as JSON text: the cookie on a production host, `localStorage` elsewhere. */
export async function consentRecordOf(page: Page): Promise<string | null> {
  if (isProductionHost(page.url())) {
    const cookie = (await page.context().cookies(page.url())).find((c) => c.name === CONSENT_KEY)
    return cookie ? decodeURIComponent(cookie.value) : null
  }
  return page.evaluate((key) => window.localStorage.getItem(key), CONSENT_KEY)
}
