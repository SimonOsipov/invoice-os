// RED specs (M3-07-01, S1-S17) — pin the session.ts persistence contract before the
// executor implements the bodies. Mirrors the mocking style of
// packages/api-client/src/client.test.ts: vi.stubGlobal for localStorage,
// vi.spyOn(console, 'warn'/'error') for the no-error invariant, afterEach cleanup.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from '../auth'
import { memberInitials } from './members'
import { handoffPersona } from './sessionHandoff'
import {
  SESSION_KEY,
  SESSION_SCHEMA_VERSION,
  cardIdentity,
  clearSession,
  decodeJwtPayload,
  isHandoffMe,
  isTokenExpired,
  loadSession,
  parseStoredSession,
  resolveBootSession,
  saveSession,
  serializeSession,
} from './session'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function firmSession(): Session {
  return {
    persona: APP_PERSONAS.firm,
    token: 'jwt',
    me: {
      tenant: { id: '11111111-1111-1111-1111-111111111111', name: 'Okafor & Partners', kind: 'firm' },
      user: { id: 'c0000000-0000-0000-0000-000000000001', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
    },
    verified: true,
  }
}

function noGatewaySession(): Session {
  return { persona: APP_PERSONAS.firm, token: null, me: null, verified: false }
}

// S4-S7 share the "corrupt input degrades to null, warns, never errors" shape.
function spyOnConsole() {
  return {
    warn: vi.spyOn(console, 'warn').mockImplementation(() => {}),
    error: vi.spyOn(console, 'error').mockImplementation(() => {}),
  }
}

// In-memory fake used for the I/O round-trip specs (S9/S10) — a minimal stand-in
// for the browser Storage interface, keyed on whatever key the module passes.
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
  }
}

describe('serializeSession / parseStoredSession round-trip', () => {
  it('S1: round-trips a firm session, rebuilding persona as the same APP_PERSONAS reference', () => {
    const session = firmSession()

    const restored = parseStoredSession(serializeSession(session))

    expect(restored).toEqual(session)
    expect(restored?.persona).toBe(APP_PERSONAS.firm)
  })

  it('S2: serializes to the minimal persisted shape (persona stored by id only)', () => {
    const session = firmSession()

    const parsed = JSON.parse(serializeSession(session))

    expect(parsed).toEqual({
      v: 1,
      personaId: 'firm',
      token: 'jwt',
      me: session.me,
      verified: true,
    })
  })
})

describe('parseStoredSession corruption/version guards', () => {
  it('S3: returns null for an absent session without warning', () => {
    const { warn } = spyOnConsole()

    const result = parseStoredSession(null)

    expect(result).toBeNull()
    expect(warn).not.toHaveBeenCalled()
  })

  it('S4: returns null and warns (never errors) on malformed JSON', () => {
    const { warn, error } = spyOnConsole()

    const result = parseStoredSession('{not json')

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
  })

  it('S5: returns null and warns (never errors) on a schema-version mismatch', () => {
    const { warn, error } = spyOnConsole()
    const raw = JSON.stringify({ v: 0, personaId: 'firm', token: 'jwt', me: null, verified: true })

    const result = parseStoredSession(raw)

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
  })

  it('S6: returns null and warns (never errors) for an unknown personaId', () => {
    const { warn, error } = spyOnConsole()
    const raw = JSON.stringify({
      v: SESSION_SCHEMA_VERSION,
      personaId: 'ghost',
      token: 'jwt',
      me: null,
      verified: true,
    })

    const result = parseStoredSession(raw)

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
  })

  it('S7: returns null and warns (never errors) when a field has the wrong type (token not string|null)', () => {
    const { warn, error } = spyOnConsole()
    const raw = JSON.stringify({
      v: SESSION_SCHEMA_VERSION,
      personaId: 'firm',
      token: 123,
      me: null,
      verified: true,
    })

    const result = parseStoredSession(raw)

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
  })

  it('S35: a stored session with verified missing is rejected', () => {
    const blob = JSON.parse(serializeSession(firmSession()))
    // Positive half: the untouched blob parses, so only the deletion below can reject it.
    expect(parseStoredSession(JSON.stringify(blob))).not.toBeNull()
    delete blob.verified
    const { warn, error } = spyOnConsole()

    const result = parseStoredSession(JSON.stringify(blob))

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(error).not.toHaveBeenCalled()
  })

  it('S36: a stored session with verified "true" (a string) is rejected', () => {
    const blob = { ...JSON.parse(serializeSession(firmSession())), verified: 'true' }
    const { warn } = spyOnConsole()

    const result = parseStoredSession(JSON.stringify(blob))

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })
})

