// Adversarial coverage (task-557, LAND-04-03 QA pass). Every existing Nav test renders via
// renderToStaticMarkup, which never runs useEffect — so nothing exercises the scroll-spy
// effect (NAV_HREFS, activeNavHref, aria-current) under a non-empty hrefPrefix. Confirmed by
// mutation: routing NAV_HREFS through the prefix before calling activeNavHref (breaking
// aria-current on every page) left the full suite green. This file closes that gap with a
// real jsdom mount.
// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { NAV_LINKS, Nav } from './Nav'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const rootMargins: string[] = []

class StubIntersectionObserver {
  constructor(_cb: unknown, opts?: { rootMargin?: string }) {
    rootMargins.push(opts?.rootMargin ?? '')
  }
  observe() {}
  unobserve() {}
  disconnect() {}
}

const SECTIONS: { id: string; top: number }[] = [
  { id: 'top', top: -500 },
  { id: 'problem', top: 10 }, // last section crossed at threshold 87 — the expected winner
  { id: 'solution', top: 500 },
  { id: 'platform', top: 900 },
  { id: 'compliance', top: 1300 },
]

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  rootMargins.length = 0
  document.documentElement.style.setProperty('--header-h', '86px')
  ;(globalThis as { IntersectionObserver?: unknown }).IntersectionObserver = StubIntersectionObserver
  for (const s of SECTIONS) {
    const el = document.createElement('section')
    el.id = s.id
    el.getBoundingClientRect = () => ({ top: s.top }) as DOMRect
    document.body.appendChild(el)
  }
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.querySelectorAll('section[id]').forEach((el) => el.remove())
  document.documentElement.style.removeProperty('--header-h')
})

describe('Nav scroll-spy under a non-empty hrefPrefix', () => {
  it('aria-current lands on the crossed section, and its href carries the prefix', () => {
    act(() => {
      root.render(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, hrefPrefix: '/' }))
    })

    const current = container.querySelectorAll('a[aria-current="true"]')
    expect(current.length).toBe(1)
    expect(current[0].getAttribute('href')).toBe('/#problem')

    // No other link is also marked current.
    const allLinks = container.querySelectorAll('.ios-nav-link')
    expect(allLinks.length).toBe(NAV_LINKS.length)
    expect(allLinks.length).toBeGreaterThanOrEqual(1)
  })

  it('the same crossed section lights up with the default (root) prefix too', () => {
    act(() => {
      root.render(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    })

    const current = container.querySelectorAll('a[aria-current="true"]')
    expect(current.length).toBe(1)
    expect(current[0].getAttribute('href')).toBe('#problem')
  })
})

describe('Nav scroll-spy follows --header-h across the breakpoint', () => {
  const frames = () => act(async () => void (await new Promise((r) => setTimeout(r, 60))))
  const resizeTo = (px: number) => {
    document.documentElement.style.setProperty('--header-h', `${px}px`)
    act(() => void window.dispatchEvent(new Event('resize')))
  }
  const mount = () =>
    act(() => {
      root.render(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    })
  const currentHref = () => container.querySelector('a[aria-current="true"]')?.getAttribute('href')

  it('the observer rootMargin takes the new header height after a resize', async () => {
    document.documentElement.style.setProperty('--header-h', '86px')
    mount()
    expect(rootMargins.at(-1), 'control: the observer is built from the mounted token').toMatch(/^-86px /)

    resizeTo(73)
    await frames()
    expect(rootMargins.at(-1), 'observer rootMargin after --header-h 86px -> 73px').toMatch(/^-73px /)
  })

  it('the active link uses the new header height after a resize', async () => {
    // problem sits at 80px: crossed at threshold 87 (header 86px), not at 74 (header 73px)
    document.getElementById('problem')!.getBoundingClientRect = () => ({ top: 80 }) as DOMRect
    document.documentElement.style.setProperty('--header-h', '86px')
    mount()
    expect(currentHref(), 'control: problem is crossed at the 86px header').toBe('#problem')

    resizeTo(73)
    await frames()
    expect(currentHref(), 'no link is current once the last crossed section is top').toBeUndefined()
  })
})

describe('NV-11 the scroll-spy marks The solution and Platform current, and clears past them', () => {
  const setTops = (tops: Record<string, number>) => {
    for (const [id, top] of Object.entries(tops)) {
      document.getElementById(id)!.getBoundingClientRect = () => ({ top }) as DOMRect
    }
  }
  const FAR = 5000
  const cases: [string, Record<string, number>, string | null][] = [
    ['The problem', { problem: 10, solution: FAR, platform: FAR, compliance: FAR }, '#problem'],
    ['The solution', { problem: -900, solution: 10, platform: FAR, compliance: FAR }, '#solution'],
    ['Platform', { problem: -1800, solution: -900, platform: 10, compliance: FAR }, '#platform'],
    ['Compliance', { problem: -2700, solution: -1800, platform: -900, compliance: 10 }, null],
  ]

  it.each(
    cases.flatMap(([name, tops, href]) => [
      { title: `${name} crossed, no prefix`, tops, href, hrefPrefix: '' },
      { title: `${name} crossed, prefix slash`, tops, href, hrefPrefix: '/' },
    ]),
  )('$title: the nav marks only its own link current', ({ tops, href, hrefPrefix }) => {
    setTops(tops)
    act(() => {
      root.render(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {}, ...(hrefPrefix ? { hrefPrefix } : {}) }))
    })
    expect(container.querySelectorAll('.ios-nav-link'), 'control: every NAV_LINKS entry rendered').toHaveLength(NAV_LINKS.length)
    const current = Array.from(container.querySelectorAll('a[aria-current="true"]')).map((a) => a.getAttribute('href'))
    expect(current).toEqual(href ? [`${hrefPrefix}${href}`] : [])
  })
})
