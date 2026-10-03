// Adversarial coverage (QA, LAND-04-04) — App.route.test.ts only regex-scans the
// source text (`hrefPrefix={privacy`), which matches equally whether the ternary
// reads `privacy ? '/' : ''` or the inverted `privacy ? '' : '/'`. Neither Footer's
// nor Nav's own unit tests can catch that either — they take hrefPrefix as an
// explicit prop, never through App's actual `privacy` boolean. This file renders
// the real App tree (SSR, no jsdom) at both paths to close that gap.
import { afterEach, describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import App from './App'
import { PLATFORM_LINKS } from './components/Footer'

// App.tsx reads window.location.pathname at render time (not just inside an
// effect, which SSR never runs anyway), so the stub must be in place before render.
function renderAppAt(pathname: string): string {
  const prevWindow = (globalThis as { window?: unknown }).window
  ;(globalThis as { window?: unknown }).window = { location: { pathname } }
  try {
    return renderToStaticMarkup(createElement(App))
  } finally {
    ;(globalThis as { window?: unknown }).window = prevWindow
  }
}

// Bounded at </footer> on purpose: the cookie notice mounts after <Footer>, and an
// open-ended slice would silently widen what the assertions below cover.
function footerSlice(html: string): string {
  const idx = html.lastIndexOf('<footer')
  expect(idx, 'expected to find a <footer> element').toBeGreaterThan(-1)
  const end = html.indexOf('</footer>', idx)
  expect(end, 'expected a closing </footer> after it').toBeGreaterThan(idx)
  return html.slice(idx, end + '</footer>'.length)
}

const footerHrefs = (footer: string) => [...footer.matchAll(/<a\s[^>]*?href="([^"]*)"/g)].map((m) => m[1])

describe('FT-10 App SSR wiring: hrefPrefix reaches Footer with the right polarity', () => {
  it('at /privacy every footer anchor is /privacy or starts with /#, and none is a bare #', () => {
    const hrefs = footerHrefs(footerSlice(renderAppAt('/privacy')))
    expect(hrefs.length, 'expected anchors in the footer').toBeGreaterThanOrEqual(1)
    expect(hrefs.filter((h) => h !== '/privacy' && !h.startsWith('/#'))).toEqual([])
    expect(hrefs).not.toContain('#')
  })

  it('at / no footer anchor starts with /# and none is a bare #', () => {
    const footer = footerSlice(renderAppAt('/'))
    const hrefs = footerHrefs(footer)
    expect(hrefs.length, 'expected anchors in the footer').toBeGreaterThanOrEqual(1)
    expect(hrefs.filter((h) => h.startsWith('/#'))).toEqual([])
    expect(hrefs).not.toContain('#')
    expect(footer).toContain('href="/privacy"')
    expect(footer).toMatch(/<button[^>]*>Book a demo</)
  })
})

// A planted list pins the hrefPrefix polarity independent of the live links.
describe('FT-10 / AN-03 with PLATFORM_LINKS populated', () => {
  const saved = [...PLATFORM_LINKS]
  const plant = (hrefs: string[]) =>
    PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...hrefs.map((href) => ({ label: href.slice(1), href })))
  afterEach(() => void PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...saved))

  it('at /privacy the planted links are /#x and /privacy is exact', () => {
    plant(['#modules', '#how'])
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(2)
    expect(footerHrefs(footerSlice(renderAppAt('/privacy')))).toEqual(['/#modules', '/#how', '/privacy'])
  })

  it('at / the planted links keep their bare #x and /privacy is exact', () => {
    plant(['#modules', '#how'])
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(2)
    expect(footerHrefs(footerSlice(renderAppAt('/')))).toEqual(['#modules', '#how', '/privacy'])
  })

  it('AN-03 reads the live list: resolving links pass, a ghost link is reported through the real App', () => {
    plant(['#modules', '#how', '#pricing'])
    const html = renderAppAt('/')
    const hashes = footerHrefs(footerSlice(html)).filter((h) => h.startsWith('#') && h.length > 1)
    expect(hashes).toHaveLength(3)
    expect(unresolvedHashes(html, hashes)).toEqual([])

    plant(['#modules', '#ghost'])
    const ghostHtml = renderAppAt('/')
    const ghostHashes = footerHrefs(footerSlice(ghostHtml)).filter((h) => h.startsWith('#') && h.length > 1)
    expect(unresolvedHashes(ghostHtml, ghostHashes)).toEqual(['#ghost'])
  })
})

describe('T3-13: the cookie notice mounts outside the footer slice', () => {
  it.each([['/'], ['/privacy']])('AC-7: at %s the notice renders after </footer>, not inside it', (pathname) => {
    const html = renderAppAt(pathname)
    // Control needle first: a misresolved render would otherwise pass vacuously.
    expect(html.length).toBeGreaterThan(0)
    expect(html).toContain('<footer')

    expect(html, 'expected the cookie notice to be mounted').toContain('cookie-note')

    const slice = footerSlice(html)
    expect(slice.endsWith('</footer>'), 'the slice must stop at </footer>').toBe(true)
    expect(slice, 'the footer slice swallowed the notice').not.toContain('cookie-note')
    // Mount position is load-bearing: inside <Footer> turns
    // e2e/smoke/landing-privacy.spec.ts:122 and :130 red at count 2.
    expect(html.indexOf('cookie-note'), 'the notice must render after the footer').toBeGreaterThan(
      html.lastIndexOf('<footer'),
    )
  })
})

