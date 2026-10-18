import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import {
  addBlock,
  authorOptions,
  bDate,
  blockCount,
  blockView,
  cleanBody,
  catFilterOptions,
  categoryOptions,
  coverCss,
  dataUrlToBytes,
  dekCount,
  draftFromPost,
  editorOf,
  fitWidth,
  isImageType,
  listRows,
  moveBlock,
  newDraft,
  patchBlock,
  pinHelp,
  pinToast,
  previewId,
  previewModel,
  removeBlock,
  saveState,
  setField,
  setPreview,
  TONES,
  type Draft,
  type DraftBlock,
  statusChips,
  subOf,
  subtitle,
  tabs,
  today,
} from './blog'
import type { Author, Block, Category, Post } from './blogApi'
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

const author = (user_id: string, o: Partial<Author> = {}): Author => ({ user_id, display_name: null, email: null, ...o })

describe('newDraft', () => {
  it('takes the first category, today and the signed-in author', () => {
    const d = newDraft([cat('a'), cat('b')], [author('S', { display_name: 'Sam' }), author('T')], 'S', '2026-10-18')
    expect(d).toMatchObject({ id: '', category_id: 'a', publish_date: '2026-10-18', author_id: 'S', status: 'draft', featured: false, cover: { kind: 'none' }, serverHasCover: false })
    expect(d.body).toEqual([{ u: expect.any(String), type: 'p', text: '' }])
  })

  it('leaves the author empty when the member is not in the list', () => {
    expect(newDraft([cat('a')], [author('T')], 'S', '2026-10-18').author_id).toBeNull()
    expect(newDraft([cat('a')], [author('T')], null, '2026-10-18').author_id).toBeNull()
    expect(newDraft([], [], 'S', '2026-10-18').category_id).toBeNull()
  })
})

describe('draftFromPost', () => {
  const p = post({ id: 'x', cover: { content_type: 'image/jpeg', size_bytes: 1, updated_at: '' }, body: [{ type: 'p', text: 'a' }, { type: 'ul', items: ['i'] }, { type: 'h', text: 'h' }], author_name: 'Ann' })

  it('keys every block and keeps the cover kind', () => {
    const d = draftFromPost(p)
    expect(new Set(d.body.map((b) => b.u)).size).toBe(3)
    expect(d.cover).toEqual({ kind: 'saved' })
    expect(d.serverHasCover).toBe(true)
    expect(d).toMatchObject({ id: 'x', author_name: 'Ann' })
  })

  it('copies the body, and a post without a cover has none', () => {
    const d = draftFromPost(p)
    d.body[1].items!.push('more')
    expect(p.body[1].items).toEqual(['i'])
    expect(draftFromPost(post()).cover).toEqual({ kind: 'none' })
    expect(draftFromPost(post()).serverHasCover).toBe(false)
  })
})

describe('setField', () => {
  it("marks dirty and clears that field's error only", () => {
    const s = { ...editorOf(newDraft([], [], null, 'd')), errors: { title: 'x', summary: 'y' }, confirm: 'leave' as const }
    const n = setField(s, { title: 'T' })
    expect(n).toMatchObject({ dirty: true, errors: { summary: 'y' }, confirm: null })
    expect(n.draft.title).toBe('T')
    expect(s.dirty).toBe(false)
  })

  it('maps a draft key to its error key', () => {
    const s = { ...editorOf(newDraft([], [], null, 'd')), errors: { category: 'c', date: 'd', author: 'a', body: 'b' } }
    expect(setField(s, { category_id: 'z' }).errors).toEqual({ date: 'd', author: 'a', body: 'b' })
    expect(setField(s, { publish_date: null, author_id: 'q' }).errors).toEqual({ category: 'c', body: 'b' })
  })
})

