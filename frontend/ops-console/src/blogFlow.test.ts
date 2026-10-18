import { ApiError } from '@invoice-os/api-client'
import { renewConsoleSession } from '@invoice-os/console-session'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

import { SESSION_KEY } from './auth'
import type { Author, Category, Post } from './blogApi'
import { editorOf, newCatForm, newDraft, saveState, type CatForm, type CatUi, type Draft, type EditorState } from './blog'
import { applyCatMove, applyCatRefresh, applyCatRemove, applyCatSave, applyRemove, applySave, BAD_IMAGE, COVER_TOO_LARGE, keepOnExpire, keepThenExpire, catsAfterRemove, loadBlog, moveCategoryFlow, postsAfterCatRemove, removeCategory, saveCategory, pickCover, pinPost, pinThenRefresh, postsAfterRemove, refreshCategories, removePost, savePost, singleFlight, STAFF_ONLY, trackCover, type CatRemoveOutcome, type CoverPickDeps, type SaveDeps } from './blogFlow'
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

  it('a small image keeps its size and a png type is read, never upscaled', async () => {
    const d = deps({ loadImage: vi.fn().mockResolvedValue({ width: 800, height: 600 }) })
    await pickCover(d, png)
    expect(d.toJpeg).toHaveBeenCalledWith({ width: 800, height: 600 }, 800, 600, 0.82)
  })

  it('an encoded image over 2 MiB is rejected with the red toast text', async () => {
    const big = (n: number) => `data:image/jpeg;base64,${btoa('a'.repeat(n))}`
    const over = await pickCover(deps({ toJpeg: vi.fn().mockReturnValue(big((2 << 20) + 1)) }), png)
    expect(over).toEqual({ kind: 'rejected', toast: COVER_TOO_LARGE })
    expect(COVER_TOO_LARGE).toBe('Choose an image of 2 MB or less')
    const exact = big(2 << 20)
    expect(await pickCover(deps({ toJpeg: vi.fn().mockReturnValue(exact) }), png)).toEqual({ kind: 'ready', dataUrl: exact })
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
    const stop = trackCover({ fetchCoverUrl: vi.fn().mockResolvedValue(h), expire: vi.fn() }, 'p1', onUrl)
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
    const stop = trackCover({ fetchCoverUrl: () => new Promise((r) => (land = r)), expire: vi.fn() }, 'p1', onUrl)
    stop()
    land(h)
    await Promise.resolve()
    expect(h.release).toHaveBeenCalledTimes(1)
    expect(onUrl).not.toHaveBeenCalled()
  })

  it('a failed fetch reports null', async () => {
    const onUrl = vi.fn()
    trackCover({ fetchCoverUrl: vi.fn().mockRejectedValue(new ApiError('http', 'no such cover', 404)), expire: vi.fn() }, 'p1', onUrl)
    await Promise.resolve()
    await Promise.resolve()
    expect(onUrl).toHaveBeenCalledWith(null)
  })

  it('a failure after stop reports nothing', async () => {
    const onUrl = vi.fn()
    const stop = trackCover({ fetchCoverUrl: vi.fn().mockRejectedValue(new Error('x')), expire: vi.fn() }, 'p1', onUrl)
    stop()
    await Promise.resolve()
    await Promise.resolve()
    expect(onUrl).not.toHaveBeenCalled()
  })

  it('a 404 does not expire the session', async () => {
    const expire = vi.fn()
    trackCover({ fetchCoverUrl: vi.fn().mockRejectedValue(new ApiError('http', 'gone', 404)), expire }, 'p1', vi.fn())
    await new Promise((r) => setTimeout(r))
    expect(expire).not.toHaveBeenCalled()
  })

  it('a 401 that survives a refused renewal expires the session once', async () => {
    const expire = vi.fn()
    const onUrl = vi.fn()
    const call = vi.fn().mockRejectedValue(new ApiError('http', 'Unauthorized', 401))
    trackCover({ fetchCoverUrl: () => withSession(call), expire }, 'p1', onUrl)
    await new Promise((r) => setTimeout(r))
    expect(call).toHaveBeenCalledTimes(1)
    expect(expire).toHaveBeenCalledTimes(1)
    expect(onUrl).toHaveBeenCalledWith(null)
  })

  it('a 401 lands after stop still expires the session and reports nothing', async () => {
    const expire = vi.fn()
    const onUrl = vi.fn()
    let fail!: (e: unknown) => void
    const stop = trackCover({ fetchCoverUrl: () => new Promise((_, r) => (fail = r)), expire }, 'p1', onUrl)
    stop()
    fail(new ApiError('http', 'Unauthorized', 401))
    await new Promise((r) => setTimeout(r))
    expect(expire).toHaveBeenCalledTimes(1)
    expect(onUrl).not.toHaveBeenCalled()
  })
})

