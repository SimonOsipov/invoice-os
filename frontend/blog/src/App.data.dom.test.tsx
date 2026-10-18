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

async function mount(path: string, load: () => Promise<BlogData>) {
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App load={load} />))
}

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

const q = <T extends HTMLElement>(sel: string) => container.querySelector<T>(sel)!
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const crumbs = () => [...q('#blog-header nav[aria-label="Breadcrumb"]').querySelectorAll('button')].map((b) => b.textContent)
const settled = () => Promise.resolve(TEST_DATA)

describe('data states', () => {
  it('DA-01 loading shows the shell only', async () => {
    await mount('/', () => new Promise(() => {}))
    expect(q('#blog-main').querySelector('section')).toBeNull()
    expect(crumbs()).toEqual(['Blog'])
    expect(q('.asc-app > aside').textContent).toContain('All posts')
    expect(q('.asc-app > aside nav button').textContent).toBe('All posts')
  })

  it('DA-02 a failed load offers Try again', async () => {
    const load = vi.fn<() => Promise<BlogData>>().mockRejectedValueOnce(new Error('x')).mockImplementation(settled)
    await mount('/', load)
    expect(q('#blog-main').textContent).toContain('The blog did not load.')
    const retry = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Try again')!
    await act(async () => retry.dispatchEvent(new MouseEvent('click', { bubbles: true })))
    expect(load).toHaveBeenCalledTimes(2)
    expect(q('#blog-main').textContent).not.toContain('The blog did not load.')
    expect(q('#blog-main section[data-view="home"]')).not.toBeNull()
  })

  it('DA-03 an unknown slug shows not found', async () => {
    await mount('/no-such-post', settled)
    expect(q('#blog-main h2').textContent).toBe('This page is not on the blog.')
    expect(crumbs()).toEqual(['Blog'])
    const chips = [...container.querySelectorAll('#blog-main button.blog-chip')]
    expect(chips.map((c) => c.textContent)).toEqual(TEST_DATA.categories.map((c) => c.name))
    click(chips[0])
    expect(window.location.pathname).toBe('/category/c1')
  })

  it('DA-04 the cookie notice comes after the view', async () => {
    await mount('/', settled)
    const main = q('#blog-main')
    expect(main.lastElementChild?.classList.contains('cn-spacer')).toBe(true)
    const view = main.querySelector('section')!
    const notice = main.querySelector('[role="region"], [role="dialog"]')!
    expect(notice).not.toBeNull()
    expect(view.compareDocumentPosition(notice) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('DA-05 a post whose category is missing still renders', async () => {
    const data: BlogData = { ...TEST_DATA, posts: [{ ...TEST_DATA.posts[0], category_id: 'new-cat' }] }
    await mount('/sage-rules', () => Promise.resolve(data))
    expect(crumbs()).toEqual(['Blog', 'New sage rules for invoices'])
    expect(q('#blog-main section[data-view="post"]')).not.toBeNull()
    expect(q('#blog-main').textContent).not.toContain('This page is not on the blog.')
  })
})
