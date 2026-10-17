import { ApiError } from '@invoice-os/api-client'
import { describe, expect, it, vi } from 'vitest'

import type { Post } from './blogApi'
import { pinPost } from './blogFlow'

const post = (featured: boolean) => ({ id: 'p1', featured }) as Post
const ok = { id: 'p1', featured: true, unpinned: null }

describe('pinPost', () => {
  it('flips the flag', async () => {
    const setFeatured = vi.fn().mockResolvedValue(ok)
    expect(await pinPost({ setFeatured }, post(false))).toEqual({ kind: 'done', toast: 'Pinned as the featured post' })
    expect(setFeatured).toHaveBeenCalledWith('p1', true)
    expect(await pinPost({ setFeatured }, post(true))).toEqual({ kind: 'done', toast: 'Unpinned. The newest post leads the blog home' })
    expect(setFeatured).toHaveBeenLastCalledWith('p1', false)
  })

  it('a 401 is expired', async () => {
    const setFeatured = vi.fn().mockRejectedValue(new ApiError('http', 'Unauthorized', 401))
    expect(await pinPost({ setFeatured }, post(false))).toEqual({ kind: 'expired' })
  })

  it('a 403 is forbidden, a 500 is failed with its message', async () => {
    const forbid = vi.fn().mockRejectedValue(new ApiError('http', 'no', 403))
    expect(await pinPost({ setFeatured: forbid }, post(false))).toEqual({ kind: 'forbidden' })
    const boom = vi.fn().mockRejectedValue(new ApiError('http', 'internal error', 500))
    expect(await pinPost({ setFeatured: boom }, post(false))).toEqual({ kind: 'failed', message: 'internal error' })
  })
})
