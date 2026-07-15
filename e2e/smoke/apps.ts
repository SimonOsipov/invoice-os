import { expect, type Page } from '@playwright/test'

// The two pure SPAs under smoke test (landing, ops-console) — no backend round trip, so a
// render check is sufficient. The app SPA is always gateway-wired in the unified dev env, so
// its (backend-verified) assertion lives in the topology suite instead (see e2e/topology/).
// Each URL defaults to its live dev deployment and is overridable via an env var, so the
// same suite runs locally and against any deploy (e.g. a PR preview) without code changes.
export interface AppTarget {
  name: string
  url: string
  // Asserts a signature element of the app's main mock view is rendered — proof
  // the SPA booted and mounted, not just that the shell HTML was served.
  assertMainView: (page: Page) => Promise<void>
}

const resolveUrl = (envVar: string, fallback: string): string => process.env[envVar]?.trim() || fallback

export const APPS: AppTarget[] = [
  {
    name: 'landing',
    url: resolveUrl('LANDING_URL', 'https://landing-development-92a2.up.railway.app'),
    assertMainView: async (page) => {
      const h1 = page.getByRole('heading', { level: 1 })
      await expect(h1).toBeVisible()
      await expect(h1).toContainText(/e-invoicing/i)
    },
  },
  {
    name: 'ops-console',
    url: resolveUrl('OPS_CONSOLE_URL', 'https://ops-console-development.up.railway.app'),
    assertMainView: async (page) => {
      // Sidebar brand + the default Submissions screen heading.
      await expect(page.getByText('FiscalBridge').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Submissions ops' })).toBeVisible()
    },
  },
]
