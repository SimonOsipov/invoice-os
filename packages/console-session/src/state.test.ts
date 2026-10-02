import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { consumeSignInState, ensureSignInState, landingSignInUrl, mintSignInState, readHandoffCode } from './state'
import { BASE64URL_43_RE, CODE, installStorage, LANDING, NOW, STATE_A, STATE_B, STATE_KEY, stateRaw, throwingStorage } from './testkit'

const TTL_MS = 600_000 // SIGN_IN_STATE_TTL_MS: 10 min (D24; frontend/app/src/lib/signInState.ts)
const REUSE_MS = 60_000 // SIGN_IN_STATE_REUSE_MS (D24; same file)

let warn: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('landingSignInUrl (AC-3)', () => {
  it('landingSignInUrl_carriesStateConsoleAndOptionalOutcome', () => {
    const rows: [Parameters<typeof landingSignInUrl>[2], Parameters<typeof landingSignInUrl>[3], string][] = [
      ['ops', undefined, `${LANDING}/?state=${STATE_A}&console=ops`],
      ['ops', 'ready', `${LANDING}/?state=${STATE_A}&console=ops&signin=ready`],
      ['ops', 'failed', `${LANDING}/?state=${STATE_A}&console=ops&signin=failed`],
      ['ops', 'not-staff', `${LANDING}/?state=${STATE_A}&console=ops&signin=not-staff`],
      ['support', undefined, `${LANDING}/?state=${STATE_A}&console=support`],
      ['support', 'ready', `${LANDING}/?state=${STATE_A}&console=support&signin=ready`],
      ['support', 'failed', `${LANDING}/?state=${STATE_A}&console=support&signin=failed`],
      ['support', 'not-staff', `${LANDING}/?state=${STATE_A}&console=support&signin=not-staff`],
    ]
    expect(rows).toHaveLength(8)
    for (const [target, outcome, want] of rows) {
      expect(landingSignInUrl(LANDING, STATE_A, target, outcome), `${target} ${outcome}`).toBe(want)
    }
    // A landing base with a path keeps it.
    expect(landingSignInUrl(`${LANDING}/base`, STATE_A, 'ops', 'ready')).toBe(`${LANDING}/base/?state=${STATE_A}&console=ops&signin=ready`)
  })
})

describe('ensureSignInState (AC-14)', () => {
  it('ensureSignInState_reusesUnderAMinuteOnly', () => {
    const t = NOW
    const { session } = installStorage({ session: { [STATE_KEY]: stateRaw(STATE_A, t) } })

    expect(ensureSignInState(t + REUSE_MS - 1)).toBe(STATE_A)
    expect(session.getItem(STATE_KEY)).toBe(stateRaw(STATE_A, t))

    const minted = ensureSignInState(t + REUSE_MS)
    expect(minted).toMatch(BASE64URL_43_RE)
    expect(minted).not.toBe(STATE_A)
    expect(session.getItem(STATE_KEY)).toBe(stateRaw(minted, t + REUSE_MS))

    // A state minted at this very instant is reusable.
    session.setItem(STATE_KEY, stateRaw(STATE_A, t))
    expect(ensureSignInState(t)).toBe(STATE_A)

    // A malformed held state is replaced, never reused.
    for (const raw of ['not json', JSON.stringify({ v: 2, s: STATE_A, at: t }), JSON.stringify({ v: 1, s: 'short', at: t }), 'null']) {
      session.setItem(STATE_KEY, raw)
      const fresh = ensureSignInState(t)
      expect(fresh, raw).toMatch(BASE64URL_43_RE)
      expect(session.getItem(STATE_KEY), raw).toBe(stateRaw(fresh, t))
    }

    // A held state minted in the future (clock moved back) is not reusable.
    session.setItem(STATE_KEY, stateRaw(STATE_B, t + 1))
    const again = ensureSignInState(t)
    expect(again).not.toBe(STATE_B)
    expect(again).toMatch(BASE64URL_43_RE)
  })
})

