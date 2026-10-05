// The sign-in hand-off over the deployed gateway: sign-in yields a single-use code bound to a
// state, the exchange redeems it once for an access and a refresh token, a refresh renews both,
// sign-out revokes every session of the account, each route answers its caller's preflight,
// sign-in is throttled per address, and the session check's cost is measured.
// Forks auto-confirm, so a fresh registration signs in at once.
import { test, expect } from '@playwright/test'
import { claimsOf, exchangeCode, mintSignInState, provisionRealAccount, rawFetch, registerFresh, signInForCode, signInSession, subjectOf } from './client'
import { assertErrorEnvelope } from './contract-helpers'
import { resolveTarget } from '../targets'

// internal/gateway/signin.go: the refusals this spec pins.
const INVALID_CREDENTIALS = 'invalid email or password'
const STATE_REQUIRED = 'state is required'
const INVALID_CODE = 'invalid or expired code'
const TOO_MANY = 'too many requests'
// internal/gateway/refresh.go: RefreshHandler's refusals.
const INVALID_REFRESH = 'invalid or expired refresh token'
const REFRESH_REQUIRED = 'refresh_token is required'
// internal/gateway/gateway.go router: authorize's refusal of a token with no tenant.
const FORBIDDEN = 'forbidden'
// internal/gateway/session_check.go SessionChecker.Middleware: a revoked session.
const UNAUTHORIZED = 'unauthorized'

// internal/gateway/signin_throttle.go SignInMaxFailures.
const THROTTLE_LIMIT = 10

const CODE_RE = /^[A-Za-z0-9_-]{43}$/
const JWT_RE = /^eyJ[\w-]+\.[\w-]+\.[\w-]+$/

function errorOf(res: { body: unknown }): string {
  return (res.body as { error: string }).error
}

function getMe(token: string) {
  return rawFetch('/api/tenancy/v1/me', { headers: { Authorization: `Bearer ${token}` } })
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b)
  const mid = sorted.length >> 1
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2
}

