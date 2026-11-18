import vm from 'node:vm'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const exec = vi.hoisted(() => vi.fn((..._args: unknown[]): string => 'user-tok\n'))
vi.mock('node:child_process', () => ({ execFileSync: exec }))

import { CtlError } from './main'
import { installIapRoute, iapRouteScript, IAP_ORIGINS, isIapOrigin, mintIapToken } from './iap'

const TOK = 'tok-9f3a' // not a substring of any message word, so "body lacks the token" is decidable
const OPS = 'https://ops.ascomply.com'
const SUP = 'https://sup.ascomply.com'

const ADMITTED = [`${OPS}/`, `${SUP}/assets/x.js`, `${OPS}:443/`]
const REFUSED = [
  'http://ops.ascomply.com/',
  'https://ops.ascomply.com:8443/',
  'https://ops.ascomply.com.evil.example/',
  'https://xops.ascomply.com/',
  'https://evil.example/https://ops.ascomply.com',
  'https://api.ascomply.com/',
  'https://app.ascomply.com/',
  'https://www.ascomply.com/',
  'https://accounts.google.com/',
  'https://ops-console-pr-1-944095690109.africa-south1.run.app/',
]

type Matcher = (url: URL) => boolean
type Handler = (route: FakeRoute) => Promise<void>
interface FakeRoute {
  request: () => { url: () => string; headers: () => Record<string, string> }
  fetch: ReturnType<typeof vi.fn>
  fulfill: ReturnType<typeof vi.fn>
  abort: ReturnType<typeof vi.fn>
}

const reply = (status: number, headers: Record<string, string> = {}) => ({ status: () => status, headers: () => headers })
const fakeRoute = (url: string, headers: Record<string, string>, answer: unknown): FakeRoute => ({
  request: () => ({ url: () => url, headers: () => headers }),
  fetch: vi.fn(async () => {
    if (answer instanceof Error) throw answer
    return answer
  }),
  fulfill: vi.fn(async () => {}),
  abort: vi.fn(async () => {}),
})
const fakeContext = () => ({ route: vi.fn(async (_m: Matcher, _h: Handler) => {}) })
const recorded = (context: ReturnType<typeof fakeContext>) => {
  expect(context.route, 'the route is installed once').toHaveBeenCalledTimes(1)
  const [matcher, handler] = context.route.mock.calls[0] as [Matcher, Handler]
  return { matcher, handler }
}
// Runs the handler on one request and returns the route it ran on.
async function run(handler: Handler, url: string, answer: unknown, headers: Record<string, string> = {}) {
  const route = fakeRoute(url, headers, answer)
  await handler(route)
  return route
}

// The route's behaviour, asserted the same way on the module function and on the serialised script.
async function expectRouteBehaviour({ matcher, handler }: { matcher: Matcher; handler: Handler }) {
  for (const u of ADMITTED) expect(matcher(new URL(u)), u).toBe(true)
  for (const u of REFUSED) expect(matcher(new URL(u)), u).toBe(false)

  const ok = reply(200)
  const sent = await run(handler, `${OPS}/assets/a.js`, ok, { accept: 'x', 'x-k': 'v' })
  expect(sent.fetch).toHaveBeenCalledTimes(1)
  expect(sent.fetch).toHaveBeenCalledWith({ headers: { accept: 'x', 'x-k': 'v', authorization: `Bearer ${TOK}` }, maxRedirects: 0 })
  expect(sent.fulfill).toHaveBeenCalledWith({ response: ok })

  const off = await run(handler, `${OPS}/`, reply(302, { location: 'https://accounts.google.com/o/oauth2/v2/auth?x' }))
  expect(off.fulfill).toHaveBeenCalledTimes(1)
  expect(off.fulfill.mock.calls[0]![0]).toMatchObject({ status: 401, contentType: 'text/plain' })

  const same = reply(302, { location: '/x/' })
  expect((await run(handler, `${OPS}/`, same)).fulfill).toHaveBeenCalledWith({ response: same })
  const bad = reply(502)
  expect((await run(handler, `${SUP}/`, bad)).fulfill).toHaveBeenCalledWith({ response: bad })
}

