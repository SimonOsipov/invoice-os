// RED specs (task-555, LAND-04-01, Test-first) — pin isPrivacyPath's total/pure
// contract before it exists. Mirrors components/activeSection.test.ts's plain
// describe/it shape: pure function, no DOM, no renderToStaticMarkup.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { isPrivacyPath } from './route'

const HERE = dirname(fileURLToPath(import.meta.url))
const ROUTE_SRC = readFileSync(join(HERE, 'route.ts'), 'utf8')

describe('isPrivacyPath', () => {
  it('the canonical path is the privacy page', () => {
    expect(isPrivacyPath('/privacy')).toBe(true)
  })

  it('a single trailing slash is the same page', () => {
    expect(isPrivacyPath('/privacy/')).toBe(true)
  })

  it('a double trailing slash is not the same page', () => {
    // Only ONE trailing slash is stripped, not greedily — a /\/+$/ implementation
    // would still pass the single-slash row above and only fail here.
    expect(isPrivacyPath('/privacy//')).toBe(false)
  })

  it('the site root is not the privacy page', () => {
    expect(isPrivacyPath('/')).toBe(false)
  })

  it('a longer path that starts with the same letters is not a match', () => {
    expect(isPrivacyPath('/privacy-policy')).toBe(false)
    expect(isPrivacyPath('/privacyx')).toBe(false)
    expect(isPrivacyPath('/privacy/extra')).toBe(false)
  })

  it('matching is case-insensitive', () => {
    expect(isPrivacyPath('/Privacy')).toBe(true)
    expect(isPrivacyPath('/PRIVACY')).toBe(true)
  })

  it('an empty string is not a match and does not throw', () => {
    expect(() => isPrivacyPath('')).not.toThrow()
    expect(isPrivacyPath('')).toBe(false)
  })

  it('surrounding whitespace is trimmed', () => {
    expect(isPrivacyPath(' /privacy ')).toBe(true)
  })

  it('the function reads no browser global', () => {
    // Control needle first: an empty or misresolved read would otherwise pass vacuously.
    expect(ROUTE_SRC.length).toBeGreaterThan(0)
    expect(ROUTE_SRC).toContain('isPrivacyPath')
    expect(ROUTE_SRC).not.toContain('window')
    expect(ROUTE_SRC).not.toContain('location')
    expect(ROUTE_SRC).not.toContain('import.meta.env')
  })

  it('does not use locale-sensitive lowercasing', () => {
    // toLocaleLowerCase('tr') maps 'I' -> dotless 'ı', which would break the
    // /PRIVACY row above under a Turkish runtime locale.
    expect(ROUTE_SRC).not.toContain('toLocaleLowerCase')
  })

  it('whitespace-only input is not a match', () => {
    expect(isPrivacyPath('   ')).toBe(false)
  })

  it('trim runs before the trailing-slash strip', () => {
    // A strip-then-trim implementation would see the trailing space, skip the
    // slash strip, and wrongly stay false here.
    expect(isPrivacyPath('/privacy/ ')).toBe(true)
  })

  it('a very long pathname does not throw and is not a match', () => {
    const long = '/privacy' + 'x'.repeat(10000)
    expect(() => isPrivacyPath(long)).not.toThrow()
    expect(isPrivacyPath(long)).toBe(false)
  })

  it('a null byte in the pathname does not throw and is not a match', () => {
    expect(() => isPrivacyPath('/privacy\u0000')).not.toThrow()
    expect(isPrivacyPath('/privacy\u0000')).toBe(false)
  })

  it('a fullwidth unicode lookalike is not folded into a match', () => {
    expect(isPrivacyPath('/ｐrivacy')).toBe(false)
  })

  it('a double leading slash is not a match', () => {
    expect(isPrivacyPath('//privacy')).toBe(false)
  })

  it('a missing leading slash is not a match', () => {
    expect(isPrivacyPath('privacy')).toBe(false)
  })
})