const CAT1 = { id: 'c1' } as Category
const dirty = (o: Partial<Draft> = {}): EditorState => ({ ...editorOf({ ...newDraft([CAT1], [], null, '2026-10-18'), title: 'T', ...o }), dirty: true })
const saved = (o: Partial<Post> = {}) => ({ id: 'N', status: 'draft', author_name: null, ...o }) as Post
const saveDeps = (o: Partial<Record<keyof SaveDeps, Mock>> = {}): Record<keyof SaveDeps, Mock> => ({
  createPost: vi.fn().mockResolvedValue(saved()),
  updatePost: vi.fn().mockResolvedValue(saved({ status: 'published' })),
  putCover: vi.fn().mockResolvedValue({}),
  removeCover: vi.fn().mockResolvedValue(undefined),
  fetchPosts: vi.fn().mockResolvedValue(POSTS),
  ...o,
})
const NEW_COVER = { kind: 'new' as const, dataUrl: 'data:image/jpeg;base64,AQID' }

describe('savePost', () => {
  it('creates, then uploads the new cover', async () => {
    const d = saveDeps()
    const o = await savePost(d, dirty({ cover: NEW_COVER }), 'draft')
    expect(o).toEqual({ kind: 'saved', post: saved(), toast: 'Draft saved', posts: POSTS })
    expect(d.createPost).toHaveBeenCalledTimes(1)
    expect(d.createPost.mock.calls[0][0]).toMatchObject({ status: 'draft', title: 'T' })
    expect(d.putCover).toHaveBeenCalledWith('N', new Uint8Array([1, 2, 3]))
    expect(d.createPost.mock.invocationCallOrder[0]).toBeLessThan(d.putCover.mock.invocationCallOrder[0])
    expect(d.updatePost).not.toHaveBeenCalled()
  })

  it('updates and removes a dropped cover', async () => {
    const d = saveDeps()
    const o = await savePost(d, dirty({ id: 'p9', status: 'published', cover: { kind: 'none' }, serverHasCover: true }), 'published')
    expect(o).toMatchObject({ kind: 'saved', toast: 'Changes published' })
    expect(d.updatePost).toHaveBeenCalledWith('p9', expect.objectContaining({ status: 'published' }))
    expect(d.removeCover).toHaveBeenCalledWith('N')
    expect(d.createPost).not.toHaveBeenCalled()
  })

  it('touches no cover when nothing changed', async () => {
    const d = saveDeps()
    await savePost(d, dirty({ id: 'p9', cover: { kind: 'saved' }, serverHasCover: true }), 'draft')
    await savePost(d, dirty({ id: 'p9', cover: { kind: 'none' }, serverHasCover: false }), 'draft')
    expect(d.putCover).not.toHaveBeenCalled()
    expect(d.removeCover).not.toHaveBeenCalled()
  })

  it('a 422 is invalid with mapped errors and the server toast', async () => {
    const e = new ApiError('http', 'Fix the highlighted fields to publish', 422, { error: 'x', fields: { summary: 'Add a summary', category_id: 'Pick one' } })
    const d = saveDeps({ createPost: vi.fn().mockRejectedValue(e) })
    expect(await savePost(d, dirty({ cover: NEW_COVER }), 'published')).toEqual({
      kind: 'invalid',
      errors: { summary: 'Add a summary', category: 'Pick one' },
      toast: 'Fix the highlighted fields to publish',
    })
    expect(d.putCover).not.toHaveBeenCalled()
    expect(d.fetchPosts).not.toHaveBeenCalled()
  })

  it('a 422 without fields is a failed save', async () => {
    const d = saveDeps({ createPost: vi.fn().mockRejectedValue(new ApiError('http', 'bad', 422)) })
    expect(await savePost(d, dirty(), 'draft')).toEqual({ kind: 'failed', message: 'bad', savedPost: undefined })
  })

  it('a 401 is expired; on the cover it keeps the saved post', async () => {
    const d = saveDeps({ updatePost: vi.fn().mockRejectedValue(http(401)) })
    expect(await savePost(d, dirty({ id: 'p9' }), 'draft')).toEqual({ kind: 'expired', savedPost: undefined })
    const c = saveDeps({ putCover: vi.fn().mockRejectedValue(http(401)) })
    expect(await savePost(c, dirty({ cover: NEW_COVER }), 'draft')).toEqual({ kind: 'expired', savedPost: saved() })
  })

  it('a 403 is forbidden; on the cover it keeps the saved post', async () => {
    const d = saveDeps({ createPost: vi.fn().mockRejectedValue(http(403)) })
    expect(await savePost(d, dirty(), 'draft')).toEqual({ kind: 'forbidden', savedPost: undefined })
    const c = saveDeps({ putCover: vi.fn().mockRejectedValue(http(403)) })
    expect(await savePost(c, dirty({ cover: NEW_COVER }), 'draft')).toEqual({ kind: 'forbidden', savedPost: saved() })
  })

  it('a failed cover after a saved post keeps the post and skips the re-read', async () => {
    const c = saveDeps({ putCover: vi.fn().mockRejectedValue(http(413, 'Choose an image of 2 MB or less')) })
    expect(await savePost(c, dirty({ cover: NEW_COVER }), 'draft')).toEqual({ kind: 'failed', message: 'Choose an image of 2 MB or less', savedPost: saved() })
    expect(c.fetchPosts).not.toHaveBeenCalled()
  })

  it('a network error is failed', async () => {
    const d = saveDeps({ updatePost: vi.fn().mockRejectedValue(new ApiError('network', 'Failed to fetch')) })
    expect(await savePost(d, dirty({ id: 'p9' }), 'draft')).toMatchObject({ kind: 'failed', message: 'Failed to fetch' })
  })

  it('a failed list re-read keeps the save', async () => {
    const d = saveDeps({ fetchPosts: vi.fn().mockRejectedValue(http(500, 'internal error')) })
    expect(await savePost(d, dirty(), 'draft')).toEqual({ kind: 'saved', post: saved(), toast: 'Draft saved', rereadError: 'internal error' })
  })

  it('was-status comes from the draft: a published draft unpublishes', async () => {
    const d = saveDeps({ updatePost: vi.fn().mockResolvedValue(saved()) })
    expect(await savePost(d, dirty({ id: 'p9', status: 'published' }), 'draft')).toMatchObject({ toast: 'Unpublished. The post is now a draft' })
  })
})

