import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { ApiError } from '@invoice-os/api-client'
import { stripComments } from '@invoice-os/api-client/strip-comments'
import { expireConsole } from '@invoice-os/console-session'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { request } from './api'
import { expireSession, SESSION_KEY } from './auth'
import {
  createPost,
  deletePost,
  fetchAuthors,
  fetchCategories,
  fetchCoverUrl,
  fetchPosts,
  fieldErrorsOf,
  putCover,
  removeCover,
  setFeatured,
  updatePost,
  type PostInput,
} from './blogApi'

vi.mock('@invoice-os/console-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@invoice-os/console-session')>()),
  expireConsole: vi.fn(),
}))

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

  it('blogApi: createPost and updatePost send the input as the JSON body', async () => {
    fetchMock.mockImplementation(async () => json({ id: 'p' }))
    await createPost(INPUT)
    await updatePost('p', INPUT)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const [create, update] = fetchMock.mock.calls as Array<[string, RequestInit]>
    expect(create[1].method).toBe('POST')
    expect(update[1].method).toBe('PUT')
    expect(Object.keys(INPUT)).toHaveLength(8)
    for (const [, init] of [create, update]) {
      expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
      expect(JSON.parse(String(init.body))).toEqual(INPUT)
    }
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

  it('putCover: no token is a 401 and sends nothing', async () => {
    storeSession(null)
    await expect(putCover('p1', BYTES)).rejects.toMatchObject({ kind: 'http', status: 401, message: 'Not signed in' })
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
})
