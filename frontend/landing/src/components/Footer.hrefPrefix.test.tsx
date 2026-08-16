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

  it('AC-9b: exactly one button regardless of hrefPrefix', () => {
    const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' }))
    const buttons = html.match(/<button/g) ?? []
    expect(buttons.length).toBe(1)
  })
})
