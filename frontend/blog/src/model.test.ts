import { describe, expect, it, vi } from 'vitest'
import type { Block, BlogData, Post } from './data'
import { activeSection, bandsOf, catOf, crumbsFor, featuredOf, fmtDate, noteKindOf, placeholderOf, resolveView, resultsTitle, scrollTargetTop, searchPosts, TONES, tocOf } from './model'
import type { Route } from './route'
import { TEST_DATA } from './testData'

const post = (id: string, featured = false): Post => ({ ...TEST_DATA.posts[0], id, featured })
const ready = (data: BlogData) => ({ status: 'ready', data }) as const

describe('featuredOf', () => {
  it('featured_pinnedPostWinsWithFeaturedLabel', () => {
    const r = featuredOf([post('a'), post('b', true)])
    expect(r?.post.id).toBe('b')
    expect(r?.label).toBe('Featured')
  })

  it('featured_newestWhenNonePinned', () => {
    const r = featuredOf([post('a'), post('b')])
    expect(r?.post.id).toBe('a')
    expect(r?.label).toBe('Latest')
    expect(featuredOf([])).toBeNull()
  })
})

describe('bandsOf', () => {
  it('bands_skipEmptyCategoriesAndAlternate', () => {
    const cat = (id: string) => ({ ...TEST_DATA.categories[0], id })
    const n = (id: string, count: number) => Array.from({ length: count }, (_, i) => ({ ...post(`${id}-${i}`), category_id: id }))
    const data: BlogData = { categories: [cat('c1'), cat('c2'), cat('c3')], posts: [...n('c1', 2), ...n('c3', 5)] }
    const bands = bandsOf(data)
    expect(bands.map((b) => b.cat.id)).toEqual(['c1', 'c3'])
    expect([bands[0].bg, bands[0].cardBd]).toEqual(['var(--sage)', 'var(--sage-border, #cedac7)'])
    expect([bands[1].bg, bands[1].cardBd]).toEqual(['var(--background)', 'var(--border)'])
    expect(bands[1].posts).toHaveLength(3)
    expect(bands[1].allLabel).toBe('All 5 posts')
  })
})

describe('fmtDate', () => {
  it('fmtDate_isDayMonthYear', () => {
    expect(fmtDate('2026-10-14')).toBe('14 Oct 2026')
    expect(fmtDate('2026-01-05')).toBe('5 Jan 2026')
  })
})

describe('placeholderOf', () => {
  it('placeholder_usesTheToneModuloSix', () => {
    const cat = { ...TEST_DATA.categories[0], tone: 7, icon: 'globe' as const }
    expect(placeholderOf(cat)).toEqual({ ...TONES[1], icon: 'globe' })
    expect(placeholderOf(undefined)).toEqual({ bg: 'var(--muted)', fg: 'var(--primary)', tile: 'var(--card)', icon: 'file-text' })
  })
})

describe('noteKindOf', () => {
  it('noteKinds_unknownFallsBackToRegulation', () => {
    const d = noteKindOf('deadline')
    expect([d.label, d.icon, d.bg]).toEqual(['Deadline', 'calendar-clock', 'var(--peach-tint)'])
    expect(noteKindOf('x').label).toBe('Regulation note')
  })
})

