import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { clearConsoleSession, isStaffToken, loadConsoleSession, parseStoredConsoleSession, saveConsoleSession } from './session'
import { b64url, borrowedRaw, installStorage, jwt, OPS_KEY, recordRaw, SUPPORT_KEY, throwingStorage, v3BorrowedRaw, v3Raw } from './testkit'

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
  it('parseStoredConsoleSession_acceptsOnlyV3', () => {
    const rows: [string, string | null, unknown, number][] = [
      ['absent', null, null, 0],
      ['v1 operator record', JSON.stringify({ v: 1, operator: 'developer' }), null, 1],
      ['v3 good', JSON.stringify({ v: 3, token: 'T' }), { token: 'T', borrowed: false }, 0],
      ['v2 landing (the load reads it, the strict parse does not)', JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }), null, 1],
      ['v3 missing token', JSON.stringify({ v: 3 }), null, 1],
      ['corrupt JSON', '{', null, 1],
    ]
    for (const [name, raw, want, warns] of rows) {
      warn.mockClear()
      expect(parseStoredConsoleSession(raw, OPS_KEY), name).toEqual(want)
      expect(warn, name).toHaveBeenCalledTimes(warns)
    }
    // Not a non-empty string token on a v3 record: refused, one warn each.
    for (const raw of [
      JSON.stringify({ v: 3, token: '' }),
      JSON.stringify({ v: 3, token: 'T', refresh_token: '' }),
      JSON.stringify({ v: 3, token: 1 }),
      JSON.stringify({ v: 3, token: ['T'] }),
      JSON.stringify({ v: 3, token: 'T', refresh_token: 'R' }),
      JSON.stringify({ v: 1, token: 'T' }),
      JSON.stringify({ v: '3', token: 'T' }),
      JSON.stringify({ v: 3, token: 'T', refresh_token: null }),
      JSON.stringify({ token: 'T' }),
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
    saveConsoleSession(OPS_KEY, { token: 'T', borrowed: false })
    expect(JSON.parse(ok.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: 'T' })
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: 'T', borrowed: false })
    clearConsoleSession(OPS_KEY)
    expect(ok.local.getItem(OPS_KEY)).toBeNull()
    expect(warn).not.toHaveBeenCalled()

    vi.stubGlobal('localStorage', throwingStorage())
    expect(loadConsoleSession(OPS_KEY)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    warn.mockClear()
    expect(() => saveConsoleSession(OPS_KEY, { token: 'T', borrowed: false })).not.toThrow()
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
    saveConsoleSession(OPS_KEY, { token: 'T-ops', borrowed: false })
    expect(loadConsoleSession(SUPPORT_KEY)).toBeNull()
    saveConsoleSession(SUPPORT_KEY, { token: 'T-sup', borrowed: false })
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: 'T-ops', borrowed: false })
    expect(loadConsoleSession(SUPPORT_KEY)).toEqual({ token: 'T-sup', borrowed: false })
    clearConsoleSession(SUPPORT_KEY)
    expect(local.getItem(SUPPORT_KEY)).toBeNull()
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: 'T-ops', borrowed: false })
    clearConsoleSession(OPS_KEY)
    expect(local.getItem(OPS_KEY)).toBeNull()
  })
})

describe('borrowed record (SUPP-11-02)', () => {
  it('parse_acceptsTheBorrowedShape', () => {
    expect(parseStoredConsoleSession(JSON.stringify({ v: 3, token: 'T', from: 'app' }), OPS_KEY)).toEqual({ token: 'T', borrowed: true })
    expect(warn).not.toHaveBeenCalled()
  })

  it('parse_refusesMixedOrNullBorrowedShapes', () => {
    for (const rec of [
      { v: 3, token: 'T', refresh_token: null },
      { v: 3, token: 'T', refresh_token: 'R', from: 'app' },
      { v: 3, token: 'T', from: 'landing' },
      { v: 3, token: '', from: 'app' },
    ]) {
      warn.mockClear()
      expect(parseStoredConsoleSession(JSON.stringify(rec), OPS_KEY), JSON.stringify(rec)).toBeNull()
      expect(warn).toHaveBeenCalledTimes(1)
    }
  })

  it('save_writesTheBorrowedShape', () => {
    const { local } = installStorage()
    saveConsoleSession(OPS_KEY, { token: 'T', borrowed: true })
    expect(local.getItem(OPS_KEY)).toBe('{"v":3,"token":"T","from":"app"}')
  })
})

describe('v3 records (SECAUD-17-10)', () => {
  const T = 'T-access'
  const R = 'R-secret'

  it('save_writesOnlyTheAccessToken', () => {
    const { local } = installStorage()
    saveConsoleSession(OPS_KEY, { token: T, borrowed: false, legacyRefreshToken: R })
    expect(JSON.parse(local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: T })
    expect(local.getItem(OPS_KEY)).not.toContain(R)
    saveConsoleSession(OPS_KEY, { token: T, borrowed: true })
    expect(JSON.parse(local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 3, token: T, from: 'app' })
  })

  it('legacyLanding_loadLeavesTheRecordUntouched', () => {
    const { local } = installStorage({ local: { [OPS_KEY]: recordRaw(T, R) } })
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: T, borrowed: false, legacyRefreshToken: R })
    // [legacy-adopt-order]: the boot rewrites it after the adoption answers 200.
    expect(local.getItem(OPS_KEY)).toBe(recordRaw(T, R))
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: T, borrowed: false, legacyRefreshToken: R })
    expect(warn).not.toHaveBeenCalled()
  })

  it('legacyBorrowed_isRewrittenAsV3', () => {
    const { local } = installStorage({ local: { [OPS_KEY]: borrowedRaw(T) } })
    expect(loadConsoleSession(OPS_KEY)).toEqual({ token: T, borrowed: true })
    expect(local.getItem(OPS_KEY)).toBe(v3BorrowedRaw(T))
  })

  it('v3WithARefreshToken_isRemoved', () => {
    for (const rec of [
      { v: 3, token: T, refresh_token: R },
      { v: 3, token: T, from: 'app', refresh_token: R },
      { v: 3, token: T, refresh_token: null },
    ]) {
      warn.mockClear()
      const { local } = installStorage({ local: { [OPS_KEY]: JSON.stringify(rec) } })
      expect(loadConsoleSession(OPS_KEY), JSON.stringify(rec)).toBeNull()
      expect(warn, JSON.stringify(rec)).toHaveBeenCalledTimes(1)
      expect(local.getItem(OPS_KEY), JSON.stringify(rec)).toBeNull()
    }
  })

  it('parse_refusesOtherShapes', () => {
    // The two good shapes first: a refusal means nothing if nothing is accepted.
    expect(parseStoredConsoleSession(v3Raw(T), OPS_KEY)).toEqual({ token: T, borrowed: false })
    expect(parseStoredConsoleSession(v3BorrowedRaw(T), OPS_KEY)).toEqual({ token: T, borrowed: true })
    expect(warn).not.toHaveBeenCalled()
    for (const rec of [{ v: 3 }, { v: 3, token: '' }, { v: 3, token: T, from: 'x' }, { v: 4, token: T }, { v: 3, token: T, extra: 1 }, { v: 3, token: T, from: 'app', extra: 1 }]) {
      warn.mockClear()
      expect(parseStoredConsoleSession(JSON.stringify(rec), OPS_KEY), JSON.stringify(rec)).toBeNull()
      expect(warn, JSON.stringify(rec)).toHaveBeenCalledTimes(1)
    }
  })
})
