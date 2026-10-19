import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { ApiError } from '@invoice-os/api-client'
import { stripComments } from '@invoice-os/api-client/strip-comments'
import { expireConsole } from '@invoice-os/console-session'
import { captureApiFailure } from '@invoice-os/monitoring/report'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { request } from './api'
import { newCatForm, toCategoryInput } from './blog'
import { expireSession, SESSION_KEY } from './auth'
import { withSession } from './session'
import {
  addCategory,
  createPost,
  deleteCategory,
  deletePost,
  fetchAuthors,
  fetchCategories,
  fetchCoverUrl,
  fetchPosts,
  fieldErrorsOf,
  moveCategory,
  putCover,
  removeCover,
  setFeatured,
  updateCategory,
  updatePost,
  type CategoryInput,
  type PostInput,
} from './blogApi'

vi.mock('@invoice-os/console-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@invoice-os/console-session')>()),
  expireConsole: vi.fn(),
}))
vi.mock('@invoice-os/monitoring/report', () => ({ captureApiFailure: vi.fn() }))

const GW = 'https://gw.test'
const LANDING = 'https://www.test'
const ROUTE = `${GW}/api/notifications/v1/staff/blog`

let fetchMock: ReturnType<typeof vi.fn>
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const storeSession = (raw: string | null) =>
  vi.stubGlobal('localStorage', { getItem: (k: string) => (k === SESSION_KEY ? raw : null) })
const signedIn = () => storeSession(JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }))

const lastCall = () => {
  expect(fetchMock, 'fetch was called').toHaveBeenCalledTimes(1)
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
  return { url, init, headers: new Headers(init.headers) }
}

const INPUT: PostInput = {
  category_id: 'c1',
  publish_date: '2026-10-17',
  title: 'Hello',
  summary: 'A summary',
  author_id: null,
  status: 'draft',
  featured: false,
  body: [{ type: 'p', text: 'One' }, { type: 'ul', items: ['a', 'b'] }],
}

const CAT_INPUT: CategoryInput = { name: 'Product', headline: 'What changed.', intro: 'Intro', icon: 'globe', tone: 2 }

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  vi.stubEnv('VITE_LANDING_URL', LANDING + '/')
  signedIn()
  fetchMock = vi.fn(async () => json({}))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.mocked(expireConsole).mockClear()
  vi.mocked(captureApiFailure).mockClear()
})

describe('request', () => {
  it('request: refuses without a gateway or a token', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    await expect(request('/x')).rejects.toMatchObject({ name: 'ApiError', kind: 'network' })

    vi.stubEnv('VITE_GATEWAY_URL', GW)
    storeSession(null)
    await expect(request('/x')).rejects.toMatchObject({ kind: 'http', status: 401 })
    expect(fetchMock).not.toHaveBeenCalled()

    signedIn()
    await request('/x')
    const { url, headers } = lastCall()
    expect(url).toBe(`${GW}/x`)
    expect(headers.get('Authorization')).toBe('Bearer T')
  })
})

describe('expireSession', () => {
  it('expireSession: signs out through expireConsole with app null', () => {
    expireSession()
    expect(expireConsole).toHaveBeenCalledTimes(1)
    expect(expireConsole).toHaveBeenCalledWith({ storageKey: 'invoice-os.ops-session', gateway: GW, landing: LANDING, app: null, target: 'ops' })
  })
})