describe('setField patches', () => {
  const s = { ...editorOf(newDraft([], [], null, 'd')), errors: { title: 't', summary: 's', body: 'b' }, confirm: 'delete' as const }

  it('a patch with several keys clears each of them and keeps the rest', () => {
    expect(setField(s, { title: 'T', summary: 'S' }).errors).toEqual({ body: 'b' })
  })

  it('a field with no error leaves every error and still marks dirty and drops the confirm', () => {
    const n = setField(s, { featured: true, cover: { kind: 'none' } })
    expect(n).toMatchObject({ dirty: true, errors: { title: 't', summary: 's', body: 'b' }, confirm: null })
    expect(n.draft.featured).toBe(true)
  })
})

describe('saveState', () => {
  it('names the three states', () => {
    expect(saveState(true, 'x')).toEqual({ label: 'UNSAVED CHANGES', color: 'var(--status-amber-text)' })
    expect(saveState(false, 'p')).toEqual({ label: 'ALL CHANGES SAVED', color: 'var(--fg-3)' })
    expect(saveState(false, '')).toEqual({ label: 'NOT SAVED YET', color: 'var(--fg-3)' })
  })
})

describe('dekCount', () => {
  it('turns red above 220', () => {
    expect(dekCount('a'.repeat(220))).toEqual({ text: '220 / 220', over220: false })
    expect(dekCount('a'.repeat(221))).toEqual({ text: '221 / 220', over220: true })
    expect(dekCount('')).toEqual({ text: '0 / 220', over220: false })
  })
})

describe('authorOptions', () => {
  it('labels by name, then email, and keeps a stored outsider', () => {
    const o = authorOptions([author('1', { display_name: 'named', email: 'n@x' }), author('2', { email: 'e@x' })], { author_id: 'X', author_name: 'Old Name' })
    expect(o).toEqual([
      { value: '1', label: 'named' },
      { value: '2', label: 'e@x' },
      { value: 'X', label: 'Old Name' },
    ])
  })

  it('an outsider without a stored name shows its id; a listed author adds nothing', () => {
    expect(authorOptions([], { author_id: 'X', author_name: null })).toEqual([{ value: 'X', label: 'X' }])
    expect(authorOptions([author('1')], { author_id: '1', author_name: null })).toEqual([{ value: '1', label: '1' }])
  })

  it('a null author leads with —', () => {
    expect(authorOptions([author('1', { display_name: 'A' })], { author_id: null, author_name: null })[0]).toEqual({ value: '', label: '—' })
  })

  it('no authors leaves only —', () => {
    expect(authorOptions([], { author_id: null, author_name: null })).toEqual([{ value: '', label: '—' }])
  })
})

describe('categoryOptions', () => {
  it('leads with — only for a null category', () => {
    expect(categoryOptions([cat('a')], { category_id: null })).toEqual([{ value: '', label: '—' }, { value: 'a', label: 'A' }])
    expect(categoryOptions([cat('a')], { category_id: 'a' })).toEqual([{ value: 'a', label: 'A' }])
  })
})

describe('pinHelp', () => {
  it('follows the flag', () => {
    expect(pinHelp(true)).toBe('Shows first on the blog home once published. Saving replaces the current pin.')
    expect(pinHelp(false)).toBe('The newest published post leads the blog home unless one is pinned.')
  })
})

describe('cover helpers', () => {
  it('fitWidth caps the width at 1600 and keeps the ratio', () => {
    expect(fitWidth(2000, 1000)).toEqual({ width: 1600, height: 800 })
    expect(fitWidth(1200, 900)).toEqual({ width: 1200, height: 900 })
    expect(fitWidth(3201, 1000)).toEqual({ width: 1600, height: 500 })
  })

  it('isImageType accepts image types only', () => {
    for (const t of ['image/png', 'image/jpeg']) expect(isImageType(t)).toBe(true)
    for (const t of ['text/plain', '', 'application/pdf', 'ximage/png']) expect(isImageType(t)).toBe(false)
  })

  it('dataUrlToBytes decodes base64', () => {
    expect([...dataUrlToBytes('data:image/jpeg;base64,/9j/')]).toEqual([0xff, 0xd8, 0xff])
  })

  it('coverCss shows a new cover, a loaded saved cover, else none', () => {
    expect(coverCss({ kind: 'new', dataUrl: 'data:x' }, 'blob:s')).toBe('url("data:x")')
    expect(coverCss({ kind: 'saved' }, 'blob:s')).toBe('url("blob:s")')
    expect(coverCss({ kind: 'saved' }, null)).toBe('none')
    expect(coverCss({ kind: 'none' }, 'blob:s')).toBe('none')
  })
})