describe('resolveView', () => {
  const at = (route: Route) => resolveView(route, '', ready(TEST_DATA)).kind

  it('resolve_unknownIdsAreMissing', () => {
    expect(at({ view: 'category', id: 'nope' })).toBe('missing')
    expect(at({ view: 'post', slug: 'no-such-post' })).toBe('missing')
    expect(at({ view: 'missing', path: '/a/b' })).toBe('missing')
    expect(at({ view: 'category', id: 'c1' })).toBe('category')
    expect(at({ view: 'post', slug: 'sage-rules' })).toBe('post')
  })

  it('resolve_loadingAndFailedWinOverDataViews', () => {
    expect(resolveView({ view: 'home' }, '', { status: 'loading' }).kind).toBe('loading')
    expect(resolveView({ view: 'home' }, 'x', { status: 'failed' }).kind).toBe('failed')
    expect(resolveView({ view: 'subscribe' }, '', { status: 'failed' }).kind).toBe('failed')
    expect(resolveView({ view: 'subscribe' }, '', { status: 'loading' }).kind).toBe('loading')
    expect(resolveView({ view: 'subscribe' }, '', ready(TEST_DATA)).kind).toBe('subscribe')
  })

  it('resolve_confirmIgnoresTheLoadState', () => {
    for (const state of [{ status: 'loading' }, { status: 'failed' }, ready(TEST_DATA)] as const) {
      expect(resolveView({ view: 'confirm' }, '', state).kind, state.status).toBe('confirm')
    }
  })
})

describe('crumbsFor', () => {
  it('crumbs_confirmIsBlogSubscribe', () => {
    const nav = vi.fn()
    const crumbs = crumbsFor(resolveView({ view: 'confirm' }, '', ready(TEST_DATA)), nav)
    expect(crumbs.map((c) => c.label)).toEqual(['Blog', 'Subscribe'])
    crumbs[1].go?.()
    expect(nav).toHaveBeenCalledWith({ view: 'subscribe' })
  })

  const labels = (route: Route, state = ready(TEST_DATA), nav = vi.fn()) =>
    crumbsFor(resolveView(route, '', state), nav).map((c) => c.label)

  it('crumbs_followTheView', () => {
    const nav = vi.fn()
    expect(labels({ view: 'home' })).toEqual(['Blog'])
    expect(labels({ view: 'subscribe' })).toEqual(['Blog', 'Subscribe'])
    expect(labels({ view: 'category', id: 'c1' })).toEqual(['Blog', 'Regulation'])
    expect(labels({ view: 'post', slug: 'sage-rules' })).toEqual(['Blog', 'Regulation', 'New sage rules for invoices'])
    expect(labels({ view: 'post', slug: 'nope' })).toEqual(['Blog'])
    expect(crumbsFor(resolveView({ view: 'home' }, 'sage', ready(TEST_DATA)), nav).map((c) => c.label)).toEqual(['Blog', 'Search'])
    expect(labels({ view: 'home' }, { status: 'loading' } as never)).toEqual(['Blog'])
    expect(labels({ view: 'home' }, { status: 'failed' } as never)).toEqual(['Blog'])
    const c = crumbsFor(resolveView({ view: 'post', slug: 'sage-rules' }, '', ready(TEST_DATA)), nav)
    c[1].go?.()
    expect(nav).toHaveBeenLastCalledWith({ view: 'category', id: 'c1' })
    c[0].go?.()
    expect(nav).toHaveBeenLastCalledWith({ view: 'home' })
    expect(c[2].go).toBeUndefined()
    for (const r of [{ view: 'home' }, { view: 'category', id: 'nope' }] as Route[]) {
      const only = crumbsFor(resolveView(r, '', ready(TEST_DATA)), nav)
      expect(only).toHaveLength(1)
      only[0].go?.()
      expect(nav).toHaveBeenLastCalledWith({ view: 'home' })
    }
  })

  it('crumbs_postWithAMissingCategorySkipsTheMiddle', () => {
    const data: BlogData = { ...TEST_DATA, posts: [{ ...TEST_DATA.posts[0], category_id: 'new-cat' }] }
    expect(catOf(data, 'new-cat')).toBeUndefined()
    expect(labels({ view: 'post', slug: 'sage-rules' }, ready(data))).toEqual(['Blog', 'New sage rules for invoices'])
  })
})

