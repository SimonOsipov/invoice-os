// RED specs (task-557, LAND-04-03) — pin the hrefPrefix render contract before Nav.tsx
// applies it. SSR-only, same idiom as Nav.aria-current.test.tsx: no jsdom, no
// testing-library, no click/scroll simulation (React strips event handlers from
// renderToStaticMarkup output, so onClick/scroll-spy internals are not observable here).
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { Nav } from './Nav'

const NAV_ONLY_TARGETS = ['#problem', '#modules', '#compliance', '#accountants', '#developers', '#pricing']

// Scoped to <a href> only — BrandMark also renders a <link rel="preload" href=...>
// for the logo asset, which is not a nav target and must not count here.
const ANCHOR_HREF = /<a\s+href="([^"]*)"/g

describe('Nav hrefPrefix contract', () => {
  it('AC-3: default hrefPrefix leaves every root href byte-identical to today', () => {
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    const hrefs = Array.from(html.matchAll(ANCHOR_HREF)).map((m) => m[1])
    // Control needle first: a misresolved render would otherwise pass vacuously below.
    expect(hrefs.length).toBeGreaterThan(0)
    expect(hrefs).toEqual(['#top', ...NAV_ONLY_TARGETS])
  })

  it('AC-4: hrefPrefix="/" prefixes all six nav links', () => {
    const html = renderToStaticMarkup(
      createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, hrefPrefix: '/' }),
    )
    for (const target of NAV_ONLY_TARGETS) {
      expect(html, target).toContain(`<a href="/${target}"`)
    }
  })

  it('AC-4: the brand lockup href is prefixed too', () => {
    const html = renderToStaticMarkup(
      createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, hrefPrefix: '/' }),
    )
    expect(html).toContain('<a href="/#top"')
    expect(html).not.toContain('<a href="#top"')
  })

  it('AC-3/4: no href is left unprefixed or double-prefixed when hrefPrefix is set', () => {
    const html = renderToStaticMarkup(
      createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, hrefPrefix: '/' }),
    )
    const hrefs = Array.from(html.matchAll(ANCHOR_HREF)).map((m) => m[1])
    // Control needle first: guards the two every()/some() checks below against a
    // vacuous pass on an empty (misresolved) href set.
    expect(hrefs.length).toBe(7)
    expect(hrefs.every((h) => h.startsWith('/#'))).toBe(true)
    expect(hrefs.some((h) => h.startsWith('//'))).toBe(false)
  })

  it('control: exactly 6 .ios-nav-link occurrences', () => {
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    const matches = html.match(/ios-nav-link/g) ?? []
    expect(matches.length).toBe(6)
  })
})
