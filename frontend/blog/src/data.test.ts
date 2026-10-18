import { afterEach, describe, expect, it, vi } from 'vitest'
import { loadBlog } from './data'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function stubGateway(posts: () => Response = () => json({ posts: [{ id: 'p' }] })) {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test/')
  const fetchMock = vi.fn<typeof fetch>(async (url) =>
    String(url).endsWith('/blog/categories') ? json({ categories: [{ id: 'c' }] }) : posts(),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('loadBlog', () => {
  it('loadBlog_getsCategoriesAndPostsFromTheGateway', async () => {
    const fetchMock = stubGateway()
    await expect(loadBlog()).resolves.toEqual({ categories: [{ id: 'c' }], posts: [{ id: 'p' }] })
    const calls = fetchMock.mock.calls
    expect(calls.map((c) => String(c[0])).sort()).toEqual(['https://gw.test/blog/categories', 'https://gw.test/blog/posts'])
    for (const [, init] of calls) {
      expect(init?.method).toBe('GET')
      expect(init?.body).toBeUndefined()
      expect(new Headers(init?.headers).has('Authorization')).toBe(false)
    }
  })

  it('loadBlog_rejectsWithNoGateway', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn<typeof fetch>()
    vi.stubGlobal('fetch', fetchMock)
    await expect(loadBlog()).rejects.toThrow('blog: no gateway URL')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('loadBlog_rejectsOnA502', async () => {
    stubGateway(() => json({ error: 'bad gateway' }, 502))
    await expect(loadBlog()).rejects.toThrow()
  })
})
