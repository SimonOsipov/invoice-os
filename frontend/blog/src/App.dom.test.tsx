// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { App } from './App'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root

function mount(path: string) {
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<StrictMode><App /></StrictMode>))
}

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

const q = <T extends HTMLElement>(sel: string) => container.querySelector<T>(sel)!
const byText = (sel: string, text: string) =>
  [...container.querySelectorAll<HTMLElement>(sel)].find((e) => e.textContent?.trim() === text)!
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const crumbButtons = () => [...q('#blog-header nav[aria-label="Breadcrumb"]').querySelectorAll<HTMLElement>('button')]
const crumbs = () => crumbButtons().map((b) => b.textContent)

async function move(go: () => void) {
  const popped = new Promise<void>((r) => window.addEventListener('popstate', () => r(), { once: true }))
  await act(async () => {
    go()
    await popped
  })
}

describe('shell', () => {
  it('SH-01 the shell renders the sidebar and the header', () => {
    mount('/')
    const aside = q('aside')
    expect(aside.textContent).toContain('BLOG')
    expect(aside.textContent).toContain('CATEGORIES')
    expect(byText('aside button', 'Subscribe')).toBeDefined()
    expect(byText('aside button', 'All posts')).toBeDefined()
    expect(byText('aside a', 'Go to ascomply.com')).toBeDefined()
    const header = q('#blog-header')
    expect(header.querySelector('nav[aria-label="Breadcrumb"]')).not.toBeNull()
    expect(header.querySelector('input[aria-label="Search posts"]')).not.toBeNull()
  })

  it.each([
    ['/', ['Blog']],
    ['/subscribe', ['Blog', 'Subscribe']],
    ['/category/x', ['Blog']],
    ['/a-post', ['Blog']],
  ])('SH-02 crumbs follow the route (%s)', (path, want) => {
    mount(path)
    expect(crumbs()).toEqual(want)
    const buttons = crumbButtons()
    const last = buttons[buttons.length - 1]
    expect(last.style.fontWeight).toBe('700')
    expect(last.style.color).toBe('var(--ink)')
    if (buttons.length > 1) {
      expect(buttons[0].style.fontWeight).toBe('600')
      expect(buttons[0].style.color).toBe('var(--muted-foreground)')
      expect(buttons[0].nextElementSibling?.querySelector('svg')).not.toBeNull()
    }
    expect(last.nextElementSibling).toBeNull()
  })

  it('SH-03 Subscribe, the brand and All posts navigate', () => {
    mount('/')
    const before = window.history.length
    click(byText('aside button', 'Subscribe'))
    expect(window.location.pathname).toBe('/subscribe')
    expect(window.history.length).toBe(before + 1)
    expect(crumbs()).toEqual(['Blog', 'Subscribe'])

    click(crumbButtons()[0])
    expect(window.location.pathname).toBe('/')
    expect(crumbs()).toEqual(['Blog'])

    const entries = window.history.length
    click(q('aside button'))
    click(byText('aside button', 'All posts'))
    expect(window.location.pathname).toBe('/')
    expect(window.history.length).toBe(entries)
  })

  it('SH-04 a non-canonical URL is corrected in place', () => {
    mount('/subscribe/')
    const before = window.history.length
    click(byText('aside button', 'Subscribe'))
    expect(window.location.pathname).toBe('/subscribe')
    expect(window.history.length).toBe(before)
  })

  it('SH-05 back and forward re-render the route', async () => {
    mount('/')
    click(byText('aside button', 'Subscribe'))
    await move(() => window.history.back())
    expect(crumbs()).toEqual(['Blog'])
    await move(() => window.history.forward())
    expect(crumbs()).toEqual(['Blog', 'Subscribe'])
  })

  it('SH-06 All posts is current only on home', () => {
    mount('/')
    const allPosts = () => byText('aside button', 'All posts')
    expect(allPosts().getAttribute('aria-current')).toBe('true')
    expect(allPosts().style.background).toBe('var(--surface-panel)')
    act(() => root.unmount())
    container.remove()
    for (const path of ['/subscribe', '/category/x']) {
      mount(path)
      expect(allPosts().hasAttribute('aria-current'), path).toBe(false)
      expect(allPosts().style.background, path).toBe('transparent')
      act(() => root.unmount())
      container.remove()
    }
    mount('/')
  })
})
