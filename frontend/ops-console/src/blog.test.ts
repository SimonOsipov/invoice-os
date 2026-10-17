import { describe, expect, it } from 'vitest'

import { bDate, catFilterOptions, listRows, pinToast, statusChips, subtitle, tabs } from './blog'
import type { Category, Post } from './blogApi'
import type { BlogFilters } from './types'

const post = (o: Partial<Post> = {}): Post => ({
  id: 'p',
  category_id: null,
  publish_date: '2026-01-01',
  title: 't',
  summary: 's',
  author_id: null,
  status: 'published',
  featured: false,
  body: [],
  author_name: null,
  cover: null,
  created_at: '',
  updated_at: '',
  ...o,
})
const cat = (id: string, o: Partial<Category> = {}): Category => ({ id, name: id.toUpperCase(), headline: '', intro: '', icon: 'shield-check', tone: 0, post_count: 0, ...o })
const F: BlogFilters = { tab: 'posts', q: '', status: 'all', cat: 'all' }
const ids = (rows: { post: Post }[]) => rows.map((r) => r.post.id)

describe('blog list view', () => {
  it('subtitle counts published and drafts', () => {
    const three = [1, 2, 3].map((i) => post({ id: `p${i}` }))
    expect(subtitle([...three, post({ status: 'draft' })])).toBe('blog.ascomply.com · 3 published · 1 draft')
    expect(subtitle([post({ status: 'draft' }), post({ status: 'draft' })])).toBe('blog.ascomply.com · 0 published · 2 drafts')
  })

  it('chips and tabs carry live counts', () => {
    const posts = [post(), post(), post({ status: 'draft' })]
    const cats = ['a', 'b', 'c', 'd'].map((id) => cat(id))
    expect(statusChips(posts, 'draft').map((c) => [c.label, c.count, c.active])).toEqual([
      ['ALL', 3, false],
      ['PUBLISHED', 2, false],
      ['DRAFTS', 1, true],
    ])
    expect(tabs(posts, cats, 'posts').map((t) => [t.num, t.label, t.count, t.active])).toEqual([
      ['01', 'Posts', 3, true],
      ['02', 'Categories', 4, false],
    ])
  })

  it('the category select starts with All categories', () => {
    expect(catFilterOptions([cat('x')])).toEqual([
      { id: 'all', name: 'All categories' },
      { id: 'x', name: 'X' },
    ])
  })

  it('listRows filters by status, category and query', () => {
    const posts = [
      post({ id: 'a', category_id: 'x', status: 'draft', title: 'Alpha', summary: 'about e-invoicing' }),
      post({ id: 'b', category_id: 'y', title: 'E-Invoicing rules', summary: 'plain' }),
      post({ id: 'c', category_id: 'x', title: 'Gamma', summary: 'nothing' }),
      post({ id: 'd', category_id: 'y', status: 'draft', title: 'Delta', summary: 'nothing' }),
    ]
    const cats = [cat('x'), cat('y')]
    expect(ids(listRows(posts, cats, { ...F, status: 'draft' })).sort()).toEqual(['a', 'd'])
    expect(ids(listRows(posts, cats, { ...F, cat: 'x' })).sort()).toEqual(['a', 'c'])
    expect(ids(listRows(posts, cats, { ...F, q: '  E-INVOICING ' })).sort()).toEqual(['a', 'b'])
  })

  it('listRows puts the newest first and null dates last', () => {
    const posts = [
      post({ id: 'x', publish_date: '2026-01-02' }),
      post({ id: 'n', publish_date: null }),
      post({ id: 'b', publish_date: '2026-03-01' }),
      post({ id: 'a', publish_date: '2026-03-01' }),
    ]
    expect(ids(listRows(posts, [], F))).toEqual(['a', 'b', 'x', 'n'])
  })

  it('a bare row uses the prototype fallbacks', () => {
    const [r] = listRows([post({ title: '', summary: '', publish_date: null })], [], F)
    expect([r.title, r.summary, r.catName, r.catIcon, r.author, r.date]).toEqual(['Untitled post', 'No summary yet', 'No category', 'file-text', '—', '—'])
  })

  it('a category icon outside the glyph set falls back to file-text', () => {
    const [r] = listRows([post({ category_id: 'x' })], [cat('x', { icon: 'nope' })], F)
    expect(r.catIcon).toBe('file-text')
  })

  it('a row carries its category, badge and pin title', () => {
    const rows = listRows([post({ category_id: 'x', author_name: 'Amara', featured: true }), post({ id: 'q', status: 'draft', publish_date: null })], [cat('x')], F)
    expect([rows[0].catName, rows[0].catIcon, rows[0].author, rows[0].badge.label, rows[0].pinTitle]).toEqual(['X', 'shield-check', 'Amara', 'PUBLISHED', 'Unpin featured post'])
    expect([rows[1].badge.label, rows[1].pinTitle]).toEqual(['DRAFT', 'Pin as featured post'])
  })

  it('bDate formats a day', () => {
    expect(bDate('2026-10-04')).toBe('4 Oct 2026')
    expect(bDate(null)).toBe('—')
  })

  it('pinToast names both directions', () => {
    expect(pinToast(false)).toBe('Pinned as the featured post')
    expect(pinToast(true)).toBe('Unpinned. The newest post leads the blog home')
  })
})
