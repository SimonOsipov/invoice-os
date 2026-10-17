import { ApiError } from '@invoice-os/api-client'
import { renewConsoleSession } from '@invoice-os/console-session'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import type { Author, Category, Post } from './blogApi'
import { loadBlog, pinPost, pinThenRefresh } from './blogFlow'
import { withSession } from './session'

vi.mock('@invoice-os/console-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@invoice-os/console-session')>()),
  renewConsoleSession: vi.fn(),
  saveConsoleSession: vi.fn(),
}))

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

const http = (status: number, msg = 'x') => new ApiError('http', msg, status)
const POSTS = [{ id: 'p1' }] as Post[]
const reads = (o: Partial<Parameters<typeof loadBlog>[0]> = {}) => ({
  fetchPosts: vi.fn().mockResolvedValue(POSTS),
  fetchCategories: vi.fn().mockResolvedValue([]),
  fetchAuthors: vi.fn().mockResolvedValue([]),
  expire: vi.fn(),
  ...o,
})

describe('loadBlog', () => {
  it('reads posts, categories and authors into a ready state', async () => {
    const cats = [{ id: 'c1' }] as Category[]
    const authors = [{ user_id: 'u1' }] as Author[]
    const d = reads({ fetchCategories: vi.fn().mockResolvedValue(cats), fetchAuthors: vi.fn().mockResolvedValue(authors) })
    expect(await loadBlog(d)).toEqual({ status: 'ready', posts: POSTS, cats, authors })
    expect(d.fetchAuthors).toHaveBeenCalledTimes(1)
    expect(d.expire).not.toHaveBeenCalled()
  })

  it('a 403 read is forbidden and does not expire the session', async () => {
    const d = reads({ fetchCategories: vi.fn().mockRejectedValue(http(403)) })
    expect(await loadBlog(d)).toEqual({ status: 'forbidden' })
    expect(d.expire).not.toHaveBeenCalled()
  })

  it('another read error keeps its message; a non-Error gets the default', async () => {
    expect(await loadBlog(reads({ fetchPosts: vi.fn().mockRejectedValue(http(500, 'boom')) }))).toEqual({ status: 'error', message: 'boom' })
    expect(await loadBlog(reads({ fetchPosts: vi.fn().mockRejectedValue('x') }))).toEqual({ status: 'error', message: 'Could not load posts' })
  })

  it('three reads that all 401 expire the session exactly once', async () => {
    const no = () => vi.fn().mockRejectedValue(http(401, 'Unauthorized'))
    const d = reads({ fetchPosts: no(), fetchCategories: no(), fetchAuthors: no() })
    expect(await loadBlog(d)).toEqual({ status: 'error', message: 'Unauthorized' })
    expect(d.expire).toHaveBeenCalledTimes(1)
  })
})

describe('loadBlog through withSession', () => {
  const renew = vi.mocked(renewConsoleSession)
  const jwt = (c: object) => `${btoa('{"alg":"x"}')}.${btoa(JSON.stringify(c)).replace(/=+$/, '')}.s`
  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test/')
    vi.stubGlobal('localStorage', { getItem: (k: string) => (k === SESSION_KEY ? JSON.stringify({ v: 2, token: 'OLD', refresh_token: 'R' }) : null) })
    renew.mockReset()
  })
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })
  const flaky = <T,>(v: T) => {
    let n = 0
    return vi.fn(async () => {
      if (n++ === 0) throw http(401, 'Unauthorized')
      return v
    })
  }

  it('a 401 on every read renews once and retries each read', async () => {
    renew.mockResolvedValue({ token: jwt({ app_metadata: { staff: true } }), refreshToken: 'R2' })
    const [p, c, a] = [flaky(POSTS), flaky([]), flaky([])]
    const d = { fetchPosts: () => withSession(p), fetchCategories: () => withSession(c), fetchAuthors: () => withSession(a), expire: vi.fn() }
    expect((await loadBlog(d)).status).toBe('ready')
    expect(renew).toHaveBeenCalledTimes(1)
    for (const f of [p, c, a]) expect(f).toHaveBeenCalledTimes(2)
    expect(d.expire).not.toHaveBeenCalled()
  })

  it('a refused renewal expires the session once for three failed reads', async () => {
    renew.mockRejectedValue(http(401, 'refused'))
    const no = () => vi.fn<() => Promise<never>>().mockRejectedValue(http(401, 'Unauthorized'))
    const [p, c, a] = [no(), no(), no()]
    const d = { fetchPosts: () => withSession(p), fetchCategories: () => withSession(c), fetchAuthors: () => withSession(a), expire: vi.fn() }
    expect((await loadBlog(d)).status).toBe('error')
    expect(renew).toHaveBeenCalledTimes(1)
    for (const f of [p, c, a]) expect(f).toHaveBeenCalledTimes(1)
    expect(d.expire).toHaveBeenCalledTimes(1)
  })
})

describe('pinThenRefresh', () => {
  const pinDeps = (o: object = {}) => ({ setFeatured: vi.fn().mockResolvedValue(ok), fetchPosts: vi.fn().mockResolvedValue(POSTS), expire: vi.fn(), ...o })

  it('a pin re-reads the posts', async () => {
    const d = pinDeps()
    expect(await pinThenRefresh(d, post(false))).toEqual({ outcome: { kind: 'done', toast: 'Pinned as the featured post' }, after: { kind: 'posts', posts: POSTS } })
    expect(d.fetchPosts).toHaveBeenCalledTimes(1)
  })

  it('a failed pin still re-reads; a forbidden one too', async () => {
    const d = pinDeps({ setFeatured: vi.fn().mockRejectedValue(http(500, 'nope')) })
    const r = await pinThenRefresh(d, post(false))
    expect(r.outcome).toEqual({ kind: 'failed', message: 'nope' })
    expect(r.after).toEqual({ kind: 'posts', posts: POSTS })
    const f = pinDeps({ setFeatured: vi.fn().mockRejectedValue(http(403)) })
    expect((await pinThenRefresh(f, post(false))).outcome).toEqual({ kind: 'forbidden' })
    expect(f.fetchPosts).toHaveBeenCalledTimes(1)
  })

  it('an expired pin expires the session once and does not re-read', async () => {
    const d = pinDeps({ setFeatured: vi.fn().mockRejectedValue(http(401)) })
    expect(await pinThenRefresh(d, post(false))).toEqual({ outcome: { kind: 'expired' }, after: { kind: 'none' } })
    expect(d.expire).toHaveBeenCalledTimes(1)
    expect(d.fetchPosts).not.toHaveBeenCalled()
  })

  it('a failed re-read replaces the list with its error state', async () => {
    const d = pinDeps({ fetchPosts: vi.fn().mockRejectedValue(http(403)) })
    expect((await pinThenRefresh(d, post(false))).after).toEqual({ kind: 'state', state: { status: 'forbidden' } })
    const e = pinDeps({ fetchPosts: vi.fn().mockRejectedValue(http(401, 'Unauthorized')) })
    expect((await pinThenRefresh(e, post(false))).after).toEqual({ kind: 'state', state: { status: 'error', message: 'Unauthorized' } })
    expect(e.expire).toHaveBeenCalledTimes(1)
  })
})
