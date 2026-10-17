import { ApiError } from '@invoice-os/api-client'
import { pinToast } from './blog'
import type { Author, Category, FeaturedResult, Post } from './blogApi'
import type { BlogState } from './components/BlogList'

export type PinOutcome = { kind: 'done'; toast: string } | { kind: 'expired' } | { kind: 'forbidden' } | { kind: 'failed'; message: string }

export type PinDeps = { setFeatured: (id: string, featured: boolean) => Promise<FeaturedResult> }

export async function pinPost(deps: PinDeps, post: Post): Promise<PinOutcome> {
  try {
    await deps.setFeatured(post.id, !post.featured)
    return { kind: 'done', toast: pinToast(post.featured) }
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return { kind: 'expired' }
    if (e instanceof ApiError && e.status === 403) return { kind: 'forbidden' }
    return { kind: 'failed', message: e instanceof Error ? e.message : 'Could not update the post' }
  }
}

export type LoadDeps = {
  fetchPosts: () => Promise<Post[]>
  fetchCategories: () => Promise<Category[]>
  fetchAuthors: () => Promise<Author[]>
  expire: () => void
}

export function readFailure(e: unknown, expire: () => void): BlogState {
  if (e instanceof ApiError && e.status === 401) expire()
  if (e instanceof ApiError && e.status === 403) return { status: 'forbidden' }
  return { status: 'error', message: e instanceof Error ? e.message : 'Could not load posts' }
}

export async function loadBlog(d: LoadDeps): Promise<BlogState> {
  try {
    const [posts, cats, authors] = await Promise.all([d.fetchPosts(), d.fetchCategories(), d.fetchAuthors()])
    return { status: 'ready', posts, cats, authors }
  } catch (e) {
    return readFailure(e, d.expire)
  }
}

export type PinFlowDeps = PinDeps & { fetchPosts: () => Promise<Post[]>; expire: () => void }
export type PinAfter = { kind: 'posts'; posts: Post[] } | { kind: 'state'; state: BlogState } | { kind: 'none' }

export async function pinThenRefresh(d: PinFlowDeps, post: Post): Promise<{ outcome: PinOutcome; after: PinAfter }> {
  const outcome = await pinPost(d, post)
  if (outcome.kind === 'expired') {
    d.expire()
    return { outcome, after: { kind: 'none' } }
  }
  try {
    return { outcome, after: { kind: 'posts', posts: await d.fetchPosts() } }
  } catch (e) {
    return { outcome, after: { kind: 'state', state: readFailure(e, d.expire) } }
  }
}
