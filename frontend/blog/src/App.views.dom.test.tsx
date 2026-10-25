// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { BlogData } from './data'
import { TEST_DATA } from './testData'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root

async function mount(path: string, data: BlogData = TEST_DATA) {
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App load={() => Promise.resolve(data)} />))
}

async function remount(path: string, data: BlogData = TEST_DATA) {
  act(() => root.unmount())
  container.remove()
  await mount(path, data)
}

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
})

const q = <T extends HTMLElement>(sel: string) => container.querySelector<T>(sel)!
const all = <T extends HTMLElement>(sel: string) => [...container.querySelectorAll<T>(sel)]
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const crumbs = () => all('#blog-header nav[aria-label="Breadcrumb"] button').map((b) => b.textContent)
const navButtons = () => all<HTMLButtonElement>('.asc-app > aside nav button')
const row = (name: string) => navButtons().find((b) => b.querySelector('span')?.textContent === name)!
const input = () => q<HTMLInputElement>('#blog-header input')
const clearButton = () => container.querySelector<HTMLButtonElement>('button[aria-label="Clear search"]')
const main = () => q('#blog-main')

function type(value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input(), value)
    input().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

const withCover = (cover: string | null): BlogData => ({
  ...TEST_DATA,
  posts: TEST_DATA.posts.map((p) => (p.id === 'sage-rules' ? { ...p, cover } : p)),
})

describe('sidebar', () => {
  it('VW-01 the sidebar lists categories with counts', async () => {
    await mount('/')
    expect(navButtons().map((b) => b.textContent)).toEqual(['All posts4', 'Regulation2', 'Product2'])
  })

  it('VW-02 the current category is highlighted, also on its post', async () => {
    await mount('/category/c1')
    for (const path of ['/category/c1', '/sage-rules']) {
      if (path !== '/category/c1') await remount(path)
      expect(row('Regulation').getAttribute('aria-current'), path).toBe('true')
      expect(row('Regulation').style.fontWeight, path).toBe('700')
      expect(row('Regulation').style.background, path).toBe('var(--surface-panel)')
      expect(row('All posts').hasAttribute('aria-current'), path).toBe(false)
      expect(row('Product').hasAttribute('aria-current'), path).toBe(false)
    }
  })

  it('VW-01b a sidebar click from /blog/ pushes the base path into history', async () => {
    await mount('/blog/')
    const before = window.history.length
    click(row('Regulation'))
    expect(window.location.pathname).toBe('/blog/category/c1')
    expect(window.history.length).toBe(before + 1)
  })

  it('VW-13 nothing is highlighted while searching', async () => {
    await mount('/category/c1')
    type('sage')
    expect(navButtons().filter((b) => b.hasAttribute('aria-current'))).toEqual([])
    expect(row('Regulation').style.fontWeight).toBe('600')
    await remount('/')
    type('sage')
    expect(row('All posts').hasAttribute('aria-current')).toBe(false)
  })
})

describe('search', () => {
  it('VW-03 typing switches to search and clear returns', async () => {
    await mount('/blog/category/c1')
    expect(clearButton()).toBeNull()
    type('sage')
    expect(crumbs()).toEqual(['Blog', 'Search'])
    expect(all('#blog-main button.blog-row').length).toBeGreaterThan(0)
    expect(window.location.pathname).toBe('/blog/category/c1')
    click(clearButton()!)
    expect(main().querySelector('h2')?.textContent).toBe('Regulation')
    expect(input().value).toBe('')
    expect(clearButton()).toBeNull()
  })

  it('VW-04 a navigation clears the query', async () => {
    await mount('/')
    type('sage')
    click(q('#blog-main button.blog-row'))
    expect(window.location.pathname).toBe('/blog/sage-rules')
    expect(input().value).toBe('')
  })

  it('VW-12 re-clicking the current view clears the query', async () => {
    await mount('/')
    type('sage')
    let entries = window.history.length
    click(row('All posts'))
    expect(main().querySelector('h1')).not.toBeNull()
    expect(input().value).toBe('')
    expect(window.history.length).toBe(entries)

    await remount('/category/c1')
    type('sage')
    entries = window.history.length
    click(row('Regulation'))
    expect(main().querySelector('h2')?.textContent).toBe('Regulation')
    expect(input().value).toBe('')
    expect(window.history.length).toBe(entries)
  })

  it('VW-15 a whitespace-only query shows no clear button (prototype L84 uses the trimmed query)', async () => {
    await mount('/')
    type('   ')
    expect(document.querySelector('[aria-label="Clear search"]')).toBeNull()
  })

  it('VW-16 Back clears the query', async () => {
    await mount('/')
    click(row('Regulation'))
    type('sage')
    expect(crumbs()).toEqual(['Blog', 'Search'])
    await act(async () => window.dispatchEvent(new PopStateEvent('popstate')))
    expect(input().value).toBe('')
    expect(crumbs()).toEqual(['Blog', 'Regulation'])
  })

  it('VW-17 a band holds at most 3 cards but its button counts all posts', async () => {
    const base = TEST_DATA.posts.find((p) => p.category_id === 'c1')!
    const posts = [...TEST_DATA.posts, ...[1, 2, 3].map((n) => ({ ...base, id: `extra-${n}`, title: `Extra ${n}` }))]
    await mount('/', { ...TEST_DATA, posts })
    const band = all('#blog-main section')[1]
    expect(band.querySelectorAll('button:not(.ds-btn)')).toHaveLength(3)
    expect(band.querySelector('button.ds-btn')?.textContent).toBe('All 5 posts')
  })

  it('VW-05 no hit shows the empty state with chips', async () => {
    await mount('/')
    type('zzzz')
    expect(main().querySelector('h2')?.textContent).toBe('Nothing found for “zzzz”')
    expect(main().textContent).toContain('Try a shorter search, or browse a category.')
    const chips = all('#blog-main button.blog-chip')
    expect(chips.map((c) => c.textContent)).toEqual(['Regulation', 'Product'])
    click(chips[0])
    expect(window.location.pathname).toBe('/blog/category/c1')
  })
})