describe('blogApi', () => {
  const cases: Array<{
    name: string
    run: (id: string) => Promise<unknown>
    method: string
    path: (id: string) => string
    status: number
    answer: unknown
    sent?: unknown
    expected: unknown
  }> = [
    { name: 'fetchPosts', run: () => fetchPosts(), method: 'GET', path: () => '/posts', status: 200, answer: { posts: [{ id: 'p1' }] }, expected: [{ id: 'p1' }] },
    { name: 'fetchCategories', run: () => fetchCategories(), method: 'GET', path: () => '/categories', status: 200, answer: { categories: [{ id: 'c1' }] }, expected: [{ id: 'c1' }] },
    { name: 'fetchAuthors', run: () => fetchAuthors(), method: 'GET', path: () => '/authors', status: 200, answer: { authors: [{ user_id: 'u1' }] }, expected: [{ user_id: 'u1' }] },
    { name: 'createPost', run: () => createPost(INPUT), method: 'POST', path: () => '/posts', status: 201, answer: { id: 'p9' }, sent: INPUT, expected: { id: 'p9' } },
    { name: 'updatePost', run: (id) => updatePost(id, INPUT), method: 'PUT', path: (id) => `/posts/${encodeURIComponent(id)}`, status: 200, answer: { id: 'p1' }, sent: INPUT, expected: { id: 'p1' } },
    { name: 'deletePost', run: (id) => deletePost(id), method: 'DELETE', path: (id) => `/posts/${encodeURIComponent(id)}`, status: 204, answer: null, expected: undefined },
    {
      name: 'setFeatured',
      run: (id) => setFeatured(id, true),
      method: 'PUT',
      path: (id) => `/posts/${encodeURIComponent(id)}/featured`,
      status: 200,
      answer: { id: 'p1', featured: true, unpinned: 'p0' },
      sent: { featured: true },
      expected: { id: 'p1', featured: true, unpinned: 'p0' },
    },
    { name: 'addCategory', run: () => addCategory(CAT_INPUT), method: 'POST', path: () => '/categories', status: 201, answer: { id: 'c9' }, sent: CAT_INPUT, expected: { id: 'c9' } },
    { name: 'updateCategory', run: (id) => updateCategory(id, CAT_INPUT), method: 'PUT', path: (id) => `/categories/${encodeURIComponent(id)}`, status: 200, answer: { id: 'p1' }, sent: CAT_INPUT, expected: { id: 'p1' } },
    {
      name: 'moveCategory',
      run: (id) => moveCategory(id, 'down'),
      method: 'POST',
      path: (id) => `/categories/${encodeURIComponent(id)}/move`,
      status: 200,
      answer: { moved: true, categories: [{ id: 'a' }, { id: 'b' }] },
      sent: { direction: 'down' },
      expected: { moved: true, categories: [{ id: 'a' }, { id: 'b' }] },
    },
    {
      name: 'deleteCategory',
      run: (id) => deleteCategory(id, null),
      method: 'DELETE',
      path: (id) => `/categories/${encodeURIComponent(id)}`,
      status: 200,
      answer: { moved_posts: 0, move_to: null },
      expected: { moved_posts: 0, move_to: null },
    },
    { name: 'removeCover', run: (id) => removeCover(id), method: 'DELETE', path: (id) => `/posts/${encodeURIComponent(id)}/cover`, status: 204, answer: null, expected: undefined },
  ]

  for (const c of cases) {
    it(`blogApi: each call hits its method and path (${c.name})`, async () => {
      fetchMock.mockResolvedValueOnce(c.status === 204 ? new Response(null, { status: 204 }) : json(c.answer, c.status))
      const out = await c.run('p1')
      const { url, init, headers } = lastCall()
      expect(url).toBe(ROUTE + c.path('p1'))
      expect(init.method ?? 'GET').toBe(c.method)
      expect(headers.get('Authorization')).toBe('Bearer T')
      expect(out).toEqual(c.expected)
      if (c.sent !== undefined) expect(JSON.parse(String(init.body))).toEqual(c.sent)
    })
  }

  it('blogApi: ids are encoded', async () => {
    fetchMock.mockImplementation(async () => new Response(null, { status: 204 }))
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x')
    const calls: Array<[string, () => Promise<unknown>, string]> = [
      ['updatePost', () => updatePost('a/b', INPUT), '/posts/a%2Fb'],
      ['updateCategory', () => updateCategory('a/b', CAT_INPUT), '/categories/a%2Fb'],
      ['deletePost', () => deletePost('a/b'), '/posts/a%2Fb'],
      ['setFeatured', () => setFeatured('a/b', false), '/posts/a%2Fb/featured'],
      ['removeCover', () => removeCover('a/b'), '/posts/a%2Fb/cover'],
      ['putCover', () => putCover('a/b', new Uint8Array([1])), '/posts/a%2Fb/cover'],
      ['fetchCoverUrl', () => fetchCoverUrl('a/b'), '/posts/a%2Fb/cover'],
    ]
    for (const [name, run, tail] of calls) {
      fetchMock.mockClear()
      await run().catch(() => undefined)
      expect(fetchMock, `${name} called fetch`).toHaveBeenCalledTimes(1)
      expect(fetchMock.mock.calls[0][0], name).toBe(ROUTE + tail)
    }
  })

  it('blogApi: create and update calls send their input as the JSON body', async () => {
    fetchMock.mockImplementation(async () => json({ id: 'p' }))
    await createPost(INPUT)
    await updatePost('p', INPUT)
    await addCategory(CAT_INPUT)
    await updateCategory('c', CAT_INPUT)
    expect(fetchMock).toHaveBeenCalledTimes(4)
    const [create, update, addCat, updateCat] = fetchMock.mock.calls as Array<[string, RequestInit]>
    expect(create[1].method).toBe('POST')
    expect(update[1].method).toBe('PUT')
    expect(addCat[1].method).toBe('POST')
    expect(updateCat[1].method).toBe('PUT')
    expect(Object.keys(INPUT)).toHaveLength(8)
    for (const [[, init], input] of [[create, INPUT], [update, INPUT], [addCat, CAT_INPUT], [updateCat, CAT_INPUT]] as const) {
      expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
      expect(JSON.parse(String(init.body))).toEqual(input)
    }
  })

  it('blogApi: moveCategory sends the direction and returns the list', async () => {
    const answer = { moved: true, categories: [{ id: 'a' }, { id: 'b' }] }
    fetchMock.mockResolvedValueOnce(json(answer))
    const out = await moveCategory('a', 'down')
    expect(JSON.parse(String(lastCall().init.body))).toEqual({ direction: 'down' })
    expect(out).toEqual(answer)
  })

  it('blogApi: deleteCategory adds move_to only with a target', async () => {
    fetchMock.mockImplementation(async () => json({ moved_posts: 1, move_to: 'b/c' }))
    await deleteCategory('a', 'b/c')
    await deleteCategory('a', null)
    const urls = fetchMock.mock.calls.map((c) => c[0])
    expect(urls).toEqual([`${ROUTE}/categories/a?move_to=b%2Fc`, `${ROUTE}/categories/a`])
  })

  // Source scan: the fetch is mocked, so a renamed Go json tag is invisible to every runtime test here.
  it('PostInput keys equal the Go PostInput json tags', () => {
    const read = (rel: string) => stripComments(readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8'))
    const goBody = /type\s+PostInput\s+struct\s*\{([^{}]*)\}/.exec(read('../../../internal/blog/blog.go'))?.[1] ?? ''
    const goKeys = [...goBody.matchAll(/`json:"([^",]+)[^"]*"/g)].map((m) => m[1])
    const tsBody = /export interface PostInput\s*\{([^{}]*)\}/.exec(read('./blogApi.ts'))?.[1] ?? ''
    const tsKeys = [...tsBody.matchAll(/^\s*([a-z_]+)\??\s*:/gm)].map((m) => m[1])
    expect(goKeys).toContain('category_id')
    expect(goKeys).toHaveLength(8)
    expect([...tsKeys].sort()).toEqual([...goKeys].sort())
  })

  it('CategoryInput keys equal the Go CategoryInput json tags', () => {
    const read = (rel: string) => stripComments(readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8'))
    const goBody = /type\s+CategoryInput\s+struct\s*\{([^{}]*)\}/.exec(read('../../../internal/blog/blog.go'))?.[1] ?? ''
    const goKeys = [...goBody.matchAll(/`json:"([^",]+)[^"]*"/g)].map((m) => m[1])
    const tsBody = /export interface CategoryInput\s*\{([^{}]*)\}/.exec(read('./blogApi.ts'))?.[1] ?? ''
    const tsKeys = [...tsBody.matchAll(/^\s*([a-z_]+)\??\s*:/gm)].map((m) => m[1])
    expect(goKeys).toHaveLength(5)
    expect([...tsKeys].sort()).toEqual([...goKeys].sort())
    expect(Object.keys(toCategoryInput(newCatForm([]))).sort()).toEqual([...goKeys].sort())
  })
})

