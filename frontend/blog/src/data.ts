import { apiFetch, gatewayBase } from '@invoice-os/api-client/client'
import type { GlyphName } from './icons'

export type Category = {
  id: string
  name: string
  headline: string
  intro: string
  icon: GlyphName
  tone: number
  post_count: number
}

// Wire shape: internal/blog/blog.go Block.MarshalJSON.
export type Block =
  | { type: 'p'; text: string }
  | { type: 'h'; text: string }
  | { type: 'ul'; items: string[] }
  | { type: 'note'; kind: string; title: string; text: string }
  | { type: 'quote'; text: string; who: string }
  | { type: 'demo'; demo: string }

export type Post = {
  id: string
  category_id: string
  publish_date: string
  title: string
  summary: string
  author_name: string
  featured: boolean
  cover: string | null
  body: Block[]
}

export type BlogData = { categories: Category[]; posts: Post[] }

export async function loadBlog(): Promise<BlogData> {
  const base = gatewayBase()
  if (base === null) throw new Error('blog: no gateway URL')
  const [cats, posts] = await Promise.all([
    apiFetch<{ categories: Category[] }>(`${base}/blog/categories`),
    apiFetch<{ posts: Post[] }>(`${base}/blog/posts`),
  ])
  return { categories: cats.categories, posts: posts.posts }
}