describe('no-gateway (unverified) session', () => {
  it('S8: round-trips the unverified, tokenless session intact', () => {
    const session = noGatewaySession()

    const restored = parseStoredSession(serializeSession(session))

    expect(restored).toEqual(session)
  })
})

describe('saveSession / loadSession / clearSession I/O', () => {
  it('S9: saveSession then loadSession round-trips through localStorage; the key is present', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)
    const session = firmSession()

    saveSession(session)
    const restored = loadSession()

    expect(restored).toEqual(session)
    expect(storage.getItem(SESSION_KEY)).not.toBeNull()
  })

  it('S10: clearSession removes the persisted key', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)
    saveSession(firmSession())

    clearSession()
    const restored = loadSession()

    expect(restored).toBeNull()
    expect(storage.getItem(SESSION_KEY)).toBeNull()
  })

  it('S11: loadSession swallows a throwing getItem to null + console.warn, never throws', () => {
    const { warn } = spyOnConsole()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => {
        throw new Error('getItem boom')
      }),
      setItem: vi.fn(),
      removeItem: vi.fn(),
    })

    let result: Session | null | undefined
    expect(() => {
      result = loadSession()
    }).not.toThrow()

    expect(result).toBeNull()
    expect(warn).toHaveBeenCalled()
  })

  it('S12: saveSession swallows a throwing setItem to console.warn, never throws', () => {
    const { warn } = spyOnConsole()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => null),
      setItem: vi.fn(() => {
        throw new Error('setItem boom')
      }),
      removeItem: vi.fn(),
    })

    expect(() => saveSession(firmSession())).not.toThrow()
    expect(warn).toHaveBeenCalled()
  })

  // Deviation from the story's literal "native Node, not stubbed" framing — see the
  // QA report for why. This deterministically simulates the same present-but-every
  // -method-throws-TypeError shape via vi.stubGlobal (verified locally on Node v25 to
  // be the actual native behavior, but pinning a unit test to an unflagged runtime
  // quirk would make it fragile across Node versions/CI images). The assertion is
  // identical either way: a present `localStorage` whose methods throw TypeError must
  // degrade cleanly, proving the implementation wraps the actual method CALL — not a
  // presence-only guard (finding C10.1).
  it('S13: a present localStorage whose every method throws TypeError degrades cleanly (not a presence-only guard)', () => {
    const { warn } = spyOnConsole()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => {
        throw new TypeError('localStorage.getItem is not a function')
      }),
      setItem: vi.fn(() => {
        throw new TypeError('localStorage.setItem is not a function')
      }),
      removeItem: vi.fn(() => {
        throw new TypeError('localStorage.removeItem is not a function')
      }),
    })

    let result: Session | null | undefined
    expect(() => {
      result = loadSession()
    }).not.toThrow()
    expect(result).toBeNull()
    expect(warn).toHaveBeenCalled()

    expect(() => saveSession(firmSession())).not.toThrow()
  })
})

