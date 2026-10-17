import { renderToStaticMarkup } from 'react-dom/server'
import type { ReactElement, ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { editorOf, newDraft, type Draft } from '../blog'
import type { Category, Post } from '../blogApi'
import type { BlogFilters } from '../types'
import { BlogEditor } from './BlogEditor'
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

// BlogList holds no hooks: calling it yields the element tree, whose handlers run in node.
type El = ReactElement<{ children?: ReactNode; [k: string]: unknown }>
const find = (n: ReactNode, pred: (e: El) => boolean, out: El[] = []): El[] => {
  if (Array.isArray(n)) n.forEach((c) => find(c, pred, out))
  else if (n && typeof n === 'object' && 'props' in n) {
    const e = n as El
    if (pred(e)) out.push(e)
    find(e.props.children, pred, out)
  }
  return out
}
const tree = (state: BlogState, o: Partial<Parameters<typeof BlogList>[0]> = {}, filters = FILTERS) =>
  BlogList({ state, filters, onFilters: () => {}, onRetry: () => {}, onPin: () => {}, ...o }) as ReactNode
const text = (n: ReactNode): string =>
  Array.isArray(n) ? n.map(text).join('') : typeof n === 'string' || typeof n === 'number' ? String(n) : n && typeof n === 'object' && 'props' in n ? text((n as El).props.children) : ''

describe('BlogList handlers', () => {
  it('a row pin stops propagation and pins that post', () => {
    const onPin = vi.fn()
    const onOpen = vi.fn()
    const p = post({ id: 'p9' })
    const t = tree(ready([p]), { onPin, onOpen })
    const [btn] = find(t, (e) => e.type === 'button' && e.props.title === 'Pin as featured post')
    expect(btn).toBeDefined()
    const stop = vi.fn()
    ;(btn.props.onClick as (e: unknown) => void)({ stopPropagation: stop })
    expect(stop).toHaveBeenCalledTimes(1)
    expect(onPin).toHaveBeenCalledWith(p)
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('Clear filters resets the query, status and category', () => {
    const onFilters = vi.fn()
    const t = tree(ready([post()]), { onFilters }, { ...FILTERS, q: 'zzz', status: 'draft', cat: 'c1' })
    const [clear] = find(t, (e) => e.type === 'button' && text(e.props.children) === 'Clear filters')
    ;(clear.props.onClick as () => void)()
    expect(onFilters).toHaveBeenCalledWith({ tab: 'posts', q: '', status: 'all', cat: 'all' })
  })

  it('a chip sets the status, a tab sets the tab and Retry re-reads', () => {
    const onFilters = vi.fn()
    const onRetry = vi.fn()
    const t = tree(ready([post()]), { onFilters })
    const chips = find(t, (e) => e.type === 'button' && e.props.className === 'ops-chip')
    expect(chips.map((c) => text(c.props.children).replace(/\d+$/, ''))).toEqual(['ALL', 'PUBLISHED', 'DRAFTS'])
    ;(chips[2].props.onClick as () => void)()
    expect(onFilters).toHaveBeenLastCalledWith({ ...FILTERS, status: 'draft' })
    const [cats] = find(t, (e) => e.type === 'button' && text(e.props.children).includes('Categories'))
    ;(cats.props.onClick as () => void)()
    expect(onFilters).toHaveBeenLastCalledWith({ ...FILTERS, tab: 'cats' })
    const [note] = find(tree({ status: 'error', message: 'boom' }, { onRetry }), (e) => e.props.text === 'boom')
    const [retry] = find((note.type as (p: unknown) => ReactNode)(note.props), (e) => e.type === 'button' && text(e.props.children) === 'Retry')
    ;(retry.props.onClick as () => void)()
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('the tab strip uses the tab tokens', () => {
    const html = render(ready([post()]))
    expect(html).toContain('border-bottom:2px solid var(--tab-border)')
    expect(html).toMatch(/background:var\(--sage-panel\);color:var\(--tab-active-text\)[^"]*border-bottom:2px solid var\(--tab-active-border\)/)
  })
})

describe('BlogEditor', () => {
  const draft = (o: Partial<Draft> = {}): Draft => ({ ...newDraft([CAT], [], null, '2026-10-18'), ...o })
  const renderEditor = (d: Draft, coverUrl: string | null = null, dirty = false) =>
    renderToStaticMarkup(<BlogEditor editor={{ ...editorOf(d), dirty }} cats={[CAT]} authors={[]} coverUrl={coverUrl} onField={() => {}} onCover={() => {}} onBack={() => {}} />)

  it('the fields render the prototype copy', () => {
    const html = renderEditor(draft())
    for (const t of [
      'Post details',
      '>Title<',
      'placeholder="What structured e-invoicing changes for…"',
      '>Summary<',
      '0 / 220',
      '>Category<',
      '>Author<',
      '>Publish date<',
      'Upload a cover image',
      'JPG or PNG, landscape. Without one, the category placeholder is used.',
      'Pin as featured post',
      'The newest published post leads the blog home unless one is pinned.',
      'All posts',
      'NOT SAVED YET',
      'DRAFT',
    ])
      expect(html).toContain(t)
    expect(html).toMatch(/<input type="date"[^>]*value="2026-10-18"/)
    expect(html).toMatch(/role="switch" aria-checked="false"/)
    expect(html).toContain('minmax(0, 1fr) minmax(0, 1fr)')
  })

  it('the action bar names the save state in amber when dirty and an existing post is saved', () => {
    expect(renderEditor(draft({ id: 'p' }))).toContain('ALL CHANGES SAVED')
    const html = renderEditor(draft({ id: 'p' }), null, true)
    expect(html).toMatch(/color:var\(--status-amber-text\)[^>]*>UNSAVED CHANGES</)
  })

  it('the summary counter turns destructive above 220 and the featured switch follows the flag', () => {
    expect(renderEditor(draft({ summary: 'a'.repeat(221) }))).toMatch(/color:var\(--destructive\)[^>]*>221 \/ 220</)
    expect(renderEditor(draft({ summary: 'a'.repeat(220) }))).toMatch(/color:var\(--fg-3\)[^>]*>220 \/ 220</)
    const on = renderEditor(draft({ featured: true }))
    expect(on).toContain('aria-checked="true"')
    expect(on).toContain('Shows first on the blog home once published. Saving replaces the current pin.')
  })

  it('a new cover shows Replace and Remove and no upload prompt', () => {
    const html = renderEditor(draft({ cover: { kind: 'new', dataUrl: 'data:image/jpeg;base64,/9j/' } }))
    expect(html).toContain('role="img" aria-label="Cover image"')
    expect(html).toContain('background-image:url(&quot;data:image/jpeg;base64,/9j/&quot;)')
    expect(html).toMatch(/Replace<input type="file" accept="image\/\*"/)
    expect(html).toContain('>Remove<')
    expect(html).not.toContain('Upload a cover image')
  })

  it('a saved cover whose bytes failed to load keeps Replace and Remove', () => {
    const html = renderEditor(draft({ cover: { kind: 'saved' } }))
    expect(html).toContain('background-image:none')
    expect(html).toContain('Replace')
    expect(html).toContain('>Remove<')
    expect(html).not.toContain('Upload a cover image')
    expect(renderEditor(draft({ cover: { kind: 'saved' } }), 'blob:abc')).toContain('background-image:url(&quot;blob:abc&quot;)')
  })

  it('a field error colours the border and prints the message', () => {
    const e = { ...editorOf(draft()), errors: { title: 'Add a title' } }
    const html = renderToStaticMarkup(<BlogEditor editor={e} cats={[CAT]} authors={[]} coverUrl={null} onField={() => {}} onCover={() => {}} onBack={() => {}} />)
    expect(html).toContain('border:1px solid var(--destructive)')
    expect(html).toContain('Add a title')
  })

  it('Back, Remove and the switch call their handlers', () => {
    const onBack = vi.fn()
    const onField = vi.fn()
    const t = BlogEditor({ editor: editorOf(draft({ cover: { kind: 'saved' } })), cats: [CAT], authors: [], coverUrl: null, onField, onCover: () => {}, onBack }) as ReactElement
    const [back] = find(t, (e) => e.type === 'button' && text(e.props.children).includes('All posts'))
    ;(back.props.onClick as () => void)()
    expect(onBack).toHaveBeenCalledTimes(1)
    const [rm] = find(t, (e) => e.type === 'button' && text(e.props.children) === 'Remove')
    ;(rm.props.onClick as () => void)()
    expect(onField).toHaveBeenLastCalledWith({ cover: { kind: 'none' } })
    const [sw] = find(t, (e) => e.type === 'button' && e.props.role === 'switch')
    ;(sw.props.onClick as () => void)()
    expect(onField).toHaveBeenLastCalledWith({ featured: true })
  })

  it('picking a file hands it on and empties the input; cancelling hands on null', () => {
    const onCover = vi.fn()
    const t = BlogEditor({ editor: editorOf(draft()), cats: [CAT], authors: [], coverUrl: null, onField: () => {}, onCover, onBack: () => {} }) as ReactElement
    const [input] = find(t, (e) => e.type === 'input' && e.props.type === 'file')
    const f = new Blob(['x'], { type: 'image/png' }) as File
    const target = { files: [f], value: 'C:\\fakepath\\a.png' }
    ;(input.props.onChange as (e: unknown) => void)({ target })
    expect(onCover).toHaveBeenLastCalledWith(f)
    expect(target.value).toBe('')
    ;(input.props.onChange as (e: unknown) => void)({ target: { files: [], value: '' } })
    expect(onCover).toHaveBeenLastCalledWith(null)
  })
})
