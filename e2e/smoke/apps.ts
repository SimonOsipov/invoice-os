import { expect, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'

// The two pure SPAs under smoke test (landing, ops-console) — no backend round trip, so a
// render check is sufficient. The app SPA is always gateway-wired in the deployed env, so
// its (backend-verified) assertion lives in the topology suite instead (see e2e/topology/).
// Each PR now deploys to its own ephemeral Railway environment (M4-23), so each URL is
// REQUIRED — resolveTarget throws rather than falling back to a hardcoded dev deployment
// (Decision [fail-loud-targets]).
export interface AppTarget {
  name: string
  url: string
  // Asserts a signature element of the app's main mock view is rendered — proof
  // the SPA booted and mounted, not just that the shell HTML was served.
  assertMainView: (page: Page) => Promise<void>
}

export const APPS: AppTarget[] = [
  {
    name: 'landing',
    url: resolveTarget('LANDING_URL'),
    assertMainView: async (page) => {
      const h1 = page.getByRole('heading', { level: 1 })
      await expect(h1).toBeVisible()
      await expect(h1).toContainText(/e-invoicing/i)
    },
  },
  {
    name: 'ops-console',
    url: resolveTarget('OPS_CONSOLE_URL'),
    assertMainView: async (page) => {
      // Sidebar brand + the default Overview screen heading.
      await expect(page.getByText('FiscalBridge').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
    },
  },
]