describe('consumeSignInState (AC-14)', () => {
  it('consumeSignInState_isOneShotAndBoundedByTheTtl', () => {
    const t = NOW
    const { session } = installStorage()
    const held = (raw: string | null) => {
      session.removeItem(STATE_KEY)
      if (raw !== null) session.setItem(STATE_KEY, raw)
    }

    held(stateRaw(STATE_A, t))
    expect(consumeSignInState(t)).toBe(STATE_A)
    expect(session.getItem(STATE_KEY)).toBeNull()

    held(stateRaw(STATE_A, t))
    expect(consumeSignInState(t + TTL_MS - 1)).toBe(STATE_A)
    expect(session.getItem(STATE_KEY)).toBeNull()
    expect(consumeSignInState(t + TTL_MS - 1)).toBeNull()

    const refused: [string, string][] = [
      ['at the TTL', stateRaw(STATE_A, t)],
      ['minted in the future', stateRaw(STATE_A, t + 1)],
      ['not JSON', 'not json'],
      ['wrong version', JSON.stringify({ v: 2, s: STATE_A, at: t })],
      ['state not 43 chars', stateRaw(STATE_A.slice(1), t)],
      ['state not base64url', stateRaw(`${STATE_A.slice(1)}+`, t)],
      ['at not a number', JSON.stringify({ v: 1, s: STATE_A, at: String(t) })],
      ['at missing', JSON.stringify({ v: 1, s: STATE_A })],
      ['JSON null', 'null'],
      ['at infinite', `{"v":1,"s":"${STATE_A}","at":1e999}`],
      ['at negative', stateRaw(STATE_A, -1)],
    ]
    for (const [name, raw] of refused) {
      held(raw)
      const now = name === 'at the TTL' ? t + TTL_MS : t
      expect(consumeSignInState(now), name).toBeNull()
      expect(session.getItem(STATE_KEY), `${name}: key removed`).toBeNull()
    }
    held(null)
    expect(consumeSignInState(t)).toBeNull()
  })
})

describe('mintSignInState (AC-5)', () => {
  it('mintSignInState_storesAFreshStateAtNowReplacingTheHeldOne', () => {
    const { session } = installStorage({ session: { [STATE_KEY]: stateRaw(STATE_A, NOW) } })
    const s = mintSignInState(NOW + 5)
    expect(s).toMatch(BASE64URL_43_RE)
    expect(s).not.toBe(STATE_A)
    expect(session.getItem(STATE_KEY)).toBe(stateRaw(s, NOW + 5))
    const seen = new Set([s])
    for (let i = 0; i < 200; i++) {
      const m = mintSignInState(NOW)
      expect(m).toMatch(BASE64URL_43_RE)
      seen.add(m)
    }
    expect(seen.size).toBe(201)
  })
})

describe('readHandoffCode (AC-7)', () => {
  it('readHandoffCode_acceptsExactlyOneWellFormedCode', () => {
    expect(CODE).toHaveLength(43)
    const rows: [string, string | null][] = [
      [`?handoff=${CODE}`, CODE],
      [`?x=1&handoff=${CODE}`, CODE],
      [`?handoff=${'_-'.repeat(21)}A`, `${'_-'.repeat(21)}A`],
      [`?handoff=${CODE.slice(1)}`, null],
      [`?handoff=${CODE}A`, null],
      [`?handoff=${CODE.slice(1)}%2B`, null],
      [`?handoff=${CODE.slice(1)}=`, null],
      [`?handoff=${CODE.slice(1)}%0A`, null],
      [`?handoff=${CODE}%20`, null],
      [`?HANDOFF=${CODE}`, null],
      [`?handoff[]=${CODE}`, null],
      [`?handoff=${CODE}&handoff=${STATE_A}`, null],
      [`?handoff=${STATE_A}&handoff=${CODE}`, null],
      ['?handoff=', null],
      ['?x=1', null],
      ['', null],
    ]
    for (const [search, want] of rows) {
      expect(readHandoffCode(search), search).toBe(want)
    }
  })
})

describe('sign-in state storage (AC-15)', () => {
  it('signInState_storageThrowsNeverEscapes', () => {
    const cases: [string, ReturnType<typeof throwingStorage>][] = [
      ['reads and writes throw', throwingStorage()],
      ['writes throw', throwingStorage({ reads: false })],
    ]
    for (const [name, store] of cases) {
      vi.stubGlobal('sessionStorage', store)
      warn.mockClear()
      let s = ''
      expect(() => (s = ensureSignInState(NOW)), `${name}: ensure`).not.toThrow()
      expect(s).toMatch(BASE64URL_43_RE)
      expect(warn, `${name}: ensure warns once`).toHaveBeenCalledTimes(1)

      warn.mockClear()
      let m = ''
      expect(() => (m = mintSignInState(NOW)), `${name}: mint`).not.toThrow()
      expect(m).toMatch(BASE64URL_43_RE)
      expect(warn, `${name}: mint warns once`).toHaveBeenCalledTimes(1)

      warn.mockClear()
      if (name === 'reads and writes throw') {
        expect(consumeSignInState(NOW), `${name}: consume`).toBeNull()
        expect(warn, `${name}: consume warns once`).toHaveBeenCalledTimes(1)
      }
    }
  })
})
