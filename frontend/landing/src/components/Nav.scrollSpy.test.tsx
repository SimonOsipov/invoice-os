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

import { Nav } from './Nav'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

class StubIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const SECTIONS: { id: string; top: number }[] = [
  { id: 'problem', top: -400 },
  { id: 'modules', top: -200 },
  { id: 'compliance', top: -50 },
  { id: 'accountants', top: 10 }, // last section crossed at threshold 66 — the expected winner
  { id: 'developers', top: 500 },
  { id: 'pricing', top: 900 },
]

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  document.documentElement.style.setProperty('--header-h', '65px')
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
    expect(current[0].getAttribute('href')).toBe('/#accountants')

    // No other link is also marked current.
    const allLinks = container.querySelectorAll('.ios-nav-link')
    expect(allLinks.length).toBe(6)
  })

  it('the same crossed section lights up with the default (root) prefix too', () => {
    act(() => {
      root.render(createElement(Nav, { onSignIn: () => {}, onBookDemo: () => {} }))
    })

    const current = container.querySelectorAll('a[aria-current="true"]')
    expect(current.length).toBe(1)
    expect(current[0].getAttribute('href')).toBe('#accountants')
  })
})