describe('removePost', () => {
  const rm = (del: Mock, fetchPosts: Mock = vi.fn().mockResolvedValue(POSTS)) => removePost({ deletePost: del, fetchPosts }, 'p1')

  it('deletes, re-reads the list and toasts; each failure has its outcome and no re-read', async () => {
    const del = vi.fn().mockResolvedValue(undefined)
    const re = vi.fn().mockResolvedValue(POSTS)
    expect(await rm(del, re)).toEqual({ kind: 'done', toast: 'Post deleted', posts: POSTS })
    expect(del).toHaveBeenCalledWith('p1')
    expect(del.mock.invocationCallOrder[0]).toBeLessThan(re.mock.invocationCallOrder[0])
    for (const [err, want] of [
      [http(401), { kind: 'expired' }],
      [http(403), { kind: 'forbidden' }],
      [http(500, 'boom'), { kind: 'failed', message: 'boom' }],
    ] as const) {
      const f = vi.fn()
      expect(await rm(vi.fn().mockRejectedValue(err), f)).toEqual(want)
      expect(f).not.toHaveBeenCalled()
    }
  })

  it('a failed re-read keeps the delete as done with the error', async () => {
    expect(await rm(vi.fn().mockResolvedValue(undefined), vi.fn().mockRejectedValue(http(500, 'internal error')))).toEqual({ kind: 'done', toast: 'Post deleted', rereadError: 'internal error' })
  })
})

describe('postsAfterRemove', () => {
  const list = [{ id: 'p1' }, { id: 'p2' }] as Post[]
  it('takes the re-read list, else drops the deleted post, and ignores a failed delete', () => {
    const fresh = [{ id: 'p2' }, { id: 'p3' }] as Post[]
    expect(postsAfterRemove(list, 'p1', { kind: 'done', toast: 't', posts: fresh })).toBe(fresh)
    expect(postsAfterRemove(list, 'p1', { kind: 'done', toast: 't', rereadError: 'x' })).toEqual([{ id: 'p2' }])
    expect(postsAfterRemove(list, 'p1', { kind: 'failed', message: 'x' })).toBe(list)
    expect(postsAfterRemove(list, 'p1', { kind: 'forbidden' })).toBe(list)
    expect(postsAfterRemove(list, 'p1', { kind: 'expired' })).toBe(list)
  })
})

