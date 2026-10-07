// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Adversarial coverage for landing's boot read and strip of `state`, `console` and `signin`.
/// <reference types="node" />
import { StrictMode, act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const OTHER = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn_-9'
const NO_WORKSPACE = 'This account has no workspace yet. If you were invited, open the invite link in your email.'
const FAILED = "We couldn't open your workspace. Sign in again."

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let writes: unknown[]
let restoreLocation: (() => void) | undefined

function spyStore() {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      writes.push([k, v])
      map.set(k, String(v))
    },
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    get length() {
      return map.size
    },
  }
}

beforeEach(() => {
  writes = []
  vi.stubGlobal('localStorage', spyStore())
  vi.stubGlobal('sessionStorage', spyStore())
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  restoreLocation?.()
  restoreLocation = undefined
  act(() => root.unmount())
  container.remove()
  window.history.replaceState(null, '', '/')
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string, strict = false): Promise<void> {
  window.history.replaceState(null, '', path)
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(path)
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(strict ? createElement(StrictMode, null, createElement(mod.default)) : createElement(mod.default))
  })
}

// Reads delegate to the real location so the strip still works; only `href` writes are captured.
function captureNavigation(): string[] {
  const assigned: string[] = []
  const original = Object.getOwnPropertyDescriptor(window, 'location')
  const real = window.location
  const stub = {
    get href() {
      return real.href
    },
    set href(v: string) {
      assigned.push(v)
    },
    get search() {
      return real.search
    },
    get pathname() {
      return real.pathname
    },
    get hash() {
      return real.hash
    },
    get origin() {
      return real.origin
    },
  }
  Object.defineProperty(window, 'location', { value: stub, writable: true, configurable: true })
  restoreLocation = () => {
    if (original) Object.defineProperty(window, 'location', original)
  }
  return assigned
}

function dialogs(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(DIALOG))
}

function onlyDialog(): HTMLElement {
  const d = dialogs()
  expect(d.length).toBe(1)
  return d[0]
}

function pageMounted(): void {
  expect(document.querySelectorAll('header button').length).toBeGreaterThan(0)
}

async function closeDialog(): Promise<void> {
  await act(async () => {
    onlyDialog().querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.click()
  })
  expect(dialogs().length).toBe(0)
}

async function openFromNav(): Promise<void> {
  const b = Array.from(document.querySelectorAll('header button')).find((x) => x.textContent?.trim() === 'Sign in')
  expect(b).toBeDefined()
  await act(async () => (b as HTMLButtonElement).click())
}

