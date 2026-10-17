import type { Category, Post } from './blogApi'
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
        badge: p.status === 'published' ? PUBLISHED : DRAFT,
        pinTitle: p.featured ? 'Unpin featured post' : 'Pin as featured post',
      }
    })
}
