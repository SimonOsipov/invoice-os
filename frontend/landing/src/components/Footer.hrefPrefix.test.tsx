// RED specs (task-558, LAND-04-04) — pin the hrefPrefix scope-addition contract before
// Footer.tsx applies it. SSR-only, same idiom as Nav.hrefPrefix.test.tsx: no jsdom, no
// testing-library (vitest.config.ts: environment 'node').
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer } from './Footer'

function noop() {}

const REAL_ANCHORS = ['#modules', '#compliance', '#accountants', '#developers', '#pricing']

// Scoped to <a href> only — same guard as Nav.hrefPrefix.test.tsx: an unrelated element's
// own href/src attribute must never count here.
const ANCHOR_HREF = /<a\s+href="([^"]*)"/g

// Duplicated rather than shared, matching the openLanding() precedent. Both bounds are
// load-bearing: Company is the last column and the copyright row follows it, so an
// unbounded slice counts the copyright row's own controls as Company items.
const COPYRIGHT_ROW = '2026 ASCOMPLY AFRICA'

function companySlice(html: string): string {
  const start = html.indexOf('>Company<')
  expect(start, 'expected to find the Company column heading').toBeGreaterThan(-1)
  const end = html.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the copyright row to follow the Company column').toBeGreaterThan(start)
  return html.slice(start, end)
}

function copyrightRowSlice(html: string): string {
  const idx = html.indexOf(COPYRIGHT_ROW)
  expect(idx, 'expected to find the copyright row').toBeGreaterThan(-1)
  const open = html.lastIndexOf('<div', idx)
  expect(open, 'expected an opening div for the copyright row').toBeGreaterThan(-1)
  return html.slice(open)
}

describe('Footer hrefPrefix contract', () => {
  it('AC-6: default hrefPrefix leaves every href byte-identical to today', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))
    const hrefs = Array.from(html.matchAll(ANCHOR_HREF)).map((m) => m[1])
    // Control needle first: a misresolved render would otherwise pass vacuously below.
    expect(hrefs.length).toBeGreaterThan(0)
    expect(hrefs).toEqual(['#modules', '#compliance', '#', '#accountants', '#developers', '#pricing', '#', '#', '/privacy'])
  })

  it('AC-7: hrefPrefix="/" prefixes the five real cross-section anchors', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))
    for (const target of REAL_ANCHORS) {
      expect(html, target).toContain(`href="/${target}"`)
      expect(html, target).not.toContain(`href="//${target}"`)
    }
  })

  it('AC-8: the three # stubs stay exactly "#" under a non-empty prefix', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))
    const stubs = html.match(/href="#"/g) ?? []
    expect(stubs.length).toBe(3)
  })

  it('AC-9: /privacy never becomes //privacy', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))
    expect(html).toContain('href="/privacy"')
    expect(html).not.toContain('href="//privacy"')
  })

  // T4-7 (task-563): the same claim — hrefPrefix multiplies no button — taken per region
  // instead of as a whole-file constant, so a second footer control cannot invert it.
  it('AC-9b: one button in the Company column and one in the copyright row, regardless of hrefPrefix', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))

    const company = companySlice(html)
    expect(company.length, 'the Company slice resolved empty').toBeGreaterThan(0)
    expect((company.match(/<button/g) ?? []).length, 'Company column button count').toBe(1)

    const row = copyrightRowSlice(html)
    expect(row.length, 'the copyright row slice resolved empty').toBeGreaterThan(0)
    expect((row.match(/<button/g) ?? []).length, 'copyright row button count').toBe(1)
  })
})
