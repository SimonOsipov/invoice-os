// RED specs (task-557, LAND-04-03) — pin the hrefPrefix render contract before Nav.tsx
// applies it. SSR-only, same idiom as Nav.aria-current.test.tsx: no jsdom, no
// testing-library, no click/scroll simulation (React strips event handlers from
// renderToStaticMarkup output, so onClick/scroll-spy internals are not observable here).
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { NAV_LINKS, Nav } from './Nav'

const NAV_ONLY_TARGETS = NAV_LINKS.map((l) => l.href)

// Scoped to <a href> only; the Logo img src is not a nav target.
const ANCHOR_HREF = /<a\s+href="([^"]*)"/g

describe('Nav hrefPrefix contract', () => {
  it('AC-3: default hrefPrefix leaves every root href byte-identical to today', () => {
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    const hrefs = Array.from(html.matchAll(ANCHOR_HREF)).map((m) => m[1])
    // Control needle first: a misresolved render would otherwise pass vacuously below.
    expect(hrefs.length).toBeGreaterThan(0)
    expect(hrefs).toEqual(['#top', ...NAV_ONLY_TARGETS])
  })

  it('AC-4: hrefPrefix="/" prefixes every nav link', () => {
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
    expect(hrefs.length).toBe(1 + NAV_LINKS.length)
    expect(hrefs.every((h) => h.startsWith('/#'))).toBe(true)
    expect(hrefs.some((h) => h.startsWith('//'))).toBe(false)
  })

  it('control: one .ios-nav-link per NAV_LINKS entry', () => {
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    const matches = html.match(/ios-nav-link/g) ?? []
    expect(matches.length).toBe(NAV_LINKS.length)
    expect(NAV_LINKS.length).toBeGreaterThanOrEqual(1)
  })
})

describe('Nav libraryHref', () => {
  const LIB = 'https://lib.x'
  const hrefsOf = (props: Record<string, unknown>) =>
    Array.from(
      renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, ...props })).matchAll(ANCHOR_HREF),
    ).map((m) => m[1])

  it('Nav_rendersNoLibraryWhenUnset', () => {
    for (const props of [{ libraryHref: null }, {}]) {
      expect(hrefsOf(props)).toEqual(['#top', ...NAV_ONLY_TARGETS])
    }
    const html = renderToStaticMarkup(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, libraryHref: null }))
    expect(html).not.toContain('Library')
  })

  it('Nav_theLibraryHrefIgnoresHrefPrefix', () => {
    const hrefs = hrefsOf({ hrefPrefix: '/', libraryHref: LIB })
    expect(hrefs.filter((h) => h === LIB), 'control: the library href renders once').toHaveLength(1)
    expect(hrefs.filter((h) => h !== LIB).every((h) => h.startsWith('/#'))).toBe(true)
    expect(hrefs.some((h) => h.startsWith('//'))).toBe(false)
  })
})