describe('today and subOf', () => {
  it('today is the local date, zero padded', () => {
    expect(today(new Date(2026, 0, 5, 23, 59))).toBe('2026-01-05')
  })

  it('subOf reads a string sub only', () => {
    expect(subOf({ sub: 'u1' })).toBe('u1')
    expect(subOf({ sub: 5 })).toBeNull()
    expect(subOf({ sub: '' })).toBeNull()
    expect(subOf(null)).toBeNull()
  })
})

describe('block editing', () => {
  const state = (types: string[] = []) => {
    const s = editorOf(newDraft([], [], null, '2026-10-04'))
    return { ...s, draft: { ...s.draft, body: types.map((t, i): DraftBlock => ({ u: `k${i}`, type: t, text: t })) } }
  }
  const texts = (s: ReturnType<typeof state>) => s.draft.body.map((b) => b.text)

  it('addBlock uses the prototype defaults', () => {
    const want = {
      p: { text: '' },
      h: { text: '' },
      ul: { items: [''] },
      note: { kind: 'regulation', title: '', text: '' },
      quote: { text: '', who: '' },
      demo: { demo: 'import' },
    } as const
    const keys = new Set<string>()
    for (const t of ['p', 'h', 'ul', 'note', 'quote', 'demo'] as const) {
      const n = addBlock(state(), t)
      const { u, ...rest } = n.draft.body[0]
      expect(rest).toEqual({ type: t, ...want[t] })
      expect(u).toBeTruthy()
      keys.add(u)
      expect(n.dirty).toBe(true)
    }
    expect(keys.size).toBe(6)
    const a = addBlock(addBlock(state(), 'ul'), 'ul')
    expect(a.draft.body[0].items).not.toBe(a.draft.body[1].items)
    expect(a.draft.body.map((b) => b.type)).toEqual(['ul', 'ul'])
  })

  it('moveBlock swaps neighbours and ignores the ends', () => {
    const s = state(['a', 'b', 'c'])
    expect(texts(moveBlock(s, 1, -1))).toEqual(['b', 'a', 'c'])
    expect(texts(moveBlock(s, 1, 1))).toEqual(['a', 'c', 'b'])
    expect(moveBlock(s, 1, -1).dirty).toBe(true)
    expect(moveBlock(s, 0, -1)).toBe(s)
    expect(moveBlock(s, 2, 1)).toBe(s)
    expect(moveBlock(s, 0, -1).dirty).toBe(false)
    expect(moveBlock(s, 5, 1)).toBe(s)
    expect(texts(s)).toEqual(['a', 'b', 'c'])
  })

  it('removeBlock drops one block', () => {
    const s = state(['a', 'b', 'c'])
    const n = removeBlock(s, 1)
    expect(texts(n)).toEqual(['a', 'c'])
    expect(n.dirty).toBe(true)
    expect(texts(removeBlock(s, 7))).toEqual(['a', 'b', 'c'])
  })

  it('patchBlock clears the body error only and keeps confirm', () => {
    const s = { ...state(['a', 'b']), errors: { body: 'x', title: 'y' }, confirm: 'leave' as const }
    const n = patchBlock(s, 1, { text: 'new' })
    expect(n.errors).toEqual({ title: 'y' })
    expect(texts(n)).toEqual(['a', 'new'])
    expect(n.draft.body[1].u).toBe('k1')
    expect(n.dirty).toBe(true)
    expect(n.confirm).toBe('leave')
    expect(s.errors).toEqual({ body: 'x', title: 'y' })
  })

  it('blockView numbers, labels and dims the ends', () => {
    const bs = [{ type: 'p' }, { type: 'note' }, { type: 'demo' }]
    expect(bs.map((b, i) => blockView(b, i, 3))).toEqual([
      { num: '01', typeLabel: 'PARAGRAPH', upOpacity: 0.35, downOpacity: 1 },
      { num: '02', typeLabel: 'CALLOUT', upOpacity: 1, downOpacity: 1 },
      { num: '03', typeLabel: 'DEMO', upOpacity: 1, downOpacity: 0.35 },
    ])
    expect(blockView({ type: 'p' }, 0, 1)).toMatchObject({ upOpacity: 0.35, downOpacity: 0.35 })
    expect(['h', 'ul', 'quote'].map((t) => blockView({ type: t }, 1, 3).typeLabel)).toEqual(['HEADING', 'LIST', 'QUOTE'])
  })

  it('blockCount pluralises', () => {
    expect([0, 1, 2].map(blockCount)).toEqual(['0 blocks', '1 block', '2 blocks'])
  })

  it('setPreview switches the tab only', () => {
    const s = state()
    expect(setPreview(s, 'card')).toEqual({ ...s, preview: 'card' })
  })
})

