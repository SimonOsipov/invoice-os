/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { sliceCookieNoticeCss } from './cookieNoticeCss'

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../landing/src/styles/landing.css'), 'utf8')

describe('sliceCookieNoticeCss', () => {
  it('slice_startsAtTheBannerAndEndsAtTheMarker', () => {
    const slice = sliceCookieNoticeCss(css)
    expect(slice.startsWith('/*')).toBe(true)
    expect(slice.slice(0, slice.indexOf('*/'))).toContain('Cookie consent notice.')
    expect(slice).toContain('.cookie-note {')
    expect(slice).not.toContain('end: cookie consent notice')
    expect(css.indexOf(slice)).toBeGreaterThan(-1)
    expect(css.slice(css.indexOf(slice) + slice.length).startsWith('/* end: cookie consent notice */')).toBe(true)
    expect(() => sliceCookieNoticeCss(css.replace('/* end: cookie consent notice */', ''))).toThrow('end')
    expect(() => sliceCookieNoticeCss(css.replace('Cookie consent notice.', ''))).toThrow('start')
  })
})
