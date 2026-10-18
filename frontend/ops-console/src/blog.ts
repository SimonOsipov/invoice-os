import type { Author, Block, Category, Post, PostInput } from './blogApi'
import { GLYPHS, type GlyphName } from './glyphs'
import type { BlogFilters } from './types'

export const BLOG_HOME = 'https://blog.ascomply.com'

const count = (posts: Post[], status: Post['status']) => posts.filter((p) => p.status === status).length

export function subtitle(posts: Post[]): string {
  const pub = count(posts, 'published')
  const drafts = posts.length - pub
  return `blog.ascomply.com · ${pub} published · ${drafts} ${drafts === 1 ? 'draft' : 'drafts'}`
}

export type Chip = { key: BlogFilters['status']; label: string; count: number; active: boolean }

export function statusChips(posts: Post[], status: BlogFilters['status']): Chip[] {
  const pub = count(posts, 'published')
  const rows: [BlogFilters['status'], string, number][] = [
    ['all', 'ALL', posts.length],
    ['published', 'PUBLISHED', pub],
    ['draft', 'DRAFTS', posts.length - pub],
  ]
  return rows.map(([key, label, n]) => ({ key, label, count: n, active: key === status }))
}

export type Tab = { key: BlogFilters['tab']; num: string; label: string; count: number; active: boolean }

export function tabs(posts: Post[], cats: Category[], tab: BlogFilters['tab']): Tab[] {
  const rows: [BlogFilters['tab'], string, number][] = [
    ['posts', 'Posts', posts.length],
    ['cats', 'Categories', cats.length],
  ]
  return rows.map(([key, label, n], i) => ({ key, num: String(i + 1).padStart(2, '0'), label, count: n, active: key === tab }))
}

export const catFilterOptions = (cats: Category[]): { id: string; name: string }[] => [
  { id: 'all', name: 'All categories' },
  ...cats.map((c) => ({ id: c.id, name: c.name })),
]

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

export function bDate(d: string | null): string {
  if (!d) return '—'
  const [y, m, day] = d.slice(0, 10).split('-')
  return `${+day} ${MONTHS[+m - 1]} ${y}`
}

export const pinToast = (wasFeatured: boolean): string =>
  wasFeatured ? 'Unpinned. The newest post leads the blog home' : 'Pinned as the featured post'

export type Badge = { bg: string; border: string; text: string; label: string }

const PUBLISHED: Badge = { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'PUBLISHED' }
const DRAFT: Badge = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'DRAFT' }

export const badgeOf = (status: Post['status']): Badge => (status === 'published' ? PUBLISHED : DRAFT)

export type Row = {
  post: Post
  title: string
  summary: string
  catName: string
  catIcon: GlyphName
  author: string
  date: string
  badge: Badge
  pinTitle: string
}

const iconOf = (name: string | undefined): GlyphName => (name && name in GLYPHS ? (name as GlyphName) : 'file-text')

export function listRows(posts: Post[], cats: Category[], f: BlogFilters): Row[] {
  const q = f.q.trim().toLowerCase()
  return posts
    .filter(
      (p) =>
        (f.status === 'all' || p.status === f.status) &&
        (f.cat === 'all' || p.category_id === f.cat) &&
        (!q || `${p.title} ${p.summary}`.toLowerCase().includes(q)),
    )
    .sort((a, b) => {
      if (a.publish_date !== b.publish_date) {
        if (a.publish_date === null) return 1
        if (b.publish_date === null) return -1
        return a.publish_date < b.publish_date ? 1 : -1
      }
      return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
    })
    .map((p) => {
      const cat = cats.find((c) => c.id === p.category_id)
      return {
        post: p,
        title: p.title || 'Untitled post',
        summary: p.summary || 'No summary yet',
        catName: cat ? cat.name : 'No category',
        catIcon: iconOf(cat?.icon),
        author: p.author_name ?? '—',
        date: bDate(p.publish_date),
        badge: badgeOf(p.status),
        pinTitle: p.featured ? 'Unpin featured post' : 'Pin as featured post',
      }
    })
}

export type DraftBlock = Block & { u: string }
export type CoverState = { kind: 'none' } | { kind: 'saved' } | { kind: 'new'; dataUrl: string }

export interface Draft extends Omit<PostInput, 'body'> {
  id: string
  author_name: string | null
  body: DraftBlock[]
  cover: CoverState
  serverHasCover: boolean
}