// Boot-time expiry gate. A reload on a token past its `exp` used to enter the workspace
// and only discover the problem when the first fetch 401'd, leaving the user on a dead
// dashboard behind an error card. These pin the pure half of that fix; the redirect half
// lives in App.tsx and is browser-verified (no component-test harness in this package).
describe('isTokenExpired / resolveBootSession', () => {
  const HOUR = 3600_000
  // Minimal JWT shape: only the payload segment is read, and only its `exp`.
  function jwt(claims: object): string {
    const b64 = btoa(JSON.stringify(claims)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    return `header.${b64}.signature`
  }

  // Built the way GoTrue does: UTF-8 bytes, base64url, no padding.
  function utf8Jwt(claims: object): string {
    const bytes = new TextEncoder().encode(JSON.stringify(claims))
    const b64 = btoa(Array.from(bytes, (b) => String.fromCharCode(b)).join('')).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    return `header.${b64}.signature`
  }

  it('S24: a token whose exp is in the past is expired', () => {
    expect(isTokenExpired(jwt({ exp: 1000 }), 2000_000)).toBe(true)
  })

  it('S25: a token whose exp is in the future is not expired', () => {
    // exp 2000s = 2_000_000ms, now = 1_000_000ms → still live.
    expect(isTokenExpired(jwt({ exp: 2000 }), 1000_000)).toBe(false)
  })

  it('S25b: exp exactly at now counts as expired (matches the gateway boundary)', () => {
    expect(isTokenExpired(jwt({ exp: 1000 }), 1000_000)).toBe(true)
  })

  it('S26: exp is compared in SECONDS, not milliseconds — a token one hour out must not read as expired', () => {
    const now = 1_700_000_000_000
    expect(isTokenExpired(jwt({ exp: Math.floor(now / 1000) + 3600 }), now)).toBe(false)
    expect(isTokenExpired(jwt({ exp: Math.floor((now - HOUR) / 1000) }), now)).toBe(true)
  })

  // The no-gateway showcase session carries token: null and must survive boot untouched —
  // treating "no token" as "expired" would sign out every mock build on reload.
  it('S27: a null token is never expired (the no-gateway showcase session)', () => {
    expect(isTokenExpired(null, Date.now())).toBe(false)
  })

  // Never invent an expiry a token does not state: an opaque/garbage token is left to the
  // 401 handler rather than guessed at here.
  it('S28: opaque, malformed and exp-less tokens are not treated as expired', () => {
    for (const t of ['tok', '', 'a.b', 'a.!!!not-base64!!!.c', jwt({ sub: 'x' }), jwt({ exp: 'soon' })]) {
      expect(isTokenExpired(t, Date.now()), `token ${JSON.stringify(t)}`).toBe(false)
    }
  })

  // `{"exp":…,"s":"???"}` encodes to a payload with `_` (standard base64 `/`), which atob rejects.
  it('S33: an expired token whose base64url payload contains "_" reads expired', () => {
    const token = jwt({ exp: 1, s: '???' })
    expect(token.split('.')[1]).toContain('_')

    expect(isTokenExpired(token, 2000_000)).toBe(true)
  })

  it('S34: the same payload with a future exp is not expired', () => {
    const token = jwt({ exp: 4102444800, s: '???' })
    expect(token.split('.')[1]).toContain('_')

    expect(isTokenExpired(token, 2000_000)).toBe(false)
  })

  it('decodeJwtPayload reads a base64url payload', () => {
    const claims = { sub: 'x', exp: 1, s: '???>>>' }
    const payload = jwt(claims).split('.')[1]
    expect(payload).toContain('-')
    expect(payload).toContain('_')
    expect(payload.length % 4).not.toBe(0)
    expect(decodeJwtPayload(`header.${payload}.signature`)).toEqual(claims)

    const text = ['Soci\u00e9t\u00e9 G\u00e9n\u00e9rale', 'Ad\u00e9b\u00e1y\u1ecd\u0300', 'Acme \u{1F680}', '\u682a\u5f0f\u4f1a\u793e \u6771\u4eac', 'Adaeze Ventures']
    const claimsFor = (s: string) => ({ sub: 'x', pad: '???>>>', user_metadata: { registration: { workspace_name: s } } })
    const tokens = text.map((s) => utf8Jwt(claimsFor(s)))
    expect(tokens.some((t) => t.split('.')[1].includes('-'))).toBe(true)
    expect(tokens.some((t) => t.split('.')[1].includes('_'))).toBe(true)
    expect(tokens.some((t) => t.split('.')[1].length % 4 !== 0)).toBe(true)
    text.forEach((s, i) => {
      expect.soft(decodeJwtPayload(tokens[i]), s).toEqual(claimsFor(s))
    })

    const nonObject = ['5', '[{"exp":1}]', 'null', '"exp"'].map((j) => `header.${btoa(j).replace(/=+$/, '')}.signature`)
    const unreadable = [null, '', 'opaque', `header.${payload}`, `header.${btoa('{not json')}.signature`, 'a.!!!not-base64!!!.c', ...nonObject]
    expect(unreadable.length).toBeGreaterThan(0)
    for (const t of unreadable) {
      expect(decodeJwtPayload(t), `token ${JSON.stringify(t)}`).toBeNull()
    }
  })

  it('S29: resolveBootSession drops an expired session so the workspace never mounts on a dead token', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveSession({ ...firmSession(), token: jwt({ exp: 1000 }) })

    expect(resolveBootSession(2000_000)).toBeNull()
  })

  it('resolveBootSession keeps an expired renewable session', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)
    const token = jwt({ exp: 1000 })
    const me = firmSession().me as Me
    const record = { v: 1, personaId: 'firm', token, me, verified: true, handoff: true }
    // Control: the same record without the pair is dropped.
    storage.setItem(SESSION_KEY, JSON.stringify(record))
    expect(resolveBootSession(2000_000)).toBeNull()

    storage.setItem(SESSION_KEY, JSON.stringify({ ...record, refresh_token: 'R0', received_at: 1000 }))

    expect(resolveBootSession(2000_000)).toEqual({
      persona: handoffPersona(me),
      token,
      me,
      verified: true,
      handoff: true,
      renewal: { refreshToken: 'R0', receivedAt: 1000 },
    })
  })

  it('S30: resolveBootSession passes a live session through unchanged', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    const live = { ...firmSession(), token: jwt({ exp: 9_000_000 }) }
    saveSession(live)

    expect(resolveBootSession(1000_000)).toEqual(live)
  })

  it('S31: absent storage resolves to null', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())

    expect(resolveBootSession(Date.now())).toBeNull()
  })

  // The expiry drop must not be a blanket "any stored session is suspect": a no-gateway
  // showcase session carries token:null and has to survive a reload.
  it('S32: a stored no-gateway session (token:null) survives boot', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveSession(noGatewaySession())

    expect(resolveBootSession(Date.now())).toEqual(noGatewaySession())
  })
})