describe('isIapOrigin', () => {
  it('admits the two console origins', () => {
    expect(ADMITTED.length).toBeGreaterThan(0)
    for (const u of ADMITTED) expect(isIapOrigin(new URL(u)), u).toBe(true)
  })

  it('refuses every other origin', () => {
    expect(REFUSED.length).toBeGreaterThan(0)
    expect(isIapOrigin(new URL(ADMITTED[0]!)), 'the guard is not simply closed').toBe(true)
    for (const u of REFUSED) expect(isIapOrigin(new URL(u)), u).toBe(false)
  })
})

describe('installIapRoute', () => {
  it('the route matcher is isIapOrigin\'s table', async () => {
    const context = fakeContext()
    await installIapRoute(context as never, TOK, IAP_ORIGINS)
    const { matcher } = recorded(context)
    expect(IAP_ORIGINS.length).toBeGreaterThan(0)
    for (const u of ADMITTED) expect(matcher(new URL(u)), u).toBe(true)
    for (const u of REFUSED) expect(matcher(new URL(u)), u).toBe(false)
  })

  it('a console request carries the bearer and keeps its headers', async () => {
    const context = fakeContext()
    await installIapRoute(context as never, TOK, IAP_ORIGINS)
    const { handler } = recorded(context)
    const res = reply(200)

    const route = await run(handler, `${OPS}/assets/a.js`, res, { accept: 'x', 'x-k': 'v' })

    expect(route.fetch).toHaveBeenCalledTimes(1)
    expect(route.fetch).toHaveBeenCalledWith({ headers: { accept: 'x', 'x-k': 'v', authorization: `Bearer ${TOK}` }, maxRedirects: 0 })
    expect(route.fulfill).toHaveBeenCalledWith({ response: res })
  })

  it('a redirect off the origin is a 401 and is never followed', async () => {
    const context = fakeContext()
    await installIapRoute(context as never, TOK, IAP_ORIGINS)
    const { handler } = recorded(context)
    const off = [
      [`${OPS}/`, 'https://accounts.google.com/o/oauth2/v2/auth?x'],
      [`${OPS}/`, `${SUP}/y`], // the other console is another origin too
      [`${OPS}/`, 'http://ops.ascomply.com/'], // a scheme downgrade is another origin
    ]
    for (const [url, location] of off) {
      const route = await run(handler, url!, reply(302, { location: location! }))

      expect(route.fetch, location).toHaveBeenCalledTimes(1)
      expect(route.fulfill, location).toHaveBeenCalledTimes(1)
      const arg = route.fulfill.mock.calls[0]![0] as { status: number; contentType: string; body: string }
      expect(arg).toMatchObject({ status: 401, contentType: 'text/plain' })
      expect(arg.body).toContain('https://ops.ascomply.com')
      expect(arg.body).toContain('ctl login')
      expect(arg.body).not.toContain(TOK)
    }
  })

  it('a relative or same-origin redirect passes through', async () => {
    const context = fakeContext()
    await installIapRoute(context as never, TOK, IAP_ORIGINS)
    const { handler } = recorded(context)
    const relative = reply(302, { location: '/x/' })
    const sameOrigin = reply(301, { location: `${SUP}/y` })

    expect((await run(handler, `${OPS}/`, relative)).fulfill).toHaveBeenCalledWith({ response: relative })
    expect((await run(handler, `${SUP}/`, sameOrigin)).fulfill).toHaveBeenCalledWith({ response: sameOrigin })
  })

  it('a 4xx and a 5xx pass through unchanged', async () => {
    const context = fakeContext()
    await installIapRoute(context as never, TOK, IAP_ORIGINS)
    const { handler } = recorded(context)
    for (const status of [403, 502]) {
      const res = reply(status)
      const route = await run(handler, `${OPS}/`, res)
      expect(route.fulfill, String(status)).toHaveBeenCalledTimes(1)
      expect(route.fulfill, String(status)).toHaveBeenCalledWith({ response: res })
    }
  })

  it('a fetch error aborts', async () => {
    const context = fakeContext()
    await installIapRoute(context as never, TOK, IAP_ORIGINS)
    const { handler } = recorded(context)

    const route = await run(handler, `${OPS}/`, new Error('net::ERR_CONNECTION_RESET'))

    expect(route.abort).toHaveBeenCalledTimes(1)
    expect(route.abort).toHaveBeenCalledWith('failed')
    expect(route.fulfill).not.toHaveBeenCalled()
  })
})

