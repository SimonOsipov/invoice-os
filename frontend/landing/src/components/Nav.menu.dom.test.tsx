// Header controls and the burger menu (RESKIN-02-01), mounted in jsdom.
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

describe('HD-04 Platform login opens sign-in', () => {
  it('the header button calls onSignIn once, is type=button and carries a-login a-link', () => {
    mount()
    const login = headerButton('Platform login')
    expect(login, 'expected a header button "Platform login"').toBeDefined()
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

describe('HD-07 the burger opens a menu that repeats the links and Platform login', () => {
  it('opens with links and one Platform login, swaps the glyph, and closes on a second click', () => {
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
    const logins = Array.from(m!.querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Platform login')
    expect(logins.length, 'exactly one Platform login in the menu').toBe(1)
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
  it('a link, Platform login and Book a demo each close the menu and fire once', () => {
    mount()

    toggle()
    act(() => void menu()!.querySelector<HTMLAnchorElement>('a.a-menu-link')!.click())
    expect(menu(), 'a menu link click closes the menu').toBeNull()

    toggle()
    const login = Array.from(menu()!.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Platform login')
    expect(login, 'expected the menu Platform login').toBeDefined()
    act(() => void login!.click())
    expect(menu(), 'menu Platform login closes the menu').toBeNull()
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
      { id: 'modules', top: 500 },
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
