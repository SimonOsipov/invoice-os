import { renderToStaticMarkup } from 'react-dom/server'
import type { ReactElement, ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { editorOf, newDraft, previewModel, type Draft, type DraftBlock } from '../blog'
import type { Block, Category, Post } from '../blogApi'
import type { BlogFilters } from '../types'
import { BlogEditor } from './BlogEditor'
import { BlogList, type BlogState } from './BlogList'
import { BlogPreview } from './BlogPreview'

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
  const blocks = { onAdd: () => {}, onMove: () => {}, onRemove: () => {}, onPatch: () => {} }
  const draft = (o: Partial<Draft> = {}): Draft => ({ ...newDraft([CAT], [], null, '2026-10-18'), ...o })
  const renderEditor = (d: Draft, coverUrl: string | null = null, dirty = false) =>
    renderToStaticMarkup(<BlogEditor editor={{ ...editorOf(d), dirty }} cats={[CAT]} authors={[]} coverUrl={coverUrl} onField={() => {}} onCover={() => {}} onBack={() => {}} {...blocks} />)

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
    const html = renderToStaticMarkup(<BlogEditor editor={e} cats={[CAT]} authors={[]} coverUrl={null} onField={() => {}} onCover={() => {}} onBack={() => {}} {...blocks} />)
    expect(html).toContain('border:1px solid var(--destructive)')
    expect(html).toContain('Add a title')
  })

  it('Back, Remove and the switch call their handlers', () => {
    const onBack = vi.fn()
    const onField = vi.fn()
    const t = BlogEditor({ editor: editorOf(draft({ cover: { kind: 'saved' } })), cats: [CAT], authors: [], coverUrl: null, onField, onCover: () => {}, onBack, ...blocks }) as ReactElement
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
    const t = BlogEditor({ editor: editorOf(draft()), cats: [CAT], authors: [], coverUrl: null, onField: () => {}, onCover, onBack: () => {}, ...blocks }) as ReactElement
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

// Like find, but renders function components (BlockCard holds no hooks).
const findDeep = (n: ReactNode, pred: (e: El) => boolean, out: El[] = []): El[] => {
  if (Array.isArray(n)) n.forEach((c) => findDeep(c, pred, out))
  else if (n && typeof n === 'object' && 'props' in n) {
    const e = n as El
    if (pred(e)) out.push(e)
    if (typeof e.type === 'function') findDeep((e.type as (p: unknown) => ReactNode)(e.props), pred, out)
    else findDeep(e.props.children, pred, out)
  }
  return out
}
const blocks = (...b: Block[]): DraftBlock[] => b.map((x, i) => ({ u: `k${i}`, ...x }))
const ONE_OF_EACH = blocks(
  { type: 'p', text: 'para' },
  { type: 'h', text: 'Head' },
  { type: 'ul', items: ['a', 'b'] },
  { type: 'note', kind: 'deadline', title: 'NT', text: 'NX' },
  { type: 'quote', text: 'QT', who: 'QW' },
  { type: 'demo', demo: 'approval' },
)

describe('BlogEditor blocks', () => {
  const props = (body: DraftBlock[], o: Partial<Parameters<typeof BlogEditor>[0]> = {}) => ({
    editor: editorOf({ ...newDraft([CAT], [], null, '2026-10-18'), body }),
    cats: [CAT],
    authors: [],
    coverUrl: null,
    onField: () => {},
    onCover: () => {},
    onBack: () => {},
    onAdd: () => {},
    onMove: () => {},
    onRemove: () => {},
    onPatch: () => {},
    ...o,
  })
  const html = (body: DraftBlock[]) => renderToStaticMarkup(<BlogEditor {...props(body)} />)

  it('each block type renders its inputs and hints', () => {
    const h = html(ONE_OF_EACH)
    for (const t of [
      'Content',
      '6 blocks',
      'placeholder="Write a paragraph…"',
      'placeholder="Section heading"',
      "Headings are listed in the article&#x27;s table of contents.",
      'placeholder="One item per line"',
      'One item per line. Each item gets a check mark.',
      '>Regulation note<',
      '>Deadline<',
      '>Availability<',
      'placeholder="Callout title"',
      'placeholder="Callout text"',
      'placeholder="Quote, without quotation marks"',
      'placeholder="Name, role, company"',
      'Plays the Feature Library demo inside the article.',
      'Add block',
      '>01<',
      '>06<',
      'PARAGRAPH',
      'HEADING',
      'LIST',
      'CALLOUT',
      'QUOTE',
      'DEMO',
    ])
      expect(h, t).toContain(t)
    for (const l of ['Paragraph', 'Heading', 'List', 'Callout', 'Quote', 'Demo']) expect(h).toMatch(new RegExp(`</svg>${l}</button>`))
    expect(h.match(/aria-label="Move up"/g)).toHaveLength(6)
    expect(h.match(/aria-label="Move down"/g)).toHaveLength(6)
    expect(h.match(/aria-label="Remove block"/g)).toHaveLength(6)
    expect(h).toMatch(/<option value="import">Import from CSV or Excel<\/option><option value="approval" selected="">One queue for approvals<\/option><option value="clearance">Submit for FIRS clearance<\/option>/)
    expect(h).toMatch(/<textarea[^>]*placeholder="One item per line"[^>]*>a\nb<\/textarea>/)
  })

  it('the ends dim and the callout kind highlights', () => {
    const h = html(ONE_OF_EACH)
    expect(h.match(/aria-label="Move up"[^>]*opacity:0\.35/g)).toHaveLength(1)
    expect(h.match(/aria-label="Move down"[^>]*opacity:0\.35/g)).toHaveLength(1)
    expect(h).toMatch(/background:var\(--primary\);color:var\(--primary-foreground\)[^>]*>Deadline</)
    expect(h).toMatch(/background:transparent;color:var\(--tab-active-text\)[^>]*>Regulation note</)
  })

  it('a block error line and the count read singular', () => {
    const e = { ...props([]).editor, errors: { body: 'Add at least one block with content.' } }
    const h = renderToStaticMarkup(<BlogEditor {...props([], { editor: e })} />)
    expect(h).toContain('0 blocks')
    expect(h).toContain('Add at least one block with content.')
    expect(html(blocks({ type: 'p', text: '' }))).toContain('1 block<')
  })

  it('the block buttons call their handlers', () => {
    const onAdd = vi.fn()
    const onMove = vi.fn()
    const onRemove = vi.fn()
    const onPatch = vi.fn()
    const t = BlogEditor(props(ONE_OF_EACH, { onAdd, onMove, onRemove, onPatch })) as ReactElement
    const btn = (label: string) => findDeep(t, (e) => e.type === 'button' && e.props['aria-label'] === label)
    ;(btn('Move down')[2].props.onClick as () => void)()
    expect(onMove).toHaveBeenLastCalledWith(2, 1)
    ;(btn('Move up')[3].props.onClick as () => void)()
    expect(onMove).toHaveBeenLastCalledWith(3, -1)
    ;(btn('Remove block')[4].props.onClick as () => void)()
    expect(onRemove).toHaveBeenCalledWith(4)
    const added = findDeep(t, (e) => e.type === 'button' && ['Paragraph', 'Heading', 'List', 'Callout', 'Quote', 'Demo'].includes(text(e.props.children)))
    added.forEach((b) => (b.props.onClick as () => void)())
    expect(onAdd.mock.calls.map((c) => c[0])).toEqual(['p', 'h', 'ul', 'note', 'quote', 'demo'])
    const [avail] = findDeep(t, (e) => e.type === 'button' && text(e.props.children) === 'Availability')
    ;(avail.props.onClick as () => void)()
    expect(onPatch).toHaveBeenLastCalledWith(3, { kind: 'product' })
  })

  it('typing patches that block; list lines split on newline', () => {
    const onPatch = vi.fn()
    const t = BlogEditor(props(ONE_OF_EACH, { onPatch })) as ReactElement
    const type = (placeholder: string, value: string) => {
      const [el] = findDeep(t, (e) => e.props.placeholder === placeholder)
      ;(el.props.onChange as (e: unknown) => void)({ target: { value } })
    }
    type('Write a paragraph…', 'p2')
    expect(onPatch).toHaveBeenLastCalledWith(0, { text: 'p2' })
    type('One item per line', 'x\n\ny')
    expect(onPatch).toHaveBeenLastCalledWith(2, { items: ['x', '', 'y'] })
    type('Callout title', 'T')
    expect(onPatch).toHaveBeenLastCalledWith(3, { title: 'T' })
    type('Name, role, company', 'W')
    expect(onPatch).toHaveBeenLastCalledWith(4, { who: 'W' })
    const [sel] = findDeep(t, (e) => e.type === 'select' && e.props.value === 'approval')
    ;(sel.props.onChange as (e: unknown) => void)({ target: { value: 'clearance' } })
    expect(onPatch).toHaveBeenLastCalledWith(5, { demo: 'clearance' })
  })
})

describe('BlogPreview', () => {
  const model = (body: DraftBlock[], o: Partial<Draft> = {}) =>
    previewModel({ ...newDraft([CAT], [], null, '2026-10-04'), body, ...o }, [CAT], [], [])
  const view = (m: ReturnType<typeof model>, tab: 'article' | 'card' = 'article', cover = 'none') =>
    renderToStaticMarkup(<BlogPreview model={m} tab={tab} cover={cover} onTab={() => {}} />)

  it('the article renders blocks, TOC and quote marks', () => {
    const body = blocks(
      { type: 'h', text: 'First' },
      { type: 'ul', items: ['one', 'two'] },
      { type: 'h', text: 'Second' },
      { type: 'quote', text: 'Said', who: 'Someone' },
      { type: 'demo', demo: 'import' },
      { type: 'note', kind: 'product', title: 'Now', text: 'Live' },
    )
    const h = view(model(body, { title: 'My post', summary: 'Sum' }))
    for (const t of ['Preview', '>Article<', '>Card<', 'blog.ascomply.com/my-post', 'On this page', '>01<', '>02<', 'First', 'Second', '“Said”', 'Someone', 'FEATURE LIBRARY · DEMO', 'Import from CSV or Excel', 'Availability', '4 Oct 2026', '>My post<', '>Sum<', '>Compliance<'])
      expect(h, t).toContain(t)
    expect(h).toContain('aspect-ratio:21 / 9')
    expect(h).not.toContain('Content blocks you add appear here.')
    expect(h.match(/<h2/g)).toHaveLength(2)
  })

  it('one heading draws no TOC', () => {
    expect(view(model(blocks({ type: 'h', text: 'Only' })))).not.toContain('On this page')
  })

  it('an empty preview says so', () => {
    const h = view(model(blocks({ type: 'p', text: '' })))
    for (const t of ['Content blocks you add appear here.', 'Untitled post', 'Your summary appears here.', 'blog.ascomply.com/untitled']) expect(h, t).toContain(t)
  })

  it('without a cover the category placeholder shows, with one the cover', () => {
    const none = view(model([]))
    expect(none).toContain('hero-grid')
    expect(none).toContain('background:var(--sage);color:var(--primary)')
    expect(none).not.toContain('aria-label="Cover image"')
    const some = view(model([]), 'article', 'url("data:image/jpeg;base64,/9j/")')
    expect(some).toContain('aria-label="Cover image"')
    expect(some).toContain('background-image:url(&quot;data:image/jpeg;base64,/9j/&quot;)')
    expect(some).not.toContain('hero-grid')
  })

  it('the card names the band', () => {
    const h = view(model([], { title: 'T', summary: 'S' }), 'card')
    for (const t of ['As it appears in the Compliance band and category page.', 'Read the post', 'aspect-ratio:16 / 9', '>T<', '>S<', '4 Oct 2026']) expect(h, t).toContain(t)
    expect(h).not.toContain('On this page')
    expect(h).not.toContain('Content blocks you add appear here.')
  })

  it('the tabs switch the preview and mark the active one', () => {
    const onTab = vi.fn()
    const t = BlogPreview({ model: model([]), tab: 'article', cover: 'none', onTab }) as ReactElement
    const [card] = find(t, (e) => e.type === 'button' && text(e.props.children) === 'Card')
    ;(card.props.onClick as () => void)()
    expect(onTab).toHaveBeenCalledWith('card')
    expect(view(model([]))).toMatch(/background:var\(--primary\);color:var\(--primary-foreground\)[^>]*>Article</)
  })
})
