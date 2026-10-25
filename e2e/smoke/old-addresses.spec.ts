import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import { resolveTarget } from '../targets'
import { libraryPathsOf, newAddressOf, oldUrls } from './oldAddresses'

// Production only, after infra stage B (D-13, D-23): previews have no load balancer, so no 301 exists there.

const LANDING_URL = resolveTarget('LANDING_URL')
const SITEMAP = 'https://www.ascomply.com/blog/sitemap.xml'
const LIBRARY_CONTENT = readFileSync(new URL('../../frontend/library/src/content.ts', import.meta.url), 'utf8')

test.skip(LANDING_URL !== 'https://www.ascomply.com', 'production only: a preview has no load balancer to redirect')

test('old addresses: each old URL answers 301 to its new address, and each new address answers 200', async ({ request }) => {
  test.setTimeout(300_000)
  const sitemap = await request.get(SITEMAP)
  expect(sitemap.status(), SITEMAP).toBe(200)
  const locs = [...(await sitemap.text()).matchAll(/<loc>([^<]*)<\/loc>/g)].map((m) => m[1])
  expect(locs.length, 'the blog sitemap lists no pages, so the crawl proves nothing').toBeGreaterThan(0)
  const libraryPaths = libraryPathsOf(LIBRARY_CONTENT)
  expect(libraryPaths.length, 'no library group or feature parsed from content.ts').toBeGreaterThan(30)

  const urls = oldUrls(locs, libraryPaths)
  const failures: string[] = []
  for (const url of urls) {
    const expected = newAddressOf(url)
    const res = await request.get(url, { maxRedirects: 0 })
    const location = res.headers()['location']
    if (res.status() !== 301 || location !== expected) {
      failures.push(`${url}: ${res.status()} location=${location} want 301 ${expected}`)
      continue
    }
    const target = await request.get(location)
    if (target.status() !== 200) failures.push(`${location}: ${target.status()} want 200`)
  }
  expect(failures, `${failures.length} of ${urls.length} old URLs failed`).toEqual([])
})

test('old addresses: the landing links name the new addresses', async ({ page }) => {
  await page.goto(`${LANDING_URL}/`)
  const nav = page.getByRole('navigation', { name: 'Primary' })
  await expect(nav.getByRole('link', { name: 'Library' })).toHaveAttribute('href', 'https://www.ascomply.com/library')
  await expect(nav.getByRole('link', { name: 'Blog' })).toHaveAttribute('href', 'https://www.ascomply.com/blog')
})
