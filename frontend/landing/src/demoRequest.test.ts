// The landing's demo-request client: POST /contacts/demo-request on the gateway. Wire contract: internal/gateway/contacts.go DemoRequestHandler.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import { MARKETING_CONSENT_TEXT } from './components/MarketingConsent'
import { DemoRateLimited, sendDemoRequest } from './demoRequest'
import type { DemoLead } from './hubspot'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const LEAD: DemoLead = {
  name: 'Ada Okafor',
  email: 'ada@okafor.ng',
  company: 'Okafor & Partners',
  role: 'Finance or Accounting lead',
  size: 'Medium ₦1bn–₦5bn',
  volume: '1k–10k',
  consent: true,
  marketing: false,
}

const accepted = () => new Response(JSON.stringify({ status: 'accepted' }), { status: 202, headers: { 'Content-Type': 'application/json' } })

function stubGateway(res: () => Response | Promise<Response> = accepted) {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
  const fetchMock = vi.fn<typeof fetch>(async () => res())
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function sentBody(fetchMock: ReturnType<typeof stubGateway>): Record<string, unknown> {
  const init = fetchMock.mock.calls[0]?.[1]
  return JSON.parse(String(init?.body))
}

describe('sendDemoRequest', () => {
  it('sendDemoRequest posts the four fields and the sentence only when ticked', async () => {
    const fetchMock = stubGateway()

    const unticked = sendDemoRequest({ ...LEAD, marketing: false })
    expect(unticked, 'control: a configured gateway returns a promise').not.toBeNull()
    await unticked
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(Object.keys(sentBody(fetchMock)).sort()).toEqual(['company', 'email', 'name'])
    expect(sentBody(fetchMock)).toEqual({ email: 'ada@okafor.ng', name: 'Ada Okafor', company: 'Okafor & Partners' })

    fetchMock.mockClear()
    await sendDemoRequest({ ...LEAD, marketing: true })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(Object.keys(sentBody(fetchMock)).sort()).toEqual(['company', 'email', 'marketing_consent_text', 'name'])
    expect(sentBody(fetchMock).marketing_consent_text).toBe(MARKETING_CONSENT_TEXT)
  })

  it('sends no key the gateway does not read: no role, size, volume, consent, marketing flag or honeypot', async () => {
    const fetchMock = stubGateway()
    await sendDemoRequest({ ...LEAD, marketing: true, website: 'https://spam.example' } as DemoLead)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const body = sentBody(fetchMock)
    for (const key of ['role', 'size', 'volume', 'consent', 'marketing', 'website']) {
      expect(body, key).not.toHaveProperty(key)
    }
    expect(Object.keys(body).sort()).toEqual(['company', 'email', 'marketing_consent_text', 'name'])
  })

  it('POSTs JSON to the gateway demo-request URL, trailing slash stripped, with no Authorization header', async () => {
    const fetchMock = stubGateway()
    await sendDemoRequest(LEAD)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('https://gw.x/contacts/demo-request')
    expect(init?.method).toBe('POST')
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.has('Authorization')).toBe(false)
    expect([...headers.keys()], 'the preflight grants exactly these request headers').toEqual(['content-type'])
  })

  it('sendDemoRequest is null without a gateway', async () => {
    const fetchMock = stubGateway()
    expect(sendDemoRequest(LEAD), 'control: the same call with a gateway is not null').not.toBeNull()
    await Promise.resolve()
    fetchMock.mockClear()

    for (const unset of [undefined, '', '   ']) {
      vi.stubEnv('VITE_GATEWAY_URL', unset)
      expect(sendDemoRequest(LEAD), `gateway ${JSON.stringify(unset)}`).toBeNull()
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('rejects with the gateway status on a 400 and on a 502', async () => {
    for (const [status, error] of [
      [400, 'email is invalid'],
      [502, 'demo request is unavailable'],
    ] as const) {
      stubGateway(() => new Response(JSON.stringify({ error }), { status, headers: { 'Content-Type': 'application/json' } }))
      const p = sendDemoRequest(LEAD)
      expect(p, `control: ${status} starts a request`).not.toBeNull()
      const err = await p!.then(
        () => null,
        (e: unknown) => e,
      )
      expect(err, `${status} rejects`).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(status)
    }
  })

  it('rejects when the network does', async () => {
    stubGateway(() => {
      throw new TypeError('Failed to fetch')
    })
    const p = sendDemoRequest(LEAD)
    expect(p, 'control: a configured gateway starts a request').not.toBeNull()
    await expect(p).rejects.toBeInstanceOf(ApiError)
  })

  // 429 is the gateway's http.StatusTooManyRequests (internal/gateway/contacts.go).
  it('sendDemoRequest rejects with DemoRateLimited on a 429', async () => {
    stubGateway(() => new Response(JSON.stringify({ error: 'too many requests' }), { status: 429, headers: { 'Content-Type': 'application/json' } }))
    const p = sendDemoRequest(LEAD)
    expect(p, 'control: a configured gateway starts a request').not.toBeNull()
    await expect(p).rejects.toBeInstanceOf(DemoRateLimited)
  })

  it('sendDemoRequest leaves other failures as ApiError', async () => {
    for (const status of [400, 502, 503]) {
      stubGateway(() => new Response(JSON.stringify({ error: 'x' }), { status, headers: { 'Content-Type': 'application/json' } }))
      const err = await sendDemoRequest(LEAD)!.then(
        () => null,
        (e: unknown) => e,
      )
      expect(err, `${status}`).toBeInstanceOf(ApiError)
      expect(err, `${status}`).not.toBeInstanceOf(DemoRateLimited)
      expect(err as ApiError).toMatchObject({ kind: 'http', status })
    }
    stubGateway(() => {
      throw new TypeError('Failed to fetch')
    })
    const err = await sendDemoRequest(LEAD)!.then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect(err).not.toBeInstanceOf(DemoRateLimited)
    expect(err as ApiError).toMatchObject({ kind: 'network' })
  })
})
