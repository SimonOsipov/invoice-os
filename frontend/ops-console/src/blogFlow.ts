import { ApiError } from '@invoice-os/api-client'
import { COVER_QUALITY, fitWidth, isImageType, pinToast } from './blog'
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

export const BAD_IMAGE = 'Choose a JPG or PNG image'

export type CoverPickDeps = {
  readAsDataUrl: (file: Blob) => Promise<string>
  loadImage: (src: string) => Promise<{ width: number; height: number }>
  toJpeg: (image: { width: number; height: number }, width: number, height: number, quality: number) => string
}

export type CoverPick = { kind: 'ignore' } | { kind: 'rejected'; toast: string } | { kind: 'ready'; dataUrl: string }

export async function pickCover(d: CoverPickDeps, file: Blob | null | undefined): Promise<CoverPick> {
  if (!file) return { kind: 'ignore' }
  if (!isImageType(file.type)) return { kind: 'rejected', toast: BAD_IMAGE }
  try {
    const img = await d.loadImage(await d.readAsDataUrl(file))
    if (!(img.width > 0 && img.height > 0)) throw new Error('empty image')
    const { width, height } = fitWidth(img.width, img.height)
    return { kind: 'ready', dataUrl: d.toJpeg(img, width, height, COVER_QUALITY) }
  } catch {
    return { kind: 'rejected', toast: BAD_IMAGE }
  }
}

export type CoverHandle = { url: string; release: () => void }

// Fetches a saved cover; `onUrl(null)` on failure. The returned stop releases the URL, also when the fetch lands late.
export function trackCover(d: { fetchCoverUrl: (id: string) => Promise<CoverHandle> }, id: string, onUrl: (url: string | null) => void): () => void {
  let live = true
  let handle: CoverHandle | null = null
  d.fetchCoverUrl(id).then(
    (h) => {
      if (!live) return h.release()
      handle = h
      onUrl(h.url)
    },
    () => {
      if (live) onUrl(null)
    },
  )
  return () => {
    live = false
    handle?.release()
    handle = null
  }
}
