// @vitest-environment jsdom
import * as Sentry from '@sentry/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { initMonitoring } from './init'
import type { Service } from './options'
import { RELEASE } from './release'

const DSN = 'https://public@o1.ingest.de.sentry.io/1'
const SERVICES: Service[] = ['app', 'landing', 'ops-console', 'support-console']
// Pinned from `printf %s "$P" | shasum -a 256`; rows never compute a digest themselves.
const P = 'asc-go-live-test-passphrase'
const DIGEST = 'd699ff08fe92157bd9a90c8d4099583b7a807ed3884094448a4c0f3e55d6e53e'

type Trigger = (passphrase?: unknown) => Promise<string | null>
const trigger = (): Trigger | undefined => (window as unknown as { __ascSentryTest?: Trigger }).__ascSentryTest

interface Item {
  type: string
  body: any
}

// Unlike runtime.dom.test.tsx, keep each request body.
const requests: Array<{ url: string; body: string }> = []
vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
  const b = init?.body
  const body = typeof b === 'string' ? b : b instanceof Uint8Array ? new TextDecoder().decode(b) : ''
  requests.push({ url: input instanceof Request ? input.url : String(input), body })
  return new Response('{}', { status: 200 })
})

// Envelope text: header line, then (item header, item body) line pairs.
function events(): Item[] {
  return requests
    .filter((r) => r.url.includes('ingest.de.sentry.io'))
    .flatMap((r) => {
      const lines = r.body.split('\n')
      const out: Item[] = []
      for (let i = 1; i + 1 < lines.length; i += 2) out.push({ type: JSON.parse(lines[i]).type, body: JSON.parse(lines[i + 1]) })
      return out
    })
    .filter((i) => i.type === 'event')
}

async function sentEvents(): Promise<Item[]> {
  await Sentry.flush(1000)
  return events()
}

async function reset(): Promise<void> {
  await Sentry.close()
  Sentry.getCurrentScope().clear()
  Sentry.getIsolationScope().clear()
  Sentry.getGlobalScope().clear()
  Sentry.getCurrentScope().setClient(undefined)
}

function boot(service: Service, digest: string | undefined = DIGEST, dsn = DSN): boolean {
  vi.stubEnv('VITE_SENTRY_DSN', dsn)
  vi.stubEnv('VITE_SENTRY_TEST_DIGEST', digest as string)
  return initMonitoring(service)
}

const undo: Array<() => void> = []
// vi.unstubAllGlobals would drop the file-level fetch stub, so plant and restore by hand.
function plant(target: object, key: string, value: unknown): void {
  const was = Object.getOwnPropertyDescriptor(target, key)
  Object.defineProperty(target, key, { configurable: true, writable: true, value })
  undo.push(() => (was ? Object.defineProperty(target, key, was) : delete (target as any)[key]))
}

beforeEach(() => {
  requests.length = 0
  window.history.replaceState(null, '', '/')
})