describe('searchPosts', () => {
  const mk = (id: string, over: Partial<Post> = {}, body: Block[] = []): Post => ({
    ...TEST_DATA.posts[0], id, category_id: 'plain', title: `Post ${id}`, summary: 'Nothing here.', featured: false, body, ...over,
  })
  const data: BlogData = {
    categories: [
      { ...TEST_DATA.categories[0], id: 'zorb', name: 'Zorbulon' },
      { ...TEST_DATA.categories[0], id: 'plain', name: 'Plain' },
    ],
    posts: [
      mk('by-title', { title: 'Headmark here' }),
      mk('by-summary', { summary: 'Summarymark here' }),
      mk('by-category', { category_id: 'zorb' }),
      mk('by-item', {}, [{ type: 'ul', items: ['one', 'Itemmark two'] }]),
      mk('by-note-title', {}, [{ type: 'note', kind: 'deadline', title: 'Notetitlemark', text: 'x' }]),
      mk('by-p', {}, [{ type: 'p', text: 'Pmark text' }]),
      mk('by-h', {}, [{ type: 'h', text: 'Hmark text' }]),
      mk('by-quote', {}, [{ type: 'quote', text: 'Quotemark text', who: 'Whomark' }]),
      mk('by-note-text', {}, [{ type: 'note', kind: 'product', title: 'x', text: 'Notetextmark' }]),
      mk('by-demo', {}, [{ type: 'demo', demo: 'clearance' }]),
    ],
  }
  const ids = (q: string) => searchPosts(data, q).map((p) => p.id)

  it('search_matchesEveryTextField', () => {
    expect(ids('HEADMARK')).toEqual(['by-title'])
    expect(ids('SUMMARYMARK')).toEqual(['by-summary'])
    expect(ids('ZORBULON')).toEqual(['by-category'])
    expect(ids('ITEMMARK')).toEqual(['by-item'])
    expect(ids('NOTETITLEMARK')).toEqual(['by-note-title'])
  })

  it('search_matchesBlockTextOfEveryKind', () => {
    expect(ids('pmark')).toEqual(['by-p'])
    expect(ids('hmark')).toEqual(['by-h'])
    expect(ids('quotemark')).toEqual(['by-quote'])
    expect(ids('notetextmark')).toEqual(['by-note-text'])
  })

  it('search_ignoresQuoteAuthorAndDemoId', () => {
    expect(ids('whomark')).toEqual([])
    expect(ids('clearance')).toEqual([])
  })

  it('search_trimsTheQuery', () => {
    expect(searchPosts(TEST_DATA, '  Sage  ').map((p) => p.id)).toEqual(['sage-rules'])
    expect(searchPosts(TEST_DATA, 'sage')).toEqual(searchPosts(TEST_DATA, '  Sage  '))
  })
})

describe('resultsTitle', () => {
  it('resultsTitle_singularPluralAndNone', () => {
    expect(resultsTitle(1, ' Sage ')).toBe('1 post matches “Sage”')
    expect(resultsTitle(3, 'tin')).toBe('3 posts match “tin”')
    expect(resultsTitle(0, 'zz')).toBe('Nothing found for “zz”')
  })
})

describe('table of contents', () => {
  it('toc_numbersHeadingsInOrder', () => {
    const blocks: Block[] = [
      { type: 'p', text: 'x' },
      { type: 'h', text: 'A' },
      { type: 'p', text: 'y' },
      { type: 'h', text: 'B' },
    ]
    expect(tocOf(blocks)).toEqual([
      { i: 0, num: '01', text: 'A' },
      { i: 1, num: '02', text: 'B' },
    ])
  })

  it('activeSection_lastTopAboveTheLine', () => {
    expect(activeSection([-400, 50, 900], 120)).toBe(1)
    expect(activeSection([300, 900], 120)).toBe(0)
    expect(activeSection([], 120)).toBe(0)
  })

  it('scrollTarget_offsetsByNinetyTwo', () => {
    expect(scrollTargetTop(1000, 500, 64)).toBe(1344)
    expect(scrollTargetTop(1000, 500, 0, 149)).toBe(1351)
  })
})
