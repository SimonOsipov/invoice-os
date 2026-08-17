// RED specs (task-558, LAND-04-04) — pin the fourth Company item before Footer.tsx
// gains it. SSR-only, same idiom as DemoModal.render.test.tsx / Nav.hrefPrefix.test.tsx:
// no jsdom, no testing-library (vitest.config.ts: environment 'node').
//
// React 19 renderToStaticMarkup escapes `&` to `&amp;` in text nodes (verified against
// the installed react-dom) — needles below use the escaped form for "Privacy & cookies".
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer } from './Footer'

function noop() {}

// ASCII sub-needle: sidesteps the copyright row's escaped glyphs, occurs exactly once.
const COPYRIGHT_ROW = '2026 ASCOMPLY AFRICA'

// Company is the last column and the copyright row follows it in markup, so BOTH bounds
// are load-bearing: unbounded, a control in that row counts as a Company item.
function companySlice(html: string): string {
  const start = html.indexOf('>Company<')
  expect(start, 'expected to find the Company column heading').toBeGreaterThan(-1)
  const end = html.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the copyright row to follow the Company column').toBeGreaterThan(start)
  return html.slice(start, end)
}

describe('Footer SSR render (LAND-04-04)', () => {
  const html = renderToStaticMarkup(createElement(Footer, { onBookDemo: noop }))

  it('AC-1: Company column renders exactly four items, in order', () => {
    const company = companySlice(html)
    const items = Array.from(company.matchAll(/<(?:a|button)[^>]*>([^<]*)</g)).map((m) => m[1])
    // Control needle first: a misresolved slice would otherwise pass vacuously below.
    expect(items.length).toBeGreaterThan(0)
    expect(items).toEqual(['Book a demo', 'Security', 'Status', 'Privacy &amp; cookies'])
  })

  it('AC-2: Security and Status still carry href="#", unchanged', () => {
    const company = companySlice(html)
    expect(company).toMatch(/<a href="#"[^>]*>Security</)
    expect(company).toMatch(/<a href="#"[^>]*>Status</)
  })

  it('AC-3: the new item is <a class="ios-link" href="/privacy">, not a button', () => {
    const company = companySlice(html)
    expect(company).toMatch(/<a href="\/privacy" class="ios-link"[^>]*>Privacy &amp; cookies</)
  })

  it('AC-4: Book a demo is still a button, no href — the special-case branch is untouched', () => {
    const company = companySlice(html)
    expect(company).toMatch(/<button[^>]*>Book a demo</)
    expect(company).not.toMatch(/<a[^>]*>Book a demo</)
  })

  it('AC-5: Platform and Solutions are untouched, and Company holds exactly one button', () => {
    for (const needle of ['Modules', 'Validation engine', 'Open the app', 'Who it&#x27;s for', 'For developers', 'Pricing']) {
      expect(html, needle).toContain(needle)
    }
    const company = companySlice(html)
    const buttons = company.match(/<button/g) ?? []
    expect(buttons.length).toBe(1)
  })
})