describe('applySave', () => {
  it('saved: clean editor, toast and the re-read list', () => {
    const sent = dirty({ cover: NEW_COVER })
    const a = applySave(sent, sent, { kind: 'saved', post: saved({ id: 'N' }), toast: 'Draft saved', posts: POSTS })
    expect(a).toMatchObject({ toast: 'Draft saved', tone: 'ok', posts: POSTS, expire: false })
    expect(a.editor).toMatchObject({ dirty: false, draft: { id: 'N', cover: { kind: 'saved' } } })
  })

  it('saved after an edit made mid-flight stays dirty', () => {
    const sent = dirty()
    const cur = { ...sent, draft: { ...sent.draft, title: 'newer' } }
    const a = applySave(cur, sent, { kind: 'saved', post: saved(), toast: 'Draft saved', posts: POSTS })
    expect(a.editor).toMatchObject({ dirty: true, draft: { id: 'N', title: 'newer' } })
  })

  it('saved after an edit mid-flight carries the cover the save uploaded', () => {
    const sent = dirty({ cover: NEW_COVER })
    const cur = { ...sent, draft: { ...sent.draft, title: 'newer' } }
    const a = applySave(cur, sent, { kind: 'saved', post: saved(), toast: 'Draft saved', posts: POSTS })
    expect(a.editor.draft).toMatchObject({ cover: NEW_COVER, serverHasCover: true })
    const removed = applySave({ ...cur, draft: { ...cur.draft, cover: { kind: 'none' } } }, sent, { kind: 'saved', post: saved(), toast: 'Draft saved', posts: POSTS })
    expect(removed.editor.draft).toMatchObject({ cover: { kind: 'none' }, serverHasCover: true })
    const gone = applySave(cur, dirty({ cover: { kind: 'none' }, serverHasCover: true }), { kind: 'saved', post: saved(), toast: 'Draft saved', posts: POSTS })
    expect(gone.editor.draft.serverHasCover).toBe(false)
  })

  it('a re-read error is the red toast and the list is not replaced', () => {
    const sent = dirty()
    const a = applySave(sent, sent, { kind: 'saved', post: saved(), toast: 'Draft saved', rereadError: 'internal error' })
    expect(a).toMatchObject({ toast: 'internal error', tone: 'red', expire: false })
    expect(a.posts).toBeUndefined()
    expect(a.editor.dirty).toBe(false)
    expect(saveState(a.editor.dirty, a.editor.draft.id).label).toBe('ALL CHANGES SAVED')
  })

  it('a cover that fails after a create keeps the id, so the next save updates and does not duplicate', async () => {
    const sent = dirty({ cover: NEW_COVER })
    const first = saveDeps({ putCover: vi.fn().mockRejectedValue(http(401)) })
    const o = await savePost(first, sent, 'draft')
    const cur = applySave(sent, sent, o).editor
    expect(cur.draft.id).toBe('N')
    const next = saveDeps({ updatePost: vi.fn().mockResolvedValue(saved()) })
    await savePost(next, cur, 'draft')
    expect(next.createPost).not.toHaveBeenCalled()
    expect(next.updatePost).toHaveBeenCalledWith('N', expect.any(Object))
    expect(next.putCover).toHaveBeenCalledWith('N', new Uint8Array([1, 2, 3]))
  })

  it('invalid marks the fields and keeps the editor dirty', () => {
    const sent = { ...dirty(), confirm: 'leave' as const }
    const a = applySave(sent, sent, { kind: 'invalid', errors: { title: 'x' }, toast: 'Fix it' })
    expect(a).toMatchObject({ toast: 'Fix it', tone: 'red', expire: false })
    expect(a.editor).toMatchObject({ errors: { title: 'x' }, dirty: true, confirm: null })
  })

  it('expired expires with no toast and takes the saved id; forbidden and failed toast red', () => {
    const sent = dirty({ cover: NEW_COVER })
    const e = applySave(sent, sent, { kind: 'expired', savedPost: saved({ id: 'N' }) })
    expect(e).toMatchObject({ expire: true, toast: null })
    expect(e.editor).toMatchObject({ dirty: true, draft: { id: 'N', cover: { kind: 'new' } } })
    expect(applySave(sent, sent, { kind: 'expired' }).editor).toBe(sent)
    expect(applySave(sent, sent, { kind: 'forbidden', savedPost: saved() })).toMatchObject({ toast: STAFF_ONLY, tone: 'red', expire: false })
    const f = applySave(sent, sent, { kind: 'failed', message: 'boom', savedPost: saved({ id: 'N' }) })
    expect(f).toMatchObject({ toast: 'boom', tone: 'red', expire: false })
    expect(f.editor).toMatchObject({ dirty: true, draft: { id: 'N' } })
  })
})

describe('applyRemove', () => {
  it('done closes the editor; failures keep it with the confirm cleared', () => {
    const cur = { ...dirty({ id: 'p9' }), confirm: 'delete' as const }
    expect(applyRemove(cur, { kind: 'done', toast: 'Post deleted', posts: POSTS })).toEqual({ editor: null, toast: 'Post deleted', tone: 'ok', expire: false })
    expect(applyRemove(cur, { kind: 'done', toast: 'Post deleted', rereadError: 'internal error' })).toEqual({ editor: null, toast: 'internal error', tone: 'red', expire: false })
    expect(applyRemove(cur, { kind: 'forbidden' })).toMatchObject({ toast: STAFF_ONLY, tone: 'red', expire: false, editor: { confirm: null, dirty: true } })
    expect(applyRemove(cur, { kind: 'failed', message: 'boom' })).toMatchObject({ toast: 'boom', tone: 'red' })
    expect(applyRemove(cur, { kind: 'expired' })).toMatchObject({ expire: true, editor: { confirm: null } })
  })
})

describe('keepOnExpire', () => {
  it('keeps the editor as it is when the callback fires, then expires', () => {
    const order: string[] = []
    const d = { write: vi.fn(() => void order.push('write')), expire: vi.fn(() => void order.push('expire')) }
    let cur: EditorState | null = null
    const onExpire = keepOnExpire(d, () => cur)
    cur = dirty()
    onExpire()
    expect(order).toEqual(['write', 'expire'])
    expect(d.write).toHaveBeenCalledWith(cur.draft)
  })
})

describe('keepThenExpire', () => {
  it('writes the dirty draft before it expires, once', () => {
    const order: string[] = []
    const d = { write: vi.fn(() => void order.push('write')), expire: vi.fn(() => void order.push('expire')) }
    const e = dirty()
    keepThenExpire(d, e)
    expect(order).toEqual(['write', 'expire'])
    expect(d.write).toHaveBeenCalledWith(e.draft)
  })

  it('a clean editor or none writes nothing but still expires', () => {
    const d = { write: vi.fn(), expire: vi.fn() }
    keepThenExpire(d, { ...dirty(), dirty: false })
    keepThenExpire(d, null)
    expect(d.write).not.toHaveBeenCalled()
    expect(d.expire).toHaveBeenCalledTimes(2)
  })
})

