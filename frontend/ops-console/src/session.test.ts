// Specs for the Ops Console sign-in gate. Before this, the console had no session concept
// at all — the landing routed here with a bare navigation and nothing on this side could
// tell a signed-in visitor from a stranger with the URL.
//
// Mirrors the app's session.test.ts idiom: vi.stubGlobal for localStorage, console.warn
// spies for the no-error invariant, afterEach cleanup.
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  OPS_SESSION_KEY,
  OPS_SESSION_SCHEMA_VERSION,
  clearOpsSession,
  loadOpsSession,
  operatorFromParam,
  parseStoredOpsSession,
  resolveOpsBootSession,
  saveOpsSession,
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
  it('OPS-1: accepts the landing developer persona', () => {
    expect(operatorFromParam('developer')).toBe('developer')
  })

  // The tenant-scoped personas open the Platform app, and 'support' opens the cross-tenant
  // Support Console. Accepting any of them here would let a link minted for one surface
  // sign someone into another.
  it('OPS-2: rejects the Platform and Support personas and unknown values', () => {
    for (const v of ['firm', 'inhouse', 'support', 'admin', '', 'DEVELOPER', null]) {
      expect(operatorFromParam(v), `param ${JSON.stringify(v)}`).toBeNull()
    }
  })

  // Object.prototype keys must not read as members — hasOwnProperty, not `in`.
  it('OPS-3: inherited Object.prototype keys are not operators', () => {
    for (const v of ['constructor', 'toString', '__proto__', 'hasOwnProperty']) {
      expect(operatorFromParam(v), `param ${v}`).toBeNull()
    }
  })
})

describe('parseStoredOpsSession', () => {
  it('OPS-4: round-trips a saved session', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())
    saveOpsSession({ operator: 'developer' })

    expect(loadOpsSession()).toEqual({ operator: 'developer' })
  })

  it('OPS-5: absent storage is the normal signed-out case and warns nothing', () => {
    const spy = spyConsole()

    expect(parseStoredOpsSession(null)).toBeNull()
    expect(spy.warn).not.toHaveBeenCalled()
    expect(spy.error).not.toHaveBeenCalled()
  })

  it('OPS-6: corrupt, wrong-version and unknown-operator blobs degrade to null with a warn, never an error', () => {
    for (const raw of [
      'not json',
      '{}',
      JSON.stringify({ v: OPS_SESSION_SCHEMA_VERSION + 1, operator: 'developer' }),
      JSON.stringify({ v: OPS_SESSION_SCHEMA_VERSION, operator: 'firm' }),
      JSON.stringify({ v: OPS_SESSION_SCHEMA_VERSION }),
      JSON.stringify(null),
    ]) {
      const spy = spyConsole()
      expect(parseStoredOpsSession(raw), `raw ${raw}`).toBeNull()
      expect(spy.warn, `raw ${raw}`).toHaveBeenCalled()
      expect(spy.error, `raw ${raw}`).not.toHaveBeenCalled()
      vi.restoreAllMocks()
    }
  })

  // A present localStorage whose methods throw (native Node, private mode, quota) must
  // degrade rather than take the console down with it.
  it('OPS-7: a throwing localStorage degrades cleanly on read, write and clear', () => {
    spyConsole()
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new TypeError('nope')
      },
      setItem: () => {
        throw new TypeError('nope')
      },
      removeItem: () => {
        throw new TypeError('nope')
      },
    })

    expect(loadOpsSession()).toBeNull()
    expect(() => saveOpsSession({ operator: 'developer' })).not.toThrow()
    expect(() => clearOpsSession()).not.toThrow()
  })
})

describe('resolveOpsBootSession', () => {
  it('OPS-8: a ?persona= hand-off from the landing signs in AND persists, so the next reload needs no param', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)

    expect(resolveOpsBootSession('?persona=developer')).toEqual({ operator: 'developer' })
    expect(storage.setItem).toHaveBeenCalledWith(OPS_SESSION_KEY, expect.stringContaining('developer'))
    expect(resolveOpsBootSession('')).toEqual({ operator: 'developer' })
  })

  // The whole point of the gate: a bare URL with nothing stored is NOT a sign-in.
  it('OPS-9: no param and no stored session resolves to null (the redirect case)', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())

    expect(resolveOpsBootSession('')).toBeNull()
    expect(resolveOpsBootSession('?screen=submissions')).toBeNull()
  })

  it('OPS-10: an unknown persona param is not a sign-in even with other params present', () => {
    vi.stubGlobal('localStorage', createMemoryStorage())

    expect(resolveOpsBootSession('?persona=firm&screen=status')).toBeNull()
  })

  // Deliberately the OPPOSITE precedence to the Platform app, where a rehydrated session
  // beats a stale param. Here the param is a fresh sign-in decision made on the landing
  // page seconds ago, so it must win.
  it('OPS-11: a deep link wins over a stored session', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)
    storage.setItem(OPS_SESSION_KEY, JSON.stringify({ v: OPS_SESSION_SCHEMA_VERSION, operator: 'developer' }))

    expect(resolveOpsBootSession('?persona=developer')).toEqual({ operator: 'developer' })
    expect(storage.setItem).toHaveBeenCalledTimes(2)
  })

  it('OPS-12: clearOpsSession removes the key, so sign-out does not leave the next visitor signed in', () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)
    saveOpsSession({ operator: 'developer' })

    clearOpsSession()

    expect(storage.removeItem).toHaveBeenCalledWith(OPS_SESSION_KEY)
    expect(loadOpsSession()).toBeNull()
  })
})
