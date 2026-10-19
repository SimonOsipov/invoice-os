// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { Block, BlogData, Post } from './data'
import { TEST_DATA } from './testData'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root

const H: Block[] = [
  { type: 'p', text: 'Intro paragraph.' },
  { type: 'h', text: 'First heading' },
  { type: 'h', text: 'Second heading' },
]

const dataWith = (body: Block[], extra: Partial<Post> = {}): BlogData => ({
  ...TEST_DATA,
  posts: TEST_DATA.posts.map((p) =>
    p.id === 'sage-rules' ? { ...p, publish_date: '2026-10-09', author_name: 'ASComply Editorial', body, ...extra } : p,
  ),
})

async function mount(path: string, data: BlogData) {
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App load={() => Promise.resolve(data)} />))
}

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

const q = <T extends HTMLElement>(sel: string) => container.querySelector<T>(sel)!
const all = <T extends HTMLElement>(sel: string) => [...container.querySelectorAll<T>(sel)]
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const crumbs = () => all('#blog-header nav[aria-label="Breadcrumb"] button').map((b) => b.textContent)
const article = () => q('article[data-view="post"]')
const tocButtons = () => all<HTMLButtonElement>('aside[aria-label="On this page"] button')
const band = () => q('#blog-main section')
const path = () => window.location.pathname

function stubTop(el: Element, top: number) {
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top, bottom: top, left: 0, right: 0, width: 0, height: 0, x: 0, y: top, toJSON: () => ({}) })
}

function stubScrolls(main: boolean) {
  const m = q('#blog-main')
  Object.defineProperty(m, 'clientHeight', { configurable: true, value: 500 })
  Object.defineProperty(m, 'scrollHeight', { configurable: true, value: main ? 2000 : 500 })
  const spy = vi.fn()
  const target = main ? m : document.documentElement
  ;(target as { scrollTo: unknown }).scrollTo = spy
  if (!main) Object.defineProperty(document, 'scrollingElement', { configurable: true, value: document.documentElement })
  return spy
}

