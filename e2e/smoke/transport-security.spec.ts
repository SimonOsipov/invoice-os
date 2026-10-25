import { expect, test } from '@playwright/test'
import { resolveTarget } from '../targets'

// Production only: a preview has no load balancer, so no apex redirect or HSTS header exists there.

const LANDING_URL = resolveTarget('LANDING_URL')
const HSTS = 'max-age=63072000; includeSubDomains; preload'

const HTTPS_URLS = [
  'https://ascomply.com/',
  'https://ascomply.com/blog/x?q=1',
  'https://www.ascomply.com/',
  'https://www.ascomply.com/blog/',
  'https://www.ascomply.com/library/',
  'https://api.ascomply.com/',
  'https://api.ascomply.com/health',
  'https://app.ascomply.com/',
  'https://ops.ascomply.com/',
  'https://sup.ascomply.com/',
  'https://blog.ascomply.com/',
  'https://blog.ascomply.com/x?q=1',
  'https://library.ascomply.com/',
  'https://library.ascomply.com/library/rules',
]

test.skip(LANDING_URL !== 'https://www.ascomply.com', 'production only: a preview has no load balancer')

test('apex: http answers 301 to https on the same host', async ({ request }) => {
  const res = await request.get('http://ascomply.com/hsts-check?q=1', { maxRedirects: 0 })
  expect(res.status()).toBe(301)
  const location = new URL(res.headers()['location'] ?? '')
  expect(location.protocol).toBe('https:')
  expect(location.hostname).toBe('ascomply.com')
  expect(location.pathname).toBe('/hsts-check')
  expect(location.search).toBe('?q=1')
})

test('apex: https answers 301 to www with path and query', async ({ request }) => {
  const res = await request.get('https://ascomply.com/blog/x?q=1', { maxRedirects: 0 })
  expect(res.status()).toBe(301)
  expect(res.headers()['location']).toBe('https://www.ascomply.com/blog/x?q=1')
})

test('every https response carries one HSTS header', async ({ request }) => {
  const failures: string[] = []
  for (const url of HTTPS_URLS) {
    const res = await request.get(url, { maxRedirects: 0 })
    const sts = res.headersArray().filter((h) => h.name.toLowerCase() === 'strict-transport-security')
    if (sts.length !== 1 || sts[0].value !== HSTS) {
      failures.push(`${url}: ${res.status()} ${sts.length} header(s) ${sts.map((h) => h.value).join(' | ')}`)
    }
  }
  expect(failures).toEqual([])
})