// The privacy page now says the control sits "at the foot of every page". Nothing asserted
// that per route: App renders <Footer> unconditionally today, and a future route that
// skipped it would falsify the published copy silently.
describe('AC-11: the Cookie choices control is in the footer on every route the SPA serves', () => {
  // isPrivacyPath splits the SPA in two: /privacy (trimmed, lowercased, one trailing
  // slash stripped) and everything else. Both arms are covered, plus an unknown path,
  // which the router serves as the landing page rather than a 404.
  const ROUTES = ['/', '/privacy', '/privacy/', '/PRIVACY', '/pricing', '/some-unknown-path']

  it.each(ROUTES.map((r) => [r]))('at %s the footer renders the control exactly once', (pathname) => {
    const html = renderAppAt(pathname)
    // Control needles: a misresolved render would make every assertion below vacuous.
    expect(html.length).toBeGreaterThan(0)
    expect(html, 'this route rendered no footer at all').toContain('<footer')

    const footer = footerSlice(html)
    expect(footer, 'the footer on this route has no Cookie choices control').toMatch(
      /<button[^>]*>Cookie choices</,
    )
    expect((footer.match(/>Cookie choices</g) ?? []).length, 'the control is duplicated').toBe(1)
    expect(html.match(/<footer/g)?.length, 'more than one footer on this route').toBe(1)
  })

  it('control: the route arms really do differ, so the sweep above is not one case six times', () => {
    // If isPrivacyPath ever collapsed, every route would render identical markup and the
    // it.each above would prove nothing about coverage.
    expect(renderAppAt('/privacy')).not.toBe(renderAppAt('/'))
    expect(renderAppAt('/privacy/'), 'the trailing-slash form took the landing arm').toBe(renderAppAt('/privacy'))
    expect(renderAppAt('/some-unknown-path'), 'an unknown path took the privacy arm').toBe(renderAppAt('/'))
  })
})

describe('RESKIN-01-03 (AC 5): the four former --gradient-hero sites render the v2 flat dark band', () => {
  const html = renderAppAt('/')

  function section(id: string): string {
    const start = html.indexOf(`<section id="${id}"`)
    expect(start, `expected <section id="${id}">`).toBeGreaterThan(-1)
    return html.slice(start, html.indexOf('</section>', start))
  }

  it('Modules is a bare band-dark section with no inline background', () => {
    const open = /^<section id="modules"[^>]*>/.exec(section('modules'))?.[0]
    expect(open).toBe('<section id="modules" class="band-dark">')
  })

  it('HowItWorks step panel is flat var(--surface) with no background-image', () => {
    const panel = /<div class="ios-grid ios-3"[^>]*>/.exec(section('how'))?.[0] ?? ''
    expect(panel, 'control: the panel was found').toContain('gap:1px')
    expect(panel).toContain('background:var(--surface);')
    expect(panel).not.toContain('background-image')
  })

  it('only the featured price card is var(--surface); the other two stay var(--bg-2)', () => {
    const cards = [...section('pricing').matchAll(/<div class="ios-price"[^>]*>/g)].map((m) => m[0])
    expect(cards.length, 'three plan cards').toBe(3)
    expect(cards.filter((c) => c.includes('background:var(--surface);'))).toHaveLength(1)
    expect(cards.filter((c) => c.includes('background:var(--bg-2);'))).toHaveLength(2)
  })

  it('the DemoCta card is flat var(--surface)', () => {
    const demo = /<div class="ios-grid ios-2 ios-demo-card"[^>]*>/.exec(section('demo'))?.[0] ?? ''
    expect(demo, 'control: the card was found').toContain('padding:64px 56px')
    expect(demo).toContain('background:var(--surface);')
  })

  it('no rendered element names a gradient token or a font-variation axis', () => {
    expect(html.length).toBeGreaterThan(50_000)
    expect(html).not.toMatch(/--gradient-/)
    expect(html).not.toMatch(/font-variation-settings/i)
  })
})

// Nav links resolve to the page's sections (AC 2). Each stub is planted in the helper control so a
// vacuous pass is visible.
const primaryNav = (html: string) => /<nav\b[^>]*aria-label="Primary"[^>]*>([\s\S]*?)<\/nav>/.exec(html)?.[1] ?? ''
const navHashes = (html: string) => [...primaryNav(html).matchAll(/href="(#[^"]+)"/g)].map((m) => m[1])
const navHrefs = (html: string) => [...primaryNav(html).matchAll(/href="([^"]*)"/g)].map((m) => m[1])

