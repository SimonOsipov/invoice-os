// The sign-in hand-off over the deployed gateway (AUTH-05): sign-in yields a single-use code
// bound to a state, the exchange redeems it once, both routes answer the landing preflight, and
// sign-in is throttled per address. Forks auto-confirm, so a fresh registration signs in at once.
import { test, expect } from '@playwright/test'
import { exchangeCode, mintSignInState, rawFetch, signInForCode } from './client'
import { assertErrorEnvelope } from './contract-helpers'
import { resolveTarget } from '../targets'

// internal/gateway/signin.go: the refusals this spec pins.
const INVALID_CREDENTIALS = 'invalid email or password'
const STATE_REQUIRED = 'state is required'
const INVALID_CODE = 'invalid or expired code'
const TOO_MANY = 'too many requests'

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

  test('both routes answer the landing preflight with a grant, never 405', async ({ request }) => {
    const origin = new URL(resolveTarget('LANDING_URL')).origin
    for (const path of ['/auth/sign-in', '/auth/exchange']) {
      const res = await request.fetch(`${resolveTarget('GATEWAY_URL')}${path}`, {
        method: 'OPTIONS',
        headers: { Origin: origin, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'content-type' },
      })
      expect(res.status(), `${path} preflight`).toBe(204)
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