const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/blog/testdata/id_rules.json', import.meta.url)), 'utf8')) as {
  post_id: { title: string; taken: string[]; want: string }[]
  clean_body: { body: Block[]; want: Block[] }[]
}

describe('shared id rules', () => {
  it('the fixture is not empty', () => {
    expect(fixture.post_id.length).toBeGreaterThanOrEqual(10)
    expect(fixture.clean_body.length).toBeGreaterThanOrEqual(8)
  })

  it('previewId follows the server id rule', () => {
    for (const r of fixture.post_id) expect(previewId(r.title, r.taken), JSON.stringify(r.title)).toBe(r.want)
    expect(previewId('', [])).toBe('untitled')
    expect(previewId('   ', [])).toBe('untitled')
    expect(previewId('', ['untitled'])).toBe('untitled-2')
  })

  it('cleanBody matches the shared table', () => {
    for (const r of fixture.clean_body) expect(cleanBody(r.body), JSON.stringify(r.body)).toEqual(r.want)
  })

  it('cleanBody matches bClean on drafts and drops the local key', () => {
    const drafts: DraftBlock[] = [
      { u: 'a', type: 'p', text: '  ' },
      { u: 'b', type: 'ul', items: ['', ' x '] },
      { u: 'c', type: 'note', kind: 'deadline', title: 'T', text: '' },
      { u: 'd', type: 'demo', demo: 'approval' },
    ]
    const out = cleanBody(drafts)
    expect(out).toEqual([
      { type: 'ul', items: ['x'] },
      { type: 'note', kind: 'deadline', title: 'T', text: '' },
      { type: 'demo', demo: 'approval' },
    ])
    expect(JSON.stringify(out)).not.toContain('"u"')
  })
})