/** The hashes that do not resolve to exactly one `<section id>` in `html`. */
function unresolvedHashes(html: string, hashes: string[]): string[] {
  const owners = [...html.matchAll(/<([a-zA-Z][\w-]*)\b[^>]*\sid="([^"]*)"/g)]
  return hashes.filter((h) => {
    const mine = owners.filter((o) => o[2] === h.slice(1))
    return mine.length !== 1 || mine[0][1] !== 'section'
  })
}
const unresolvedNavLinks = (html: string) => unresolvedHashes(html, navHashes(html))

describe('AN-01 every nav in-page link resolves to one section', () => {
  it('controls: the helper reports a ghost link, a duplicate id and a non-section owner', () => {
    const nav = '<nav aria-label="Primary"><a href="#ghost">x</a></nav>'
    expect(unresolvedNavLinks(nav)).toEqual(['#ghost'])
    expect(unresolvedNavLinks(`${nav}<section id="ghost"></section>`), 'a resolving link is not reported').toEqual([])
    expect(unresolvedNavLinks(`${nav}<section id="ghost"></section><section id="ghost"></section>`)).toEqual(['#ghost'])
    expect(unresolvedNavLinks(`${nav}<div id="ghost"></div>`)).toEqual(['#ghost'])
  })

  it('at / each href="#x" in the Primary nav has exactly one id="x", on a <section', () => {
    const html = renderAppAt('/')
    expect(navHashes(html).length, 'expected in-page links in the Primary nav').toBeGreaterThanOrEqual(1)
    expect(unresolvedNavLinks(html)).toEqual([])
  })
})

describe('AN-02 on /privacy every nav link carries the prefix', () => {
  it('each Primary nav href starts with /# and none with //', () => {
    const hrefs = navHrefs(renderAppAt('/privacy'))
    expect(hrefs.length, 'expected links in the Primary nav').toBeGreaterThanOrEqual(1)
    expect(hrefs.filter((h) => !h.startsWith('/#'))).toEqual([])
    expect(hrefs.filter((h) => h.startsWith('//'))).toEqual([])
  })
})

describe('R4-AN-1 the landing renders #coverage once, the privacy page none', () => {
  const count = (html: string) => (html.match(/<section id="coverage"/g) ?? []).length

  it('/ renders one <section id="coverage" class="ds-section band-peach">, /privacy none', () => {
    const landing = renderAppAt('/')
    const privacy = renderAppAt('/privacy')
    expect(landing, 'control: the landing rendered its sections').toContain('<section id="modules"')
    expect(privacy, 'control: the privacy page rendered its footer').toContain('<footer')
    expect(count(landing), 'sections on /').toBe(1)
    expect(landing).toContain('<section id="coverage" class="ds-section band-peach">')
    expect(count(privacy), 'sections on /privacy').toBe(0)
  })
})

describe('R4-AN-2 the two sections render in v2 order', () => {
  const count = (html: string, id: string) => (html.match(new RegExp(`<section id="${id}"`, 'g')) ?? []).length

  it('/ renders one #coverage then one #intelligence, /privacy neither', () => {
    const landing = renderAppAt('/')
    const privacy = renderAppAt('/privacy')
    expect(landing, 'control: the landing rendered its sections').toContain('<section id="modules"')
    expect(privacy, 'control: the privacy page rendered its footer').toContain('<footer')
    expect(count(landing, 'coverage'), '#coverage on /').toBe(1)
    expect(count(landing, 'intelligence'), '#intelligence on /').toBe(1)
    expect(landing).toContain('<section id="intelligence" class="ds-section band-dark2">')
    expect(landing.indexOf('<section id="coverage"'), '#coverage comes first').toBeLessThan(landing.indexOf('<section id="intelligence"'))
    expect(count(privacy, 'coverage'), '#coverage on /privacy').toBe(0)
    expect(count(privacy, 'intelligence'), '#intelligence on /privacy').toBe(0)
  })
})

describe('AN-03 every footer in-page link resolves to one section', () => {
  it('controls: the helper reports a ghost footer link and passes a resolving one', () => {
    const footer = '<footer><a href="#ghost">x</a></footer>'
    expect(unresolvedHashes(footer, ['#ghost'])).toEqual(['#ghost'])
    expect(unresolvedHashes(`${footer}<section id="ghost"></section>`, ['#ghost'])).toEqual([])
    expect(unresolvedHashes(`${footer}<div id="ghost"></div>`, ['#ghost'])).toEqual(['#ghost'])
  })

  it('at / the footer in-page hrefs are exactly PLATFORM_LINKS, each with one id on a <section', () => {
    const html = renderAppAt('/')
    const hashes = footerHrefs(footerSlice(html)).filter((h) => h.startsWith('#') && h.length > 1)
    expect(hashes, 'the footer hashes are not the PLATFORM_LINKS population').toHaveLength(PLATFORM_LINKS.length)
    expect(unresolvedHashes(html, hashes)).toEqual([])
  })
})
