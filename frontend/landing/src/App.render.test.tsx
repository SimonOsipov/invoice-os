// Adversarial coverage (QA, LAND-04-04) — App.route.test.ts only regex-scans the
// source text (`hrefPrefix={privacy`), which matches equally whether the ternary
// reads `privacy ? '/' : ''` or the inverted `privacy ? '' : '/'`. Neither Footer's
// nor Nav's own unit tests can catch that either — they take hrefPrefix as an
// explicit prop, never through App's actual `privacy` boolean. This file renders
// the real App tree (SSR, no jsdom) at both paths to close that gap.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import App from './App'

const REAL_ANCHORS = ['#modules', '#compliance', '#accountants', '#developers', '#pricing']

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

describe('App SSR wiring — hrefPrefix reaches Footer with the right polarity', () => {
  it('at /privacy: the five real anchors are prefixed, the three stubs and /privacy are not doubled', () => {
    const footer = footerSlice(renderAppAt('/privacy'))
    for (const target of REAL_ANCHORS) {
      expect(footer, target).toContain(`href="/${target}"`)
      expect(footer, target).not.toContain(`href="//${target}"`)
    }
    expect(footer.match(/href="#"/g)?.length, 'expected exactly 3 unprefixed stubs').toBe(3)
    expect(footer).toContain('href="/privacy"')
    expect(footer).not.toContain('href="//privacy"')
    expect(footer).toMatch(/<button[^>]*>Book a demo</)
  })

  it('at /: the footer anchors are NOT prefixed (catches an inverted ternary)', () => {
    const footer = footerSlice(renderAppAt('/'))
    for (const target of REAL_ANCHORS) {
      expect(footer, target).toContain(`href="${target}"`)
      expect(footer, target).not.toContain(`href="/${target}"`)
    }
    expect(footer).toContain('href="/privacy"')
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
