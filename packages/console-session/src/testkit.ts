// Shared fixtures for the package tests: storage, fetch and window stubs, JWT builders.
import { vi } from 'vitest'

export const GW = 'https://gw.test'
export const LANDING = 'https://www.test'
export const OPS_KEY = 'invoice-os.ops-session'
export const SUPPORT_KEY = 'invoice-os.support-session'
export const STATE_KEY = 'invoice-os.signInState' // SIGN_IN_STATE_KEY, frontend/app/src/lib/signInState.ts
export const NOW = 1_800_000_000_000
export const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
export const STATE_A = 'ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvutsrqponmlk_'
export const STATE_B = '9876543210987654321098765432109876543210abc'
export const BASE64URL_43_RE = /^[A-Za-z0-9_-]{43}$/

export const b64url = (o: unknown): string =>
  btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(o))))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')

export function jwt(claims: Record<string, unknown>): string {
  return `${b64url({ alg: 'RS256', typ: 'JWT' })}.${b64url(claims)}.c2lnbmF0dXJl`
}

export const staffToken = (tag: string, expSec = NOW / 1000 + 3600): string =>
  jwt({ sub: 'staff-1', exp: expSec, tag, app_metadata: { staff: true } })

export const customerToken = (tag: string, expSec = NOW / 1000 + 3600): string =>
  jwt({ sub: 'cust-1', exp: expSec, tag, app_metadata: { tenant_id: 'tenant-1' } })

// The stored console record: AC-6 `{v:2, token, refresh_token}`.
export const recordRaw = (token: string, refresh: string): string => JSON.stringify({ v: 2, token, refresh_token: refresh })

// The stored sign-in state: `{v, s, at}` with v = SIGN_IN_STATE_SCHEMA_VERSION (1).
export const stateRaw = (s: string, at: number): string => JSON.stringify({ v: 1, s, at })

export function memoryStorage(initial: Record<string, string> = {}) {
  const m = new Map(Object.entries(initial))
  return {
    getItem: vi.fn((k: string) => m.get(k) ?? null),
    setItem: vi.fn((k: string, v: string) => void m.set(k, String(v))),
    removeItem: vi.fn((k: string) => void m.delete(k)),
    clear: vi.fn(() => m.clear()),
    key: (i: number) => [...m.keys()][i] ?? null,
    get length() {
      return m.size
    },
  }
}
export type MemoryStorage = ReturnType<typeof memoryStorage>

export function throwingStorage(o: { reads?: boolean; writes?: boolean } = {}) {
  const boom = (): never => {
    throw new DOMException('storage denied', 'SecurityError')
  }
  const { reads = true, writes = true } = o
  return {
    getItem: vi.fn(reads ? boom : () => null),
    setItem: vi.fn(writes ? boom : () => undefined),
    removeItem: vi.fn(writes ? boom : () => undefined),
    clear: vi.fn(writes ? boom : () => undefined),
    key: () => null,
    length: 0,
  }
}

export function installStorage(o: { local?: Record<string, string>; session?: Record<string, string> } = {}) {
  const local = memoryStorage(o.local)
  const session = memoryStorage(o.session)
  vi.stubGlobal('localStorage', local)
  vi.stubGlobal('sessionStorage', session)
  return { local, session }
}

export interface Call {
  url: string
  method: string
  body: unknown
  headers: Record<string, string>
  signal: AbortSignal | null | undefined
}

export const reply = (status: number, body?: unknown): Response =>
  new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
  })

// A returned Response is the answer; anything else the handler returns is thrown, as fetch rejects.
export function installFetch(handler: (c: Call) => Response | Error | Promise<Response | Error>) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers: Record<string, string> = {}
    new Headers(init?.headers).forEach((v, k) => (headers[k] = v))
    const call: Call = {
      url: String(input),
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      headers,
      signal: init?.signal,
    }
    calls.push(call)
    const out = await handler(call)
    if (out instanceof Response) return out
    throw out
  })
  return { calls }
}

// apiFetch passes the signal to fetch; the spy records the timeout without a live timer.
export const spyTimeouts = () => vi.spyOn(AbortSignal, 'timeout').mockImplementation(() => new AbortController().signal)

// Node env only: the package writes `window.location.href` and calls `window.location.reload()`.
export function installWindow(log: string[] = []) {
  const hrefWrites: string[] = []
  const reload = vi.fn(() => void log.push('reload'))
  vi.stubGlobal('window', {
    location: {
      set href(v: string) {
        hrefWrites.push(v)
        log.push('href')
      },
      get href() {
        return ''
      },
      reload,
    },
  })
  return { hrefWrites, reload }
}

export const timeoutError = (): DOMException => new DOMException('The operation timed out', 'TimeoutError')