describe('putCover', () => {
  const BYTES = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3])

  it('putCover: raw bytes, jpeg type, bearer', async () => {
    fetchMock.mockResolvedValueOnce(json({ content_type: 'image/jpeg', size_bytes: BYTES.length, updated_at: '2026-10-17T10:00:00Z' }))
    await putCover('p1', BYTES)
    const { url, init, headers } = lastCall()
    expect(url).toBe(`${ROUTE}/posts/p1/cover`)
    expect(init.method).toBe('PUT')
    expect(headers.get('Content-Type')).toBe('image/jpeg')
    expect(headers.get('Authorization')).toBe('Bearer T')
    expect(init.body, 'body is sent').toBeDefined()
    expect(Array.from(new Uint8Array(await new Response(init.body as BodyInit).arrayBuffer()))).toEqual(Array.from(BYTES))
  })

  it('putCover: a 413 rejects with the server message', async () => {
    fetchMock.mockResolvedValueOnce(json({ error: 'Choose an image of 2 MB or less' }, 413))
    const err = await putCover('p1', BYTES).then(
      () => null,
      (e: unknown) => e,
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ kind: 'http', status: 413, message: 'Choose an image of 2 MB or less', body: { error: 'Choose an image of 2 MB or less' } })
  })

  it('putCover: no token is a 401 and sends nothing (fetchCoverUrl too)', async () => {
    storeSession(null)
    await expect(putCover('p1', BYTES)).rejects.toMatchObject({ kind: 'http', status: 401, message: 'Not signed in' })
    await expect(fetchCoverUrl('p1')).rejects.toMatchObject({ kind: 'http', status: 401, message: 'Not signed in' })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('cover transport failures', () => {
  const BYTES = new Uint8Array([1, 2, 3])
  const calls: Array<[string, string, () => Promise<unknown>]> = [
    ['putCover', 'PUT', () => putCover('p1', BYTES)],
    ['fetchCoverUrl', 'GET', () => fetchCoverUrl('p1')],
  ]

  for (const [name, method, run] of calls) {
    it(`cover failures: ${name} reports a network failure and rethrows the same error`, async () => {
      const boom = new TypeError('Failed to fetch')
      fetchMock.mockRejectedValueOnce(boom)
      await expect(run()).rejects.toBe(boom)
      expect(captureApiFailure).toHaveBeenCalledTimes(1)
      expect(captureApiFailure).toHaveBeenCalledWith({ kind: 'network', status: null, method, url: `${ROUTE}/posts/p1/cover`, error: boom })
    })

    it(`cover failures: ${name} reports a 5xx with its status`, async () => {
      fetchMock.mockResolvedValueOnce(json({ error: 'db down' }, 503))
      await expect(run()).rejects.toMatchObject({ kind: 'http', status: 503, message: 'db down' })
      expect(captureApiFailure).toHaveBeenCalledTimes(1)
      expect(captureApiFailure).toHaveBeenCalledWith(expect.objectContaining({ kind: 'http', status: 503, method, url: `${ROUTE}/posts/p1/cover` }))
    })

    it(`cover failures: ${name} does not report a 4xx the editor shows itself`, async () => {
      fetchMock.mockResolvedValueOnce(json({ error: 'no such cover' }, 404))
      await expect(run()).rejects.toMatchObject({ status: 404 })
      expect(captureApiFailure).not.toHaveBeenCalled()
    })

    it(`cover failures: ${name} falls back to the status text when the error body is not JSON`, async () => {
      fetchMock.mockResolvedValueOnce(new Response('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway' }))
      const err = await run().then(
        () => null,
        (e: unknown) => e,
      )
      expect(err).toBeInstanceOf(ApiError)
      expect(err).toMatchObject({ kind: 'http', status: 502, message: 'Bad Gateway' })
    })

    it(`cover failures: ${name} carries a 401 and does not report it`, async () => {
      fetchMock.mockResolvedValueOnce(json({ error: 'expired' }, 401))
      await expect(run()).rejects.toMatchObject({ kind: 'http', status: 401 })
      expect(captureApiFailure).not.toHaveBeenCalled()
    })
  }

  it('cover failures: a body that dies mid-read is reported and creates no object URL', async () => {
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:never')
    const boom = new TypeError('terminated')
    const res = new Response('x', { status: 200 })
    vi.spyOn(res, 'blob').mockRejectedValue(boom)
    fetchMock.mockResolvedValueOnce(res)
    await expect(fetchCoverUrl('p1')).rejects.toBe(boom)
    expect(create).not.toHaveBeenCalled()
    expect(captureApiFailure).toHaveBeenCalledTimes(1)
    expect(captureApiFailure).toHaveBeenCalledWith(expect.objectContaining({ kind: 'network', method: 'GET', error: boom }))
  })

  it('cover failures: a putCover answer that dies mid-read is reported', async () => {
    const boom = new TypeError('terminated')
    const res = new Response('{}', { status: 200 })
    vi.spyOn(res, 'json').mockRejectedValue(boom)
    fetchMock.mockResolvedValueOnce(res)
    await expect(putCover('p1', BYTES)).rejects.toBe(boom)
    expect(captureApiFailure).toHaveBeenCalledTimes(1)
    expect(captureApiFailure).toHaveBeenCalledWith(expect.objectContaining({ kind: 'network', method: 'PUT', error: boom }))
  })

  it('cover failures: a gateway-less console sends nothing from either cover call', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    for (const [, , run] of calls) await expect(run()).rejects.toMatchObject({ kind: 'network' })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('fetchCoverUrl', () => {
  it('fetchCoverUrl: returns an object URL and releases it once', async () => {
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:cover-1')
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    fetchMock.mockResolvedValueOnce(new Response(new Uint8Array([1, 2, 3]), { status: 200, headers: { 'Content-Type': 'image/jpeg' } }))
    const r = await fetchCoverUrl('p1')
    const { url, headers } = lastCall()
    expect(url).toBe(`${ROUTE}/posts/p1/cover`)
    expect(headers.get('Authorization')).toBe('Bearer T')
    expect(r).toMatchObject({ url: 'blob:cover-1' })
    expect(create).toHaveBeenCalledTimes(1)
    const blob = create.mock.calls[0][0] as Blob
    expect(Array.from(new Uint8Array(await blob.arrayBuffer()))).toEqual([1, 2, 3])
    expect(revoke).not.toHaveBeenCalled()
    r.release()
    r.release()
    expect(revoke).toHaveBeenCalledTimes(1)
    expect(revoke).toHaveBeenCalledWith('blob:cover-1')
  })

  it('fetchCoverUrl: a 404 creates no URL', async () => {
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:never')
    fetchMock.mockResolvedValueOnce(json({ error: 'no such cover' }, 404))
    await expect(fetchCoverUrl('p1')).rejects.toMatchObject({ kind: 'http', status: 404, message: 'no such cover' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(create).not.toHaveBeenCalled()
  })
})

describe('fieldErrorsOf', () => {
  it('fieldErrorsOf: maps a 422 and nothing else', () => {
    const fields = { title: 'Add a title.' }
    expect(fieldErrorsOf(new ApiError('http', 'Fix the highlighted fields', 422, { error: 'Fix the highlighted fields', fields }))).toEqual({
      summary: 'Fix the highlighted fields',
      fields,
    })
    expect(fieldErrorsOf(new ApiError('http', 'bad', 400, { error: 'bad', fields }))).toBeNull()
    expect(fieldErrorsOf(new ApiError('http', 'bad', 422, { error: 'bad' }))).toBeNull()
    expect(fieldErrorsOf(new Error('boom'))).toBeNull()
  })

  it('fieldErrorsOf: refuses a 422 whose fields is not an object', () => {
    expect(fieldErrorsOf(new ApiError('http', 'bad', 422, { error: 'bad', fields: { title: 'x' } }))).not.toBeNull()
    for (const fields of [['title'], 'title', null, 7]) {
      expect(fieldErrorsOf(new ApiError('http', 'bad', 422, { error: 'bad', fields })), JSON.stringify(fields)).toBeNull()
    }
    expect(fieldErrorsOf(new ApiError('http', 'bad', 422))).toBeNull()
    expect(fieldErrorsOf(new ApiError('network', 'bad', 422, { fields: { title: 'x' } }))).not.toBeNull()
    expect(fieldErrorsOf(undefined)).toBeNull()
    const lookalike = Object.assign(new Error('bad'), { status: 422, body: { fields: { title: 'x' } } })
    expect(fieldErrorsOf(lookalike)).toBeNull()
  })
})

describe('blog calls through withSession', () => {
  const b64url = (o: unknown) => btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  const STAFF = `${b64url({ alg: 'RS256' })}.${b64url({ sub: 's1', app_metadata: { staff: true } })}.c2ln`
  let stored: string | null
  const bearer = (c: unknown[]) => new Headers((c[1] as RequestInit).headers).get('Authorization')

  beforeEach(() => {
    stored = JSON.stringify({ v: 2, token: 'OLD', refresh_token: 'R' })
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => (k === SESSION_KEY ? stored : null),
      setItem: (k: string, v: string) => {
        if (k === SESSION_KEY) stored = v
      },
    })
  })

  const answer = (refresh: Response | (() => Response)) =>
    fetchMock.mockImplementation(async (url: string, init: RequestInit) => {
      if (url === `${GW}/auth/refresh`) return typeof refresh === 'function' ? refresh() : refresh
      return new Headers(init.headers).get('Authorization') === 'Bearer OLD' ? json({ error: 'expired' }, 401) : json({ posts: [], categories: [], authors: [] }, 200)
    })

  it('withSession: an expired token is renewed over the wire and the retry carries the new token', async () => {
    fetchMock.mockImplementation(async (url: string, init: RequestInit) => {
      if (url === `${GW}/auth/refresh`) return json({ access_token: STAFF, refresh_token: 'R2' })
      return new Headers(init.headers).get('Authorization') === 'Bearer OLD' ? json({ error: 'expired' }, 401) : json({ id: 'p9' }, 201)
    })
    await expect(withSession(() => createPost(INPUT))).resolves.toEqual({ id: 'p9' })
    const urls = fetchMock.mock.calls.map((c) => c[0])
    expect(urls).toEqual([`${ROUTE}/posts`, `${GW}/auth/refresh`, `${ROUTE}/posts`])
    expect(bearer(fetchMock.mock.calls[0])).toBe('Bearer OLD')
    expect(bearer(fetchMock.mock.calls[2])).toBe(`Bearer ${STAFF}`)
    expect(JSON.parse(stored ?? '')).toEqual({ v: 2, token: STAFF, refresh_token: 'R2' })
  })

  it('withSession: parallel blog reads send one refresh', async () => {
    answer(() => json({ access_token: STAFF, refresh_token: 'R2' }))
    await expect(Promise.all([withSession(fetchPosts), withSession(fetchCategories), withSession(fetchAuthors)])).resolves.toHaveLength(3)
    expect(fetchMock.mock.calls.filter((c) => c[0] === `${GW}/auth/refresh`)).toHaveLength(1)
  })

  it('withSession: a refused refresh leaves the stored session alone and surfaces the 401', async () => {
    const before = stored
    answer(() => json({ error: 'revoked' }, 401))
    await expect(withSession(() => createPost(INPUT))).rejects.toMatchObject({ kind: 'http', status: 401, message: 'expired' })
    expect(stored).toBe(before)
    expect(fetchMock.mock.calls.filter((c) => c[0] === `${ROUTE}/posts`)).toHaveLength(1)
  })

  it('withSession: a 403 is the staff-only refusal and never renews', async () => {
    fetchMock.mockImplementation(async () => json({ error: 'Staff only' }, 403))
    await expect(withSession(() => createPost(INPUT))).rejects.toMatchObject({ kind: 'http', status: 403, message: 'Staff only' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
