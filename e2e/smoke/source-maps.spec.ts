import { expect, test } from '@playwright/test'
import { resolveTarget } from '../targets'

// The deployed bundles must not leak source maps; Sentry holds them privately.

const TARGETS = [
  { name: 'landing', env: 'LANDING_URL' },
  { name: 'app', env: 'APP_URL' },
  { name: 'ops-console', env: 'OPS_CONSOLE_URL' },
  { name: 'support-console', env: 'SUPPORT_CONSOLE_URL' },
  { name: 'library', env: 'LIBRARY_URL' },
]

const SCRIPT_REF = /(?:src|href)="(\/assets\/[^"]+\.js)"/g
const ENTRY_SRC = /<script\b[^>]*\btype="module"[^>]*\bsrc="(\/assets\/[^"]+\.js)"/

for (const target of TARGETS) {
  test(`${target.name}: bundles carry a debug id and no .map is served`, async ({ request }) => {
    const base = resolveTarget(target.env)
    const index = await request.get(`${base}/`)
    expect(index.status()).toBe(200)
    const html = await index.text()

    const scripts = [...new Set([...html.matchAll(SCRIPT_REF)].map((m) => m[1]))]
    expect(scripts.length, `no same-origin /assets/*.js script in ${base}/, so the sweep proves nothing`).toBeGreaterThan(0)

    for (const path of scripts) {
      const script = await request.get(`${base}${path}`)
      expect(script.status(), path).toBe(200)
      expect(script.headers()['content-type'], path).toContain('javascript')
      expect(/\/\/[#@]\s*sourceMappingURL=/.test(await script.text()), `${path} still carries a sourceMappingURL comment`).toBe(false)
      const map = await request.get(`${base}${path}.map`)
      expect(map.status(), `${path}.map`).toBe(404)
    }

    const entry = html.match(ENTRY_SRC)?.[1]
    expect(entry, 'index.html names no module entry script').toBeTruthy()
    // Booleans, not toContain: a failed toContain prints the whole minified bundle.
    const entryBody = await (await request.get(`${base}${entry}`)).text()
    expect(entryBody.includes('sentry-dbid-'), `${entry} carries no Sentry debug id`).toBe(true)

    // Controls: the fallback still serves index.html, so the 404s above come from the .map rule.
    const fallback = await request.get(`${base}/assets/sentry08-no-such-file.js`)
    expect(fallback.status()).toBe(200)
    expect(fallback.headers()['content-type']).toContain('text/html')
    expect((await request.get(`${base}/sentry08/no-such.map`)).status()).toBe(404)
    expect((await request.get(`${base}/health`)).status()).toBe(200)
  })
}