describe('home', () => {
  it('VW-06 home shows the featured card and the bands', async () => {
    await mount('/')
    expect(q('section .t-step').textContent).toMatch(/^Featured · Regulation · /)
    const bands = all('#blog-main section').slice(1, -1)
    expect(bands).toHaveLength(2)
    for (const b of bands) {
      expect(b.querySelectorAll('button.ds-btn').length).toBe(1)
      expect(b.querySelectorAll('button:not(.ds-btn)').length).toBeLessThanOrEqual(3)
    }
    expect(bands[0].querySelector('button.ds-btn')?.textContent).toBe('All 2 posts')
    for (const b of bands) expect(b.querySelector('button.ds-btn svg'), 'the band button keeps its arrow').not.toBeNull()
    const sub = all('#blog-main button.ds-btn').find((b) => b.textContent === 'Subscribe')!
    expect(sub.querySelector('svg'), 'the newsletter Subscribe keeps its arrow').not.toBeNull()
    expect(bands[0].style.background).toBe('var(--sage)')
    expect(bands[1].style.background).toBe('var(--background)')
    expect(main().textContent).toContain('New posts in your inbox.')
  })

  it('VW-07 with no pin the newest is Latest; with no posts no card', async () => {
    const unpinned: BlogData = { ...TEST_DATA, posts: TEST_DATA.posts.map((p) => ({ ...p, featured: false })) }
    await mount('/', unpinned)
    expect(q('section .t-step').textContent).toMatch(/^Latest · /)
    await remount('/', { ...TEST_DATA, posts: [] })
    expect(main().querySelector('h1')).not.toBeNull()
    expect(main().textContent).not.toContain('Read the post')
    expect(main().querySelector('section .t-step')).toBeNull()
    expect(main().textContent).toContain('New posts in your inbox.')
  })

  it('VW-08 Subscribe in the newsletter band opens /subscribe', async () => {
    await mount('/')
    const sub = all('#blog-main button.ds-btn').find((b) => b.textContent === 'Subscribe')!
    click(sub)
    expect(window.location.pathname).toBe('/blog/subscribe')
    expect(crumbs()).toEqual(['Blog', 'Subscribe'])
  })
})

describe('category page', () => {
  it('VW-14 an empty category shows its head and no cards', async () => {
    const data: BlogData = {
      ...TEST_DATA,
      categories: [
        ...TEST_DATA.categories,
        { id: 'c3', name: 'Customers', headline: 'Stories', intro: 'Teams that use ASComply.', icon: 'handshake', tone: 2, post_count: 0 },
      ],
    }
    await mount('/category/c3', data)
    expect(main().querySelector('.t-eyebrow')?.textContent).toBe('Category · 0 posts')
    expect(main().querySelector('h2')?.textContent).toBe('Customers')
    expect(main().textContent).toContain('Teams that use ASComply.')
    expect(main().querySelectorAll('section button')).toHaveLength(0)
    expect(main().textContent).not.toContain('This page is not on the blog.')
  })

  it('VW-09 the category page lists every post of the category', async () => {
    await mount('/category/c1')
    expect(main().querySelector('.t-eyebrow')?.textContent).toBe('Category · 2 posts')
    expect(main().querySelector('h2')?.textContent).toBe('Regulation')
    expect(main().textContent).toContain('What the tax authority changed.')
    const cards = all('#blog-main button').filter((b) => b.textContent?.includes('Read the post'))
    expect(cards).toHaveLength(2)
    click(cards[0])
    expect(window.location.pathname).toBe('/blog/sage-rules')
  })
})

describe('covers', () => {
  it('VW-10 a post without a cover shows the tone placeholder', async () => {
    await mount('/category/c2')
    expect(main().querySelector('img')).toBeNull()
    const ph = all('#blog-main div').filter((d) => d.style.background === 'var(--surface)')
    expect(ph).toHaveLength(2)
    expect(ph[0].querySelector('svg')).not.toBeNull()
  })

  it('VW-11 a failed cover falls back to the placeholder', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    await mount('/category/c1', withCover('/blog/covers/p/1'))
    const img = q<HTMLImageElement>('img[alt="Cover image"]')
    expect(img.getAttribute('src')).toBe('https://gw.test/blog/covers/p/1')
    act(() => img.dispatchEvent(new Event('error')))
    expect(main().querySelector('img')).toBeNull()
    expect(all('#blog-main div').filter((d) => d.style.background === 'var(--sage)')).toHaveLength(2)
  })
})
