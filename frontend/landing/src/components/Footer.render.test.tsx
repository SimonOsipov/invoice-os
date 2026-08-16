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

// Company is the last column; slicing from its heading to end-of-doc scopes every
// assertion below to it, so Platform/Solutions items can never leak into a count.
function companySlice(html: string): string {
  const idx = html.indexOf('>Company<')
  expect(idx, 'expected to find the Company column heading').toBeGreaterThan(-1)
  return html.slice(idx)
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
