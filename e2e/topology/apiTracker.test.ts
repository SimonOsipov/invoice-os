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
    if (pathname === '/api/fast') return route.fulfill({ body: '{}' })
    if (pathname === '/api/fail') return route.abort()
    if (pathname === '/api/slow') {
      await gate
      return route.fulfill({ body: '{}' }).catch(() => {})
    }
    return route.fulfill({ contentType: 'text/html', body: page_(pathname === '/firm' ? '/api/slow' : pathname === '/fast' ? '/api/fast' : pathname === '/failing' ? '/api/fail' : pathname === '/late' ? '/api/slow' : '') })
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

  it('still fails when the SPA changes its URL (pushState) while a slow /api/ request is in flight', async () => {
    const page = await browser.newPage()
    const release = await serve(page)
    const api = trackApi(page)
    await page.goto(`${ORIGIN}/firm`)
    await page.waitForTimeout(300)
    await page.evaluate(() => history.pushState({}, '', '/firm/page-2'))
    await expect(api.quiet('firm / pushState')).rejects.toThrow(/requests still in flight after 15s \(1\)/)
    release()
    await page.close()
  }, 30_000)

  it('does not count a request that finished or failed', async () => {
    const page = await browser.newPage()
    await serve(page)
    const api = trackApi(page)
    await page.goto(`${ORIGIN}/fast`)
    await expect(api.quiet('fast')).resolves.toBeUndefined() // no later navigation clears it for us
    await page.goto(`${ORIGIN}/failing`)
    await expect(api.quiet('fast and failed')).resolves.toBeUndefined()
    await page.close()
  }, 30_000)

  it('counts a slow request started on the page after the navigation', async () => {
    const page = await browser.newPage()
    const release = await serve(page)
    const api = trackApi(page)
    await page.goto(`${ORIGIN}/inhouse`)
    await page.goto(`${ORIGIN}/late`)
    await expect(api.quiet('late')).rejects.toThrow(/late: \/api\/ requests still in flight after 15s/)
    release()
    await page.close()
  }, 30_000)
})
