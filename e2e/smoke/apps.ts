import { expect, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'

// The three pure SPAs under smoke test (landing, ops-console, support-console) — no
// backend round trip, so a render check is sufficient. The app SPA is always gateway-wired
// in the deployed env, so its (backend-verified) assertion lives in the topology suite
// instead (see e2e/topology/).
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
    // The console now sits behind the landing page's sign-in hand-off: a bare URL is not a
    // sign-in and redirects to the front door. Arrive the way the landing actually routes
    // here (destUrl -> ?persona=developer) rather than through a test-only backdoor, so
    // this smoke test still exercises the real entry path. The redirect itself is pinned by
    // its own spec in smoke.spec.ts.
    url: `${resolveTarget('OPS_CONSOLE_URL')}?persona=developer`,
    assertMainView: async (page) => {
      // Sidebar brand + the default Overview screen heading.
      await expect(page.getByText('ASComply').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
    },
  },
  {
    name: 'support-console',
    // Same sign-in hand-off shape as the developer console, with this console's own
    // persona id — the two gates reject each other's links, which is the point.
    url: `${resolveTarget('SUPPORT_CONSOLE_URL')}?persona=support`,
    assertMainView: async (page) => {
      // Sidebar brand + the default Submissions ops heading. The cross-tenant strip is
      // asserted too: it is the one piece of chrome that distinguishes this console from
      // the tenant-scoped ones, so a build that lost it should fail the smoke test.
      await expect(page.getByText('ASComply').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Submissions ops' })).toBeVisible()
      await expect(page.getByText('CROSS-TENANT VIEW')).toBeVisible()
    },
  },
]
