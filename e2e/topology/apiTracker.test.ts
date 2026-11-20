// BUG-165: AS-06's in-flight /api/ counter outlived the page. Chromium fires neither requestfinished nor
// requestfailed for a request that page.goto cuts off, so the cut-off request stayed counted. Real browser, no deployed target.
import { type Browser, chromium, type Page } from '@playwright/test'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { trackApi } from './apiTracker'

const ORIGIN = 'https://app.test'
const page_ = (api: string) => `<main class="pf-main">${api ? `<script>fetch('${api}').catch(() => {})</script>` : ''}</main>`

let browser: Browser
beforeAll(async () => { browser = await chromium.launch() })
afterAll(async () => { await browser.close() })

// /api/slow never answers until `release`; every other path is a static page.
async function serve(page: Page): Promise<() => void> {
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  await page.route(`${ORIGIN}/**`, async (route) => {
    const { pathname } = new URL(route.request().url())
    if (pathname === '/api/slow') {
      await gate
      return route.fulfill({ body: '{}' }).catch(() => {})
    }
    return route.fulfill({ contentType: 'text/html', body: page_(pathname === '/firm' ? '/api/slow' : '') })
  })
  return release
}

describe('trackApi across a navigation (BUG-165)', () => {
  it('is quiet on the next page after a navigation cuts off an in-flight /api/ request', async () => {
    const page = await browser.newPage()
    const release = await serve(page)
    const api = trackApi(page)
    await page.goto(`${ORIGIN}/firm`)
    await page.waitForTimeout(300) // /api/slow is now in flight
    await page.goto(`${ORIGIN}/inhouse`) // cuts it off, as signInAs does
    await expect(api.quiet('inhouse / Overview')).resolves.toBeUndefined()
    release()
    await page.close()
  }, 30_000)

  it('still fails on a slow /api/ request of the page being swept', async () => {
    const page = await browser.newPage()
    const release = await serve(page)
    const api = trackApi(page)
    await page.goto(`${ORIGIN}/firm`)
    await expect(api.quiet('firm / Overview')).rejects.toThrow(/firm \/ Overview: \/api\/ requests still in flight after 15s/)
    release()
    await page.close()
  }, 30_000)
})
