// RED specs (M3-07-01, S1-S17) — pin the session.ts persistence contract before the
// executor implements the bodies. Mirrors the mocking style of
// packages/api-client/src/client.test.ts: vi.stubGlobal for localStorage,
// vi.spyOn(console, 'warn'/'error') for the no-error invariant, afterEach cleanup.
//
// Every spec below currently fails because the stub throws `new Error('not
// implemented')` — that IS the correct RED reason (assertion / not-implemented),
// not an import/compile/setup error.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from '../auth'
import {
  SESSION_KEY,
  SESSION_SCHEMA_VERSION,
  clearSession,
  isTokenExpired,
  loadSession,
  parseStoredSession,
  resolveBootSession,
  saveSession,
  serializeSession,
  shouldAutoSignIn,
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
      tenant: { id: '11111111-1111-1111-1111-111111111111', name: 'Okafor & Partners' },
      user: { id: 'c0000000-0000-0000-0000-000000000001', role: 'authenticated' },
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

describe('shouldAutoSignIn deep-link guard', () => {
  it('S14: auto-signs-in when personaParam is a known persona', () => {
    expect(shouldAutoSignIn('firm')).toBe(true)
  })

  // Was the inverse (a rehydrated session beat the param) until that turned out to BE the
  // persona-switch bug: the landing page is a different origin, so it cannot clear this
  // origin's stored session when the user picks a profile — reaching landing without the
  // in-app Sign out and choosing the other accountant silently reopened the previous one.
  // The param is a choice made seconds ago on the only front door; it wins.
  it('S15: a persona deep-link param wins over a rehydrated boot session', () => {
    expect(shouldAutoSignIn('inhouse')).toBe(true)
  })

  it('S16: does not auto-sign-in for an unknown persona param', () => {
    expect(shouldAutoSignIn('bogus')).toBe(false)
  })

  it('S17: does not auto-sign-in when there is no persona param', () => {
    expect(shouldAutoSignIn(null)).toBe(false)
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

  it('S29: resolveBootSession drops an expired session so the workspace never mounts on a dead token', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveSession({ ...firmSession(), token: jwt({ exp: 1000 }) })

    expect(resolveBootSession(2000_000)).toBeNull()
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
        tenant: { id: '22222222-2222-2222-2222-222222222222', name: 'Honeywell Group' },
        user: { id: 'c0000000-0000-0000-0000-000000000002', role: 'authenticated' },
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

  it("S19: does not auto-sign-in for the landing-only 'support' persona (an Ops Console persona, not an APP_PERSONAS entry — only 'firm'/'inhouse' auto-sign-in)", () => {
    expect(shouldAutoSignIn('support')).toBe(false)
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