export type EditorState = {
  draft: Draft
  dirty: boolean
  errors: Record<string, string>
  confirm: null | 'delete' | 'leave'
  preview: 'article' | 'card'
}

let keySeq = 0
const nextKey = (): string => `b${++keySeq}`

export const editorOf = (draft: Draft): EditorState => ({ draft, dirty: false, errors: {}, confirm: null, preview: 'article' })

export function today(now = new Date()): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`
}

export const subOf = (claims: Record<string, unknown> | null): string | null => (typeof claims?.sub === 'string' && claims.sub ? claims.sub : null)

export function newDraft(cats: Category[], authors: Author[], sub: string | null, day: string): Draft {
  const member = sub ? authors.find((a) => a.user_id === sub) : undefined
  return {
    id: '',
    category_id: cats[0]?.id ?? null,
    publish_date: day,
    title: '',
    summary: '',
    author_id: member ? member.user_id : null,
    author_name: null,
    status: 'draft',
    featured: false,
    body: [{ u: nextKey(), type: 'p', text: '' }],
    cover: { kind: 'none' },
    serverHasCover: false,
  }
}

export function draftFromPost(post: Post): Draft {
  return {
    id: post.id,
    category_id: post.category_id,
    publish_date: post.publish_date,
    title: post.title,
    summary: post.summary,
    author_id: post.author_id,
    author_name: post.author_name,
    status: post.status,
    featured: post.featured,
    body: structuredClone(post.body).map((b) => ({ ...b, u: nextKey() })),
    cover: post.cover ? { kind: 'saved' } : { kind: 'none' },
    serverHasCover: post.cover !== null,
  }
}

// Draft keys whose error key differs; the rest share their name.
const ERROR_KEY: Record<string, string> = { category_id: 'category', publish_date: 'date', author_id: 'author' }

export function setField(state: EditorState, patch: Partial<Draft>): EditorState {
  const errors = { ...state.errors }
  for (const k of Object.keys(patch)) delete errors[ERROR_KEY[k] ?? k]
  return { ...state, draft: { ...state.draft, ...patch }, dirty: true, errors, confirm: null }
}

export const DEK_MAX = 220

export function dekCount(summary: string): { text: string; over220: boolean } {
  return { text: `${summary.length} / ${DEK_MAX}`, over220: summary.length > DEK_MAX }
}

export type Option = { value: string; label: string }

const NONE: Option = { value: '', label: '—' }

export function authorOptions(authors: Author[], draft: Pick<Draft, 'author_id' | 'author_name'>): Option[] {
  const out = authors.map((a) => ({ value: a.user_id, label: a.display_name ?? a.email ?? a.user_id }))
  if (draft.author_id === null) return [NONE, ...out]
  if (!authors.some((a) => a.user_id === draft.author_id)) out.push({ value: draft.author_id, label: draft.author_name ?? draft.author_id })
  return out
}

export function categoryOptions(cats: Category[], draft: Pick<Draft, 'category_id'>): Option[] {
  const out = cats.map((c) => ({ value: c.id, label: c.name }))
  return draft.category_id === null ? [NONE, ...out] : out
}

export const pinHelp = (featured: boolean): string =>
  featured
    ? 'Shows first on the blog home once published. Saving replaces the current pin.'
    : 'The newest published post leads the blog home unless one is pinned.'

export const saveState = (dirty: boolean, id: string): { label: string; color: string } =>
  dirty
    ? { label: 'UNSAVED CHANGES', color: 'var(--status-amber-text)' }
    : { label: id ? 'ALL CHANGES SAVED' : 'NOT SAVED YET', color: 'var(--fg-3)' }

export const COVER_MAX_WIDTH = 1600
export const COVER_QUALITY = 0.82

export function fitWidth(w: number, h: number): { width: number; height: number } {
  const width = Math.min(COVER_MAX_WIDTH, w)
  return { width, height: Math.round((h * width) / w) }
}

export const isImageType = (type: string): boolean => /^image\//.test(type)

export function dataUrlToBytes(dataUrl: string): Uint8Array {
  const bin = atob(dataUrl.slice(dataUrl.indexOf(',') + 1))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0))
}

export const coverCss = (cover: CoverState, savedUrl: string | null): string => {
  const src = cover.kind === 'new' ? cover.dataUrl : cover.kind === 'saved' ? savedUrl : null
  return src ? `url("${src}")` : 'none'
}

export type BlockType = 'p' | 'h' | 'ul' | 'note' | 'quote' | 'demo'

const BLOCK_DEFAULTS: Record<BlockType, () => Omit<Block, 'type'>> = {
  p: () => ({ text: '' }),
  h: () => ({ text: '' }),
  ul: () => ({ items: [''] }),
  note: () => ({ kind: 'regulation', title: '', text: '' }),
  quote: () => ({ text: '', who: '' }),
  demo: () => ({ demo: 'import' }),
}

export const ADD_TYPES: { type: BlockType; label: string }[] = [
  { type: 'p', label: 'Paragraph' },
  { type: 'h', label: 'Heading' },
  { type: 'ul', label: 'List' },
  { type: 'note', label: 'Callout' },
  { type: 'quote', label: 'Quote' },
  { type: 'demo', label: 'Demo' },
]

export const NOTE_KINDS: { kind: string; label: string }[] = [
  { kind: 'regulation', label: 'Regulation note' },
  { kind: 'deadline', label: 'Deadline' },
  { kind: 'product', label: 'Availability' },
]

export const DEMOS: { id: string; title: string }[] = [
  { id: 'import', title: 'Import from CSV or Excel' },
  { id: 'approval', title: 'One queue for approvals' },
  { id: 'clearance', title: 'Submit for FIRS clearance' },
]

const TYPE_LABEL: Record<string, string> = { p: 'PARAGRAPH', h: 'HEADING', ul: 'LIST', note: 'CALLOUT', quote: 'QUOTE', demo: 'DEMO' }

export const blockCount = (n: number): string => `${n} ${n === 1 ? 'block' : 'blocks'}`

export const addBlock = (state: EditorState, type: BlockType): EditorState =>
  setField(state, { body: [...state.draft.body, { u: nextKey(), type, ...BLOCK_DEFAULTS[type]() }] })

export function moveBlock(state: EditorState, i: number, d: number): EditorState {
  const j = i + d
  const body = state.draft.body
  if (i < 0 || i >= body.length || j < 0 || j >= body.length) return state
  const next = body.slice()
  ;[next[i], next[j]] = [next[j], next[i]]
  return setField(state, { body: next })
}

export const removeBlock = (state: EditorState, i: number): EditorState => setField(state, { body: state.draft.body.filter((_, k) => k !== i) })

// Unlike setField, a block edit keeps `confirm`.
export function patchBlock(state: EditorState, i: number, patch: Partial<Block>): EditorState {
  const errors = { ...state.errors }
  delete errors.body
  const body = state.draft.body.map((b, k) => (k === i ? { ...b, ...patch } : b))
  return { ...state, draft: { ...state.draft, body }, dirty: true, errors }
}

export const setPreview = (state: EditorState, preview: EditorState['preview']): EditorState => ({ ...state, preview })

export type BlockView = { num: string; typeLabel: string; upOpacity: number; downOpacity: number }

export const blockView = (b: Block, i: number, n: number): BlockView => ({
  num: String(i + 1).padStart(2, '0'),
  typeLabel: TYPE_LABEL[b.type] ?? b.type.toUpperCase(),
  upOpacity: i === 0 ? 0.35 : 1,
  downOpacity: i === n - 1 ? 0.35 : 1,
})

// Equals Go blog.CleanBody: keep each type's own fields, trim, drop blank blocks.
export function cleanBody(body: Block[]): Block[] {
  const out: Block[] = []
  for (const b of body) {
    const c: Block = { type: b.type }
    if (b.type === 'ul') {
      const items = (b.items ?? []).map((x) => x.trim()).filter(Boolean)
      if (items.length) c.items = items
    } else if (b.type === 'note') {
      c.kind = b.kind ?? ''
      c.title = (b.title ?? '').trim()
      c.text = (b.text ?? '').trim()
    } else if (b.type === 'quote') {
      c.text = (b.text ?? '').trim()
      c.who = (b.who ?? '').trim()
    } else if (b.type === 'demo') {
      c.demo = b.demo ?? ''
    } else {
      c.text = (b.text ?? '').trim()
    }
    if (c.type === 'demo' || c.text || c.items?.length || c.title) out.push(c)
  }
  return out
}

const RESERVED_IDS = ['subscribe', 'category']
export const MAX_SLUG = 64

// Equals Go blog.Slug; U+0130 lowers to one letter in Go and to two code points in JS.
export const slugOf = (s: string): string =>
  s
    .replace(/İ/g, 'i')
    .toLowerCase()
    .replace(/&/g, ' and ')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, MAX_SLUG) || 'post'

// The id the server will give an unsaved post (Go blog.PostID).
export function previewId(title: string, taken: Iterable<string>): string {
  const used = new Set(taken)
  const base = slugOf(title.trim() ? title : 'untitled')
  let id = base
  for (let k = 2; RESERVED_IDS.includes(id) || used.has(id); k++) id = `${base}-${k}`
  return id
}

export type Tone = { bg: string; fg: string; tile: string }

export const TONES: Tone[] = [
  { bg: 'var(--sage)', fg: 'var(--primary)', tile: 'var(--card)' },
  { bg: 'var(--surface)', fg: 'var(--accent)', tile: 'var(--surface-panel)' },
  { bg: 'var(--peach-band)', fg: 'var(--primary)', tile: 'var(--card)' },
  { bg: 'var(--mint-soft)', fg: 'var(--teal)', tile: 'var(--card)' },
  { bg: 'var(--surface-2, var(--surface))', fg: 'var(--surface-foreground)', tile: 'var(--surface-panel)' },
  { bg: 'var(--muted)', fg: 'var(--primary)', tile: 'var(--card)' },
]

type NoteStyle = { label: string; icon: GlyphName; bg: string; border: string; tile: string; labelColor: string }

const NOTES: Record<string, NoteStyle> = {
  regulation: { label: 'Regulation note', icon: 'landmark', bg: 'var(--sage-card)', border: 'var(--border)', tile: 'var(--sage-panel)', labelColor: 'var(--step-label)' },
  deadline: { label: 'Deadline', icon: 'calendar-clock', bg: 'var(--peach-tint)', border: 'var(--status-amber-border)', tile: 'var(--card)', labelColor: 'var(--status-amber-text)' },
  product: { label: 'Availability', icon: 'circle-check', bg: 'var(--mint-soft)', border: 'var(--border)', tile: 'var(--card)', labelColor: 'var(--teal)' },
}

export type PreviewBlock =
  | { type: 'p' | 'h'; text: string }
  | { type: 'ul'; items: string[] }
  | ({ type: 'note'; title: string; text: string } & NoteStyle)
  | { type: 'quote'; text: string; who: string }
  | { type: 'demo'; title: string }

export type Preview = {
  url: string
  catName: string
  meta: string
  date: string
  title: string
  titleMuted: boolean
  dek: string
  tone: Tone
  icon: GlyphName
  blocks: PreviewBlock[]
  toc: { num: string; text: string }[]
  hasToc: boolean
  emptyBody: boolean
}

export const TEAM_AUTHOR = 'ASComply team'

function byline(draft: Draft, authors: Author[]): string | null {
  if (draft.author_id === null) return null
  const a = authors.find((x) => x.user_id === draft.author_id)
  return a ? (a.display_name ?? TEAM_AUTHOR) : draft.author_name
}

export function previewModel(draft: Draft, cats: Category[], authors: Author[], takenIds: Iterable<string>): Preview {
  const cat = cats.find((c) => c.id === draft.category_id)
  const who = byline(draft, authors)
  const toc: Preview['toc'] = []
  const blocks = cleanBody(draft.body).map((b): PreviewBlock => {
    if (b.type === 'ul') return { type: 'ul', items: b.items ?? [] }
    if (b.type === 'note') return { type: 'note', ...(NOTES[b.kind ?? ''] ?? NOTES.regulation), title: b.title ?? '', text: b.text ?? '' }
    if (b.type === 'quote') return { type: 'quote', text: b.text ?? '', who: b.who ?? '' }
    if (b.type === 'demo') return { type: 'demo', title: DEMOS.find((d) => d.id === b.demo)?.title ?? '' }
    if (b.type === 'h') toc.push({ num: String(toc.length + 1).padStart(2, '0'), text: b.text ?? '' })
    return { type: b.type === 'h' ? 'h' : 'p', text: b.text ?? '' }
  })
  const title = draft.title.trim()
  return {
    url: `blog.ascomply.com/${draft.id || previewId(draft.title, takenIds)}`,
    catName: cat ? cat.name : 'No category',
    meta: bDate(draft.publish_date) + (who ? ` · ${who}` : ''),
    date: bDate(draft.publish_date),
    title: title || 'Untitled post',
    titleMuted: !title,
    dek: draft.summary.trim() || 'Your summary appears here.',
    tone: cat ? TONES[cat.tone % 6] : TONES[5],
    icon: iconOf(cat?.icon),
    blocks,
    toc,
    hasToc: toc.length > 1,
    emptyBody: blocks.length === 0,
  }
}
