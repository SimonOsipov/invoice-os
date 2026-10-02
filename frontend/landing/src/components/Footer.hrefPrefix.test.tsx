// hrefPrefix contract of the v2 footer. SSR-only, same
// idiom as Nav.hrefPrefix.test.tsx: no jsdom, no testing-library (vitest.config.ts: environment 'node').
import { afterEach, describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer, PLATFORM_LINKS, footerHref } from './Footer'

function noop() {}

// Scoped to <a href> only; the Logo img src is not a footer target.
const ANCHOR_HREF = /<a\s+href="([^"]*)"/g

// ASCII sub-needle of the bottom row.
const COPYRIGHT_ROW = '2026 ASComply Africa Limited'

const hrefsOf = (html: string) => Array.from(html.matchAll(ANCHOR_HREF)).map((m) => m[1])

function connectSlice(html: string): string {
  const start = html.indexOf('>Connect<')
  expect(start, 'expected to find the Connect column heading').toBeGreaterThan(-1)
  const end = html.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the bottom row to follow the Connect column').toBeGreaterThan(start)
  return html.slice(start, end)
}

function copyrightRowSlice(html: string): string {
  const idx = html.indexOf(COPYRIGHT_ROW)
  expect(idx, 'expected to find the bottom row').toBeGreaterThan(-1)
  const open = html.lastIndexOf('<div', idx)
  expect(open, 'expected an opening div for the bottom row').toBeGreaterThan(-1)
  return html.slice(open)
}

describe('footerHref', () => {
  it('FT-10b: an in-page #x takes the prefix; /privacy and a bare # do not', () => {
    expect(footerHref('#platform', '/')).toBe('/#platform')
    expect(footerHref('#platform', '')).toBe('#platform')
    expect(footerHref('/privacy', '/')).toBe('/privacy')
    expect(footerHref('#', '/')).toBe('#')
    expect(footerHref('#', '')).toBe('#')
    expect(footerHref('#a#b', '/')).toBe('/#a#b')
    expect(footerHref('', '/'), 'an empty href is not an in-page anchor').toBe('')
    expect(footerHref('https://example.com/#x', '/'), 'an absolute URL is never prefixed').toBe('https://example.com/#x')
    expect(footerHref('/#platform', '/'), 'an already prefixed anchor is not prefixed twice').toBe('/#platform')
  })
})

describe('Footer hrefPrefix contract', () => {
  it('AC-6: default hrefPrefix leaves every href as authored: the Platform list, then /privacy', () => {
    const hrefs = hrefsOf(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop })))
    // Control needle first: a misresolved render would otherwise pass vacuously below.
    expect(hrefs.length).toBeGreaterThan(0)
    expect(hrefs).toEqual([...PLATFORM_LINKS.map((l) => l.href), '/privacy'])
  })

  it('AC-7: hrefPrefix="/" prefixes every Platform link once and leaves /privacy alone', () => {
    const hrefs = hrefsOf(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' })))
    expect(hrefs.length).toBeGreaterThan(0)
    expect(hrefs).toEqual([...PLATFORM_LINKS.map((l) => `/${l.href}`), '/privacy'])
    expect(hrefs.filter((h) => h.startsWith('//'))).toEqual([])
  })

  it('AC-8: no footer anchor is a bare # stub, with or without a prefix', () => {
    for (const hrefPrefix of ['', '/']) {
      const hrefs = hrefsOf(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix })))
      expect(hrefs.length, `control: anchors at prefix ${JSON.stringify(hrefPrefix)}`).toBeGreaterThan(0)
      expect(hrefs, `a stub survived at prefix ${JSON.stringify(hrefPrefix)}`).not.toContain('#')
    }
  })

  it('AC-9: /privacy never becomes //privacy', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))
    expect(html).toContain('href="/privacy"')
    expect(html).not.toContain('href="//privacy"')
  })

  // T4-7: hrefPrefix multiplies no button, counted per region.
  it('AC-9b: three buttons in Connect and one in the bottom row, regardless of hrefPrefix', () => {
    for (const hrefPrefix of ['', '/']) {
      const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix }))

      const connect = connectSlice(html)
      expect(connect.length, 'the Connect slice resolved empty').toBeGreaterThan(0)
      expect((connect.match(/<button/g) ?? []).length, `Connect button count at ${JSON.stringify(hrefPrefix)}`).toBe(3)

      const row = copyrightRowSlice(html)
      expect(row.length, 'the bottom row slice resolved empty').toBeGreaterThan(0)
      expect((row.match(/<button/g) ?? []).length, `bottom row button count at ${JSON.stringify(hrefPrefix)}`).toBe(1)
    }
  })
})

describe('Footer hrefPrefix contract with a populated Platform column', () => {
  const saved = [...PLATFORM_LINKS]
  const PLANTED = [
    { label: 'Invoice workflows', href: '#platform' },
    { label: 'Country roadmap', href: '#coverage' },
  ]
  afterEach(() => void PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...saved))

  it.each([
    ['', ''],
    ['/', '/'],
  ])('with hrefPrefix %j every in-page link takes the prefix once and /privacy stays exactly /privacy', (hrefPrefix, p) => {
    PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...PLANTED)
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(PLANTED.length)
    const hrefs = hrefsOf(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix })))
    expect(hrefs).toEqual([...PLANTED.map((l) => `${p}${l.href}`), '/privacy'])
    expect(hrefs.filter((h) => h.startsWith('//'))).toEqual([])
  })

  it('with no hrefPrefix prop the in-page links stay bare', () => {
    PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...PLANTED)
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(PLANTED.length)
    expect(hrefsOf(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop })))).toEqual([
      ...PLANTED.map((l) => l.href),
      '/privacy',
    ])
  })
})
