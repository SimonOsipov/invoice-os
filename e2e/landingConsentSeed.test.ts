// AC-4 and AC-5 of LAND-05-01 had no runnable oracle. The seeding they describe
// only changes behaviour when the Playwright suite is pointed at
// www.ascomply.com: on every preview host isProductionHost closes the gate
// regardless of consent, so deleting the seed is green in CI and red only in
// production. These read the spec's source instead, the way rule-set.test.ts and
// workspaceCoverage.test.ts do for facts CI cannot otherwise reach.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const SPEC = join(REPO, 'e2e/smoke/landing-demo.spec.ts')
const CONSENT_TS = join(REPO, 'frontend/landing/src/consent.ts')

const spec = readFileSync(SPEC, 'utf8')
const specLines = spec.split('\n')
const consentSrc = readFileSync(CONSENT_TS, 'utf8')

const SEED_FN = 'seedGrantedConsent'

/** Body of a top-level `async function <name>(...)`, to its closing brace at column 0. */
function functionBody(src: string, name: string): string {
  const start = src.indexOf(`async function ${name}(`)
  expect(start, `function ${name} not found`).toBeGreaterThan(-1)
  const end = src.indexOf('\n}\n', start)
  expect(end, `function ${name} has no column-0 close`).toBeGreaterThan(start)
  return src.slice(start, end)
}

describe('control: the spec was read and still has the shape these assertions walk', () => {
  it('the file is non-trivial and defines both helpers', () => {
    expect(spec.length).toBeGreaterThan(20000)
    expect(specLines.length).toBeGreaterThan(500)
    expect(spec).toContain('async function openLanding(')
    expect(spec).toContain(`async function ${SEED_FN}(`)
  })

  it('every navigation in this file goes through openLanding', () => {
    // The floor that makes the ordering assertion below cover the whole suite:
    // a second page.goto would be an unseeded entry point.
    expect(spec.match(/page\.goto\(/g) ?? []).toHaveLength(1)
    expect(spec.match(/await openLanding\(page\)/g) ?? []).toHaveLength(7)
    expect(spec.match(/expectClosedGateStayedSilent\(sinks\)/g) ?? []).toHaveLength(7)
  })
})

describe('AC-4: openLanding seeds a granted record before navigating', () => {
  const body = functionBody(spec, 'openLanding')

  it('the seed call precedes page.goto', () => {
    const seedAt = body.indexOf(`${SEED_FN}(page)`)
    const gotoAt = body.indexOf('page.goto(')
    expect(seedAt, 'openLanding does not call the seed helper').toBeGreaterThan(-1)
    expect(gotoAt).toBeGreaterThan(-1)
    expect(seedAt, 'the seed lands after navigation, so bootAnalytics has already run').toBeLessThan(gotoAt)
  })

  it('the seed uses addInitScript, not a pre-goto evaluate', () => {
    const seed = functionBody(spec, SEED_FN)
    expect(seed).toContain('page.addInitScript(')
    // A pre-navigation page.evaluate touching localStorage throws: there is no origin yet.
    expect(seed, 'a pre-goto page.evaluate has no origin to write to').not.toContain('page.evaluate(')
  })

  it('the record seeded is GRANTED — a denied seed would falsify the biconditional', () => {
    const seed = functionBody(spec, SEED_FN)
    expect(seed).toMatch(/analytics:\s*true/)
    expect(seed, 'a denied seed keeps the tag dark on production').not.toMatch(/analytics:\s*false/)
  })

  it("the retyped key and version still match consent.ts's contract", () => {
    // The spec retypes rather than imports, on purpose. Retyped constants drift
    // silently; this is the only thing that notices.
    const seed = functionBody(spec, SEED_FN)
    const key = consentSrc.match(/export const CONSENT_STORAGE_KEY = '([^']+)'/)?.[1]
    const version = consentSrc.match(/export const CONSENT_VERSION = (\d+)/)?.[1]
    expect(key, 'CONSENT_STORAGE_KEY not found in consent.ts').toBe('asc_consent')
    expect(version, 'CONSENT_VERSION not found in consent.ts').toBe('1')
    expect(seed, `the spec seeds a key other than ${key}`).toContain(`'${key}'`)
    expect(seed, `the spec seeds a version other than ${version}`).toMatch(new RegExp(`v:\\s*${version}\\b`))
  })

  it("EXPECT_TAG's definition is unchanged, on line 38", () => {
    expect(specLines[37]).toBe("const EXPECT_TAG = LANDING_HOST === 'www.ascomply.com'")
  })

  it('the biconditional was not weakened to a one-way assertion', () => {
    expect(spec).toContain('.toBe(EXPECT_TAG)')
  })
})

describe('AC-5: the seeding is documented in-file as the reason the biconditional holds', () => {
  it('a doc comment immediately above the helper names the gate it restores', () => {
    const at = spec.indexOf(`async function ${SEED_FN}(`)
    const doc = spec.slice(Math.max(0, at - 700), at)
    const block = doc.slice(doc.lastIndexOf('/**'))
    expect(block, 'no JSDoc block precedes the seed helper').toContain('/**')
    expect(block).toContain('EXPECT_TAG')
    expect(block.toLowerCase()).toContain('biconditional')
    expect(block.toLowerCase(), 'the doc does not say WHY the seed is needed').toContain('denied')
    expect(block, 'the doc does not record the addInitScript reason').toContain('addInitScript')
  })
})
