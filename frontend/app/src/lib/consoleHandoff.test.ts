import { afterEach, describe, expect, it, vi } from 'vitest'

import { consoleHandoffUrl, consoleStartUrl, readConsoleStart, requestConsoleCode } from './consoleHandoff'

it('consoleStartUrl_isTheConsoleAuthAppDoor', () => {
  expect(consoleStartUrl('https://sup.x')).toBe('https://sup.x/?auth=app')
})

const S = 'S'.repeat(43)

it('readConsoleStart_acceptsOnlyTheExactShape', () => {
  expect(readConsoleStart(`?auth=console&console=support&state=${S}`)).toBe(S)
  expect(readConsoleStart(`?state=${S}&console=support&auth=console&x=1`)).toBe(S)
  for (const q of [
    `?auth=console&console=ops&state=${S}`,
    `?auth=console&console=support&console=support&state=${S}`,
    `?auth=console&console=support&state=${S}&state=${S}`,
    `?auth=console&console=support&state=${S.slice(1)}`,
    `?auth=console&console=support&state=${S}A`,
    `?auth=console&console=support&state=${'+'.repeat(43)}`,
    `?auth=Console&console=support&state=${S}`,
    `?auth=console&auth=console&console=support&state=${S}`,
    `?auth=console&state=${S}`,
    `?auth=start&console=support&state=${S}`,
    '',
  ]) {
    expect(readConsoleStart(q), q).toBeNull()
  }
})

describe('requestConsoleCode_postsStateWithTheBearer', () => {
  afterEach(() => vi.unstubAllGlobals())
  const stub = (body: unknown) => {
    const f = vi.fn(() => Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body) }))
    vi.stubGlobal('fetch', f)
    return f
  }
  const CODE = 'c'.repeat(43)

  it('posts {state} with the bearer and returns the code', async () => {
    const f = stub({ code: CODE })
    await expect(requestConsoleCode('https://gw.x', 'tok', S)).resolves.toBe(CODE)
    expect(f).toHaveBeenCalledTimes(1)
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/console-handoff')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ state: S })
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer tok')
  })

  it.each([{}, { code: 'short' }, { code: 7 }, null, { code: 'c'.repeat(44) }, { code: `${'c'.repeat(42)}+` }])('throws malformed for %j', async (body) => {
    stub(body)
    await expect(requestConsoleCode('https://gw.x', 'tok', S)).rejects.toMatchObject({ kind: 'malformed' })
  })
})

it.each([401, 403, 500])('requestConsoleCode rejects a %i answer', async (status) => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status, statusText: 'x', json: () => Promise.resolve({ error: 'e' }) })))
  await expect(requestConsoleCode('https://gw.x', 'tok', S)).rejects.toBeDefined()
  vi.unstubAllGlobals()
})

it('requestConsoleCode rejects a network error', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
  await expect(requestConsoleCode('https://gw.x', 'tok', S)).rejects.toBeDefined()
  vi.unstubAllGlobals()
})

it('consoleHandoffUrl_carriesOnlyTheCode', () => {
  expect(consoleHandoffUrl('https://sup.x', 'a_b-c')).toBe('https://sup.x/?handoff=a_b-c')
})
