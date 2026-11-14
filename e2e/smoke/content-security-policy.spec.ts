import { expect, type APIResponse } from '@playwright/test'
import { refreshCookie, type Audience } from '../sessionCookie'
import { CONSOLE_SESSION_KEY, openWithHandoff, test } from '../staffSession'
import { resolveTarget } from '../targets'

// The deployed SPA script policy, its violations and the HttpOnly-only refresh token.

// Copied from frontend/app/src/cspSources.ts APP_CSP_SOURCES.script.
const APP_SCRIPT_SOURCES = [
  'https://www.googletagmanager.com',
  'https://*.hs-scripts.com',
  'https://*.hs-analytics.net',
  'https://*.hs-banner.com',
  'https://*.hscollectedforms.net',
  'https://*.hsappstatic.net',
  'https://*.usemessages.com',
  'https://*.hubspot.com',
]
const FORBIDDEN_SCRIPT_TOKENS = ["'unsafe-inline'", "'unsafe-eval'", '*', 'https:', 'data:', 'blob:']
const ENTRY_SRC = /<script\b[^>]*\btype="module"[^>]*\bsrc="((?:\/[a-z]+)?\/assets\/[^"]+\.js)"/
const CONTROL_HOST = 'csp-control.invalid'

const TARGETS: { name: string; env: string; target: 'app' | 'ops' | 'support'; audience: Audience; sessionKey: string }[] = [
  { name: 'app', env: 'APP_URL', target: 'app', audience: 'app', sessionKey: 'invoice-os.session' },
  { name: 'ops-console', env: 'OPS_CONSOLE_URL', target: 'ops', audience: 'ops', sessionKey: CONSOLE_SESSION_KEY.ops },
  { name: 'support-console', env: 'SUPPORT_CONSOLE_URL', target: 'support', audience: 'support', sessionKey: CONSOLE_SESSION_KEY.support },
]

type Report = { effectiveDirective: string; blockedURI: string; documentURI: string; sample: string }

function policies(res: APIResponse): string[] {
  return res
    .headersArray()
    .filter((h) => h.name.toLowerCase() === 'content-security-policy')
    .map((h) => h.value)
}

function directive(policy: string, name: string): string[] | null {
  for (const part of policy.split(';')) {
    const [head, ...tokens] = part.trim().split(/\s+/)
    if (head === name) return tokens
  }
  return null
}

for (const t of TARGETS) {
  test(`${t.name}: one policy names only allowed sources`, async ({ request }) => {
    const base = resolveTarget(t.env)
    const html = await (await request.get(`${base}/`)).text()
    const entry = ENTRY_SRC.exec(html)?.[1]
    expect(entry, `no module entry script in ${base}/`).toBeTruthy()

    const urls = [`${base}/`, new URL(entry!, base).toString(), `${base}/health`, `${base}/secaud17/deep/link`]
    const seen: string[] = []
    for (const url of urls) {
      const found = policies(await request.get(url))
      expect(found.length, `${url}: want exactly one content-security-policy, got ${found.length}`).toBe(1)
      seen.push(found[0])
    }
    expect(new Set(seen).size, `the policy differs across ${urls.join(', ')}`).toBe(1)

    const policy = seen[0]
    const script = directive(policy, 'script-src')
    expect(script, 'no script-src').not.toBeNull()
    expect(script![0]).toBe("'self'")
    if (t.name === 'app') {
      for (const token of script!.slice(1)) {
        expect(/^'sha256-[A-Za-z0-9+/=]+'$/.test(token) || APP_SCRIPT_SOURCES.includes(token), `script-src token ${token} is not allowed`).toBe(true)
      }
    } else {
      expect(script).toEqual(["'self'"])
    }
    for (const bad of FORBIDDEN_SCRIPT_TOKENS) expect(script, `script-src has ${bad}`).not.toContain(bad)

    expect(directive(policy, 'connect-src')).toEqual(expect.arrayContaining(["'self'", new URL(resolveTarget('GATEWAY_URL')).origin]))
    expect(directive(policy, 'frame-ancestors')).toEqual(["'none'"])
    expect(directive(policy, 'base-uri')).toEqual(["'none'"])
    expect(directive(policy, 'object-src'), 'no object-src').not.toBeNull()
  })

  test(`${t.name}: a signed-in load reports no policy violation`, async ({ page, staffAccount }) => {
    const origin = new URL(resolveTarget(t.env)).origin
    const reports: Report[] = []
    const consoleLines: string[] = []
    await page.exposeBinding('__cspViolation', (_src, r: Report) => {
      reports.push(r)
    })
    await page.context().addInitScript(() => {
      document.addEventListener('securitypolicyviolation', (e) => {
        const report = { effectiveDirective: e.effectiveDirective, blockedURI: e.blockedURI, documentURI: e.documentURI, sample: e.sample }
        void (window as unknown as { __cspViolation: (r: unknown) => void }).__cspViolation(report)
      })
    })
    page.on('console', (msg) => {
      if (msg.text().includes('Content Security Policy')) consoleLines.push(msg.text())
    })

    await openWithHandoff(page, t.target, staffAccount)
    // Control needle, fired on the target document (the landing has no connect-src): a blocked connect-src must reach the listener, or an empty list proves nothing.
    await page.evaluate((host) => fetch(`https://${host}/`).catch(() => {}), CONTROL_HOST)
    await expect.poll(() => reports.some((r) => r.effectiveDirective.startsWith('connect-src') && r.blockedURI.includes(CONTROL_HOST))).toBe(true)
    if (t.name === 'app') {
      // Runs the hashed inline hop in index.html, which lands on the landing with signin=ready.
      await page.goto(`${origin}/?auth=start`)
      await page.waitForURL((u) => u.toString().startsWith(resolveTarget('LANDING_URL')) && u.searchParams.get('signin') === 'ready')
    }

    const isControl = (r: Report) => r.blockedURI.includes(CONTROL_HOST)
    expect(reports.filter((r) => !isControl(r))).toEqual([])
    expect(consoleLines.filter((l) => !l.includes(CONTROL_HOST))).toEqual([])
  })

  test(`${t.name}: the refresh token lives only in an HttpOnly cookie`, async ({ page, context, staffAccount }) => {
    await openWithHandoff(page, t.target, staffAccount)
    const cookie = await refreshCookie(context, t.audience)
    expect(cookie, `no __Host-rt-${t.audience} cookie`).not.toBeNull()
    expect(cookie!.httpOnly).toBe(true)
    expect(cookie!.secure).toBe(true)

    const stored = await page.evaluate(() => ({
      local: Object.values(localStorage),
      session: Object.values(sessionStorage),
      cookie: document.cookie,
    }))
    for (const value of [...stored.local, ...stored.session]) expect(value).not.toContain(cookie!.value)
    expect(stored.cookie).not.toContain(cookie!.value)

    const record = await page.evaluate((key) => localStorage.getItem(key), t.sessionKey)
    expect(record, `no ${t.sessionKey} record`).not.toBeNull()
    expect(Object.keys(JSON.parse(record!))).not.toContain('refresh_token')
  })
}