describe('previewModel', () => {
  const C = [cat('c1', { name: 'Compliance', tone: 8, icon: 'landmark' })]
  const A: Author[] = [
    { user_id: 'u1', display_name: 'Ada', email: 'a@x' },
    { user_id: 'u2', display_name: null, email: 'n@x' },
  ]
  const draft = (o: Partial<Draft> = {}): Draft => ({ ...newDraft(C, A, null, '2026-10-04'), ...o })
  const model = (o: Partial<Draft> = {}, taken: string[] = []) => previewModel(draft(o), C, A, taken)
  const blk = (...b: Block[]) => b.map((x, i) => ({ u: `k${i}`, ...x }))

  it('a saved post shows its own id, an unsaved one the id the server will give', () => {
    expect(model({ id: 'kept-id', title: 'Changed' }).url).toBe('blog.ascomply.com/kept-id')
    expect(model({ title: 'New thing' }, ['new-thing']).url).toBe('blog.ascomply.com/new-thing-2')
    expect(model({ title: '' }).url).toBe('blog.ascomply.com/untitled')
  })

  it('the TOC needs two headings', () => {
    const h = (text: string): Block => ({ type: 'h', text })
    const one = model({ body: blk(h('A'), { type: 'p', text: 'x' }) })
    expect(one.hasToc).toBe(false)
    const two = model({ body: blk(h('A'), { type: 'p', text: 'x' }, h(' '), h('B')) })
    expect(two.hasToc).toBe(true)
    expect(two.toc).toEqual([
      { num: '01', text: 'A' },
      { num: '02', text: 'B' },
    ])
  })

  it('an empty body says so', () => {
    const m = model({ body: blk({ type: 'p', text: ' ' }, { type: 'ul', items: [''] }) })
    expect(m.emptyBody).toBe(true)
    expect(m.blocks).toEqual([])
    expect(model({ body: blk({ type: 'demo', demo: 'import' }) }).emptyBody).toBe(false)
  })

  it('meta names the public byline', () => {
    expect(model({ author_id: 'u1', publish_date: '2026-10-04' }).meta).toBe('4 Oct 2026 · Ada')
    expect(model({ author_id: 'u2' }).meta).toBe('4 Oct 2026 · ASComply team')
    expect(model({ author_id: null }).meta).toBe('4 Oct 2026')
    expect(model({ author_id: 'gone', author_name: 'Old Name' }).meta).toBe('4 Oct 2026 · Old Name')
    expect(model({ author_id: null, publish_date: null }).meta).toBe('—')
  })

  it('the placeholder takes the category tone', () => {
    const m = model()
    expect(m.tone).toBe(TONES[2])
    expect(m.icon).toBe('landmark')
    expect(m.catName).toBe('Compliance')
    const none = model({ category_id: null })
    expect(none.tone).toBe(TONES[5])
    expect(none.icon).toBe('file-text')
    expect(none.catName).toBe('No category')
  })

  it('the tone index wraps at six and an unknown icon falls back', () => {
    const tone = (t: number, icon = 'landmark') => previewModel(draft(), [cat('c1', { tone: t, icon })], A, []).tone
    expect([0, 5, 6, 11, 14].map((t) => TONES.indexOf(tone(t)))).toEqual([0, 5, 0, 5, 2])
    expect(previewModel(draft(), [cat('c1', { icon: 'no-such-glyph' })], A, []).icon).toBe('file-text')
  })

  it('an author gone from the list with no stored name adds no byline', () => {
    expect(model({ author_id: 'gone', author_name: null }).meta).toBe('4 Oct 2026')
  })

  it('taken ids may be any iterable and a saved id ignores them', () => {
    expect(previewModel(draft({ title: 'Dup' }), C, A, new Set(['dup'])).url).toBe('blog.ascomply.com/dup-2')
    expect(previewModel(draft({ id: 'dup', title: 'Dup' }), C, A, new Set(['dup'])).url).toBe('blog.ascomply.com/dup')
  })

  it('building the preview leaves the draft body untouched', () => {
    const body = blk({ type: 'p', text: '  x  ' }, { type: 'ul', items: [' a ', ''] })
    const before = JSON.stringify(body)
    model({ body })
    expect(JSON.stringify(body)).toBe(before)
  })

  it('empty text falls back to the placeholders and mutes the title', () => {
    const m = model({ title: '  ', summary: ' ' })
    expect([m.title, m.titleMuted, m.dek]).toEqual(['Untitled post', true, 'Your summary appears here.'])
    const f = model({ title: ' Hi ', summary: ' S ' })
    expect([f.title, f.titleMuted, f.dek]).toEqual(['Hi', false, 'S'])
  })

  it('blocks carry the callout style, the demo title and the quote', () => {
    const m = model({
      body: blk({ type: 'note', kind: 'deadline', title: 'T', text: 'x' }, { type: 'note', kind: 'odd', title: 'R', text: '' }, { type: 'demo', demo: 'clearance' }, { type: 'quote', text: 'q', who: 'w' }),
    })
    expect(m.blocks[0]).toMatchObject({ type: 'note', label: 'Deadline', icon: 'calendar-clock', labelColor: 'var(--status-amber-text)' })
    expect(m.blocks[1]).toMatchObject({ label: 'Regulation note', icon: 'landmark' })
    expect(m.blocks[2]).toEqual({ type: 'demo', title: 'Submit for FIRS clearance' })
    expect(m.blocks[3]).toEqual({ type: 'quote', text: 'q', who: 'w' })
  })
})
