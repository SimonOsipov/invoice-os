import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { clearConsoleSession, isStaffToken, loadConsoleSession, parseStoredConsoleSession, saveConsoleSession } from './session'
import { b64url, installStorage, jwt, OPS_KEY, SUPPORT_KEY, throwingStorage } from './testkit'

let warn: ReturnType<typeof vi.spyOn>
let error: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  error = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('isStaffToken (AC-1)', () => {
  it('isStaffToken_isTrueOnlyForBooleanTrue', () => {
    // '>>>???' encodes with '+' and '/' in plain base64, so this row needs the url-safe decode.
    const urlSafe = jwt({ app_metadata: { staff: true }, note: '>>>???~~~' })
    expect(urlSafe.split('.')[1]).toMatch(/[-_]/)
    // Standard base64 with its '=' padding kept: the decode must not reject it.
    const padded = `${b64url({ alg: 'RS256' })}.${btoa(JSON.stringify({ app_metadata: { staff: true }, n: 'ab' }))}.c2ln`
    expect(padded.split('.')[1]).toMatch(/=$/)
    const rows: [string, string | null, boolean][] = [
      ['staff true', jwt({ app_metadata: { staff: true } }), true],
      ['staff true, padded payload', padded, true],
      ['staff true, url-safe payload', urlSafe, true],
      ['staff "true"', jwt({ app_metadata: { staff: 'true' } }), false],
      ['staff 1', jwt({ app_metadata: { staff: 1 } }), false],
      ['staff false', jwt({ app_metadata: { staff: false } }), false],
      ['staff absent', jwt({ app_metadata: { tenant_id: 't' } }), false],
      ['app_metadata absent', jwt({ sub: 'u' }), false],
      ['app_metadata null', jwt({ app_metadata: null }), false],
      ['app_metadata a string', jwt({ app_metadata: 'x' }), false],
      ['app_metadata an array', jwt({ app_metadata: [] }), false],
      ['top-level staff', jwt({ staff: true }), false],
      ['user_metadata staff', jwt({ user_metadata: { staff: true } }), false],
      ['staff nested one level deeper', jwt({ app_metadata: { staff: { staff: true } } }), false],
      ['staff in an array', jwt({ app_metadata: { staff: [true] } }), false],
      ['payload is an array', 'e30.W10.c2ln', false],
      ['payload is JSON null', `e30.${btoa('null')}.c2ln`, false],
      ['payload is a JSON string', `e30.${btoa('"staff"')}.c2ln`, false],
      ['payload is not JSON', `e30.${btoa('not json')}.c2ln`, false],
      ['payload is empty', 'e30..c2ln', false],
      ['four parts', `${jwt({ app_metadata: { staff: true } })}.extra`, false],
      ['two parts', jwt({ app_metadata: { staff: true } }).split('.').slice(0, 2).join('.'), false],
      ['empty string', '', false],
      ['not a JWT', 'garbage', false],
      ['undecodable payload', 'aaa.!!!.ccc', false],
      ['null', null, false],
    ]
    expect(rows.filter(([, , want]) => want).length).toBeGreaterThan(0)
    for (const [name, token, want] of rows) {
      expect(isStaffToken(token), name).toBe(want)
    }
  })
})

describe('parseStoredConsoleSession (AC-2)', () => {
  it('parseStoredConsoleSession_acceptsOnlyV2', () => {
    const rows: [string, string | null, unknown, number][] = [
      ['absent', null, null, 0],
      ['v1 operator record', JSON.stringify({ v: 1, operator: 'developer' }), null, 1],
      ['v2 good', JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }), { token: 'T', refreshToken: 'R' }, 0],
      ['v2 missing refresh', JSON.stringify({ v: 2, token: 'T' }), null, 1],
      ['v2 missing token', JSON.stringify({ v: 2, refresh_token: 'R' }), null, 1],
      ['corrupt JSON', '{', null, 1],
    ]
    for (const [name, raw, want, warns] of rows) {
      warn.mockClear()
      expect(parseStoredConsoleSession(raw, OPS_KEY), name).toEqual(want)
      expect(warn, name).toHaveBeenCalledTimes(warns)
    }
    // Not two non-empty strings: refused, one warn each.
    for (const raw of [
      JSON.stringify({ v: 2, token: '', refresh_token: 'R' }),
      JSON.stringify({ v: 2, token: 'T', refresh_token: '' }),
      JSON.stringify({ v: 2, token: 1, refresh_token: 'R' }),
      JSON.stringify({ v: 2, token: 'T', refresh_token: ['R'] }),
      JSON.stringify({ v: 3, token: 'T', refresh_token: 'R' }),
      JSON.stringify({ v: 1, token: 'T', refresh_token: 'R' }),
      JSON.stringify({ v: '2', token: 'T', refresh_token: 'R' }),
      JSON.stringify({ v: 2, token: 'T', refresh_token: null }),
      JSON.stringify({ token: 'T', refresh_token: 'R' }),
      'null',
      '[]',
      '"v2"',
      '{}',
    ]) {
      warn.mockClear()
      expect(parseStoredConsoleSession(raw, OPS_KEY), raw).toBeNull()
      expect(warn, raw).toHaveBeenCalledTimes(1)
    }
    expect(error).not.toHaveBeenCalled()
  })
})

describe('console session storage (AC-15)', () => {
  it('consoleSession_storageThrowsNeverEscapes', () => {
    const ok = installStorage()
    saveConsoleSession(OPS_KEY, { token: 'T', refreshToken: 'R' })
    expect(JSON.parse(ok.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: 'T', refresh_token: 'R' })
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: 'T', refreshToken: 'R' })
    clearConsoleSession(OPS_KEY)
    expect(ok.local.getItem(OPS_KEY)).toBeNull()
    expect(warn).not.toHaveBeenCalled()

    vi.stubGlobal('localStorage', throwingStorage())
    expect(loadConsoleSession(OPS_KEY)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    warn.mockClear()
    expect(() => saveConsoleSession(OPS_KEY, { token: 'T', refreshToken: 'R' })).not.toThrow()
    expect(warn).toHaveBeenCalledTimes(1)
    warn.mockClear()
    expect(() => clearConsoleSession(OPS_KEY)).not.toThrow()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(error).not.toHaveBeenCalled()
  })
})

describe('console session keys (AC-15)', () => {
  it('consoleSession_keysAreIsolated', () => {
    const { local } = installStorage()
    saveConsoleSession(OPS_KEY, { token: 'T-ops', refreshToken: 'R-ops' })
    expect(loadConsoleSession(SUPPORT_KEY)).toBeNull()
    saveConsoleSession(SUPPORT_KEY, { token: 'T-sup', refreshToken: 'R-sup' })
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: 'T-ops', refreshToken: 'R-ops' })
    expect(loadConsoleSession(SUPPORT_KEY)).toEqual({ token: 'T-sup', refreshToken: 'R-sup' })
    clearConsoleSession(SUPPORT_KEY)
    expect(local.getItem(SUPPORT_KEY)).toBeNull()
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: 'T-ops', refreshToken: 'R-ops' })
    clearConsoleSession(OPS_KEY)
    expect(local.getItem(OPS_KEY)).toBeNull()
  })
})
