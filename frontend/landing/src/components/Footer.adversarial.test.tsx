// Adversarial coverage (QA, LAND-04-04) — gaps the RED specs didn't cover:
// target/rel absence on the new anchor, and the Q10 fence pinned by NAME under
// a non-empty prefix (Footer.hrefPrefix.test.tsx AC-8 only counts "#" in
// aggregate, so a mutation that moved the stub count around without touching
// Security/Status specifically would slip through unnoticed).
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Footer } from './Footer'

function noop() {}

const COPYRIGHT_ROW = '2026 ASCOMPLY AFRICA'

function companySlice(html: string): string {
  const start = html.indexOf('>Company<')
  expect(start, 'expected to find the Company column heading').toBeGreaterThan(-1)
  const end = html.indexOf(COPYRIGHT_ROW)
  expect(end, 'expected the copyright row to follow the Company column').toBeGreaterThan(start)
  return html.slice(start, end)
}

describe('Footer adversarial coverage (LAND-04-04)', () => {
  it('Privacy & cookies carries no target/rel — same-origin nav, not an external link', () => {
    const company = companySlice(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop })))
    const tag = company.match(/<a[^>]*href="\/privacy"[^>]*>/)
    expect(tag, 'expected to find the /privacy anchor tag').not.toBeNull()
    expect(tag![0]).not.toMatch(/target=/)
    expect(tag![0]).not.toMatch(/rel=/)
  })

  it('Q10 fence by name: Security and Status stay href="#" under a non-empty prefix', () => {
    const company = companySlice(
      renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' })),
    )
    expect(company).toMatch(/<a href="#"[^>]*>Security</)
    expect(company).toMatch(/<a href="#"[^>]*>Status</)
  })

  it('Book a demo button markup is byte-identical regardless of hrefPrefix', () => {
    const withoutPrefix = companySlice(renderToStaticMarkup(createElement(Footer, { onBookDemo: noop })))
    const withPrefix = companySlice(
      renderToStaticMarkup(createElement(Footer, { onBookDemo: noop, hrefPrefix: '/' })),
    )
    const buttonOf = (html: string) => html.match(/<button[^>]*>Book a demo<\/button>/)?.[0]
    expect(buttonOf(withoutPrefix)).toBeTruthy()
    expect(buttonOf(withoutPrefix)).toBe(buttonOf(withPrefix))
  })
})