describe('singleFlight', () => {
  it('ignores a call while one runs and accepts the next after it ends', async () => {
    const run = singleFlight()
    let release: () => void = () => {}
    const first = vi.fn(() => new Promise<void>((r) => (release = r)))
    const second = vi.fn(async () => {})
    const p = run(first)
    await run(second)
    expect(second).not.toHaveBeenCalled()
    release()
    await p
    await run(second)
    expect(second).toHaveBeenCalledTimes(1)
  })

  it('a call made while a throwing action runs is ignored, then the guard frees', async () => {
    const run = singleFlight()
    let fail: (e: Error) => void = () => {}
    const p = run(() => new Promise<void>((_, rej) => (fail = rej)))
    const mid = vi.fn(async () => {})
    await run(mid)
    expect(mid).not.toHaveBeenCalled()
    fail(new Error('x'))
    await expect(p).rejects.toThrow('x')
    await run(mid)
    expect(mid).toHaveBeenCalledTimes(1)
  })

  it('user waits for a running background refresh, then runs once', async () => {
    const run = singleFlight()
    let release: () => void = () => {}
    const refresh = run.after(() => new Promise<void>((r) => (release = r)))
    const save = vi.fn(async () => {})
    const p = run.user(save)
    await Promise.resolve()
    expect(save).not.toHaveBeenCalled()
    release()
    await refresh
    await p
    expect(save).toHaveBeenCalledTimes(1)
  })

  it('user runs after the refresh even when another refresh is queued behind it', async () => {
    const run = singleFlight()
    let release: () => void = () => {}
    void run.after(() => new Promise<void>((r) => (release = r)))
    await run.after(async () => {})
    const del = vi.fn(async () => {})
    const p = run.user(del)
    release()
    await p
    expect(del).toHaveBeenCalledTimes(1)
  })

  it('user is still dropped while another user action runs', async () => {
    const run = singleFlight()
    let release: () => void = () => {}
    const first = run.user(() => new Promise<void>((r) => (release = r)))
    const second = vi.fn(async () => {})
    await run.user(second)
    expect(second).not.toHaveBeenCalled()
    release()
    await first
  })

  it('after runs once the running action ends, and runs at once when idle', async () => {
    const run = singleFlight()
    let release: () => void = () => {}
    const p = run(() => new Promise<void>((r) => (release = r)))
    const refresh = vi.fn(async () => {})
    await run.after(refresh)
    expect(refresh).not.toHaveBeenCalled()
    release()
    await p
    await vi.waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
    const idle = vi.fn(async () => {})
    await run.after(idle)
    expect(idle).toHaveBeenCalledTimes(1)
  })

  it('after keeps only the last queued action, and runs it when the flight throws', async () => {
    const run = singleFlight()
    let fail: (e: Error) => void = () => {}
    const p = run(() => new Promise<void>((_, rej) => (fail = rej)))
    const first = vi.fn(async () => {})
    const last = vi.fn(async () => {})
    await run.after(first)
    await run.after(last)
    fail(new Error('x'))
    await expect(p).rejects.toThrow('x')
    await vi.waitFor(() => expect(last).toHaveBeenCalledTimes(1))
    expect(first).not.toHaveBeenCalled()
    const again = vi.fn(async () => {})
    await run(again)
    expect(again).toHaveBeenCalledTimes(1)
  })

  it('a queued action runs after the flight ends and holds the guard while it runs', async () => {
    const run = singleFlight()
    const order: string[] = []
    let release: () => void = () => {}
    let releaseQ: () => void = () => {}
    const p = run(async () => {
      await new Promise<void>((r) => (release = r))
      order.push('flight')
    })
    await run.after(async () => {
      order.push('queued')
      await new Promise<void>((r) => (releaseQ = r))
    })
    release()
    await p
    await vi.waitFor(() => expect(order).toEqual(['flight', 'queued']))
    const mid = vi.fn(async () => {})
    await run(mid)
    expect(mid).not.toHaveBeenCalled()
    releaseQ()
  })

  it('a plain call made while one runs is still dropped and never queued', async () => {
    const run = singleFlight()
    let release: () => void = () => {}
    const p = run(() => new Promise<void>((r) => (release = r)))
    const dropped = vi.fn(async () => {})
    await run(dropped)
    release()
    await p
    await Promise.resolve()
    expect(dropped).not.toHaveBeenCalled()
  })

  it('frees the guard when the action throws', async () => {
    const run = singleFlight()
    await expect(run(async () => Promise.reject(new Error('x')))).rejects.toThrow('x')
    const again = vi.fn(async () => {})
    await run(again)
    expect(again).toHaveBeenCalledTimes(1)
  })
})

