import { expect, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import type { ConsoleTarget } from '../staffSession'

// The SPAs under smoke test (landing, ops-console, support-console, library). The consoles open
// only on a real staff session, which `console` names (staffSession.ts). The app SPA is always gateway-wired in the deployed env,
// so its (backend-verified) assertion lives in the topology suite instead (see e2e/topology/).
// Each PR now deploys to its own ephemeral Railway environment (M4-23), so each URL is
// REQUIRED — resolveTarget throws rather than falling back to a hardcoded dev deployment
// (Decision [fail-loud-targets]).
export interface AppTarget {
  name: string
  url: string
  // Set for a console: the test opens it on a seeded staff session instead of a bare visit.
  console?: ConsoleTarget
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
      await expect(page.locator('#top .t-eyebrow')).toContainText(/e-invoicing/i)
    },
  },
  {
    name: 'ops-console',
    url: resolveTarget('OPS_CONSOLE_URL'),
    console: 'ops',
    assertMainView: async (page) => {
      // Sidebar brand + the default Overview screen heading.
      await expect(page.getByText('ASComply').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
    },
  },
  {
    name: 'support-console',
    url: resolveTarget('SUPPORT_CONSOLE_URL'),
    console: 'support',
    assertMainView: async (page) => {
      // Sidebar brand + the default Submissions ops heading. The cross-tenant strip is
      // asserted too: it is the one piece of chrome that distinguishes this console from
      // the tenant-scoped ones, so a build that lost it should fail the smoke test.
      await expect(page.getByText('ASComply').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Submissions ops' })).toBeVisible()
      await expect(page.getByText('CROSS-TENANT VIEW')).toBeVisible()
    },
  },
  {
    name: 'library',
    url: resolveTarget('LIBRARY_URL'),
    assertMainView: async (page) => {
      await expect(page).toHaveTitle('ASComply Africa — Feature Library')
      await expect(page.locator('#root')).toBeAttached()
      // The SPA fallback answers any path with index.html, so a missing entry script shows as text/html.
      const src = await page.locator('script[type="module"][src]').first().getAttribute('src')
      expect(src, 'index.html has no module entry script').not.toBeNull()
      const res = await page.request.get(new URL(src as string, page.url()).href)
      expect(res.status()).toBe(200)
      expect(res.headers()['content-type']).toContain('javascript')
      // Home route: the rendered hero proves the app mounted.
      await expect(page.getByRole('heading', { level: 1 })).toContainText('Every step from invoice')
    },
  },
]
