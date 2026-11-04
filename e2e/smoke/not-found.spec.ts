import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { resolveTarget } from '../targets'
import { libraryPathsOf } from './oldAddresses'
import { seedConsent } from './landingConsent'

// A wrong address on the landing or the library answers the noindex not-found page with 404; a real one answers 200.

const LANDING_URL = resolveTarget('LANDING_URL')
const LIBRARY_URL = resolveTarget('LIBRARY_URL')
const GATEWAY_URL = resolveTarget('GATEWAY_URL')
const APP_URL = resolveTarget('APP_URL')
const LIBRARY_CONTENT = readFileSync(new URL('../../frontend/library/src/content.ts', import.meta.url), 'utf8')
const NOINDEX = '<meta name="robots" content="noindex"'
const MARKER = 'data-page="not-found"' // NOT_FOUND_PAGE in frontend/landing/src/route.ts
const wrong = (): string => `/nope-${crypto.randomUUID()}`

function watchPageErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('pageerror', (err) => errors.push(err.message))
  return errors
}

test('N1 not found: a wrong www address answers 404 with the noindex page', async ({ request }) => {
  for (const path of [wrong(), '/.env']) {
    const res = await request.get(`${LANDING_URL}${path}`)
    const body = await res.text()
    expect(res.status(), path).toBe(404)
    expect(res.headers()['content-type'], path).toContain('text/html')
    expect(body, `${path}: noindex meta`).toContain(NOINDEX)
    expect(body, `${path}: page marker`).toContain(MARKER)
  }
})

test('N2 not found: a wrong library address answers 404 with the noindex page', async ({ request }) => {
  const group = libraryPathsOf(LIBRARY_CONTENT)[0]
  for (const path of [wrong(), `${group}${wrong()}`]) {
    const res = await request.get(`${LIBRARY_URL}${path}`)
    const body = await res.text()
    expect(res.status(), path).toBe(404)
    expect(res.headers()['content-type'], path).toContain('text/html')
    expect(body, `${path}: noindex meta`).toContain(NOINDEX)
    expect(body, `${path}: page marker`).toContain(MARKER)
  }
})

test('N3 not found: every real page answers 200', async ({ request }) => {
  const libraryPaths = libraryPathsOf(LIBRARY_CONTENT)
  expect(libraryPaths.length, 'no library group or feature parsed from content.ts').toBeGreaterThan(30)
  const landing = ['/', '/?verified=1', '/?verify=failed', '/?reset=1', '/?confirm=1', '/?confirm=invite', '/?forgot', '/?signin=ready', '/privacy', '/invite', '/robots.txt', '/build.txt', '/health', '/favicon.ico']
  const library = ['/', ...libraryPaths, '/build.txt', '/health']
  const urls = [...landing.map((p) => `${LANDING_URL}${p}`), ...library.map((p) => `${LIBRARY_URL}${p}`)]
  const failures: string[] = []
  for (const url of urls) {
    const res = await request.get(url)
    if (res.status() !== 200) failures.push(`${url}: ${res.status()} want 200`)
    else if ((res.headers()['content-type'] ?? '').includes('text/html') && (await res.text()).includes(NOINDEX)) failures.push(`${url}: carries the noindex meta`)
  }
  expect(failures, `${failures.length} of ${urls.length} real pages failed`).toEqual([])
})

async function expectNotFoundPage(page: Page, url: string): Promise<void> {
  const res = await page.goto(url)
  expect(res?.status(), url).toBe(404)
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible()
  await expect(page.getByRole('contentinfo')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1, name: 'Page not found' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Back to home' })).toHaveAttribute('href', '/')
}

test('N4 not found: the landing page renders the header and footer', async ({ page }) => {
  const errors = watchPageErrors(page)
  await seedConsent(page, false)
  await expectNotFoundPage(page, `${LANDING_URL}${wrong()}`)
  await expect(page.getByRole('link', { name: 'Browse the library' })).toHaveCount(0)
  expect(errors, `page errors:\n${errors.join('\n')}`).toEqual([])
})

test('N5 not found: the library page adds Browse the library', async ({ page }) => {
  const errors = watchPageErrors(page)
  await seedConsent(page, false)
  await expectNotFoundPage(page, `${LIBRARY_URL}${wrong()}`)
  await expect(page.getByRole('link', { name: 'Browse the library' })).toHaveAttribute('href', LIBRARY_URL)
  const src = await page.locator('script[type="module"][src]').first().getAttribute('src')
  expect(src, 'no module script on the page').not.toBeNull()
  const script = new URL(src as string, page.url())
  expect(script.origin, 'the module script comes from the library host').toBe(new URL(LIBRARY_URL).origin)
  const res = await page.request.get(script.href)
  expect(res.status(), script.href).toBe(200)
  expect(res.headers()['content-type'], script.href).toContain('javascript')
  expect(errors, `page errors:\n${errors.join('\n')}`).toEqual([])
})

test('N6 not found: Sign in works from the library page', async ({ page }) => {
  await seedConsent(page, false)
  await page.goto(`${LIBRARY_URL}${wrong()}`)
  // Armed before the click: Sign in goes to the app's ?auth=start, then back to the landing with signin=ready.
  await Promise.all([
    page.waitForRequest((r) => r.isNavigationRequest() && r.url().startsWith(APP_URL) && new URL(r.url()).searchParams.get('auth') === 'start'),
    page.waitForRequest((r) => r.isNavigationRequest() && r.url().startsWith(LANDING_URL) && new URL(r.url()).searchParams.get('signin') === 'ready'),
    page.getByRole('button', { name: 'Sign in', exact: true }).first().click(),
  ])
})

test('N7 not found: the gateway admits the library origin', async ({ request }) => {
  const origin = new URL(LIBRARY_URL).origin
  const res = await request.fetch(`${GATEWAY_URL}/contacts/demo-request`, {
    method: 'OPTIONS',
    headers: { Origin: origin, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'content-type' },
  })
  expect(res.ok(), `preflight status ${res.status()}`).toBe(true)
  expect(res.headers()['access-control-allow-origin']).toBe(origin)
})
