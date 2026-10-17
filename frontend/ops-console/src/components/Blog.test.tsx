import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import type { Category, Post } from '../blogApi'
import type { BlogFilters } from '../types'
import { BlogList, type BlogState } from './BlogList'

const FILTERS: BlogFilters = { tab: 'posts', q: '', status: 'all', cat: 'all' }
const post = (o: Partial<Post> = {}): Post => ({
  id: 'p1',
  category_id: 'c1',
  publish_date: '2026-10-04',
  title: 'Title',
  summary: 'Summary',
  author_id: null,
  status: 'published',
  featured: false,
  body: [],
  author_name: 'Amara',
  cover: null,
  created_at: '',
  updated_at: '',
  ...o,
})
const CAT: Category = { id: 'c1', name: 'Compliance', headline: '', intro: '', icon: 'shield-check', tone: 0, post_count: 1 }
const ready = (posts: Post[]): BlogState => ({ status: 'ready', posts, cats: [CAT], authors: [] })
const render = (state: BlogState, filters = FILTERS) =>
  renderToStaticMarkup(<BlogList state={state} filters={filters} onFilters={() => {}} onRetry={() => {}} onPin={() => {}} />)

describe('BlogList', () => {
  it('the Blog screen has its eyebrow and one h1', () => {
    const html = render({ status: 'loading' })
    expect(html.match(/class="eyebrow"[^>]*>07 — CONTENT</g)).toHaveLength(1)
    expect(html.match(/<h1[^>]*>Blog<\/h1>/g)).toHaveLength(1)
    expect(html.match(/<h1/g)).toHaveLength(1)
  })

  it('the header row keeps the prototype margins inside the screen pad', () => {
    const html = render(ready([post()]))
    expect(html.startsWith('<div class="ops-screen-pad">')).toBe(true)
    expect(html).toMatch(/<div style="[^"]*margin-bottom:20px[^"]*">/)
    expect(html).toMatch(/class="eyebrow" style="margin-bottom:10px"/)
    expect(html).toMatch(/<h1 style="[^"]*font-size:28px[^"]*letter-spacing:-0\.04em[^"]*">Blog<\/h1>/)
    expect(html).toMatch(/class="mono"[^>]*>blog\.ascomply\.com · 1 published · 0 drafts</)
  })

  it('the list renders the prototype copy and no Reset', () => {
    const html = render(ready([post(), post({ id: 'p2', status: 'draft' })]))
    for (const t of ['View blog', 'New post', 'Search titles and summaries…', 'All categories', 'Pinned post shows first on the blog home · drafts are not public'])
      expect(html).toContain(t)
    for (const l of ['Post', 'Category', 'Author', 'Date', 'Status', 'Pin']) expect(html).toContain(`>${l}</span>`)
    expect(html).not.toContain('Reset sample content')
  })

  it('View blog opens in a new tab without an opener', () => {
    const a = render(ready([post()])).match(/<a [^>]*>/)![0]
    expect(a).toContain('href="https://blog.ascomply.com"')
    expect(a).toContain('target="_blank"')
    expect(a).toMatch(/rel="[^"]*noopener/)
  })

  it('the empty state offers Clear filters', () => {
    const html = render(ready([post()]), { ...FILTERS, q: 'zzz' })
    expect(html).toContain('No posts match these filters.')
    expect(html).toContain('>Clear filters</button>')
  })

  it('a blog with no posts shows the empty state and zero counts', () => {
    const html = render(ready([]))
    expect(html).toContain('blog.ascomply.com · 0 published · 0 drafts')
    expect(html).toMatch(/ALL<span[^>]*>0</)
    expect(html).toContain('No posts match these filters.')
  })

  it('a long title and summary stay inside the Post cell', () => {
    const html = render(ready([post({ title: 'T'.repeat(300), summary: 'S'.repeat(300) })]))
    for (const text of ['T'.repeat(300), 'S'.repeat(300)]) {
      const span = html.match(new RegExp(`<span style="([^"]*)">${text}</span>`))![1]
      expect(span).toContain('white-space:nowrap')
      expect(span).toContain('overflow:hidden')
      expect(span).toContain('text-overflow:ellipsis')
    }
  })

  it('the forbidden list shows the staff-only note', () => {
    const html = render({ status: 'forbidden' })
    expect(html).toContain('Your account is not ASComply staff.')
    expect(html).not.toContain('data-blog-table')
  })

  it('the error list shows the message and Retry', () => {
    const html = render({ status: 'error', message: 'boom' })
    expect(html).toContain('boom')
    expect(html).toContain('>Retry</button>')
    expect(html).not.toContain('data-blog-table')
  })

  it('the loading list shows its note and no table', () => {
    const html = render({ status: 'loading' })
    expect(html).toContain('Loading posts…')
    expect(html).not.toContain('data-blog-table')
  })

  it('a featured row draws the pinned button and the Unpin title', () => {
    const html = render(ready([post({ featured: true })]))
    expect(html).toContain('aria-label="Unpin featured post"')
    expect(html).toMatch(/background:var\(--primary\);color:var\(--primary-foreground\)/)
  })

  it('the Categories tab renders no post table', () => {
    const html = render(ready([post()]), { ...FILTERS, tab: 'cats' })
    expect(html).toContain('Categories')
    expect(html).not.toContain('data-blog-table')
  })
})