describe('iapRouteScript', () => {
  it('the script installs the same route in a bare scope', async () => {
    const install = vm.runInNewContext(`(${iapRouteScript(TOK)})`, { URL }) as (page: unknown) => Promise<void>
    const context = fakeContext()

    await install({ context: () => context })

    await expectRouteBehaviour(recorded(context))
  })

  it('the script holds the token once and no module syntax', () => {
    const script = iapRouteScript('t"k')

    expect(script.split(JSON.stringify('t"k'))).toHaveLength(2)
    expect(script).toContain(OPS)
    expect(script).toContain(SUP)
    expect(script).not.toMatch(/\b(import|export|require|process)\b/)
  })
})

describe('mintIapToken', () => {
  const MINT_URL = 'https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/ctl-console@ascomply-prod.iam.gserviceaccount.com:generateAccessToken'
  const fetchMock = vi.fn()
  const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status })
  const failure = (): Promise<unknown> => mintIapToken().then(() => undefined, (e: unknown) => e)
  const expectCtlError = (err: unknown): CtlError => {
    expect(err).toBeInstanceOf(CtlError)
    expect((err as CtlError).code).toBe(1)
    return err as CtlError
  }

  beforeEach(() => {
    exec.mockReset()
    exec.mockReturnValue('user-tok\n')
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('the mint sends the user\'s token to iamcredentials only', async () => {
    fetchMock.mockResolvedValue(json(200, { accessToken: 'ya29.x', expireTime: '2026-11-18T12:00:00Z' }))

    const out = await mintIapToken()

    expect(exec).toHaveBeenCalledTimes(1)
    expect(exec.mock.calls[0]!.slice(0, 2)).toEqual(['gcloud', ['auth', 'print-access-token']])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, { method: string; headers: HeadersInit; body: string }]
    expect(url).toBe(MINT_URL)
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('authorization')).toBe('Bearer user-tok')
    expect(headers.get('x-goog-user-project')).toBe('ascomply-prod')
    expect(JSON.parse(init.body)).toEqual({ scope: ['openid', 'https://www.googleapis.com/auth/userinfo.email'], lifetime: '3600s' })
    expect(out).toEqual({ token: 'ya29.x', expiresAt: '2026-11-18T12:00:00Z' })
  })

  it('no gcloud token fails and names gcloud', async () => {
    for (const give of [() => { throw new Error('spawn gcloud ENOENT\nsecond line') }, () => '', () => '  \n']) {
      exec.mockReset()
      exec.mockImplementation(give)
      const err = expectCtlError(await failure())

      expect(err.message).toContain('gcloud auth print-access-token')
      expect(err.hint).toContain('gcloud auth login')
      expect(err.hint).toContain('SECAUD-35-05')
      expect(err.message).not.toContain('second line')
      expect(fetchMock).not.toHaveBeenCalled()
    }
  })

  it('a 403 names the account and the grant', async () => {
    fetchMock.mockResolvedValue(json(403, { error: { message: 'denied' } }))

    const err = expectCtlError(await failure())

    expect(err.message).toContain('ctl-console@ascomply-prod.iam.gserviceaccount.com')
    expect(err.message).toContain('403')
    expect(err.hint).toContain('roles/iam.serviceAccountTokenCreator')
    expect(err.hint).toContain('SECAUD-35-05')
    expect(`${err.message} ${err.hint}`).not.toContain('user-tok')
  })

  it('another status and a body without a token fail', async () => {
    fetchMock.mockResolvedValueOnce(json(500, {}))
    const other = expectCtlError(await failure())
    expect(other.message).toContain('answered 500')
    expect(other.hint).toContain('gcloud auth list')

    for (const body of [{}, { accessToken: 'ya29.x' }, { expireTime: '2026-11-18T12:00:00Z' }, { accessToken: 7, expireTime: 'x' }]) {
      fetchMock.mockResolvedValueOnce(json(200, body))
      const err = expectCtlError(await failure())
      expect(err.message, JSON.stringify(body)).toContain('without a token')
    }
    expect(`${other.message} ${other.hint}`).not.toContain('user-tok')
  })
})