test.describe('sign-in hand-off (API E2E, over the deployed gateway)', () => {
  test('sign-in yields a code, and the code redeems exactly once', async () => {
    const { email, password } = await registerFresh()
    const state = mintSignInState()

    const wrong = await rawFetch('/auth/sign-in', { method: 'POST', body: { email, password: `${password}x`, state } })
    assertErrorEnvelope(wrong, 401, 'wrong password')
    expect(errorOf(wrong)).toBe(INVALID_CREDENTIALS)

    const stateless = await rawFetch('/auth/sign-in', { method: 'POST', body: { email, password } })
    assertErrorEnvelope(stateless, 400, 'no state')
    expect(errorOf(stateless)).toBe(STATE_REQUIRED)

    const signIn = await rawFetch('/auth/sign-in', { method: 'POST', body: { email, password, state } })
    expect(signIn.status, JSON.stringify(signIn.body)).toBe(200)
    expect(Object.keys(signIn.body as object), 'the sign-in answer carries the code only').toEqual(['code'])
    const { code } = signIn.body as { code: string }
    expect(code).toMatch(CODE_RE)

    const first = await rawFetch('/auth/exchange', { method: 'POST', body: { code, state } })
    expect(first.status, JSON.stringify(first.body)).toBe(200)
    expect((first.body as { access_token: string }).access_token).toMatch(JWT_RE)

    const second = await rawFetch('/auth/exchange', { method: 'POST', body: { code, state } })
    assertErrorEnvelope(second, 400, 'second redemption')
    expect(errorOf(second)).toBe(INVALID_CODE)
  })

  test('a code redeems only with its own state, and a wrong state spends it', async () => {
    const { email, password } = await registerFresh()
    const state = mintSignInState()

    // Positive control: this account and state redeem a code of their own.
    expect(await exchangeCode(await signInForCode(email, password, state), state)).toMatch(JWT_RE)

    const code = await signInForCode(email, password, state)
    const other = await rawFetch('/auth/exchange', { method: 'POST', body: { code, state: mintSignInState() } })
    assertErrorEnvelope(other, 400, 'another state')
    expect(errorOf(other)).toBe(INVALID_CODE)

    const right = await rawFetch('/auth/exchange', { method: 'POST', body: { code, state } })
    assertErrorEnvelope(right, 400, 'the right state after a wrong one')
    expect(errorOf(right)).toBe(INVALID_CODE)
  })

  test('the exchange answers a refresh token, and a refresh renews it', async ({ request }) => {
    const { email, password } = await registerFresh()
    const state = mintSignInState()

    const exchange = await rawFetch('/auth/exchange', { method: 'POST', body: { code: await signInForCode(email, password, state), state } })
    expect(exchange.status, JSON.stringify(exchange.body)).toBe(200)
    expect(Object.keys(exchange.body as object).sort(), 'the exchange answer keys').toEqual(['access_token', 'refresh_token'])
    const first = exchange.body as { access_token: string; refresh_token: string }
    expect(first.access_token).toMatch(JWT_RE)
    expect(first.refresh_token, 'the exchange refresh token').not.toBe('')

    const refresh = (body: unknown) => request.post(`${resolveTarget('GATEWAY_URL')}/auth/refresh`, { data: body })

    const renewed = await refresh({ refresh_token: first.refresh_token })
    const next = (await renewed.json()) as { access_token: string; refresh_token: string }
    expect(renewed.status(), JSON.stringify(next)).toBe(200)
    expect(renewed.headers()['cache-control'], 'the refresh answer is cacheable').toBe('no-store')
    expect(Object.keys(next).sort(), 'the refresh answer keys').toEqual(['access_token', 'refresh_token'])
    expect(next.access_token).toMatch(JWT_RE)
    expect(next.access_token, 'the refresh returned the same access token').not.toBe(first.access_token)
    expect(next.refresh_token, 'the refresh token did not rotate').not.toBe(first.refresh_token)
    expect(next.refresh_token).not.toBe('')

    // The account has no workspace: authorize's 403 means the verifier accepted the renewed token.
    const me = await rawFetch('/api/tenancy/v1/me', { headers: { Authorization: `Bearer ${next.access_token}` } })
    expect([me.status, me.body], 'the renewed token on /me').toEqual([403, { error: FORBIDDEN }])

    const unknown = await refresh({ refresh_token: 'aaaaaaaaaaaa' })
    expect([unknown.status(), await unknown.json()], 'an unknown refresh token').toEqual([401, { error: INVALID_REFRESH }])
    expect(unknown.headers()['cache-control'], 'the 401 is cacheable').toBe('no-store')

    const empty = await refresh({})
    expect([empty.status(), await empty.json()], 'no refresh token').toEqual([400, { error: REFRESH_REQUIRED }])
  })

  test('sign-out revokes every session of the account', async ({ request }) => {
    const { email, password } = await registerFresh()
    const one = await signInSession(email, password)
    const two = await signInSession(email, password)
    const post = (path: string, body: unknown) => request.post(`${resolveTarget('GATEWAY_URL')}${path}`, { data: body })

    // The account has no workspace: 403 is authorize's answer to a live session.
    for (const [name, session] of [['first', one], ['second', two]] as const) {
      const live = await getMe(session.access_token)
      expect([live.status, live.body], `the ${name} access token before sign-out`).toEqual([403, { error: FORBIDDEN }])
    }

    const signOut = await post('/auth/sign-out', { refresh_token: one.refresh_token })
    expect(signOut.status(), await signOut.text()).toBe(204)
    expect(await signOut.text(), 'the 204 carries a body').toBe('')
    expect(signOut.headers()['cache-control'], 'the sign-out answer is cacheable').toBe('no-store')

    // Refused at the edge, before authorization could answer 403.
    for (const [name, session] of [['first', one], ['second', two]] as const) {
      const me = await getMe(session.access_token)
      expect([me.status, me.body], `the ${name} access token after sign-out`).toEqual([401, { error: UNAUTHORIZED }])
      const renewed = await post('/auth/refresh', { refresh_token: session.refresh_token })
      expect([renewed.status(), await renewed.json()], `the ${name} refresh token after sign-out`).toEqual([401, { error: INVALID_REFRESH }])
    }

    // internal/gateway/signout.go SignOutHandler: the same refusals as RefreshHandler.
    const empty = await post('/auth/sign-out', {})
    expect([empty.status(), await empty.json()], 'sign-out with no refresh token').toEqual([400, { error: REFRESH_REQUIRED }])
    const unknown = await post('/auth/sign-out', { refresh_token: 'aaaaaaaaaaaa' })
    expect([unknown.status(), await unknown.json()], 'sign-out with an unknown refresh token').toEqual([401, { error: INVALID_REFRESH }])
  })

  test('a provisioned account has no staff claim; the mock grant puts it on the next sign-in and the next refresh', async () => {
    const { email, password } = await provisionRealAccount('staff-claim')
    const before = await signInSession(email, password)
    const beforeMeta = claimsOf(before.access_token).app_metadata as { tenant_id?: unknown; staff?: unknown } | undefined
    // Positive control: this token carries the tenant claim, so an absent staff claim is not a failed read.
    expect(beforeMeta?.tenant_id, 'the tenant claim on a provisioned account').toBeTruthy()
    expect(beforeMeta?.staff, 'staff on a customer token').toBeUndefined()

    const grant = await rawFetch('/auth/mock/staff', { method: 'POST', body: { user_id: subjectOf(before.access_token) } })
    expect(grant.status, JSON.stringify(grant.body)).toBe(204)

    const after = await signInSession(email, password)
    expect((claimsOf(after.access_token).app_metadata as { staff?: unknown }).staff, 'staff on a sign-in after the grant').toBe(true)

    const renewed = await rawFetch('/auth/refresh', { method: 'POST', body: { refresh_token: before.refresh_token } })
    expect(renewed.status, JSON.stringify(renewed.body)).toBe(200)
    const renewedToken = (renewed.body as { access_token: string }).access_token
    expect((claimsOf(renewedToken).app_metadata as { staff?: unknown }).staff, 'staff on a refresh of the earlier session').toBe(true)
  })

  // The first call after sign-in misses the session cache, the second hits it. Timings are
  // attached, not asserted: a latency bound would flake.
  test("the session check's deployed cost", async () => {
    const { email, password } = await registerFresh()
    const pairs: { miss_ms: number; hit_ms: number }[] = []
    for (let i = 0; i < 10; i++) {
      const { access_token } = await signInSession(email, password)
      const times: number[] = []
      for (const call of ['miss', 'hit']) {
        const started = performance.now()
        const me = await getMe(access_token)
        times.push(performance.now() - started)
        expect([me.status, me.body], `session ${i + 1} ${call}`).toEqual([403, { error: FORBIDDEN }])
      }
      pairs.push({ miss_ms: times[0], hit_ms: times[1] })
    }
    const medianMiss = median(pairs.map((p) => p.miss_ms))
    const medianHit = median(pairs.map((p) => p.hit_ms))
    await test.info().attach('session-check-cost', {
      contentType: 'application/json',
      body: JSON.stringify({ pairs, median_miss_ms: medianMiss, median_hit_ms: medianHit, median_difference_ms: medianMiss - medianHit }, null, 2),
    })
  })

  test("each hand-off route answers its caller's preflight with a grant, never 405", async ({ request }) => {
    const landing = new URL(resolveTarget('LANDING_URL')).origin
    const app = new URL(resolveTarget('APP_URL')).origin
    const routes: [string, string][] = [
      ['/auth/sign-in', landing],
      // The app redeems the code (redeemHandoff), so the exchange's caller is the app.
      ['/auth/exchange', app],
      ['/auth/refresh', app],
      ['/auth/sign-out', app],
    ]
    for (const [path, origin] of routes) {
      const res = await request.fetch(`${resolveTarget('GATEWAY_URL')}${path}`, {
        method: 'OPTIONS',
        headers: { Origin: origin, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'content-type' },
      })
      expect(res.status(), `${path} preflight from ${origin}`).toBe(204)
      expect(res.headers()['access-control-allow-origin'], `${path} grant`).toBe(origin)
    }
  })

  test('the eleventh wrong password for one address answers 429', async () => {
    // Minted here, so a retry throttles a new address.
    const email = `handoff-throttle-${crypto.randomUUID()}@example.com`
    const body = { email, password: 'not-the-password', state: mintSignInState() }

    for (let i = 1; i <= THROTTLE_LIMIT; i++) {
      const res = await rawFetch('/auth/sign-in', { method: 'POST', body })
      assertErrorEnvelope(res, 401, `attempt ${i}`)
    }
    const over = await rawFetch('/auth/sign-in', { method: 'POST', body })
    assertErrorEnvelope(over, 429, `attempt ${THROTTLE_LIMIT + 1}`)
    expect(errorOf(over)).toBe(TOO_MANY)
  })
})
