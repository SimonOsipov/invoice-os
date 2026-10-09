// Adversarial coverage (QA, LAND-04-04) — App.route.test.ts only regex-scans the
// source text (`hrefPrefix={privacy`), which matches equally whether the ternary
// reads `privacy ? '/' : ''` or the inverted `privacy ? '' : '/'`. Neither Footer's
// nor Nav's own unit tests can catch that either — they take hrefPrefix as an
// explicit prop, never through App's actual `privacy` boolean. This file renders
// the real App tree (SSR, no jsdom) at both paths to close that gap.
import { readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import App from './App'
import * as data from './data'
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

// Planted lists observe the hrefPrefix polarity whatever the live list holds.
describe('FT-10 / AN-03 with PLATFORM_LINKS populated', () => {
  const saved = [...PLATFORM_LINKS]
  const plant = (hrefs: string[]) =>
    PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...hrefs.map((href) => ({ label: href.slice(1), href })))
  afterEach(() => void PLATFORM_LINKS.splice(0, PLATFORM_LINKS.length, ...saved))

  it('at /privacy the planted links are /#x and /privacy is exact', () => {
    plant(['#solution', '#platform'])
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(2)
    expect(footerHrefs(footerSlice(renderAppAt('/privacy')))).toEqual(['/#solution', '/#platform', '/privacy'])
  })

  it('at / the planted links keep their bare #x and /privacy is exact', () => {
    plant(['#solution', '#platform'])
    expect(PLATFORM_LINKS.length, 'control: the fixture is in place').toBe(2)
    expect(footerHrefs(footerSlice(renderAppAt('/')))).toEqual(['#solution', '#platform', '/privacy'])
  })

  it('AN-03 reads the live list: resolving links pass, a ghost link is reported through the real App', () => {
    plant(['#solution', '#platform', '#coverage'])
    const html = renderAppAt('/')
    const hashes = footerHrefs(footerSlice(html)).filter((h) => h.startsWith('#') && h.length > 1)
    expect(hashes).toHaveLength(3)
    expect(unresolvedHashes(html, hashes)).toEqual([])

    plant(['#solution', '#ghost'])
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

describe('RESKIN-01-03 (AC 5): the former --gradient-hero sites render the v2 flat dark band', () => {
  const html = renderAppAt('/')

  function section(id: string): string {
    const start = html.indexOf(`<section id="${id}"`)
    expect(start, `expected <section id="${id}">`).toBeGreaterThan(-1)
    return html.slice(start, html.indexOf('</section>', start))
  }

  it('SO-07 the Solution band is flat: no inline background', () => {
    const open = /^<section id="solution"[^>]*>/.exec(section('solution'))?.[0]
    expect(open).toBe('<section id="solution" class="ds-section band-dark">')
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
const LIB = 'https://lib.x'
afterEach(() => void vi.unstubAllEnvs())
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

  it('AN-01 holds with the Library link rendered', () => {
    vi.stubEnv('VITE_LIBRARY_URL', LIB)
    const html = renderAppAt('/')
    expect(navHrefs(html), 'control: the Library link rendered').toContain(LIB)
    expect(navHashes(html)).toHaveLength(5)
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

  it('AN-02 names the one external link', () => {
    vi.stubEnv('VITE_LIBRARY_URL', LIB)
    const hrefs = navHrefs(renderAppAt('/privacy'))
    expect(hrefs.filter((h) => h === LIB)).toHaveLength(1)
    const others = hrefs.filter((h) => h !== LIB)
    expect(others.filter((h) => !h.startsWith('/#'))).toEqual([])
    expect(others.filter((h) => h.startsWith('//'))).toEqual([])
    vi.stubEnv('VITE_LIBRARY_URL', '')
    expect(navHrefs(renderAppAt('/privacy')), 'control: no stub, no Library link').not.toContain(LIB)
  })
})

describe('R4-AN-2 the two sections render in v2 order', () => {
  const count = (html: string, id: string) => (html.match(new RegExp(`<section id="${id}"`, 'g')) ?? []).length

  it('/ renders one #coverage then one #intelligence, /privacy neither', () => {
    const landing = renderAppAt('/')
    const privacy = renderAppAt('/privacy')
    expect(landing, 'control: the landing rendered its sections').toContain('<section id="platform"')
    expect(privacy, 'control: the privacy page rendered its footer').toContain('<footer')
    expect(count(landing, 'coverage'), '#coverage on /').toBe(1)
    expect(count(landing, 'intelligence'), '#intelligence on /').toBe(1)
    expect(landing).toContain('<section id="coverage" class="ds-section band-peach">')
    expect(landing).toContain('<section id="intelligence" class="ds-section band-dark2">')
    expect(landing.indexOf('<section id="platform"'), '#platform comes before #coverage').toBeLessThan(landing.indexOf('<section id="coverage"'))
    expect(landing.indexOf('<section id="coverage"'), '#coverage comes first').toBeLessThan(landing.indexOf('<section id="intelligence"'))
    expect(count(privacy, 'coverage'), '#coverage on /privacy').toBe(0)
    expect(count(privacy, 'intelligence'), '#intelligence on /privacy').toBe(0)
  })
})

// Section order is the page contract; later sections extend it.
describe('R5-AN section order', () => {
  const sectionIds = (html: string) => [...html.matchAll(/<section id="([^"]+)"/g)].map((m) => m[1])

  it('/ renders #solutions directly after #intelligence and no #accountants; /privacy neither', () => {
    const landing = sectionIds(renderAppAt('/'))
    const privacy = sectionIds(renderAppAt('/privacy'))
    expect(landing, 'control: the landing rendered its sections').toContain('intelligence')
    expect(renderAppAt('/privacy'), 'control: the privacy page rendered its footer').toContain('<footer')
    expect(landing.filter((id) => id === 'solutions'), 'one #solutions on /').toHaveLength(1)
    expect(landing[landing.indexOf('intelligence') + 1], '#solutions directly after #intelligence').toBe('solutions')
    expect(landing, 'no #accountants on /').not.toContain('accountants')
    expect(privacy, 'neither on /privacy').not.toContain('solutions')
    expect(privacy).not.toContain('accountants')
  })

  it('/ renders #integrations directly after #solutions, #api directly after it, and no #developers; /privacy none', () => {
    const landing = sectionIds(renderAppAt('/'))
    const privacy = sectionIds(renderAppAt('/privacy'))
    expect(landing, 'control: the landing rendered its sections').toContain('solutions')
    expect(landing.filter((id) => id === 'integrations'), 'one #integrations on /').toHaveLength(1)
    expect(landing.filter((id) => id === 'api'), 'one #api on /').toHaveLength(1)
    expect(landing[landing.indexOf('solutions') + 1], '#integrations directly after #solutions').toBe('integrations')
    expect(landing[landing.indexOf('integrations') + 1], '#api directly after #integrations').toBe('api')
    expect(landing, 'no #developers on /').not.toContain('developers')
    for (const id of ['integrations', 'api', 'developers']) expect(privacy, `no #${id} on /privacy`).not.toContain(id)
  })

  it('/ renders #faq directly after #api, then an id-less section holding [data-closing]; /privacy neither', () => {
    // The closing section has no id, so read every <section> in order rather than the id list.
    const sections = (html: string) => html.split('<section').slice(1).map((chunk) => ({ id: /^ id="([^"]+)"/.exec(chunk)?.[1], closing: chunk.includes('data-closing') }))
    const landing = sections(renderAppAt('/'))
    const privacy = sections(renderAppAt('/privacy'))
    const api = landing.findIndex((s) => s.id === 'api')
    expect(api, 'control: the landing rendered #api').toBeGreaterThan(-1)
    expect(landing.filter((s) => s.id === 'faq'), 'one #faq on /').toHaveLength(1)
    expect(landing[api + 1]?.id, '#faq directly after #api').toBe('faq')
    expect(landing[api + 2], 'the next section holds [data-closing] and has no id').toEqual({ id: undefined, closing: true })
    expect(landing.filter((s) => s.closing), 'one closing section on /').toHaveLength(1)
    expect(privacy.length, 'control: /privacy rendered sections').toBeGreaterThan(0)
    expect(privacy.some((s) => s.id === 'faq' || s.closing), 'neither on /privacy').toBe(false)
  })

  it('the page is v2 from hero to closing CTA: sections and bands in order, no retired section', () => {
    const html = renderAppAt('/')
    // The strip and the closing section carry no id, so each is named by its marker.
    const sections = html
      .split('<section')
      .slice(1)
      .map((chunk) => ({
        id: /^ id="([^"]+)"/.exec(chunk)?.[1] ?? (chunk.includes('data-strip=') ? '[data-strip]' : chunk.includes('data-closing') ? '[data-closing]' : '?'),
        band: /^(?: id="[^"]*")? class="ds-section band-([a-z0-9]+)"/.exec(chunk)?.[1],
      }))
    expect(sections.length, 'control: the landing rendered its sections').toBeGreaterThan(5)
    expect(sections.map((s) => s.id)).toEqual(['top', '[data-strip]', 'problem', 'solution', 'platform', 'coverage', 'intelligence', 'solutions', 'integrations', 'api', 'faq', '[data-closing]'])
    expect(sections.map((s) => s.band)).toEqual(['dark', 'sage', 'cream', 'dark', 'cream', 'peach', 'dark2', 'cream', 'peach', 'dark', 'cream', 'cream'])
    for (const id of ['compliance', 'pricing', 'accountants', 'developers', 'demo']) {
      expect(sections.map((s) => s.id), `no #${id} on /`).not.toContain(id)
    }
    expect(html.indexOf('data-closing'), 'the footer follows the closing section').toBeLessThan(html.lastIndexOf('<footer'))
  })

  it('/ renders no #demo section and no id starting dc-', () => {
    const html = renderAppAt('/')
    expect(sectionIds(html), 'control: the landing rendered its sections').toContain('faq')
    expect(sectionIds(html), 'no #demo on /').not.toContain('demo')
    expect([...html.matchAll(/\sid="(dc-[^"]*)"/g)].map((m) => m[1]), 'no dc-* id on /').toEqual([])
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
    expect(hashes, 'the footer links Solutions for partners').toContain('#solutions')
    expect(unresolvedHashes(html, hashes)).toEqual([])
  })
})

describe('NV-03 the hero Explore anchors land on Platform', () => {
  const heroHashes = (html: string) => {
    const start = html.indexOf('<section id="top"')
    expect(start, 'expected <section id="top">').toBeGreaterThan(-1)
    const slice = html.slice(start, html.indexOf('</section>', start))
    return [...slice.matchAll(/href="(#[^"]*)"/g)].map((m) => m[1])
  }

  it('both in-page hrefs inside #top are #platform and resolve; the helper reports a planted #ghost', () => {
    const html = renderAppAt('/')
    expect(heroHashes(html)).toEqual(['#platform', '#platform'])
    expect(unresolvedHashes(html, heroHashes(html))).toEqual([])

    // Planted inside the #top slice: the header nav also links #platform, earlier in the markup.
    const top = html.indexOf('<section id="top"')
    const at = html.indexOf('href="#platform"', top)
    const ghost = `${html.slice(0, at)}href="#ghost"${html.slice(at + 'href="#platform"'.length)}`
    expect(heroHashes(ghost), 'control: the ghost sits in the hero').toEqual(['#ghost', '#platform'])
    expect(unresolvedHashes(ghost, heroHashes(ghost)), 'control: a ghost hero anchor is reported').toEqual(['#ghost'])
  })
})

describe('NV-04 How it works is gone from the page and the data', () => {
  // Needles are split so the removal sweep (NV-08) does not match this test.
  const SECTION_ID = `id="${'ho' + 'w'}"`
  const BANNER = ['HOW IT', 'WORKS'].join(' ')
  const DATA_KEY = 'STE' + 'PS'
  const OLD_COPY = ['Connect or ' + 'import', 'Approve, archive &amp; ' + 'transmit', 'No rip-and-' + 'replace', 'in three ' + 'steps']

  it('the markup has no How-it-works section id or banner, and data has no step list; the Platform id is present', () => {
    const html = renderAppAt('/')
    expect(html, 'control: the Platform section rendered').toContain('id="platform"')
    expect(html, 'control: the page escapes & as &amp;, so the needles below can match').toContain('&amp;')
    expect(html).not.toContain(SECTION_ID)
    expect(html).not.toContain(BANNER)
    for (const copy of OLD_COPY) expect(html, `old step copy "${copy}" is back`).not.toContain(copy)
    expect(Object.keys(data), 'control: data.tsx exports are enumerated').not.toHaveLength(0)
    expect(Object.keys(data)).not.toContain(DATA_KEY)
  })
})

describe('R5-AC6 Compliance, Pricing and the TrustStrip name are gone from the page, the data and the tree', () => {
  const RETIRED_COPY = ['Priced by compliance need', '₦340k', '–2 MONTHS', 'Know exactly how compliant you are', 'Compliance readiness', 'TIN &amp; VAT identifier checks', 'Readiness score, live', 'Transmit-ready invoices']

  it('the markup carries none of their copy, and data.tsx exports none of their lists', () => {
    const html = renderAppAt('/')
    expect(html, 'control: the audience strip rendered').toContain('data-strip="audience"')
    expect(html, 'control: the page escapes & as &amp;, so the needle below can match').toContain('&amp;')
    for (const copy of RETIRED_COPY) expect(html, `retired copy "${copy}" is back`).not.toContain(copy)
    const keys = Object.keys(data)
    expect(keys, 'control: data.tsx exports are enumerated').toContain('FAQS')
    for (const key of ['PLANS', 'PLAN_COLORS', 'RULES']) expect(keys, `${key} is back in data.tsx`).not.toContain(key)
  })

  it('no component file of the retired sections remains, and the renamed strip is the one that does', () => {
    const files = readdirSync(fileURLToPath(new URL('./components', import.meta.url)))
    expect(files.length, 'population floor: the components directory resolved').toBeGreaterThan(30)
    expect(files, 'control: the strip kept under its v2 name').toContain('AudienceStrip.tsx')
    for (const gone of ['Compliance.tsx', 'Pricing.tsx', 'TrustStrip.tsx']) expect(files, `${gone} is back`).not.toContain(gone)
  })
})

describe('NV-12 no in-page anchor on the page resolves to a missing section', () => {
  const allHashes = (html: string) => [...html.matchAll(/href="\/?(#[^"]+)"/g)].map((m) => m[1])

  it('at / every href="#x" in the whole page has exactly one <section id="x">, and the nav, hero and footer anchors are in the population', () => {
    const html = renderAppAt('/')
    const hashes = allHashes(html)
    for (const h of ['#top', '#problem', '#solution', '#platform', '#solutions', '#integrations']) {
      expect(hashes, `control: ${h} is linked from the page`).toContain(h)
    }
    expect(unresolvedHashes(html, hashes)).toEqual([])
  })

  it('at /privacy every /#x anchor resolves against the sales page it links to', () => {
    const privacy = allHashes(renderAppAt('/privacy'))
    for (const h of ['#problem', '#solution', '#platform', '#solutions', '#integrations']) {
      expect(privacy, `control: /privacy links ${h}`).toContain(h)
    }
    expect(unresolvedHashes(renderAppAt('/'), privacy)).toEqual([])
  })
})
