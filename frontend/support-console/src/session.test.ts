// Specs for the Support Console sign-in gate. Mirrors the ops console's session.test.ts
// idiom: vi.stubGlobal for localStorage, console.warn spies for the no-error invariant,
// afterEach cleanup.
//
// The cases that matter most here are the REJECTIONS: this console reads across every
// tenant, so a link minted for any other persona — including the developer console's —
// must not sign anyone in.
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  SUPPORT_SESSION_KEY,
  SUPPORT_SESSION_SCHEMA_VERSION,
  clearSupportSession,
  loadSupportSession,
  operatorFromParam,
  parseStoredSupportSession,
  resolveSupportBootSession,
  saveSupportSession,
} from './session'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((k: string) => (store.has(k) ? (store.get(k) as string) : null)),
    setItem: vi.fn((k: string, v: string) => {
      store.set(k, v)
    }),
    removeItem: vi.fn((k: string) => {
      store.delete(k)
    }),
  }
}

function spyConsole() {
  return {
    warn: vi.spyOn(console, 'warn').mockImplementation(() => {}),
    error: vi.spyOn(console, 'error').mockImplementation(() => {}),
  }
}

describe('operatorFromParam', () => {
  it('SUP-1: accepts the landing support persona', () => {
    expect(operatorFromParam('support')).toBe('support')
  })

  // 'developer' is the DEVELOPER console's persona and 'firm'/'inhouse' are the Platform
  // app's. None of them may open a cross-tenant console.
  it('SUP-2: rejects every other persona and unknown values', () => {
    for (const v of ['developer', 'firm', 'inhouse', 'admin', 'ops', '', 'SUPPORT', null]) {
      expect(operatorFromParam(v), `param ${JSON.stringify(v)}`).toBeNull()
    }
  })

  // Object.prototype keys must not read as members — hasOwnProperty, not `in`.
  it('SUP-3: inherited Object.prototype keys are not operators', () => {
    for (const v of ['constructor', 'toString', '__proto__', 'hasOwnProperty']) {
      expect(operatorFromParam(v), `param ${v}`).toBeNull()
    }
  })
})

describe('parseStoredSupportSession', () => {
  it('SUP-4: round-trips a saved session', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveSupportSession({ operator: 'support' })

    expect(loadSupportSession()).toEqual({ operator: 'support' })
  })

  it('SUP-5: absent storage is the normal signed-out case and warns nothing', () => {
    const spy = spyConsole()

    expect(parseStoredSupportSession(null)).toBeNull()
    expect(spy.warn).not.toHaveBeenCalled()
    expect(spy.error).not.toHaveBeenCalled()
  })

  it('SUP-6: corrupt, wrong-version and unknown-operator blobs degrade to null with a warn, never an error', () => {
    for (const raw of [
      'not json',
      '{}',
      JSON.stringify({ v: SUPPORT_SESSION_SCHEMA_VERSION + 1, operator: 'support' }),
      JSON.stringify({ v: SUPPORT_SESSION_SCHEMA_VERSION, operator: 'developer' }),
      JSON.stringify({ v: SUPPORT_SESSION_SCHEMA_VERSION }),
      JSON.stringify(null),
    ]) {
      const spy = spyConsole()
      expect(parseStoredSupportSession(raw), `blob ${raw}`).toBeNull()
      expect(spy.warn, `blob ${raw}`).toHaveBeenCalled()
      expect(spy.error, `blob ${raw}`).not.toHaveBeenCalled()
      vi.restoreAllMocks()
    }
  })

  // Under native Node `globalThis.localStorage` exists but its methods throw, so the
  // try/catch must wrap the CALL, not a presence check.
  it('SUP-7: a throwing localStorage degrades to signed-out rather than crashing the boot', () => {
    const spy = spyConsole()
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('SecurityError')
      },
      setItem: () => {
        throw new Error('SecurityError')
      },
      removeItem: () => {
        throw new Error('SecurityError')
      },
    })

    expect(loadSupportSession()).toBeNull()
    expect(() => saveSupportSession({ operator: 'support' })).not.toThrow()
    expect(() => clearSupportSession()).not.toThrow()
    expect(spy.error).not.toHaveBeenCalled()
  })
})

describe('resolveSupportBootSession', () => {
  it('SUP-8: a ?persona= hand-off signs in and is persisted for the next load', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)

    expect(resolveSupportBootSession('?persona=support')).toEqual({ operator: 'support' })
    expect(storage.setItem).toHaveBeenCalledWith(
      SUPPORT_SESSION_KEY,
      JSON.stringify({ v: SUPPORT_SESSION_SCHEMA_VERSION, operator: 'support' }),
    )
  })

  it('SUP-9: falls back to the stored session when no param is present', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveSupportSession({ operator: 'support' })

    expect(resolveSupportBootSession('')).toEqual({ operator: 'support' })
  })

  // A bare URL is not a sign-in — this is the whole point of the gate.
  it('SUP-10: no param and no stored session is signed-out', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())

    expect(resolveSupportBootSession('')).toBeNull()
    expect(resolveSupportBootSession('?persona=developer')).toBeNull()
  })

  // Clearing must actually remove the key, or Sign out would leave the next visitor in.
  it('SUP-11: clearing removes the stored session', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveSupportSession({ operator: 'support' })
    clearSupportSession()

    expect(loadSupportSession()).toBeNull()
  })
})
