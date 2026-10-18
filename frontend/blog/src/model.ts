import { gatewayBase } from '@invoice-os/api-client/client'
import type { BlogData, Category, Post } from './data'
import type { GlyphName } from './icons'
import type { Route } from './route'

export type BlogState = { status: 'loading' } | { status: 'ready'; data: BlogData } | { status: 'failed' }

export type View =
  | { kind: 'home' }
  | { kind: 'subscribe' }
  | { kind: 'category'; cat: Category }
  | { kind: 'post'; post: Post; cat: Category | undefined }
  | { kind: 'search'; q: string }
  | { kind: 'missing' }
  | { kind: 'loading' }
  | { kind: 'failed' }

export type Crumb = { label: string; go?: () => void }

type Tone = { bg: string; fg: string; tile: string }

// Verbatim from the prototype's blog-data.js TONES.
export const TONES: readonly Tone[] = [
  { bg: 'var(--sage)', fg: 'var(--primary)', tile: 'var(--card)' },
  { bg: 'var(--surface)', fg: 'var(--accent)', tile: 'var(--surface-panel)' },
  { bg: 'var(--peach-band)', fg: 'var(--primary)', tile: 'var(--card)' },
  { bg: 'var(--mint-soft)', fg: 'var(--teal)', tile: 'var(--card)' },
  { bg: 'var(--surface-2, var(--surface))', fg: 'var(--surface-foreground)', tile: 'var(--surface-panel)' },
  { bg: 'var(--muted)', fg: 'var(--primary)', tile: 'var(--card)' },
]

type NoteKind = { label: string; icon: GlyphName; bg: string; bd: string; tileBg: string; labelColor: string }

export const NOTE_KINDS: Record<string, NoteKind> = {
  regulation: { label: 'Regulation note', icon: 'landmark', bg: 'var(--sage-card)', bd: 'var(--border)', tileBg: 'var(--sage-panel)', labelColor: 'var(--step-label)' },
  deadline: { label: 'Deadline', icon: 'calendar-clock', bg: 'var(--peach-tint)', bd: 'var(--status-amber-border)', tileBg: 'var(--card)', labelColor: 'var(--status-amber-text)' },
  product: { label: 'Availability', icon: 'circle-check', bg: 'var(--mint-soft)', bd: 'var(--border)', tileBg: 'var(--card)', labelColor: 'var(--teal)' },
}

export const noteKindOf = (kind: string): NoteKind => NOTE_KINDS[kind] ?? NOTE_KINDS.regulation

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

export function fmtDate(d: string): string {
  const [y, m, day] = d.split('-')
  return `${+day} ${MONTHS[+m - 1]} ${y}`
}

export const postsOf = (data: BlogData, catId: string): Post[] => data.posts.filter((p) => p.category_id === catId)

export const catOf = (data: BlogData, id: string): Category | undefined => data.categories.find((c) => c.id === id)

export function featuredOf(posts: Post[]): { post: Post; label: 'Featured' | 'Latest' } | null {
  const pinned = posts.find((p) => p.featured)
  if (pinned) return { post: pinned, label: 'Featured' }
  return posts.length > 0 ? { post: posts[0], label: 'Latest' } : null
}

export function bandsOf(data: BlogData) {
  return data.categories
    .map((cat) => ({ cat, all: postsOf(data, cat.id) }))
    .filter((b) => b.all.length > 0)
    .map(({ cat, all }, i) => ({
      cat,
      bg: i % 2 === 0 ? 'var(--sage)' : 'var(--background)',
      cardBd: i % 2 === 0 ? 'var(--sage-border, #cedac7)' : 'var(--border)',
      allLabel: `All ${all.length} posts`,
      posts: all.slice(0, 3),
    }))
}

export function placeholderOf(cat: Category | undefined): Tone & { icon: GlyphName } {
  if (!cat) return { bg: 'var(--muted)', fg: 'var(--primary)', tile: 'var(--card)', icon: 'file-text' }
  return { ...TONES[cat.tone % TONES.length], icon: cat.icon }
}

export const coverUrl = (path: string): string => (gatewayBase() ?? '') + path

// Subscribe needs no data, so it shows while the load runs or fails.
export function resolveView(route: Route, q: string, blog: BlogState): View {
  const searching = q.trim() !== ''
  if (route.view === 'subscribe' && !searching) return { kind: 'subscribe' }
  if (blog.status === 'failed') return { kind: 'failed' }
  if (blog.status === 'loading') return { kind: 'loading' }
  if (searching) return { kind: 'search', q }
  const { data } = blog
  switch (route.view) {
    case 'home':
      return { kind: 'home' }
    case 'subscribe':
      return { kind: 'subscribe' }
    case 'category': {
      const cat = catOf(data, route.id)
      return cat ? { kind: 'category', cat } : { kind: 'missing' }
    }
    case 'post': {
      const post = data.posts.find((p) => p.id === route.slug)
      return post ? { kind: 'post', post, cat: catOf(data, post.category_id) } : { kind: 'missing' }
    }
  }
}

export function crumbsFor(view: View, navigate: (r: Route) => void): Crumb[] {
  const blog = (last: boolean): Crumb => ({ label: 'Blog', go: last ? undefined : () => navigate({ view: 'home' }) })
  switch (view.kind) {
    case 'search':
      return [blog(false), { label: 'Search' }]
    case 'subscribe':
      return [blog(false), { label: 'Subscribe' }]
    case 'category':
      return [blog(false), { label: view.cat.name }]
    case 'post': {
      const { cat, post } = view
      if (!cat) return [blog(false), { label: post.title }]
      return [blog(false), { label: cat.name, go: () => navigate({ view: 'category', id: cat.id }) }, { label: post.title }]
    }
    default:
      return [blog(true)]
  }
}