describe('article', () => {
  it('AR-01 the article head shows meta, title, summary and cover', async () => {
    await mount('/sage-rules', dataWith(H))
    expect(q('article button.blog-back').textContent).toBe('Regulation')
    expect(article().querySelector('span.mono')!.textContent).toBe('9 Oct 2026 · ASComply Editorial')
    expect(article().querySelector('h1')!.textContent).toBe('New sage rules for invoices')
    expect(article().querySelector('p.t-lead')!.textContent).toBe('A short summary of the rules.')
    expect(article().querySelector<HTMLElement>('div[style*="aspect-ratio"]')!.style.aspectRatio).toBe('21 / 9')
  })

  it('AR-02 the back link opens the category', async () => {
    await mount('/sage-rules', dataWith(H))
    click(q('article button.blog-back'))
    expect(path()).toBe('/category/c1')
  })

  it('AR-03 each block type renders', async () => {
    const body: Block[] = [
      { type: 'p', text: 'Plain paragraph.' },
      { type: 'h', text: 'A heading' },
      { type: 'ul', items: ['one', 'two', 'three'] },
      { type: 'note', kind: 'deadline', title: 'Cut-off', text: 'File by month end.' },
      { type: 'quote', text: 'Check twice.', who: 'An accountant' },
      { type: 'demo', demo: 'import' },
    ]
    await mount('/sage-rules', dataWith(body))
    expect(article().querySelector('p.t-body')!.textContent).toBe('Plain paragraph.')
    expect(article().querySelector('h2')!.id).toBe('sec-0')
    const items = all('article span.t-body')
    expect(items.map((i) => i.textContent)).toEqual(['one', 'two', 'three'])
    items.forEach((i) => expect(i.previousElementSibling!.querySelector('svg')).not.toBeNull())
    const label = all('article .t-step').find((e) => e.textContent === 'Deadline')!
    expect((label.parentElement!.parentElement as HTMLElement).style.background).toBe('var(--peach-tint)')
    expect(article().querySelector('blockquote')!.textContent).toBe('“Check twice.”')
    expect(article().querySelector('figcaption')!.textContent).toBe('An accountant')
  })

  it('AR-04 the three note kinds', async () => {
    const body: Block[] = (['regulation', 'deadline', 'product'] as const).map((kind) => ({ type: 'note', kind, title: 't', text: 'x' }))
    await mount('/sage-rules', dataWith(body))
    const rows = all('article .t-step').map((l) => ({
      label: l.textContent,
      color: (l as HTMLElement).style.color,
      bg: (l.parentElement!.parentElement as HTMLElement).style.background,
      bd: (l.parentElement!.parentElement as HTMLElement).style.border,
    }))
    expect(rows.map((r) => [r.label, r.color, r.bg])).toEqual([
      ['Regulation note', 'var(--step-label)', 'var(--sage-card)'],
      ['Deadline', 'var(--status-amber-text)', 'var(--peach-tint)'],
      ['Availability', 'var(--teal)', 'var(--mint-soft)'],
    ])
    expect(rows[1].bd).toContain('var(--status-amber-border)')
  })

  it('AR-05 the TOC shows from two headings', async () => {
    await mount('/sage-rules', dataWith(H))
    expect(tocButtons().map((b) => b.textContent)).toEqual(['01First heading', '02Second heading'])
    act(() => root.unmount())
    container.remove()
    await mount('/sage-rules', dataWith(H.slice(0, 2)))
    expect(q('aside[aria-label="On this page"]')).toBeNull()
  })

  it('AR-06 a TOC click scrolls the scroller', async () => {
    await mount('/sage-rules', dataWith(H))
    const spy = stubScrolls(true)
    click(tocButtons()[1])
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0]).toMatchObject({ behavior: 'smooth' })
  })

  it('AR-07 the TOC follows the section in view', async () => {
    await mount('/sage-rules', dataWith(H))
    stubScrolls(true)
    stubTop(q('#blog-main'), 0)
    stubTop(q('#sec-0'), -200)
    stubTop(q('#sec-1'), 80)
    act(() => q('#blog-main').dispatchEvent(new Event('scroll')))
    expect(tocButtons()[1].querySelector<HTMLElement>('span:last-child')!.style.fontWeight).toBe('700')
    expect(tocButtons()[0].querySelector<HTMLElement>('span:last-child')!.style.fontWeight).toBe('500')
  })

  it('AR-10 on a phone the TOC scrolls the document', async () => {
    await mount('/sage-rules', dataWith(H))
    const spy = stubScrolls(false)
    click(tocButtons()[1])
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0]).toMatchObject({ behavior: 'smooth' })
    stubTop(q('#sec-0'), -200)
    stubTop(q('#sec-1'), 80)
    act(() => window.dispatchEvent(new Event('scroll')))
    expect(tocButtons()[1].querySelector<HTMLElement>('span:last-child')!.style.fontWeight).toBe('700')
  })

  it('AR-11 an empty author shows the date alone', async () => {
    await mount('/sage-rules', dataWith(H, { author_name: '' }))
    expect(article().querySelector('span.mono')!.textContent).toBe('9 Oct 2026')
  })

  it('AR-08 Book a demo opens the landing demo form', async () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1.up.railway.app/')
    await mount('/sage-rules', dataWith(H))
    const demo = [...band().querySelectorAll('a')].find((a) => a.textContent === 'Book a demo')!
    expect(demo.getAttribute('href')).toBe('https://landing-pr-1.up.railway.app/?demo')
    click([...band().querySelectorAll('button')].find((b) => b.textContent === 'Subscribe')!)
    expect(path()).toBe('/subscribe')
  })

  it('AR-08b Book a demo falls back to the production host', async () => {
    vi.stubEnv('VITE_LANDING_URL', '')
    await mount('/sage-rules', dataWith(H))
    expect(band().querySelector('a')!.getAttribute('href')).toBe('https://www.ascomply.com/?demo')
  })

  it('AR-09 the article crumbs', async () => {
    await mount('/sage-rules', dataWith(H))
    expect(crumbs()).toEqual(['Blog', 'Regulation', 'New sage rules for invoices'])
    click(all('#blog-header nav[aria-label="Breadcrumb"] button')[1])
    expect(path()).toBe('/category/c1')
  })

  it('AR-12 a post without a category has no back link', async () => {
    await mount('/sage-rules', dataWith(H, { category_id: 'gone' }))
    expect(article().querySelector('button.blog-back')).toBeNull()
  })
  it('AR-13 an unknown block and an unknown demo render nothing', async () => {
    const body = [
      { type: 'p', text: 'Only paragraph.' },
      { type: 'demo', demo: 'gone' },
      { type: 'chart', text: 'ghost' },
    ] as unknown as Block[]
    await mount('/sage-rules', dataWith(body))
    const blocks = article().querySelectorAll(':scope > div:nth-of-type(3) > div:first-child > div')
    expect(blocks).toHaveLength(3)
    expect([...blocks].map((b) => b.childElementCount)).toEqual([1, 0, 0])
    expect(article().textContent).not.toContain('ghost')
  })

  it('AR-14 a TOC click targets the heading offset by 92 under the scroller top', async () => {
    await mount('/sage-rules', dataWith(H))
    const spy = stubScrolls(true)
    const m = q('#blog-main')
    m.scrollTop = 1000
    stubTop(m, 64)
    stubTop(q('#sec-1'), 500)
    click(tocButtons()[1])
    expect(spy.mock.calls[0][0]).toEqual({ top: 1344, behavior: 'smooth' })
  })

  it('AR-15 the active line sits 120 below the scroller top, and the active row is inked and teal', async () => {
    await mount('/sage-rules', dataWith(H))
    stubScrolls(true)
    stubTop(q('#blog-main'), 64)
    stubTop(q('#sec-0'), -200)
    stubTop(q('#sec-1'), 150)
    act(() => q('#blog-main').dispatchEvent(new Event('scroll')))
    const [a, b] = tocButtons()
    expect(b.style.color).toBe('var(--ink)')
    expect(b.querySelector<HTMLElement>('span.mono')!.style.color).toBe('var(--teal)')
    expect(a.style.color).toBe('var(--text-copy)')
    expect(a.querySelector<HTMLElement>('span.mono')!.style.color).toBe('var(--step-label)')
    stubTop(q('#sec-1'), 185)
    act(() => q('#blog-main').dispatchEvent(new Event('scroll')))
    expect(tocButtons()[1].style.color).toBe('var(--text-copy)')
  })

  it('AR-16 opening another post resets the active section to the first', async () => {
    const two: Post = { ...TEST_DATA.posts[1], body: H }
    const data: BlogData = { ...dataWith(H), posts: [...dataWith(H).posts.filter((p) => p.id !== 'tin-checks'), two] }
    await mount('/sage-rules', data)
    stubScrolls(true)
    stubTop(q('#blog-main'), 0)
    stubTop(q('#sec-0'), -200)
    stubTop(q('#sec-1'), 80)
    act(() => q('#blog-main').dispatchEvent(new Event('scroll')))
    expect(tocButtons()[1].style.color).toBe('var(--ink)')
    act(() => {
      window.history.pushState(null, '', '/tin-checks')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })
    expect(article().querySelector('h1')!.textContent).toBe('TIN checks explained')
    expect(tocButtons()[0].style.color).toBe('var(--ink)')
    expect(tocButtons()[1].style.color).toBe('var(--text-copy)')
  })

  it('AR-17 the cover box shows the image, or the category tone and icon without one', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
    await mount('/sage-rules', dataWith(H, { cover: '/blog/covers/p/1' }))
    expect(article().querySelector('img[alt="Cover image"]')).not.toBeNull()
    act(() => root.unmount())
    container.remove()
    await mount('/sage-rules', dataWith(H))
    expect(article().querySelector('img')).toBeNull()
    const box = article().querySelector<HTMLElement>('div[style*="aspect-ratio"]')!
    expect(box.querySelector('svg')).not.toBeNull()
    expect(box.querySelector<HTMLElement>(':scope > div')!.style.background).toBe('var(--sage)')
  })
})
