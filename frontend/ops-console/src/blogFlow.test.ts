import { ApiError } from '@invoice-os/api-client'
import { renewConsoleSession } from '@invoice-os/console-session'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import type { Author, Category, Post } from './blogApi'
import { BAD_IMAGE, loadBlog, pickCover, pinPost, pinThenRefresh, trackCover, type CoverPickDeps } from './blogFlow'
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

describe('pickCover', () => {
  const png = new Blob(['x'], { type: 'image/png' })
  const deps = (o: Partial<CoverPickDeps> = {}): CoverPickDeps => ({
    readAsDataUrl: vi.fn().mockResolvedValue('data:image/png;base64,AA=='),
    loadImage: vi.fn().mockResolvedValue({ width: 2000, height: 1000 }),
    toJpeg: vi.fn().mockReturnValue('data:image/jpeg;base64,/9j/'),
    ...o,
  })

  it('no file does nothing', async () => {
    const d = deps()
    expect(await pickCover(d, undefined)).toEqual({ kind: 'ignore' })
    expect(await pickCover(d, null)).toEqual({ kind: 'ignore' })
    expect(d.readAsDataUrl).not.toHaveBeenCalled()
  })

  it('a non-image is rejected before it is read', async () => {
    const d = deps()
    expect(await pickCover(d, new Blob(['x'], { type: 'text/plain' }))).toEqual({ kind: 'rejected', toast: BAD_IMAGE })
    expect(BAD_IMAGE).toBe('Choose a JPG or PNG image')
    expect(d.readAsDataUrl).not.toHaveBeenCalled()
  })

  it('draws at the fitted size with quality 0.82 and returns the data URL', async () => {
    const d = deps()
    expect(await pickCover(d, png)).toEqual({ kind: 'ready', dataUrl: 'data:image/jpeg;base64,/9j/' })
    expect(d.loadImage).toHaveBeenCalledWith('data:image/png;base64,AA==')
    expect(d.toJpeg).toHaveBeenCalledWith({ width: 2000, height: 1000 }, 1600, 800, 0.82)
  })

  it('an unreadable file, an undecodable image and an empty image are rejected', async () => {
    const bad = { kind: 'rejected', toast: BAD_IMAGE }
    expect(await pickCover(deps({ readAsDataUrl: vi.fn().mockRejectedValue(new Error('io')) }), png)).toEqual(bad)
    expect(await pickCover(deps({ loadImage: vi.fn().mockRejectedValue(new Error('decode')) }), png)).toEqual(bad)
    expect(await pickCover(deps({ loadImage: vi.fn().mockResolvedValue({ width: 0, height: 0 }) }), png)).toEqual(bad)
    expect(await pickCover(deps({ toJpeg: vi.fn().mockImplementation(() => { throw new Error('canvas') }) }), png)).toEqual(bad)
  })
})

describe('trackCover', () => {
  const handle = () => ({ url: 'blob:1', release: vi.fn() })

  it('reports the URL and releases it on stop, once', async () => {
    const h = handle()
    const onUrl = vi.fn()
    const stop = trackCover({ fetchCoverUrl: vi.fn().mockResolvedValue(h) }, 'p1', onUrl)
    await Promise.resolve()
    expect(onUrl).toHaveBeenCalledWith('blob:1')
    stop()
    stop()
    expect(h.release).toHaveBeenCalledTimes(1)
  })

  it('a fetch that lands after stop is released and never reported', async () => {
    const h = handle()
    let land!: (v: typeof h) => void
    const onUrl = vi.fn()
    const stop = trackCover({ fetchCoverUrl: () => new Promise((r) => (land = r)) }, 'p1', onUrl)
    stop()
    land(h)
    await Promise.resolve()
    expect(h.release).toHaveBeenCalledTimes(1)
    expect(onUrl).not.toHaveBeenCalled()
  })

  it('a failed fetch reports null', async () => {
    const onUrl = vi.fn()
    trackCover({ fetchCoverUrl: vi.fn().mockRejectedValue(new ApiError('http', 'no such cover', 404)) }, 'p1', onUrl)
    await Promise.resolve()
    await Promise.resolve()
    expect(onUrl).toHaveBeenCalledWith(null)
  })

  it('a failure after stop reports nothing', async () => {
    const onUrl = vi.fn()
    const stop = trackCover({ fetchCoverUrl: vi.fn().mockRejectedValue(new Error('x')) }, 'p1', onUrl)
    stop()
    await Promise.resolve()
    await Promise.resolve()
    expect(onUrl).not.toHaveBeenCalled()
  })
})
