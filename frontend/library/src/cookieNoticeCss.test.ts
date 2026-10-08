/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { sliceCookieNoticeCss } from './cookieNoticeCss'

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../landing/src/styles/landing.css'), 'utf8')
const stripComments = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '')

describe('sliceCookieNoticeCss', () => {
  it("CSS-01 the slice is the landing's whole notice block", () => {
    const slice = sliceCookieNoticeCss(css)
    for (const want of [
      '@keyframes cn-in',
      '.cookie-note {',
      '.cn-actions [data-consent="accept"]',
      '.cn-spacer',
      'html:has(.cookie-note)',
      '@media (max-width: 640px)',
      '--cn-band: 282px',
    ]) {
      expect(slice, want).toContain(want)
    }
    // `.cn-body {` is the notice's own; only a bare `body` rule is foreign.
    expect(slice).not.toMatch(/(^|[^-\w.])body\s*\{/)
    for (const not of ['.ios-nav-link', '.btn-on-dark']) expect(slice, not).not.toContain(not)

    const outside = stripComments(css.replace(slice, ''))
    expect(outside.match(/cookie-note|\.cn-|@keyframes cn-/g) ?? []).toEqual([])
  })

  it('CSS-02 a missing marker throws', () => {
    expect(() => sliceCookieNoticeCss(css.replace('/* end: cookie consent notice */', ''))).toThrow('end')
    expect(() => sliceCookieNoticeCss(css.replace('Cookie consent notice.', ''))).toThrow('start')
  })
})