describe('AUTH-05-07 adversarial: boot params', () => {
  it('a repeated state is ignored and every copy is stripped', async () => {
    vi.stubEnv('VITE_OPS_URL', 'https://ops.x')
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x')
    const assigned = captureNavigation()
    await bootAt(`/?state=${STATE}&console=ops&signin=ready&state=${OTHER}&console=support`)
    const d = onlyDialog()
    expect(d.querySelectorAll('input').length).toBe(0)
    expect(d.textContent).toContain('Continue with email')
    expect(window.location.search).toBe('')
    // Two console values hold no target: the bounce goes to the app.
    const cont = Array.from(d.querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Continue with email')
    expect(cont.length).toBe(1)
    await act(async () => cont[0].click())
    expect(assigned).toEqual(['https://app.x?auth=start'])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('an empty signin is stripped and opens nothing', async () => {
    await bootAt('/?signin=&keep=1')
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('?keep=1')
  })

  it('a repeated signin is stripped whole, and the first value decides', async () => {
    await bootAt('/?a=1&signin=failed&b=2&signin=no-workspace')
    const alerts = Array.from(onlyDialog().querySelectorAll('[role="alert"]'))
    expect(alerts.length).toBe(1)
    expect(alerts[0].textContent).toContain(FAILED)
    expect(window.location.search).toBe('?a=1&b=2')
  })

  it('the strip keeps the path and the hash', async () => {
    await bootAt(`/?utm_source=x&state=${STATE}&console=ops&signin=ready#faq`)
    onlyDialog()
    expect(window.location.pathname).toBe('/')
    expect(window.location.search).toBe('?utm_source=x')
    expect(window.location.hash).toBe('#faq')

    // A path that is not `/` tells a kept path from a rebuilt one.
    await act(async () => root.unmount())
    root = createRoot(container)
    vi.resetModules()
    await bootAt(`/privacy?state=${STATE}&console=ops&keep=1#top`)
    expect(window.location.pathname).toBe('/privacy')
    expect(window.location.search).toBe('?keep=1')
    expect(window.location.hash).toBe('#top')
  })

  it('verified and verify are stripped whole with the sign-in params; a sign-in outcome still opens', async () => {
    await bootAt(`/?keep=1&state=${STATE}&signin=ready&verified=1&verify=failed&verified=0#faq`)
    onlyDialog()
    expect(window.location.search).toBe('?keep=1')
    expect(window.location.hash).toBe('#faq')
  })

  it('a boot with neither param does not touch history', async () => {
    const replace = vi.spyOn(window.history, 'replaceState')
    await bootAt('/?keep=1')
    replace.mockClear()
    const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
    await act(async () => root.unmount())
    root = createRoot(container)
    await act(async () => root.render(createElement(mod.default)))
    pageMounted()
    expect(replace).not.toHaveBeenCalled()
    expect(window.location.search).toBe('?keep=1')
  })

  it('a malformed state alone opens nothing and is stripped', async () => {
    await bootAt('/?state=short')
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('')
  })

  it('configured: a front-door state alone opens nothing but is held', async () => {
    // Only a `signin` outcome opens the modal; the front-door bounce carries none.
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}`)
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('')

    await openFromNav()
    const d = onlyDialog()
    expect(d.querySelectorAll('[role="alert"]').length).toBe(0)
    expect(d.textContent).not.toContain('Continue with email')
    const email = d.querySelectorAll<HTMLInputElement>('input[type="email"]')
    const password = d.querySelectorAll<HTMLInputElement>('input[type="password"]')
    expect(email.length).toBe(1)
    expect(password.length).toBe(1)
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setValue.call(email[0], 'ada@okafor.ng')
      email[0].dispatchEvent(new Event('input', { bubbles: true }))
      setValue.call(password[0], 'pw')
      password[0].dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((JSON.parse(init.body as string) as { state: unknown }).state).toBe(STATE)
  })

  it('StrictMode: the boot read survives the double effect', async () => {
    await bootAt(`/?state=${STATE}&signin=no-workspace`, true)
    const d = onlyDialog()
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(d.querySelector('[role="alert"]')?.textContent).toContain(NO_WORKSPACE)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AUTH-05-07 adversarial: after boot', () => {
  it('the boot error clears on close, and the held state survives a reopen', async () => {
    await bootAt(`/?state=${STATE}&signin=failed`)
    expect(onlyDialog().querySelector('[role="alert"]')?.textContent).toContain(FAILED)
    await closeDialog()
    await openFromNav()
    const d = onlyDialog()
    expect(d.querySelectorAll('[role="alert"]').length).toBe(0)
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(d.textContent).not.toContain('Continue with email')
  })

  it('no state is written to storage on any path', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async () => new Response(JSON.stringify({ error: 'x' }), { status: 401, headers: { 'Content-Type': 'application/json' } }))
      .mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}&signin=failed`)
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    for (let i = 0; i < 2; i++) {
      const d = onlyDialog()
      await act(async () => {
        for (const [sel, v] of [['input[type="email"]', 'ada@okafor.ng'], ['input[type="password"]', 'pw']]) {
          const el = d.querySelector<HTMLInputElement>(sel)!
          setValue.call(el, v)
          el.dispatchEvent(new Event('input', { bubbles: true }))
        }
      })
      await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
      await act(async () => {
        await new Promise((r) => setTimeout(r, 0))
      })
    }
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await closeDialog()
    await openFromNav()
    for (const w of writes) expect(JSON.stringify(w)).not.toContain(STATE)
    expect(document.cookie).not.toContain(STATE)
    expect(window.location.href).not.toContain(STATE)
  })
})

describe('AUTH-05-07 adversarial: unconfigured landing (D7: production unchanged until U2)', () => {
  it('unconfigured: a front-door state alone opens nothing', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    await bootAt(`/?state=${STATE}`)
    pageMounted()
    expect(dialogs().length).toBe(0)
    expect(window.location.search).toBe('')
  })
})