afterEach(async () => {
  delete (window as unknown as { __ascSentryTest?: Trigger }).__ascSentryTest
  undo.splice(0).forEach((f) => f())
  await reset()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

describe('SPA test event', () => {
  it('testEvent_rightPassphraseSendsOneLabelledEventPerService', async () => {
    for (const service of SERVICES) {
      requests.length = 0
      expect(boot(service), service).toBe(true)
      const fn = trigger()
      expect(typeof fn, `${service}: __ascSentryTest installed`).toBe('function')
      const id = await fn!(P)
      const sent = await sentEvents()
      expect(sent.length, `${service}: exactly one event`).toBe(1)
      const e = sent[0].body
      expect(e.environment, service).toBe('production')
      expect(e.release, service).toBe(RELEASE)
      expect(e.tags.service, service).toBe(service)
      expect(e.fingerprint, service).toEqual(['sentry-test-event', service])
      expect(e.exception.values[0].type, service).toBe('SentryTestEvent')
      expect(e.exception.values[0].value, service).toBe('sentry test event')
      expect(typeof id, `${service}: resolves the event id`).toBe('string')
      expect(id, service).toBe(e.event_id)
      delete (window as unknown as { __ascSentryTest?: Trigger }).__ascSentryTest
      await reset()
    }
  })

  it('testEvent_stackStartsInTheBundledTrigger', async () => {
    expect(boot('app')).toBe(true)
    const fn = trigger()
    expect(typeof fn, 'installed').toBe('function')
    await fn!(P)
    const sent = await sentEvents()
    expect(sent.length, 'one event').toBe(1)
    const frames = sent[0].body.exception.values[0].stacktrace?.frames ?? []
    expect(frames.length, 'stacktrace has frames').toBeGreaterThan(0)
    expect(frames.some((f: { filename?: string }) => String(f.filename).includes('testEvent'))).toBe(true)
  })

  it('testEvent_anyOtherInputSendsNothing', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(boot('app')).toBe(true)
    const fn = trigger()
    expect(typeof fn, 'installed').toBe('function')
    // The last two stringify to P, so only a typeof check refuses them.
    for (const bad of ['wrong', DIGEST, '', 42, { toString: () => P }, [P]]) expect(await fn!(bad), `input ${String(bad)}`).toBeNull()
    expect(await fn!(undefined), 'undefined').toBeNull()
    expect(await fn!(), 'no argument').toBeNull()
    expect((await sentEvents()).length, 'nothing sent').toBe(0)
    expect(err).not.toHaveBeenCalled()
    expect(warn).not.toHaveBeenCalled()

    expect(typeof (await fn!(P)), 'control: the passphrase resolves an id').toBe('string')
    expect((await sentEvents()).length, 'control: the passphrase sends one').toBe(1)
  })

  it('testEvent_notInstalledWithoutDsnOrDigest', async () => {
    expect(boot('app'), 'control: valid DSN and digest').toBe(true)
    expect(typeof trigger(), 'control: installed').toBe('function')

    for (const dsn of ['', ' ', 'not-a-dsn']) {
      delete (window as unknown as { __ascSentryTest?: Trigger }).__ascSentryTest
      await reset()
      expect(boot('app', DIGEST, dsn), `dsn ${JSON.stringify(dsn)}`).toBe(false)
      expect(trigger(), `dsn ${JSON.stringify(dsn)}`).toBeUndefined()
    }
    for (const digest of ['', ' ', 'abc', `${DIGEST}a`, 'z'.repeat(64)]) {
      delete (window as unknown as { __ascSentryTest?: Trigger }).__ascSentryTest
      await reset()
      expect(boot('app', digest), `digest ${JSON.stringify(digest)}: monitoring itself stays on`).toBe(true)
      expect(trigger(), `digest ${JSON.stringify(digest)}`).toBeUndefined()
    }
  })

  it('testEvent_digestIsTrimmedAndCaseFolded', async () => {
    expect(boot('app', `  ${DIGEST.toUpperCase()}\n`)).toBe(true)
    const fn = trigger()
    expect(typeof fn, 'installed').toBe('function')
    expect(typeof (await fn!(P)), 'resolves an id').toBe('string')
    expect((await sentEvents()).length).toBe(1)
  })

  it('testEvent_installingSendsAndStoresNothing', async () => {
    const cookie = vi.spyOn(Document.prototype, 'cookie', 'set').mockImplementation(() => {})
    const store = () => ({ getItem: () => null, setItem: vi.fn(), removeItem: vi.fn(), clear: vi.fn() })
    const local = store()
    const session = store()
    plant(globalThis, 'localStorage', local)
    plant(globalThis, 'sessionStorage', session)

    document.cookie = 'x=1'
    local.setItem('x', '1')
    session.setItem('x', '1')
    expect([cookie.mock.calls.length, local.setItem.mock.calls.length, session.setItem.mock.calls.length], 'control: writes register').toEqual([1, 1, 1])
    for (const m of [cookie, local.setItem, session.setItem]) m.mockClear()

    expect(boot('landing')).toBe(true)
    expect(typeof trigger(), 'installed').toBe('function')
    expect((await sentEvents()).length, 'no event on install').toBe(0)
    expect(cookie).not.toHaveBeenCalled()
    for (const s of [local, session]) for (const m of [s.setItem, s.removeItem, s.clear]) expect(m).not.toHaveBeenCalled()
  })

  it('testEvent_knownAnswerVectorMatchesTheShellDigest', async () => {
    expect(boot('app', DIGEST)).toBe(true)
    const fn = trigger()
    expect(typeof fn, 'installed').toBe('function')
    expect(typeof (await fn!(P)), 'the pinned vector resolves an id').toBe('string')
    expect((await sentEvents()).length, 'one event').toBe(1)
    expect(await fn!('asc-go-live-test-passphrasE'), 'control: one character off').toBeNull()
    expect((await sentEvents()).length, 'still one event').toBe(1)
  })

  it('testEvent_noSubtleCryptoResolvesNull', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    Object.defineProperty(globalThis.crypto, 'subtle', { configurable: true, value: undefined })
    const restore = () => delete (globalThis.crypto as any).subtle
    undo.push(restore)
    expect(globalThis.crypto.subtle, 'control: crypto.subtle is gone').toBeUndefined()
    expect(boot('app')).toBe(true)
    const fn = trigger()
    expect(typeof fn, 'installed').toBe('function')
    await expect(fn!(P)).resolves.toBeNull()
    expect((await sentEvents()).length, 'nothing sent').toBe(0)
    expect(err).not.toHaveBeenCalled()
    expect(warn).not.toHaveBeenCalled()

    restore()
    expect(globalThis.crypto.subtle, 'control: crypto.subtle is back').toBeDefined()
    delete (window as unknown as { __ascSentryTest?: Trigger }).__ascSentryTest
    await reset()
    expect(boot('app')).toBe(true)
    expect(typeof (await trigger()!(P)), 'control: with crypto.subtle the passphrase resolves an id').toBe('string')
    expect((await sentEvents()).length, 'control: one event').toBe(1)
  })
})
