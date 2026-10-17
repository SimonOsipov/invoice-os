import { ApiError } from '@invoice-os/api-client'
import { pinToast } from './blog'
import type { FeaturedResult, Post } from './blogApi'

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
