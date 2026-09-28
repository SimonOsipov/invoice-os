// The sign-in hand-off over the deployed gateway: sign-in yields a single-use code
// bound to a state, the exchange redeems it once for an access and a refresh token, a refresh
// renews both, each route answers its caller's preflight, and sign-in is throttled per address.
// Forks auto-confirm, so a fresh registration signs in at once.
import { test, expect } from '@playwright/test'
import { exchangeCode, mintSignInState, rawFetch, signInForCode } from './client'
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

// internal/gateway/signin_throttle.go SignInMaxFailures.
const THROTTLE_LIMIT = 10

const CODE_RE = /^[A-Za-z0-9_-]{43}$/
const JWT_RE = /^eyJ[\w-]+\.[\w-]+\.[\w-]+$/

async function registerFresh(): Promise<{ email: string; password: string }> {
  const id = crypto.randomUUID()
  const account = { email: `handoff-${id}@example.com`, password: id.slice(0, 16) }
  const res = await rawFetch('/auth/register', { method: 'POST', body: account })
  expect(res.status, JSON.stringify(res.body)).toBe(202)
  return account
}

function errorOf(res: { body: unknown }): string {
  return (res.body as { error: string }).error
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

  test("each hand-off route answers its caller's preflight with a grant, never 405", async ({ request }) => {
    const landing = new URL(resolveTarget('LANDING_URL')).origin
    const app = new URL(resolveTarget('APP_URL')).origin
    const routes: [string, string][] = [
      ['/auth/sign-in', landing],
      ['/auth/exchange', landing],
      ['/auth/refresh', app],
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