describe('category move and refresh', () => {
  const a = { id: 'a' } as Category
  const b = { id: 'b' } as Category

  it('moveCategoryFlow returns the server order', async () => {
    const moveCategory = vi.fn().mockResolvedValue({ moved: true, categories: [b, a] })
    expect(await moveCategoryFlow({ moveCategory }, 'a', 'up')).toEqual({ kind: 'moved', cats: [b, a] })
    expect(moveCategory).toHaveBeenCalledTimes(1)
    expect(moveCategory).toHaveBeenCalledWith('a', 'up')
  })

  it('moveCategoryFlow maps 401, 403 and other failures', async () => {
    const run = (e: unknown) => moveCategoryFlow({ moveCategory: vi.fn().mockRejectedValue(e) }, 'a', 'down')
    expect(await run(http(401))).toEqual({ kind: 'expired' })
    expect(await run(http(403))).toEqual({ kind: 'forbidden' })
    expect(await run(http(500, 'internal error'))).toEqual({ kind: 'failed', message: 'internal error', status: 500 })
    expect(await run(new TypeError('Failed to fetch'))).toEqual({ kind: 'failed', message: 'Failed to fetch', status: undefined })
  })

  it('applyCatMove applies each outcome', () => {
    expect(applyCatMove({ kind: 'moved', cats: [b, a] })).toEqual({ cats: [b, a], toast: null, tone: 'red', expire: false, reread: false })
    expect(applyCatMove({ kind: 'expired' })).toMatchObject({ expire: true, toast: null })
    expect(applyCatMove({ kind: 'forbidden' })).toMatchObject({ toast: 'Your account is not ASComply staff.', tone: 'red', expire: false })
    expect(applyCatMove({ kind: 'failed', message: 'internal error', status: 500 })).toMatchObject({ toast: 'internal error', tone: 'red', reread: false })
    expect(applyCatMove({ kind: 'failed', message: 'no such category', status: 404 })).toMatchObject({ toast: 'no such category', tone: 'red', reread: true })
  })

  it('refreshCategories and applyCatRefresh', async () => {
    const ok = await refreshCategories({ fetchCategories: vi.fn().mockResolvedValue([a]) })
    expect(ok).toEqual({ kind: 'cats', cats: [a] })
    expect(applyCatRefresh(ok)).toMatchObject({ cats: [a], toast: null, expire: false })
    const gone = await refreshCategories({ fetchCategories: vi.fn().mockRejectedValue(http(401)) })
    expect(gone).toEqual({ kind: 'expired' })
    expect(applyCatRefresh(gone).expire).toBe(true)
    const boom = applyCatRefresh(await refreshCategories({ fetchCategories: vi.fn().mockRejectedValue(http(500, 'internal error')) }))
    expect(boom).toMatchObject({ toast: 'internal error', tone: 'red', reread: false })
    expect(boom.cats).toBeUndefined()
  })
})