// QA (M3-07-01, Mode B): adversarial/edge coverage added on top of the RED-first S1-S17
// specs above. These are NOT padding — each one is a genuine regression guard for a
// specific way the implementation could silently regress (see the QA report for the
// mutation-tested rationale behind each).
describe('adversarial / edge coverage (QA)', () => {
  function inhouseSession(): Session {
    return {
      persona: APP_PERSONAS.inhouse,
      token: 'jwt-inhouse',
      me: {
        tenant: { id: '22222222-2222-2222-2222-222222222222', name: 'Honeywell Group', kind: 'in_house' },
        user: { id: 'c0000000-0000-0000-0000-000000000002', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
      },
      verified: true,
    }
  }

  it('S18: round-trips an inhouse session, rebuilding persona as the same APP_PERSONAS reference (S1-S8 only exercise firm — this proves the persona-by-id lookup is not firm-hardcoded)', () => {
    const session = inhouseSession()

    const restored = parseStoredSession(serializeSession(session))

    expect(restored).toEqual(session)
    expect(restored?.persona).toBe(APP_PERSONAS.inhouse)
  })

  it('S20: parseStoredSession ignores unknown extra fields in a stored blob (a forward-compat blob from a later schema still parses, picking only known fields)', () => {
    const session = firmSession()
    const raw = JSON.stringify({
      ...JSON.parse(serializeSession(session)),
      futureField: 'added-by-a-later-schema-version',
      anotherExtra: { nested: true },
    })

    const restored = parseStoredSession(raw)

    expect(restored).toEqual(session)
  })

  it('S21: an empty-string token is a valid token and round-trips (the type guard checks typeof, not truthiness)', () => {
    const session: Session = { ...firmSession(), token: '' }

    const restored = parseStoredSession(serializeSession(session))

    expect(restored).toEqual(session)
    expect(restored?.token).toBe('')
  })

  it('S22 (documentation, not a bug): a non-null "me" object that is not a full Me shape is accepted as-is — parseStoredSession only shallow-checks me is null|object per Decision (c); it does not deep-validate tenant/user fields', () => {
    const raw = JSON.stringify({
      v: SESSION_SCHEMA_VERSION,
      personaId: 'firm',
      token: 'jwt',
      me: { unexpectedShape: true },
      verified: true,
    })

    const restored = parseStoredSession(raw)

    expect(restored).not.toBeNull()
    expect(restored?.me).toEqual({ unexpectedShape: true })
  })
})

// The hand-off record.
// Each is refused by isHandoffMe; 'firm' and 'in_house' are the accepted kinds.
const KIND_REFUSALS: [string, unknown][] = [
  ['null', null],
  ['empty', ''],
  ['inhouse', 'inhouse'],
  ['IN_HOUSE', 'IN_HOUSE'],
  ['bogus', 'bogus'],
  ['toString', 'toString'],
  ['constructor', 'constructor'],
  ['__proto__', '__proto__'],
  ['number', 7],
  ['true', true],
  ['array', ['firm']],
  ['object with toString', { toString: () => 'firm' }],
]

describe('hand-off session record (AUTH-05 D8)', () => {
  const ME: Me = {
    tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
    user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
  }
  const IN_HOUSE_ME: Me = { ...ME, tenant: { ...ME.tenant, kind: 'in_house' } }
  const inhouseSessionOf = (me: Me): Session => ({ persona: APP_PERSONAS.inhouse, token: 'jwt', me, verified: true })

  it('a hand-off session round-trips', () => {
    const session: Session = { persona: handoffPersona(ME), token: 'jwt', me: ME, verified: true, handoff: true }
    const raw = serializeSession(session)
    expect(JSON.parse(raw)).toEqual({ v: 1, personaId: 'firm', token: 'jwt', me: ME, verified: true, handoff: true })
    const restored = parseStoredSession(raw)
    expect(restored?.persona.subject).toBe(ME.user.id)
    expect(restored?.persona.tenantId).toBe(ME.tenant.id)
    expect(restored?.handoff).toBe(true)
    expect(restored).toEqual(session)

    // An in-house tenant: same persona id and verbatim me on disk, in-house mode after parse.
    const inHouse: Session = { persona: handoffPersona(IN_HOUSE_ME), token: 'jwt', me: IN_HOUSE_ME, verified: true, handoff: true }
    const inHouseRaw = serializeSession(inHouse)
    expect(JSON.parse(inHouseRaw)).toEqual({ v: 1, personaId: 'firm', token: 'jwt', me: IN_HOUSE_ME, verified: true, handoff: true })
    const inHouseRestored = parseStoredSession(inHouseRaw)
    expect(inHouseRestored?.persona.mode).toBe('inhouse')
    expect(inHouseRestored).toEqual(inHouse)
  })

  it('a persona session keeps its own mode whatever me says', () => {
    const rows: [string, Session, string][] = [
      ['inhouse persona, me firm', { ...inhouseSessionOf(ME) }, 'inhouse'],
      ['firm persona, me in_house', { persona: APP_PERSONAS.firm, token: 'jwt', me: IN_HOUSE_ME, verified: true }, 'firm'],
    ]
    for (const [name, session, want] of rows) {
      expect(parseStoredSession(serializeSession(session))?.persona.mode, name).toBe(want)
    }

    // Stored records: a persona record never reads kind, so a missing or unknown one is not corrupt.
    const stored = (personaId: string, me: unknown, extra: object = {}) => JSON.stringify({ v: 1, personaId, token: 'jwt', me, verified: true, ...extra })
    const meKind = (kind: unknown) => ({ tenant: { id: ME.tenant.id, name: 'X', ...(kind === undefined ? {} : { kind }) }, user: ME.user })
    const raw: [string, string, string][] = [
      ['inhouse persona, no kind', stored('inhouse', meKind(undefined)), 'inhouse'],
      ['firm persona, bogus kind', stored('firm', meKind('bogus')), 'firm'],
      ['inhouse persona, handoff "true" is not a hand-off, me firm', stored('inhouse', meKind('firm'), { handoff: 'true' }), 'inhouse'],
      ['firm persona, handoff "true" is not a hand-off, me in_house', stored('firm', meKind('in_house'), { handoff: 'true' }), 'firm'],
    ]
    for (const [name, rec, want] of raw) {
      const { warn } = spyOnConsole()
      const restored = parseStoredSession(rec)
      expect(restored?.persona.mode, name).toBe(want)
      expect(restored?.handoff, name).toBeUndefined()
      expect(warn, name).not.toHaveBeenCalled()
      vi.restoreAllMocks()
    }
  })

  it('a stored hand-off record with no tenant kind is dropped, renewal pair and all', () => {
    const noKindMe = { tenant: { id: ME.tenant.id, name: ME.tenant.name }, user: ME.user }
    const noKind = { v: 1, personaId: 'firm', token: 'jwt', me: noKindMe, verified: true, handoff: true, refresh_token: 'R0', received_at: 1000 }
    // Control: the same record with a kind parses and keeps its renewal.
    const kept = parseStoredSession(JSON.stringify({ ...noKind, me: ME }))
    expect(kept?.renewal).toEqual({ refreshToken: 'R0', receivedAt: 1000 })
    const { warn, error } = spyOnConsole()
    expect(parseStoredSession(JSON.stringify(noKind))).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(error).not.toHaveBeenCalled()
  })

  it('a persona record is unchanged', () => {
    const persona =
      '{"v":1,"personaId":"firm","token":"jwt","me":{"tenant":{"id":"11111111-1111-1111-1111-111111111111","name":"Okafor & Partners","kind":"firm"},"user":{"id":"c0000000-0000-0000-0000-000000000001","role":"authenticated","display_name":"Adaeze Nwankwo","email":"adaeze.nwankwo@example.com"}},"verified":true}'
    expect(serializeSession(firmSession())).toBe(persona)
    // A persona session never writes the pair, even if it carries a renewal.
    expect(serializeSession({ ...firmSession(), renewal: { refreshToken: 'R0', receivedAt: 1000 } })).toBe(persona)
  })

  it('a handoff record without a usable me is rejected', () => {
    const base = { v: 1, personaId: 'firm', token: 'jwt', verified: true, handoff: true }
    // JSON.stringify drops a function, so the toString object arrives as {}; the direct isHandoffMe test below keeps the real one.
    const kinds: [string, unknown][] = KIND_REFUSALS.map(([name, kind]) => [`kind ${name}`, { tenant: { id: ME.tenant.id, name: 'X', kind }, user: ME.user }])
    const rows: [string, unknown][] = [
      ['me null', null],
      ['no user id', { tenant: { id: ME.tenant.id, name: 'X', kind: 'firm' }, user: { role: 'authenticated' } }],
      ['no tenant id', { tenant: { name: 'X', kind: 'firm' }, user: { id: ME.user.id, role: 'authenticated' } }],
      ['numeric user id', { tenant: { id: ME.tenant.id, name: 'X', kind: 'firm' }, user: { id: 9, role: 'authenticated' } }],
      ['kind absent', { tenant: { id: ME.tenant.id, name: 'X' }, user: ME.user }],
      ...kinds,
    ]
    // Controls: the same record with a usable me parses, in either kind.
    expect(parseStoredSession(JSON.stringify({ ...base, me: ME }))).not.toBeNull()
    expect(parseStoredSession(JSON.stringify({ ...base, me: IN_HOUSE_ME }))).not.toBeNull()
    for (const [name, me] of rows) {
      const { warn } = spyOnConsole()
      expect(parseStoredSession(JSON.stringify({ ...base, me })), name).toBeNull()
      expect(warn, name).toHaveBeenCalledTimes(1)
      vi.restoreAllMocks()
    }
  })

  it('isHandoffMe refuses a tenant kind that is not an own key of the mode table', () => {
    const meWith = (kind: unknown) => ({ tenant: { id: ME.tenant.id, name: 'X', kind }, user: ME.user })
    expect(isHandoffMe(meWith('firm'))).toBe(true)
    expect(isHandoffMe(meWith('in_house'))).toBe(true)
    for (const [name, kind] of KIND_REFUSALS) {
      expect(isHandoffMe(meWith(kind)), name).toBe(false)
    }
    expect(isHandoffMe({ tenant: { id: ME.tenant.id, name: 'X' }, user: ME.user }), 'kind absent').toBe(false)
  })
})

// The refresh token rides in the same record as the access token.
describe('renewal pair in the stored record (AUTH-06 D1)', () => {
  const ME: Me = {
    tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
    user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
  }
  const HANDOFF = { v: 1, personaId: 'firm', token: 'jwt', me: ME, verified: true, handoff: true }

  it('a renewable hand-off session round-trips', () => {
    const session: Session = {
      persona: handoffPersona(ME),
      token: 'jwt',
      me: ME,
      verified: true,
      handoff: true,
      renewal: { refreshToken: 'R0', receivedAt: 1000 },
    }

    const raw = serializeSession(session)

    expect(JSON.parse(raw)).toEqual({ ...HANDOFF, refresh_token: 'R0', received_at: 1000 })
    expect(parseStoredSession(raw)).toEqual(session)
    // Boundary: epoch 0 is a finite receipt time, not an absent one.
    const zero: Session = { ...session, renewal: { refreshToken: 'R0', receivedAt: 0 } }
    expect(parseStoredSession(serializeSession(zero))).toEqual(zero)
  })

  it('an AUTH-05-era hand-off record parses without renewal', () => {
    const { warn } = spyOnConsole()

    const restored = parseStoredSession(JSON.stringify(HANDOFF))

    expect(restored).toEqual({ persona: handoffPersona(ME), token: 'jwt', me: ME, verified: true, handoff: true })
    expect(restored?.renewal).toBeUndefined()
    expect(warn).not.toHaveBeenCalled()
  })

  it('a malformed renewal pair is corrupt', () => {
    const PERSONA = { v: 1, personaId: 'firm', token: 'jwt', me: ME, verified: true }
    const rows: [string, string][] = [
      ['pair on a persona record', JSON.stringify({ ...PERSONA, refresh_token: 'R0', received_at: 1000 })],
      ['pair on a handoff:"true" record', JSON.stringify({ ...PERSONA, handoff: 'true', refresh_token: 'R0', received_at: 1000 })],
      ['refresh_token only', JSON.stringify({ ...HANDOFF, refresh_token: 'R0' })],
      ['received_at only', JSON.stringify({ ...HANDOFF, received_at: 1000 })],
      ["refresh_token ''", JSON.stringify({ ...HANDOFF, refresh_token: '', received_at: 1000 })],
      ['numeric refresh_token', JSON.stringify({ ...HANDOFF, refresh_token: 123, received_at: 1000 })],
      ['null refresh_token', JSON.stringify({ ...HANDOFF, refresh_token: null, received_at: 1000 })],
      ['both null', JSON.stringify({ ...HANDOFF, refresh_token: null, received_at: null })],
      ["received_at 'x'", JSON.stringify({ ...HANDOFF, refresh_token: 'R0', received_at: 'x' })],
      ['null received_at', JSON.stringify({ ...HANDOFF, refresh_token: 'R0', received_at: null })],
      // JSON.parse reads 1e400 as Infinity.
      ['non-finite received_at', JSON.stringify({ ...HANDOFF, refresh_token: 'R0' }).replace(/\}$/, ',"received_at":1e400}')],
    ]
    // Control: the well-formed pair on a hand-off record parses.
    expect(parseStoredSession(JSON.stringify({ ...HANDOFF, refresh_token: 'R0', received_at: 1000 }))).not.toBeNull()
    for (const [name, raw] of rows) {
      const { warn, error } = spyOnConsole()
      expect(parseStoredSession(raw), name).toBeNull()
      expect(warn, name).toHaveBeenCalledTimes(1)
      expect(error, name).not.toHaveBeenCalled()
      vi.restoreAllMocks()
    }
  })
})

