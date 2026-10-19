import { afterEach, describe, expect, it, vi } from 'vitest'
import { confirmSubscription, subscribe } from './subscribeApi'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const BODY = { email: 'a@b.co', category_ids: ['c1'], frequency: 'every_post' as const }

describe('subscribe', () => {
  it('subscribeApi_postsJsonToTheGateway', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw/')
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('{"status":"accepted"}', { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    await subscribe(BODY)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe('https://gw/blog/subscriptions')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toEqual(BODY)
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.has('Authorization')).toBe(false)
  })

  it('subscribeApi_noGatewayRejectsWithoutFetch', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn<typeof fetch>()
    vi.stubGlobal('fetch', fetchMock)
    await expect(subscribe(BODY)).rejects.toThrow('blog: no gateway URL')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('confirmSubscription', () => {
  const json = (body: string) => () => new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } })

  it('subscribeApi_confirmReadsUnsubscribedEverywhere', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw/')
    for (const [body, want] of [
      ['{"status":"confirmed","unsubscribed_everywhere":true}', true],
      ['{"status":"confirmed","unsubscribed_everywhere":false}', false],
      ['{"status":"confirmed"}', false],
      ['{"unsubscribed_everywhere":"true"}', false],
    ] as const) {
      const fetchMock = vi.fn<typeof fetch>(async () => json(body)())
      vi.stubGlobal('fetch', fetchMock)
      expect(await confirmSubscription('tok'), body).toEqual({ unsubscribedEverywhere: want })
      const [url, init] = fetchMock.mock.calls[0]
      expect(String(url)).toBe('https://gw/blog/subscriptions/confirm')
      expect(init?.method).toBe('POST')
      expect(JSON.parse(String(init?.body))).toEqual({ token: 'tok' })
    }
  })

  it('subscribeApi_confirmNoGatewayRejectsWithoutFetch', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn<typeof fetch>()
    vi.stubGlobal('fetch', fetchMock)
    await expect(confirmSubscription('tok')).rejects.toThrow('blog: no gateway URL')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
