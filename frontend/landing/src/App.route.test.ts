// RED spec (task-557, LAND-04-03) — cheap raw-text wiring pin, same technique as
// analytics.test.ts's App.tsx scans. Proves the wiring exists, not that it renders
// correctly (that's e2e/smoke/landing-privacy.spec.ts, task-559).
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const APP_SRC = readFileSync(join(HERE, 'App.tsx'), 'utf8')

describe('App.tsx route wiring', () => {
  it('AC-1/2: imports Privacy and isPrivacyPath, reads the pathname, wires hrefPrefix onto Nav', () => {
    // Control needle first: a misresolved/empty read would otherwise pass vacuously.
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')

    expect(APP_SRC).toMatch(/import\s*\{\s*Privacy\s*\}\s*from\s*['"]\.\/components\/Privacy['"]/)
    expect(APP_SRC).toMatch(/import\s*\{\s*isPrivacyPath\s*\}\s*from\s*['"]\.\/route['"]/)
    expect(APP_SRC).toMatch(/isPrivacyPath\(window\.location\.pathname\)/)
    expect(APP_SRC).toMatch(/<Privacy\b/)
    expect(APP_SRC).toMatch(/hrefPrefix=\{privacy/)
  })

  it('AC-10 (task-558): hrefPrefix is wired onto <Footer> specifically, not just present anywhere (Nav already has one)', () => {
    // Scoping to the <Footer> tag itself is what stops the generic hrefPrefix={privacy
    // check above from passing vacuously off Nav's own prop on this same assertion.
    const footerTag = APP_SRC.match(/<Footer\b[^>]*>/)
    expect(footerTag, 'expected to find a <Footer> element').not.toBeNull()
    expect(footerTag![0]).toMatch(/hrefPrefix=\{privacy/)
  })
})