// Ends the current boot: restores `location`, then mounts a fresh root on re-imported modules.
async function remountFresh(): Promise<void> {
  restoreLocation?.()
  restoreLocation = undefined
  await act(async () => root.unmount())
  root = createRoot(container)
  vi.resetModules()
}

const NOT_STAFF = 'This account cannot open the ASComply consoles.'
const CONTINUE = 'Continue with email'

function continueButtons(d: HTMLElement): HTMLButtonElement[] {
  return Array.from(d.querySelectorAll('button')).filter((b) => b.textContent?.trim() === CONTINUE)
}

async function clickContinue(): Promise<void> {
  const b = continueButtons(onlyDialog())
  expect(b.length).toBe(1)
  await act(async () => b[0].click())
}

async function fillAndSubmit(d: HTMLElement): Promise<void> {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  const email = d.querySelectorAll<HTMLInputElement>('input[type="email"]')
  const password = d.querySelectorAll<HTMLInputElement>('input[type="password"]')
  expect(email.length).toBe(1)
  expect(password.length).toBe(1)
  await act(async () => {
    setValue.call(email[0], 'ada@okafor.ng')
    email[0].dispatchEvent(new Event('input', { bubbles: true }))
    setValue.call(password[0], 'pw')
    password[0].dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

function stubCode() {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify({ code: 'the-code' }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('adversarial: the console target', () => {
  beforeEach(() => {
    vi.stubEnv('VITE_OPS_URL', 'https://ops.x')
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x')
  })

  it('a single console and no state reaches that console through App, modal and form', async () => {
    const cases: [string, string][] = [
      ['ops', 'https://ops.x?auth=start'],
      ['support', 'https://support.x?auth=start'],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [target, want] of cases) {
      const assigned = captureNavigation()
      await bootAt(`/?console=${target}`)
      pageMounted()
      expect(dialogs().length).toBe(0)
      expect(window.location.search).toBe('')
      await openFromNav()
      await clickContinue()
      expect(assigned, target).toEqual([want])
      await remountFresh()
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a console value that is not exactly ops or support holds no target and never reaches the URL', async () => {
    const refused = ['OPS', 'Support', 'ops%20', '%20ops', '', 'ops,support', 'ops%00', 'https%3A%2F%2Fevil.example', '%2F%2Fevil.example', 'ops%40evil.example', 'app']
    for (const v of refused) {
      const assigned = captureNavigation()
      await bootAt(`/?console=${v}&signin=ready`)
      expect(window.location.search, v).toBe('')
      await clickContinue()
      expect(assigned, v).toEqual(['https://app.x?auth=start'])
      await remountFresh()
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a URL in console never receives the code', async () => {
    stubCode()
    const assigned = captureNavigation()
    await bootAt(`/?state=${STATE}&console=${encodeURIComponent('https://evil.example/x')}&signin=ready`)
    await fillAndSubmit(onlyDialog())
    expect(assigned).toEqual(['https://app.x?handoff=the-code'])
    for (const a of assigned) expect(a).not.toContain('evil')
  })

  it('a console with an unset base navigates nowhere and never falls back to the app', async () => {
    const cases: [string, string, string][] = [
      ['ops', 'VITE_OPS_URL', 'https://support.x'],
      ['support', 'VITE_SUPPORT_URL', 'https://ops.x'],
    ]
    for (const [target, env] of cases) {
      vi.stubEnv(env, '')
      const fetchMock = stubCode()
      const assigned = captureNavigation()
      await bootAt(`/?state=${STATE}&console=${target}&signin=ready`)
      await fillAndSubmit(onlyDialog())
      expect(fetchMock, target).toHaveBeenCalledTimes(1)
      expect(assigned, `${target} submit`).toEqual([])
      await remountFresh()

      const bounced = captureNavigation()
      await bootAt(`/?console=${target}&signin=ready`)
      await clickContinue()
      expect(bounced, `${target} continue`).toEqual([])
      await remountFresh()
      vi.stubEnv('VITE_OPS_URL', 'https://ops.x')
      vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x')
    }
  })

  it('not-staff with a held console keeps the target for the retry', async () => {
    stubCode()
    const assigned = captureNavigation()
    await bootAt(`/?state=${STATE}&console=support&signin=not-staff`)
    const d = onlyDialog()
    expect(Array.from(d.querySelectorAll('[role="alert"]')).map((a) => a.textContent)).toEqual([expect.stringContaining(NOT_STAFF)])
    await fillAndSubmit(d)
    expect(assigned).toEqual(['https://support.x?handoff=the-code'])
  })

  it('not-staff with a held console and no state keeps the alert and bounces through that console', async () => {
    const assigned = captureNavigation()
    await bootAt('/?console=ops&signin=not-staff')
    const alerts = Array.from(onlyDialog().querySelectorAll('[role="alert"]'))
    expect(alerts.length).toBe(1)
    expect(alerts[0].textContent).toContain(NOT_STAFF)
    await clickContinue()
    expect(assigned).toEqual(['https://ops.x?auth=start'])
  })

  it('a bare console param is stripped alone, and a repeated one in every copy', async () => {
    await bootAt('/?console&keep=1')
    pageMounted()
    expect(window.location.search).toBe('?keep=1')
    await act(async () => root.unmount())
    root = createRoot(container)
    vi.resetModules()
    await bootAt('/?console=ops&a=1&console=ops&console=support&b=2')
    pageMounted()
    expect(window.location.search).toBe('?a=1&b=2')
    expect(dialogs().length).toBe(0)
  })

  it('no dialog control navigates to a persona URL', async () => {
    const assigned = captureNavigation()
    await bootAt('/?console=ops&signin=ready')
    const d = onlyDialog()
    const all = Array.from(d.querySelectorAll<HTMLButtonElement>('button'))
    // Close last: it unmounts the dialog and would turn later clicks into no-ops.
    const buttons = [...all.filter((b) => b.getAttribute('aria-label') !== 'Close'), ...all.filter((b) => b.getAttribute('aria-label') === 'Close')]
    expect(buttons.length, 'control: the dialog has controls to click').toBeGreaterThanOrEqual(2)
    for (const b of buttons) await act(async () => b.click())
    expect(assigned.length, 'control: a real control still navigates').toBeGreaterThan(0)
    expect(assigned.filter((h) => h.includes('persona=')), assigned.join('\n')).toEqual([])
    expect(d.querySelectorAll('[data-persona]').length).toBe(0)
  })

  it('StrictMode: the held console survives the double effect and the strip', async () => {
    stubCode()
    const assigned = captureNavigation()
    await bootAt(`/?state=${STATE}&console=ops&signin=ready`, true)
    expect(window.location.search).toBe('')
    await fillAndSubmit(onlyDialog())
    expect(assigned).toEqual(['https://ops.x?handoff=the-code'])
  })

  it('the console target is held in memory only', async () => {
    const assigned = captureNavigation()
    await bootAt('/?console=ops&signin=ready')
    await clickContinue()
    expect(assigned).toEqual(['https://ops.x?auth=start'])
    for (const w of writes) expect(JSON.stringify(w)).not.toContain('console')
    expect(document.cookie).not.toContain('console')
    expect(window.location.href).not.toContain('console')
  })
})
