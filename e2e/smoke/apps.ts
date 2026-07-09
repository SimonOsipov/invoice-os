import { expect, type Page } from '@playwright/test'

// The three deployed SPAs under smoke test. Each URL defaults to its live dev
// deployment and is overridable via an env var, so the same suite runs locally
// and against any deploy (e.g. a PR preview) without code changes.
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
    name: 'app',
    url: resolveUrl('APP_URL', 'https://app-development-3b4b.up.railway.app'),
    assertMainView: async (page) => {
      // The app gates on a mock sign-in (M2-13): pick the firm persona to enter the
      // workspace. With no gateway configured on the deployed build the sign-in is a
      // pure client-side mock (no backend call), so the shell mounts either way.
      await page.getByRole('button', { name: /Chinedu Okafor/ }).click()
      // Sidebar brand + the signed-in firm persona prove the workspace shell mounted;
      // the dashboard is the default view.
      await expect(page.getByText('InvoiceOS').first()).toBeVisible()
      await expect(page.getByText('Chinedu Okafor')).toBeVisible()
    },
  },
  {
    name: 'ops-console',
    url: resolveUrl('OPS_CONSOLE_URL', 'https://ops-console-development.up.railway.app'),
    assertMainView: async (page) => {
      // Sidebar brand + the default Submissions screen heading.
      await expect(page.getByText('InvoiceOS').first()).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Submissions ops' })).toBeVisible()
    },
  },
]