describe('cardIdentity (AUTH-09-02)', () => {
  const USER_ID = 'd0000000-0000-0000-0000-000000000009'
  const handoffOf = (user: Record<string, unknown>): Session => {
    const me = {
      tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
      user: { id: USER_ID, role: 'authenticated', ...user },
    } as unknown as Me
    return { persona: handoffPersona(me), token: 'jwt', me, verified: true, handoff: true }
  }

  // Every shape the stored /me may hold that names no one.
  const ABSENT: [string, (u: Record<string, unknown>, k: string) => void][] = [
    ['null', (u, k) => { u[k] = null }],
    ['missing', () => {}],
    ["''", (u, k) => { u[k] = '' }],
    ["'   '", (u, k) => { u[k] = '   ' }],
    ['7', (u, k) => { u[k] = 7 }],
    ['{}', (u, k) => { u[k] = {} }],
    ["['x']", (u, k) => { u[k] = ['x'] }],
    ['tab and newline', (u, k) => { u[k] = '\t\n' }],
    ['no-break space', (u, k) => { u[k] = '\u00a0' }],
    ['true', (u, k) => { u[k] = true }],
    ['0', (u, k) => { u[k] = 0 }],
  ]

  it('cardIdentity names a hand-off session from /me', () => {
    const session = handoffOf({ display_name: 'Adaeze Nwankwo', email: 'a@acme.ng' })
    expect(cardIdentity(session)).toEqual({ name: 'Adaeze Nwankwo', initials: 'AN' })
  })

  it('cardIdentity falls back to the email', () => {
    const email = 'folake.adesina@acme.ng'
    const got = cardIdentity(handoffOf({ display_name: null, email }))
    expect(got).toEqual({ name: email, initials: 'FA' })
    expect(got.initials).toBe(memberInitials(null, email, ''))
  })

  it('cardIdentity shows nothing legible, never the user id', () => {
    expect(ABSENT).toHaveLength(11)
    const pairs = (v: string) => Array.from({ length: v.length - 1 }, (_, i) => v.slice(i, i + 2).toLowerCase())
    for (const [dn, setDn] of ABSENT) {
      for (const [em, setEm] of ABSENT) {
        const user: Record<string, unknown> = {}
        setDn(user, 'display_name')
        setEm(user, 'email')
        const got = cardIdentity(handoffOf(user))
        const label = `display_name ${dn}, email ${em}`
        expect(got, label).toEqual({ name: '', initials: '' })
        for (const pair of pairs(USER_ID)) {
          expect(got.name.toLowerCase(), label).not.toContain(pair)
          expect(got.initials.toLowerCase(), label).not.toContain(pair)
        }
      }
    }
  })

  it('cardIdentity skips a blank display name', () => {
    expect(cardIdentity(handoffOf({ display_name: '  ', email: 'zainab@acme.ng' }))).toEqual({
      name: 'zainab@acme.ng',
      initials: 'ZA',
    })
  })

  // Accepted behaviour (D6): memberInitials' output is shown as is.
  it("cardIdentity keeps memberInitials' output for non-ASCII and hyphenated names", () => {
    const rows: [string, { name: string; initials: string }][] = [
      ['Ọlá Adébáyọ̀', { name: 'Ọlá Adébáyọ̀', initials: 'LA' }],
      ['Ada-Obi', { name: 'Ada-Obi', initials: 'A' }],
      ['张伟', { name: '张伟', initials: '' }],
      ['  Adaeze Nwankwo  ', { name: 'Adaeze Nwankwo', initials: 'AN' }],
    ]
    for (const [displayName, want] of rows) {
      expect(cardIdentity(handoffOf({ display_name: displayName, email: null })), displayName).toEqual(want)
    }
  })

  it('cardIdentity keeps the persona for a persona session', () => {
    const me = firmSession().me
    const withName = { ...firmSession(), me: { ...me!, user: { ...me!.user, display_name: 'Someone Else' } } }
    const want = { name: APP_PERSONAS.firm.name, initials: APP_PERSONAS.firm.initials }
    expect(want.name).not.toBe('Someone Else')
    expect(cardIdentity(withName)).toEqual(want)
    expect(cardIdentity({ ...firmSession(), me: null })).toEqual(want)
  })

  it('cardIdentity falls through every absent display name to the email', () => {
    expect(ABSENT.length).toBeGreaterThan(0)
    for (const [label, set] of ABSENT) {
      const user: Record<string, unknown> = { email: '  zainab@acme.ng ' }
      set(user, 'display_name')
      expect(cardIdentity(handoffOf(user)), `display_name ${label}`).toEqual({ name: 'zainab@acme.ng', initials: 'ZA' })
    }
  })

  it('cardIdentity prefers the display name over the email', () => {
    expect(cardIdentity(handoffOf({ display_name: 'Adaeze Nwankwo', email: 'zainab@acme.ng' }))).toEqual({
      name: 'Adaeze Nwankwo',
      initials: 'AN',
    })
  })

  it('cardIdentity reads /me for a hand-off session even when its persona still carries a name', () => {
    const session = { ...handoffOf({ display_name: 'Adaeze Nwankwo', email: null }), persona: APP_PERSONAS.firm }
    expect(APP_PERSONAS.firm.name).not.toBe('')
    expect(cardIdentity(session)).toEqual({ name: 'Adaeze Nwankwo', initials: 'AN' })
  })

  it('cardIdentity never falls back to the persona for a hand-off session without /me', () => {
    const session: Session = { ...handoffOf({}), persona: APP_PERSONAS.firm, me: null }
    expect(APP_PERSONAS.firm.name).not.toBe('')
    expect(cardIdentity(session)).toEqual({ name: '', initials: '' })
  })

  it('cardIdentity keeps the persona for a persona session whose /me names no one', () => {
    const me = firmSession().me!
    const session = { ...firmSession(), me: { ...me, user: { ...me.user, display_name: null, email: null } } }
    expect(cardIdentity(session)).toEqual({ name: APP_PERSONAS.firm.name, initials: APP_PERSONAS.firm.initials })
  })
})
