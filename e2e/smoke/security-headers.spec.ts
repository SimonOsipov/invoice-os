import { expect, test, type APIResponse } from '@playwright/test'
import { resolveTarget } from '../targets'

// Values copied from the root Caddyfile and internal/blogweb/server.go securityHeaders.

const NOSNIFF = 'nosniff'
const REFERRER = 'strict-origin-when-cross-origin'
const PERMISSIONS = 'camera=(), microphone=(), geolocation=(), payment=(), usb=()'

const SECURITY = [
  ['x-content-type-options', NOSNIFF],
  ['referrer-policy', REFERRER],
  ['permissions-policy', PERMISSIONS],
] as const

type Kind = 'spa' | 'strict' | 'blog'
const TARGETS: { name: string; env: string; kind: Kind }[] = [
  { name: 'landing', env: 'LANDING_URL', kind: 'strict' },
  { name: 'app', env: 'APP_URL', kind: 'spa' },
  { name: 'ops-console', env: 'OPS_CONSOLE_URL', kind: 'spa' },
  { name: 'support-console', env: 'SUPPORT_CONSOLE_URL', kind: 'spa' },
  { name: 'library', env: 'LIBRARY_URL', kind: 'strict' },
  { name: 'blog', env: 'BLOG_URL', kind: 'blog' },
  { name: 'check', env: 'CHECK_URL', kind: 'strict' },
]

const ENTRY_SRC = /<script\b[^>]*\btype="module"[^>]*\bsrc="((?:\/[a-z]+)?\/assets\/[^"]+\.js)"/

function values(res: APIResponse, header: string): string[] {
  return res
    .headersArray()
    .filter((h) => h.name.toLowerCase() === header)
    .map((h) => h.value)
}

function misses(label: string, res: APIResponse, status: number, cache?: (v: string[]) => string | null): string[] {
  const out: string[] = []
  if (res.status() !== status) out.push(`${label}: status ${res.status()}, want ${status}`)
  for (const [name, want] of SECURITY) {
    const got = values(res, name)
    if (got.length !== 1 || got[0] !== want) out.push(`${label}: ${name} [${got.join(' | ')}], want one "${want}"`)
  }
  const problem = cache?.(values(res, 'cache-control'))
  if (problem) out.push(`${label}: cache-control ${problem}`)
  return out
}

const exactly = (want: string) => (v: string[]) =>
  v.length === 1 && v[0] === want ? null : `[${v.join(' | ')}], want one "${want}"`
const cacheable = (v: string[]) =>
  v.some((x) => /no-cache|no-store/.test(x)) ? `[${v.join(' | ')}], want neither no-cache nor no-store` : null

for (const target of TARGETS) {
  test(`${target.name}: every response kind carries the security headers`, async ({ request }) => {
    const base = resolveTarget(target.env)
    const get = (path: string) => request.get(`${base}${path}`, { maxRedirects: 0 })
    const failures: string[] = []

    const home = await get('/')
    failures.push(...misses(`${base}/`, home, 200, exactly('no-cache')))

    const entry = ENTRY_SRC.exec(await home.text())?.[1]
    if (!entry) failures.push(`${base}/: no module entry script found`)
    else
      failures.push(
        ...misses(
          new URL(entry, `${base}/`).href,
          await request.get(new URL(entry, `${base}/`).href, {
            maxRedirects: 0,
          }),
          200,
          cacheable,
        ),
      )

    failures.push(...misses(`${base}/health`, await get('/health'), 200))

    if (target.kind === 'spa') {
      failures.push(...misses(`${base}/secaud33/deep/link`, await get('/secaud33/deep/link'), 200, exactly('no-cache')))
    } else {
      const cache = target.kind === 'strict' ? exactly('no-store') : undefined
      failures.push(...misses(`${base}/secaud33-no-such-page`, await get('/secaud33-no-such-page'), 404, cache))
    }

    expect(failures).toEqual([])
  })
}
