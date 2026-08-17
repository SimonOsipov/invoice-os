// The cookie answer a landing spec arrives with. Shared because more than one spec needs it
// and the record has to be written before the first navigation.
//
// Imports nothing but Playwright's types on purpose: test:unit runs with no deploy URLs, so
// pulling in a module that resolves a target at import time would break it (e2e/README.md).
import type { Page } from '@playwright/test'

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
