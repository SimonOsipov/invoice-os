// Source-level contract for the consent default, added at QA after a mutation
// sweep. `expect(analyticsAllowed(null)).toBe(CONSENT_DEFAULT_ANALYTICS)` was the
// guard that kept the default one literal rather than a branch. It stopped doing
// that job the moment the flip made both sides `false`: replacing
// `: CONSENT_DEFAULT_ANALYTICS` with `: false` leaves every consent assertion
// green. These read the source instead of the value.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { CONSENT_DEFAULT_ANALYTICS } from './consent'

const SRC_DIR = fileURLToPath(new URL('.', import.meta.url))
const CONSENT_TS = join(SRC_DIR, 'consent.ts')
const CONSENT_TEST = join(SRC_DIR, 'consent.test.ts')
const ANALYTICS_DOM_TEST = join(SRC_DIR, 'analytics.dom.test.ts')

const consentSrc = readFileSync(CONSENT_TS, 'utf8')

describe('the default reaches analyticsAllowed only through the exported constant', () => {
  it('control: the source read found the module and its gate function', () => {
    expect(consentSrc.length).toBeGreaterThan(1000)
    expect(consentSrc).toContain('export function analyticsAllowed')
    expect(consentSrc).toContain('export const CONSENT_DEFAULT_ANALYTICS')
  })

  it('analyticsAllowed inlines no boolean literal', () => {
    const body = consentSrc.slice(consentSrc.indexOf('export function analyticsAllowed'))
    expect(body).toContain('CONSENT_DEFAULT_ANALYTICS')
    expect(body, 'a boolean literal here decouples the gate from the exported default').not.toMatch(
      /\b(?:true|false)\b/,
    )
  })

  it('the module names the default exactly twice - one declaration, one use', () => {
    expect(consentSrc.match(/CONSENT_DEFAULT_ANALYTICS/g) ?? []).toHaveLength(2)
  })

  it('the declaration is a literal, not an expression', () => {
    expect(consentSrc).toMatch(/^export const CONSENT_DEFAULT_ANALYTICS: boolean = (?:true|false)$/m)
  })
})

// AC-6: a test name that states the opposite of what it asserts is read as
// documentation. The flip renamed two of them.
describe('AC-6: no test title in a changed file contradicts the denied default', () => {
  // Third element is a control needle: a title this file is known to carry, so a
  // read that returned the wrong file cannot report a clean absence below.
  const files: readonly (readonly [string, string, string])[] = [
    ['consent.test.ts', readFileSync(CONSENT_TEST, 'utf8'), 'no stored record means analytics is denied'],
    ['analytics.dom.test.ts', readFileSync(ANALYTICS_DOM_TEST, 'utf8'), 'falls back to the denied default'],
  ]

  it('control: both files were read and each carries its own denied-default title', () => {
    expect(files.length).toBe(2)
    for (const [label, src, needle] of files) {
      expect(src.length, `${label}: read nothing`).toBeGreaterThan(1000)
      expect(src, `${label}: control needle "${needle}" not found - wrong file read`).toContain(needle)
    }
  })

  it('the two renamed titles say denied, and their pre-flip wording is gone', () => {
    const [, consentTest] = files[0]
    const [, domTest] = files[1]
    expect(consentTest).toContain("it('no stored record means analytics is denied'")
    expect(consentTest).not.toContain('no stored record means analytics is allowed')
    expect(domTest).toContain("it('AC-2: a null consent record falls back to the denied default'")
    expect(domTest).not.toContain('falls back to the granted default')
  })

  it.each([['analytics is allowed'], ['the granted default'], ['granted by default']])(
    'neither file claims "%s" anywhere',
    (phrase) => {
      for (const [label, src] of files) {
        expect(src.toLowerCase(), `${label} still claims "${phrase}"`).not.toContain(phrase)
      }
    },
  )
})

describe('the exported default is the denied one', () => {
  it('is boolean false, not merely falsy', () => {
    expect(CONSENT_DEFAULT_ANALYTICS).toBe(false)
    expect(typeof CONSENT_DEFAULT_ANALYTICS).toBe('boolean')
  })
})
