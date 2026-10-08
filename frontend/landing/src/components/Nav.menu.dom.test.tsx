// Header controls and the burger menu, mounted in jsdom.
// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

import { GLYPHS } from '../icons'
import { NAV_LINKS, Nav } from './Nav'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

class StubIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

let container: HTMLDivElement
let root: Root
let mounted = false
let onSignIn: Mock<() => void>
let onBookDemo: Mock<() => void>

const mount = (props: { hrefPrefix?: string } = {}) => {
  act(() => {
    root.render(createElement(Nav, { onSignIn, onBookDemo, ...props }))
  })
  mounted = true
}

beforeEach(() => {
  onSignIn = vi.fn<() => void>()
  onBookDemo = vi.fn<() => void>()
  document.documentElement.style.setProperty('--header-h', '86px')
  ;(globalThis as { IntersectionObserver?: unknown }).IntersectionObserver = StubIntersectionObserver
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  if (mounted) act(() => root.unmount())
  mounted = false
  container.remove()
  document.querySelectorAll('section[id]').forEach((el) => el.remove())
  document.documentElement.style.removeProperty('--header-h')
  vi.restoreAllMocks()
})

const header = () => document.querySelector('header')!
const headerButton = (text: string) =>
  Array.from(header().querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
const burger = () => {
  const b = document.querySelector<HTMLButtonElement>('button.a-burger')
  expect(b, 'expected button.a-burger').not.toBeNull()
  return b!
}
const menu = () => document.getElementById(burger().getAttribute('aria-controls') ?? '')
const toggle = () => act(() => void burger().click())
const paths = (el: Element) => Array.from(el.querySelectorAll('path')).map((p) => p.getAttribute('d'))

describe('HD-04 Sign in opens sign-in', () => {
  it('the header button calls onSignIn once, is type=button and carries a-login a-link', () => {
    mount()
    const login = headerButton('Sign in')
    expect(login, 'expected a header button "Sign in"').toBeDefined()
    expect(login!.getAttribute('type')).toBe('button')
    expect(login!.classList.contains('a-login')).toBe(true)
    expect(login!.classList.contains('a-link')).toBe(true)

    act(() => void login!.click())
    expect(onSignIn).toHaveBeenCalledTimes(1)
    expect(onBookDemo).toHaveBeenCalledTimes(0)
  })
})

describe('HD-05 Book a demo is the small primary DS button', () => {
  it('carries ds-btn ds-btn--primary ds-btn--sm and calls onBookDemo once', () => {
    mount()
    const book = headerButton('Book a demo')
    expect(book, 'expected a header button "Book a demo"').toBeDefined()
    expect(book!.className.split(/\s+/)).toEqual(expect.arrayContaining(['ds-btn', 'ds-btn--primary', 'ds-btn--sm']))

    act(() => void book!.click())
    expect(onBookDemo).toHaveBeenCalledTimes(1)
    expect(onSignIn).toHaveBeenCalledTimes(0)
  })
})

describe('HD-07 the burger opens a menu that repeats the links and Sign in', () => {
  it('opens with links and one Sign in, swaps the glyph, and closes on a second click', () => {
    mount({ hrefPrefix: '/' })
    expect(burger().getAttribute('aria-expanded'), 'control: closed at rest').toBe('false')
    expect(menu(), 'control: no menu at rest').toBeNull()

    toggle()
    expect(burger().getAttribute('aria-expanded')).toBe('true')
    const m = menu()
    expect(m, 'aria-controls names no element while open').not.toBeNull()
    const links = Array.from(m!.querySelectorAll('a.a-menu-link'))
    expect(links.length, 'one a.a-menu-link per NAV_LINKS entry').toBe(NAV_LINKS.length)
    expect(links.map((a) => a.getAttribute('href'))).toEqual(NAV_LINKS.map((l) => `/${l.href}`))
    const logins = Array.from(m!.querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Sign in')
    expect(logins.length, 'exactly one Sign in in the menu').toBe(1)
    expect(Array.from(m!.querySelectorAll('button'), (b) => b.textContent?.trim()), 'menu_holdsOnlySignIn').toEqual(['Sign in'])
    expect(paths(burger())).toEqual([...GLYPHS.x])

    toggle()
    expect(burger().getAttribute('aria-expanded')).toBe('false')
    expect(menu(), 'the menu is removed on a second click').toBeNull()
    expect(paths(burger())).toEqual([...GLYPHS.menu])
  })
})

describe('HD-08 Escape closes the menu and returns focus', () => {
  it('Escape on document closes it and focuses the burger', () => {
    mount()
    toggle()
    const link = menu()!.querySelector<HTMLAnchorElement>('a.a-menu-link')
    expect(link, 'expected a menu link to focus').not.toBeNull()
    link!.focus()
    expect(document.activeElement, 'control: focus sits on the menu link').toBe(link)

    act(() => void document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(menu(), 'the menu is gone').toBeNull()
    expect(burger().getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement, 'focus returns to the burger').toBe(burger())
  })
})

describe('HD-08b Escape with the menu closed does nothing', () => {
  it('logs no error, keeps aria-expanded false and leaves focus on Book a demo', () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    mount()
    expect(burger().getAttribute('aria-expanded'), 'control: the burger exists and is closed').toBe('false')
    const book = headerButton('Book a demo')
    expect(book, 'expected a header button "Book a demo"').toBeDefined()
    book!.focus()

    act(() => void document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(consoleError).not.toHaveBeenCalled()
    expect(burger().getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(book)
  })
})

describe('HD-09 menu actions close the menu first', () => {
  it('a link, Sign in and Book a demo each close the menu and fire once', () => {
    mount()

    toggle()
    act(() => void menu()!.querySelector<HTMLAnchorElement>('a.a-menu-link')!.click())
    expect(menu(), 'a menu link click closes the menu').toBeNull()

    toggle()
    const login = Array.from(menu()!.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Sign in')
    expect(login, 'expected the menu Sign in').toBeDefined()
    act(() => void login!.click())
    expect(menu(), 'menu Sign in closes the menu').toBeNull()
    expect(onSignIn).toHaveBeenCalledTimes(1)

    toggle()
    expect(menu(), 'control: reopened').not.toBeNull()
    act(() => void headerButton('Book a demo')!.click())
    expect(menu(), 'the row Book a demo closes the menu').toBeNull()
    expect(onBookDemo).toHaveBeenCalledTimes(1)
  })
})

describe('HD-10 the keydown listener lives only while the menu is open', () => {
  it('keydown is added twice and removed twice over open, close, open, unmount', () => {
    const add = vi.spyOn(document, 'addEventListener')
    const remove = vi.spyOn(document, 'removeEventListener')
    const keydowns = (spy: typeof add) => spy.mock.calls.filter((c) => c[0] === 'keydown').length

    mount()
    expect(burger().getAttribute('aria-expanded'), 'control: the burger exists').toBe('false')
    expect(keydowns(add), 'no listener before the first open').toBe(0)

    toggle()
    expect(keydowns(add)).toBe(1)
    toggle()
    expect(keydowns(remove)).toBe(1)
    toggle()
    expect(keydowns(add)).toBe(2)

    act(() => root.unmount())
    mounted = false
    expect(keydowns(remove), 'unmount while open removes the listener').toBe(2)
  })
})

describe('HD-17 menu links share the nav link current state', () => {
  it('the nav link and the menu link for #problem are both current and nothing else is', () => {
    for (const s of [
      { id: 'top', top: -500 },
      { id: 'problem', top: 10 },
      { id: 'solution', top: 500 },
    ]) {
      const el = document.createElement('section')
      el.id = s.id
      el.getBoundingClientRect = () => ({ top: s.top }) as DOMRect
      document.body.appendChild(el)
    }
    mount()
    const navLink = document.querySelector('nav[aria-label="Primary"] a[aria-current="true"]')
    expect(navLink?.getAttribute('href'), 'control: the nav link is current before the menu opens').toBe('#problem')

    toggle()
    const menuLink = menu()?.querySelector('a.a-menu-link[href$="#problem"]')
    expect(menuLink, 'expected the menu link for #problem').not.toBeNull()
    expect(menuLink!.getAttribute('aria-current')).toBe('true')
    expect(Array.from(document.querySelectorAll('[aria-current]'))).toEqual([navLink, menuLink])
  })
})

describe('HD-07b aria-controls names the same menu id at rest and while open', () => {
  it('the id is non-empty, identical closed, open and reopened, and is the menu element id', () => {
    mount()
    const atRest = burger().getAttribute('aria-controls')
    expect(atRest, 'aria-controls is empty at rest').toBeTruthy()

    toggle()
    expect(burger().getAttribute('aria-controls')).toBe(atRest)
    expect(menu()?.id).toBe(atRest)
    expect(document.querySelectorAll(`[id="${atRest}"]`).length, 'the id is unique on the page').toBe(1)

    toggle()
    toggle()
    expect(burger().getAttribute('aria-controls'), 'the id survives a reopen').toBe(atRest)
    expect(menu()?.id).toBe(atRest)
  })
})

describe('HD-07c the menu lists every NAV_LINKS entry in order, prefixed or not', () => {
  // Planted: the menu must grow with the list, whatever the live list holds.
  const EXTRA = [
    { label: 'Extra A', href: '#extra-a' },
    { label: 'Extra B', href: '#extra-b' },
  ]
  let before: number
  beforeEach(() => {
    before = NAV_LINKS.length
    NAV_LINKS.push(...EXTRA)
  })
  afterEach(() => void NAV_LINKS.splice(before))

  it.each([
    ['', ''],
    ['/', '/'],
  ])('with hrefPrefix %j the menu hrefs are the prefixed NAV_LINKS hrefs, in list order', (hrefPrefix, p) => {
    expect(NAV_LINKS.length, 'control: the fixture grew the list to seven').toBe(7)
    mount(hrefPrefix ? { hrefPrefix } : {})
    toggle()
    const links = Array.from(menu()!.querySelectorAll('a.a-menu-link'))
    expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual(
      NAV_LINKS.map((l) => [l.label, `${p}${l.href}`]),
    )
  })

  it('NV-09 the grown list holds distinct hrefs and mounts without a duplicate-key warning', () => {
    const hrefs = NAV_LINKS.map((l) => l.href)
    expect(hrefs.length, 'control: the fixture grew the list to seven').toBe(7)
    expect(new Set(hrefs).size, `duplicate href in ${JSON.stringify(hrefs)}`).toBe(hrefs.length)
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    mount()
    toggle()
    expect(Array.from(menu()!.querySelectorAll('a.a-menu-link')).map((a) => a.getAttribute('href'))).toEqual(hrefs)
    expect(spy.mock.calls).toEqual([])
  })
})

describe('NV-10 the menu lists the five sections', () => {
  it.each([
    ['', ''],
    ['/', '/'],
  ])("with hrefPrefix %j the open menu holds exactly V851's five sections", (hrefPrefix, p) => {
    mount(hrefPrefix ? { hrefPrefix } : {})
    toggle()
    const links = Array.from(menu()!.querySelectorAll('a.a-menu-link'))
    expect(links.length, 'control: the menu rendered its links').toBeGreaterThanOrEqual(1)
    expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['The problem', `${p}#problem`],
      ['The solution', `${p}#solution`],
      ['Platform', `${p}#platform`],
      ["Who it's for", `${p}#solutions`],
      ['Integrations', `${p}#integrations`],
    ])
  })
})

describe('HD-08c only Escape closes the open menu', () => {
  const key = (k: string) => act(() => void document.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })))

  it('other keys leave it open; Escape closes it from a body focus and sends focus to the burger; a second Escape is inert', () => {
    mount()
    toggle()
    expect(menu(), 'control: open').not.toBeNull()
    ;(document.activeElement as HTMLElement | null)?.blur()
    for (const k of ['Enter', 'Esc', ' ', 'Tab', 'ArrowDown', 'escape']) key(k)
    expect(menu(), 'a non-Escape key closed the menu').not.toBeNull()
    expect(burger().getAttribute('aria-expanded')).toBe('true')

    key('Escape')
    expect(menu()).toBeNull()
    expect(document.activeElement, 'focus lands on the burger even from <body>').toBe(burger())

    const book = headerButton('Book a demo')!
    book.focus()
    key('Escape')
    expect(document.activeElement, 'a second Escape must not move focus').toBe(book)
  })
})