describe('category save and delete', () => {
  const cat = (id: string, o: Partial<Category> = {}): Category => ({ id, name: id.toUpperCase(), headline: '', intro: '', icon: 'globe', tone: 0, post_count: 0, ...o })
  const form = (o: Partial<CatForm> = {}): CatForm => ({ ...newCatForm([]), name: 'Events', ...o })
  const input = { name: 'Events', headline: '', intro: '', icon: 'globe', tone: 0 }
  const unprocessable = new ApiError('http', 'invalid category', 422, { fields: { name: 'Add a name.' } })

  it('saveCategory adds a new category and re-reads the list', async () => {
    const addCategory = vi.fn().mockResolvedValue(cat('x'))
    const fetchCategories = vi.fn().mockResolvedValue([cat('x')])
    const o = await saveCategory({ addCategory, updateCategory: vi.fn(), fetchCategories }, form())
    expect(o).toEqual({ kind: 'saved', toast: 'Category added', category: cat('x'), cats: [cat('x')] })
    expect(addCategory).toHaveBeenCalledWith(input)
    expect(fetchCategories).toHaveBeenCalledTimes(1)
  })

  it('saveCategory updates an existing category', async () => {
    const addCategory = vi.fn()
    const updateCategory = vi.fn().mockResolvedValue(cat('c'))
    const o = await saveCategory({ addCategory, updateCategory, fetchCategories: vi.fn().mockResolvedValue([]) }, form({ id: 'c' }))
    expect(o).toMatchObject({ kind: 'saved', toast: 'Category updated' })
    expect(updateCategory).toHaveBeenCalledWith('c', input)
    expect(addCategory).not.toHaveBeenCalled()
  })

  it('saveCategory: a 422 name and a 409 duplicate are invalid', async () => {
    const fetchCategories = vi.fn()
    const run = (e: unknown) => saveCategory({ addCategory: vi.fn().mockRejectedValue(e), updateCategory: vi.fn(), fetchCategories }, form())
    expect(await run(unprocessable)).toEqual({ kind: 'invalid', error: 'Add a name.' })
    const dup = 'A category with this name already exists.'
    expect(await run(new ApiError('http', dup, 409))).toEqual({ kind: 'invalid', error: dup })
    expect(fetchCategories).not.toHaveBeenCalled()
  })

  it('saveCategory maps 401, 403 and other failures', async () => {
    const run = (e: unknown) => saveCategory({ addCategory: vi.fn().mockRejectedValue(e), updateCategory: vi.fn(), fetchCategories: vi.fn() }, form())
    expect(await run(http(401))).toEqual({ kind: 'expired' })
    expect(await run(http(403))).toEqual({ kind: 'forbidden' })
    expect(await run(http(500, 'internal error'))).toEqual({ kind: 'failed', message: 'internal error', status: 500 })
    expect(await run(http(404, 'no such category'))).toEqual({ kind: 'failed', message: 'no such category', status: 404 })
  })

  it('saveCategory sends a 140-character unbroken name unchanged', async () => {
    const name = 'x'.repeat(140)
    const addCategory = vi.fn().mockResolvedValue(cat('x'))
    await saveCategory({ addCategory, updateCategory: vi.fn(), fetchCategories: vi.fn().mockResolvedValue([]) }, form({ name }))
    expect(addCategory).toHaveBeenCalledWith({ ...input, name })
  })

  it('saveCategory: a failed re-read keeps the save', async () => {
    const o = await saveCategory({ addCategory: vi.fn().mockResolvedValue(cat('x')), updateCategory: vi.fn(), fetchCategories: vi.fn().mockRejectedValue(http(500, 'internal error')) }, form())
    expect(o).toEqual({ kind: 'saved', toast: 'Category added', category: cat('x'), rereadError: 'internal error' })
    expect(o).not.toHaveProperty('cats')
  })

  it('applyCatSave: a save whose re-read fails still adds the row at the end and toasts success', () => {
    const o = { kind: 'saved' as const, toast: 'Category added', category: cat('n'), rereadError: 'internal error' }
    const a = applyCatSave(ui({}), o, [cat('a'), cat('b')])
    expect(a.cats).toEqual([cat('a'), cat('b'), cat('n')])
    expect(a.toast).toContain('Category added')
    expect(a.toast).toContain('internal error')
    expect(a.ui.form).toBeNull()
  })

  it('applyCatSave: a failed re-read replaces an edited row by id and appends one missing from the list', () => {
    const edited = { kind: 'saved' as const, toast: 'Category updated', category: cat('b', { name: 'New' }), rereadError: 'x' }
    expect(applyCatSave(ui({ id: 'b' }), edited, [cat('a'), cat('b'), cat('c')]).cats).toEqual([cat('a'), cat('b', { name: 'New' }), cat('c')])
    expect(applyCatSave(ui({ id: 'b' }), edited, [cat('a')]).cats).toEqual([cat('a'), cat('b', { name: 'New' })])
  })

  it('removeCategory moves the posts to the target and names the count', async () => {
    const cats = [cat('a', { post_count: 2 }), cat('b')]
    const deleteCategory = vi.fn().mockResolvedValue({ moved_posts: 2, move_to: 'b' })
    const fetchCategories = vi.fn().mockResolvedValue([cat('b', { post_count: 2 })])
    const fetchPosts = vi.fn().mockResolvedValue([])
    const o = await removeCategory({ deleteCategory, fetchCategories, fetchPosts }, cats, 'a', 'b')
    expect(deleteCategory).toHaveBeenCalledWith('a', 'b')
    expect(o).toEqual({ kind: 'done', toast: 'A deleted. 2 posts moved to B', move_to: 'b', cats: [cat('b', { post_count: 2 })], posts: [] })
    expect(fetchCategories).toHaveBeenCalledTimes(1)
    expect(fetchPosts).toHaveBeenCalledTimes(1)
  })

  it('removeCategory sends no target for an empty category', async () => {
    const deleteCategory = vi.fn().mockResolvedValue({ moved_posts: 0, move_to: null })
    const o = await removeCategory({ deleteCategory, fetchCategories: vi.fn().mockResolvedValue([]), fetchPosts: vi.fn().mockResolvedValue([]) }, [cat('a'), cat('b')], 'a', 'b')
    expect(deleteCategory).toHaveBeenCalledWith('a', null)
    expect(o).toMatchObject({ kind: 'done', toast: 'A deleted' })
  })

  it('removeCategory maps 401, 403, 409, 422 and 5xx', async () => {
    const run = (e: unknown) => removeCategory({ deleteCategory: vi.fn().mockRejectedValue(e), fetchCategories: vi.fn(), fetchPosts: vi.fn() }, [cat('a'), cat('b')], 'a', null)
    expect(await run(http(401))).toEqual({ kind: 'expired' })
    expect(await run(http(403))).toEqual({ kind: 'forbidden' })
    const only = 'This is the only category. Add another before deleting it.'
    expect(await run(new ApiError('http', only, 409))).toEqual({ kind: 'failed', message: only, status: 409 })
    const choose = 'Choose a category to move its posts to.'
    expect(await run(new ApiError('http', choose, 422, { fields: { move_to: choose } }))).toEqual({ kind: 'failed', message: choose, status: 422 })
    expect(await run(http(500, 'internal error'))).toEqual({ kind: 'failed', message: 'internal error', status: 500 })
    expect(await run(http(404, 'no such category'))).toEqual({ kind: 'failed', message: 'no such category', status: 404 })
  })

  it('removeCategory: a failed re-read keeps the delete', async () => {
    const deleteCategory = vi.fn().mockResolvedValue({ moved_posts: 0, move_to: null })
    const o = await removeCategory({ deleteCategory, fetchCategories: vi.fn().mockResolvedValue([]), fetchPosts: vi.fn().mockRejectedValue(http(500, 'internal error')) }, [cat('a'), cat('b')], 'a', null)
    expect(o).toMatchObject({ kind: 'done', rereadError: 'internal error' })
    expect(o).not.toHaveProperty('cats')
    expect(o).not.toHaveProperty('posts')
  })

  const ui = (f: Partial<CatForm> | null): CatUi => ({ form: f ? form(f) : null, del: null })

  it('applyCatSave applies each outcome', () => {
    const open = ui({ id: 'c' })
    const saved = applyCatSave(open, { kind: 'saved', toast: 'Category updated', category: cat('c'), cats: [cat('c')] })
    expect(saved).toMatchObject({ ui: { form: null }, cats: [cat('c')], toast: 'Category updated', tone: 'ok', expire: false, reread: false })
    const noRead = applyCatSave(open, { kind: 'saved', toast: 'Category updated', category: cat('c', { name: 'New' }), rereadError: 'internal error' }, [cat('c'), cat('d')])
    expect(noRead).toMatchObject({ ui: { form: null }, toast: 'Category updated. internal error', tone: 'red' })
    expect(noRead.cats).toEqual([cat('c', { name: 'New' }), cat('d')])
    const invalid = applyCatSave(open, { kind: 'invalid', error: 'Add a name.' })
    expect(invalid.ui.form).toMatchObject({ id: 'c', error: 'Add a name.' })
    expect(invalid.toast).toBeNull()
    expect(applyCatSave(open, { kind: 'expired' })).toMatchObject({ expire: true, toast: null })
    expect(applyCatSave(open, { kind: 'forbidden' })).toMatchObject({ toast: STAFF_ONLY, tone: 'red', ui: open })
    expect(applyCatSave(open, { kind: 'failed', message: 'internal error', status: 500 })).toMatchObject({ toast: 'internal error', tone: 'red', reread: false, ui: open })
    expect(applyCatSave(open, { kind: 'failed', message: 'no such category', status: 404 })).toMatchObject({ toast: 'no such category', tone: 'red', reread: true, ui: { form: null } })
  })

  const FILTERS = { tab: 'cats', q: 'x', status: 'all', cat: 'a' } as const
  const lists = { cats: [cat('a', { post_count: 1 }), cat('b')], posts: [{ id: 'p', category_id: 'a' }] as Post[] }
  const withDel: CatUi = { form: null, del: { id: 'a', moveTo: 'b' } }
  const done: CatRemoveOutcome & { kind: 'done' } = { kind: 'done', toast: 'A deleted. 1 post moved to B', move_to: 'b', cats: [cat('b', { post_count: 1 })], posts: [{ id: 'p', category_id: 'b' }] as Post[] }

  it('applyCatRemove applies each outcome and resets the filter', () => {
    const a = applyCatRemove(lists, withDel, FILTERS, 'a', done)
    expect(a).toMatchObject({ ui: { del: null }, cats: done.cats, posts: done.posts, filters: { ...FILTERS, cat: 'all' }, toast: done.toast, tone: 'ok' })
    expect(applyCatRemove(lists, withDel, { ...FILTERS, cat: 'b' }, 'a', done).filters.cat).toBe('b')
    expect(applyCatRemove(lists, withDel, FILTERS, 'a', { kind: 'forbidden' })).toMatchObject({ ui: { del: null }, toast: STAFF_ONLY, tone: 'red', cats: lists.cats })
    expect(applyCatRemove(lists, withDel, FILTERS, 'a', { kind: 'failed', message: 'x', status: 422 })).toMatchObject({ ui: { del: null }, toast: 'x', tone: 'red', reread: true })
    expect(applyCatRemove(lists, withDel, FILTERS, 'a', { kind: 'failed', message: 'x', status: 404 })).toMatchObject({ ui: { del: null }, reread: true })
    expect(applyCatRemove(lists, withDel, FILTERS, 'a', { kind: 'failed', message: 'x', status: 409 })).toMatchObject({ ui: { del: null }, toast: 'x', tone: 'red', reread: true })
    expect(applyCatRemove(lists, withDel, FILTERS, 'a', { kind: 'expired' })).toMatchObject({ expire: true })
  })

  it('applyCatRemove: a failed re-read still removes the category, with a red toast', () => {
    const a = applyCatRemove(lists, withDel, FILTERS, 'a', { kind: 'done', toast: 'ok', move_to: 'b', rereadError: 'internal error' })
    expect(a).toMatchObject({ toast: 'internal error', tone: 'red', ui: { del: null } })
    expect(a.cats.map((c) => c.id)).toEqual(['b'])
    expect(a.posts[0].category_id).toBe('b')
  })

  it('catsAfterRemove and postsAfterCatRemove apply a delete whose re-read failed', () => {
    const o = { kind: 'done', toast: '', move_to: 'b', rereadError: 'internal error' } as const
    const posts = [{ id: 'p', category_id: 'a' }, { id: 'q', category_id: 'b' }] as Post[]
    expect(catsAfterRemove([cat('a'), cat('b')], 'a', o).map((c) => c.id)).toEqual(['b'])
    expect(postsAfterCatRemove(posts, 'a', o).map((p) => p.category_id)).toEqual(['b', 'b'])
    const fail = { kind: 'failed', message: 'x' } as const
    const cs = [cat('a'), cat('b')]
    expect(catsAfterRemove(cs, 'a', fail)).toBe(cs)
    expect(postsAfterCatRemove(posts, 'a', fail)).toBe(posts)
  })
})
