import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import { resolveTarget } from '../targets'

// Each deployed frontend serves the shared favicon; the SPA fallback would answer 200 with index.html.

const TARGETS = [
  { name: 'landing', env: 'LANDING_URL' },
  { name: 'app', env: 'APP_URL' },
  { name: 'ops-console', env: 'OPS_CONSOLE_URL' },
  { name: 'support-console', env: 'SUPPORT_CONSOLE_URL' },
]

const FAVICON_REPO_PATH = 'packages/design-tokens/assets/favicon/favicon.ico'
const EXPECTED = readFileSync(new URL(`../../${FAVICON_REPO_PATH}`, import.meta.url))

for (const target of TARGETS) {
  test(`${target.name}: serves the shared favicon`, async ({ request }) => {
    const base = resolveTarget(target.env)
    expect(EXPECTED.length, `${FAVICON_REPO_PATH} is empty`).toBeGreaterThan(0)

    const res = await request.get(`${base}/favicon.ico`)
    expect(res.status()).toBe(200)
    expect(res.headers()['content-type']).not.toContain('text/html')
    // Boolean, not buffers: a failed buffer compare would print the whole index.html.
    expect((await res.body()).equals(EXPECTED), `${base}/favicon.ico differs from ${FAVICON_REPO_PATH}`).toBe(true)

    // Control: the fallback answers 200 text/html, which is not the favicon.
    const missing = await request.get(`${base}/no-such-favicon.ico`)
    expect(missing.status()).toBe(200)
    expect(missing.headers()['content-type']).toContain('text/html')
    expect((await missing.body()).equals(EXPECTED)).toBe(false)
  })
}
