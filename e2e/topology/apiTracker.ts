import { expect, type Page } from '@playwright/test'

// Counts in-flight /api/ requests. `quiet` waits until none has been in flight for 300 ms, then until no
// loading text or audit skeleton row is left; it fails with a named message on timeout.
export function trackApi(page: Page): { quiet: (where: string) => Promise<void> } {
  const inflight = new Set<unknown>()
  const isApi = (url: string) => new URL(url).pathname.includes('/api/')
  // Chromium emits neither finished nor failed for a request a navigation cuts off, so a new document drops
  // them. A same-document change (pushState) is no request and keeps them: its /api/ calls are still live.
  page.on('request', (r) => {
    if (r.isNavigationRequest() && r.frame() === page.mainFrame()) inflight.clear()
    else if (isApi(r.url())) inflight.add(r)
  })
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
