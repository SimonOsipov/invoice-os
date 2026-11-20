import { expect, type Page } from '@playwright/test'

// Verbatim copy of trackApi in app-shell.spec.ts, so the red test can import it. The fix replaces this body and the spec imports it from here.
export function trackApi(page: Page): { quiet: (where: string) => Promise<void> } {
  const inflight = new Set<unknown>()
  const isApi = (url: string) => new URL(url).pathname.includes('/api/')
  page.on('request', (r) => { if (isApi(r.url())) inflight.add(r) })
  const done = (r: { url(): string }) => inflight.delete(r)
  page.on('requestfinished', done)
  page.on('requestfailed', done)
  return {
    quiet: async (where) => {
      const deadline = Date.now() + 15_000
      let idleSince = Date.now()
      while (Date.now() - idleSince < 300) {
        if (Date.now() > deadline) throw new Error(`${where}: /api/ requests still in flight after 15s (${inflight.size})`)
        if (inflight.size > 0) idleSince = Date.now()
        await page.waitForTimeout(50)
      }
      const main = page.locator('main.pf-main')
      await expect(main.getByText(/^Loading .+…$/), `${where}: a loading label never went away`).toHaveCount(0, { timeout: 15_000 })
      await expect(main.getByTestId('audit-skeleton-row'), `${where}: audit skeleton rows never went away`).toHaveCount(0, { timeout: 15_000 })
    },
  }
}