describe('HD-09b header Book a demo and burger interplay', () => {
  it('Book a demo with the menu open closes it and the listener goes with it', () => {
    const remove = vi.spyOn(document, 'removeEventListener')
    mount()
    toggle()
    act(() => void headerButton('Book a demo')!.click())
    expect(menu()).toBeNull()
    expect(burger().getAttribute('aria-expanded')).toBe('false')
    expect(remove.mock.calls.filter((c) => c[0] === 'keydown').length).toBe(1)
    expect(onBookDemo).toHaveBeenCalledTimes(1)
  })

  it('a menu link click sets the active link and closes; the header Sign in stays one control', () => {
    for (const s of [
      { id: 'top', top: -500 },
      { id: 'problem', top: 500 },
    ]) {
      const el = document.createElement('section')
      el.id = s.id
      el.getBoundingClientRect = () => ({ top: s.top }) as DOMRect
      document.body.appendChild(el)
    }
    mount()
    expect(document.querySelector('[aria-current]'), 'control: nothing is current before the click').toBeNull()
    toggle()
    act(() => void menu()!.querySelector<HTMLAnchorElement>('a.a-menu-link')!.click())
    expect(menu()).toBeNull()
    const current = document.querySelector('nav[aria-label="Primary"] a[aria-current="true"]')
    expect(current?.getAttribute('href'), 'the click lights the nav link at once').toBe('#problem')
    expect(Array.from(header().querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Sign in').length).toBe(1)
  })
})

describe('HD-14b a menu left open across a resize stays coherent', () => {
  it('the menu and listener survive a resize above the breakpoint, links re-evaluate, Escape still closes', () => {
    const sec = document.createElement('section')
    sec.id = 'problem'
    sec.getBoundingClientRect = () => ({ top: 80 }) as DOMRect
    document.body.appendChild(sec)
    mount()
    toggle()
    expect(menu()!.querySelector('a.a-menu-link')?.getAttribute('aria-current'), 'control: current at 86px').toBe('true')

    document.documentElement.style.setProperty('--header-h', '73px')
    act(() => void window.dispatchEvent(new Event('resize')))
    expect(menu(), 'CSS hides the menu above 1120px; the JS state is not reset').not.toBeNull()
    expect(burger().getAttribute('aria-expanded')).toBe('true')
    expect(document.querySelector('[aria-current]'), 'no link is current at 73px, nav or menu').toBeNull()

    act(() => void document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(menu()).toBeNull()
    expect(document.activeElement).toBe(burger())
  })
})
