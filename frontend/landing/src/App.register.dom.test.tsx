// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The registration window, driven through App: entries, form, outcomes. Setup mirrors App.signIn.dom.test.tsx.
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'
import { FREE_MAIL_REFUSED } from './register'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const LOGIN = 'Platform login'
const CREATE = 'Create an account'
const DIALOG = '[role="dialog"]'
const REGISTER_DIALOG = `${DIALOG}[aria-label="${CREATE}"]`
const NOTICE = '[aria-label="Cookie notice"]'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const DIVIDER = 'or explore with a demo profile'

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, String(v))
    },
  }
}

let container: HTMLDivElement
let root: Root
let originalStorage: PropertyDescriptor | undefined

beforeEach(() => {
  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true })
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.stubEnv('VITE_REGISTRATION_OPEN', 'true')
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
  else delete (globalThis as { localStorage?: unknown }).localStorage
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

async function mountApp(): Promise<void> {
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

// Drops the mounted tree and mounts a new one, so a test can change the env or repeat a flow.
async function remountApp(): Promise<void> {
  act(() => root.unmount())
  root = createRoot(container)
  await mountApp()
}

async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
  }
}

function buttons(scope: ParentNode): HTMLButtonElement[] {
  return Array.from(scope.querySelectorAll('button'))
}

function byText(scope: ParentNode, text: string): HTMLElement | undefined {
  return Array.from(scope.querySelectorAll<HTMLElement>('button, a')).find((b) => b.textContent?.trim() === text)
}

async function clickByText(scope: ParentNode, text: string): Promise<void> {
  const el = byText(scope, text)
  expect(el, `expected a control labelled "${text}" within the given scope`).toBeDefined()
  await act(async () => {
    el!.click()
  })
}

const header = () => document.querySelector('header')!

async function openMenu(): Promise<Element> {
  const burger = document.querySelector<HTMLButtonElement>('header button.a-burger')
  expect(burger, 'expected the header burger').not.toBeNull()
  await act(async () => {
    burger!.click()
  })
  const menu = document.querySelector('.a-menu')
  expect(menu, 'control: the burger opened the menu').not.toBeNull()
  return menu!
}

async function openRegistration(): Promise<HTMLElement> {
  await clickByText(header(), CREATE)
  const d = document.querySelector<HTMLElement>(REGISTER_DIALOG)
  expect(d, 'expected the registration window').not.toBeNull()
  return d!
}

async function bootAt(path: string): Promise<void> {
  window.history.replaceState(null, '', path)
  await mountApp()
}

const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!

function labelled(d: ParentNode, label: string): HTMLInputElement {
  const l = Array.from(d.querySelectorAll('label')).find((x) => x.textContent?.trim() === label)
  expect(l, `expected a label "${label}"`).toBeDefined()
  const input = document.getElementById(l!.htmlFor)
  expect(input, `expected the input of "${label}"`).not.toBeNull()
  return input as HTMLInputElement
}

async function type(input: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    setValue.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

type Fill = { email?: string; password?: string; name?: string; workspace?: string; kind?: 'firm' | 'in_house' | null }
const FULL: Required<Fill> = { email: 'ada@okafor.ng', password: 'pw-123456', name: 'Ada Okafor', workspace: 'Okafor & Partners', kind: 'firm' }

async function fillForm(d: HTMLElement, over: Fill = {}): Promise<void> {
  const v = { ...FULL, ...over }
  await type(labelled(d, 'Work email'), v.email)
  await type(labelled(d, 'Password'), v.password)
  await type(labelled(d, 'Your name'), v.name)
  await type(labelled(d, 'Workspace name'), v.workspace)
  if (v.kind) {
    const radio = d.querySelector<HTMLInputElement>(`input[type="radio"][value="${v.kind}"]`)
    expect(radio, `expected the ${v.kind} radio`).not.toBeNull()
    await act(async () => {
      radio!.click()
    })
  }
}

async function submit(d: HTMLElement): Promise<void> {
  const btn = d.querySelector<HTMLButtonElement>('button[type="submit"]')
  expect(btn, 'expected the submit button').not.toBeNull()
  await act(async () => {
    btn!.click()
  })
  await settle()
}

function alerts(d: ParentNode): string[] {
  return Array.from(d.querySelectorAll('[role="alert"]')).map((a) => a.textContent?.trim() ?? '')
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function stubFetch(make: () => Response | Promise<Response>) {
  const fetchMock = vi.fn().mockImplementation(async () => make())
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('the entries', () => {
  it('registration open: the header entry follows Platform login and opens the registration window', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length, 'control: no dialog at rest').toBe(0)
    const login = buttons(header()).find((b) => b.textContent?.trim() === LOGIN)
    expect(login, 'control: Platform login is in the header').toBeDefined()

    const create = login!.nextElementSibling as HTMLElement | null
    expect(create?.textContent?.trim(), 'the entry right after Platform login').toBe(CREATE)
    expect(create!.tagName).toBe('BUTTON')
    expect(create!.classList.contains('a-create')).toBe(true)
    expect(create!.classList.contains('a-link')).toBe(true)

    await act(async () => {
      create!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
  })

  it('registration open: the mobile menu entry follows Platform login and opens the registration window', async () => {
    await mountApp()
    const menu = await openMenu()
    const login = buttons(menu).find((b) => b.textContent?.trim() === LOGIN)
    expect(login, 'control: Platform login is in the menu').toBeDefined()

    const create = login!.nextElementSibling as HTMLElement | null
    expect(create?.textContent?.trim(), 'the entry right after Platform login').toBe(CREATE)
    expect(create!.classList.contains('a-link')).toBe(true)
    expect(create!.classList.contains('a-menu-login')).toBe(true)

    await act(async () => {
      create!.click()
    })
    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
  })

  it('the menu entry closes the menu first', async () => {
    await mountApp()
    const burger = document.querySelector<HTMLButtonElement>('header button.a-burger')!
    const menu = await openMenu()
    expect(document.querySelectorAll(DIALOG).length, 'control: no dialog yet').toBe(0)

    await clickByText(menu, CREATE)

    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
    expect(document.querySelector('.a-menu'), 'the menu closes before the window opens').toBeNull()
    expect(burger.getAttribute('aria-expanded')).toBe('false')
  })

  it('registration open: the sign-in window link closes it and opens the registration window', async () => {
    await mountApp()
    await clickByText(header(), LOGIN)
    const signIn = document.querySelector<HTMLElement>(`${DIALOG}[aria-label="${LOGIN}"]`)
    expect(signIn, 'control: the sign-in window opened').not.toBeNull()

    await clickByText(signIn!, CREATE)

    expect(document.querySelectorAll(DIALOG).length, 'one window at a time').toBe(1)
    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
    expect(document.querySelectorAll(`${DIALOG}[aria-label="${LOGIN}"]`).length, 'the sign-in window is gone').toBe(0)
  })

  it('the sign-in window link shows with a held state too', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    const signIn = document.querySelector<HTMLElement>(`${DIALOG}[aria-label="${LOGIN}"]`)
    expect(signIn, 'control: boot opened the sign-in window').not.toBeNull()
    expect(signIn!.querySelectorAll('input[type="email"]').length, 'control: the email form shows').toBe(1)

    await clickByText(signIn!, CREATE)

    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
    expect(document.querySelectorAll(`${DIALOG}[aria-label="${LOGIN}"]`).length).toBe(0)
  })

  it('the sign-in window link shows without a held state too', async () => {
    await mountApp()
    await clickByText(header(), LOGIN)
    const signIn = document.querySelector<HTMLElement>(DIALOG)!
    expect(byText(signIn, 'Continue with email'), 'control: no held state, the bounce button shows').toBeDefined()
    expect(byText(signIn, CREATE)).toBeDefined()
  })

  it('registration closed: no entry anywhere, and the same page shows one when open', async () => {
    // Positive half first: open renders the entries, so the closed assertions below cannot pass on a missing page.
    await mountApp()
    expect(byText(header(), CREATE), 'control: open shows the header entry').toBeDefined()

    for (const flag of [undefined, 'false']) {
      vi.stubEnv('VITE_REGISTRATION_OPEN', flag)
      await remountApp()
      expect(byText(header(), LOGIN), `control: Platform login stays (flag ${flag})`).toBeDefined()
      expect(byText(header(), CREATE), `header, flag ${flag}`).toBeUndefined()
      const menu = await openMenu()
      expect(byText(menu, LOGIN), 'control: menu login stays').toBeDefined()
      expect(byText(menu, CREATE), `menu, flag ${flag}`).toBeUndefined()
      await clickByText(menu, LOGIN)
      const signIn = document.querySelector<HTMLElement>(DIALOG)!
      expect(signIn.querySelectorAll('input[type="email"]').length + (byText(signIn, 'Continue with email') ? 1 : 0), 'control: the sign-in form shows').toBe(1)
      expect(signIn.textContent).not.toContain(CREATE)
      expect(document.body.textContent).not.toContain(CREATE)
    }
  })

  it('the sign-in window link sits outside the persona list and before the divider', async () => {
    await mountApp()
    await clickByText(header(), LOGIN)
    const signIn = document.querySelector<HTMLElement>(DIALOG)!
    const picker = signIn.querySelector('[data-testid="persona-picker"]')
    expect(picker, 'control: the persona picker is in the window').not.toBeNull()
    const link = byText(signIn, CREATE)
    expect(link, 'expected the link in the sign-in window').toBeDefined()

    expect(link!.closest('[data-testid="persona-picker"]')).toBeNull()
    const walker = document.createTreeWalker(signIn, NodeFilter.SHOW_TEXT)
    let divider: Node | null = null
    for (let n = walker.nextNode(); n && !divider; n = walker.nextNode()) if (n.textContent?.includes(DIVIDER)) divider = n
    expect(divider, 'control: the divider row is in the window').toBeDefined()
    expect(link!.compareDocumentPosition(divider!) & Node.DOCUMENT_POSITION_FOLLOWING, 'the link precedes the divider').toBeTruthy()
    const form = signIn.querySelector('form') ?? byText(signIn, 'Continue with email')
    expect(form!.compareDocumentPosition(link!) & Node.DOCUMENT_POSITION_FOLLOWING, 'the link sits under the sign-in form').toBeTruthy()
    expect(link!.parentElement!.textContent).toContain('New to ASComply?')
  })
})

describe('the window and its form', () => {
  it('the window asks five things and preselects no kind', async () => {
    await mountApp()
    const d = await openRegistration()
    expect(d.getAttribute('aria-modal')).toBe('true')

    const email = labelled(d, 'Work email')
    const password = labelled(d, 'Password')
    const name = labelled(d, 'Your name')
    const workspace = labelled(d, 'Workspace name')
    expect(email.type).toBe('email')
    expect(email.autocomplete).toBe('email')
    expect(password.type).toBe('password')
    expect(password.autocomplete).toBe('new-password')
    const order = [email, password, name, workspace]
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING, `field ${i} follows field ${i - 1}`).toBeTruthy()
    }

    const legend = d.querySelector('fieldset > legend')
    expect(legend?.textContent?.trim()).toBe('How will this workspace file invoices?')
    const radios = Array.from(d.querySelectorAll<HTMLInputElement>('fieldset input[type="radio"]'))
    expect(radios.map((r) => r.value)).toEqual(['firm', 'in_house'])
    expect(radios.every((r) => r.name === 'reg-kind')).toBe(true)
    expect(radios.map((r) => r.checked)).toEqual([false, false])
    const kindLabels = Array.from(d.querySelectorAll('fieldset label')).map((l) => l.textContent?.trim())
    expect(kindLabels).toEqual(['For clients — an accounting or tax firm', 'For our own company — in-house'])
    expect(d.querySelector('button[type="submit"]')?.textContent?.trim()).toBe('Create account →')
    expect(d.textContent).toContain('Create your workspace')
  })

  it('no kind, no request', async () => {
    const fetchMock = stubFetch(() => json(202, { status: 'verification_pending' }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { kind: null })

    await submit(d)

    expect(alerts(d)).toEqual(['Choose how this workspace files invoices.'])
    expect(document.activeElement).toBe(d.querySelector('input[type="radio"][value="firm"]'))
    expect(fetchMock).not.toHaveBeenCalled()
    expect(d.querySelector('button[type="submit"]')?.hasAttribute('disabled'), 'never disabled for validation').toBe(false)
  })

  it('an empty name blocks the submit, and the first invalid field takes focus', async () => {
    const fetchMock = stubFetch(() => json(202, { status: 'verification_pending' }))
    await mountApp()
    const d = await openRegistration()

    await submit(d)
    expect(alerts(d).length, 'every missing field shows its message').toBe(5)
    expect(document.activeElement, 'focus on the first invalid field').toBe(labelled(d, 'Work email'))
    expect(fetchMock).not.toHaveBeenCalled()

    await fillForm(d, { name: '' })
    await submit(d)
    expect(alerts(d)).toEqual(['Enter your name.'])
    expect(document.activeElement).toBe(labelled(d, 'Your name'))
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('a valid submit posts the five values once', async () => {
    const fetchMock = stubFetch(() => json(202, { status: 'verification_pending' }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { email: '  ada@okafor.ng ', kind: 'in_house' })

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/register')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({
      email: 'ada@okafor.ng',
      password: FULL.password,
      display_name: FULL.name,
      workspace_name: FULL.workspace,
      kind: 'in_house',
    })
  })
})

describe('the outcomes', () => {
  it('a free-mail refusal is inline and keeps the other fields', async () => {
    const fetchMock = stubFetch(() => json(400, { error: FREE_MAIL_REFUSED }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { email: 'ada@gmail.com', kind: 'in_house' })

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const email = labelled(d, 'Work email')
    const alert = Array.from(d.querySelectorAll('[role="alert"]')).find((a) => a.textContent?.includes(FREE_MAIL_REFUSED))
    expect(alert, 'expected the policy text').toBeDefined()
    expect(email.getAttribute('aria-describedby')).toBe(alert!.id)
    expect(alert!.id).not.toBe('')
    expect(labelled(d, 'Password').value).toBe(FULL.password)
    expect(labelled(d, 'Your name').value).toBe(FULL.name)
    expect(labelled(d, 'Workspace name').value).toBe(FULL.workspace)
    expect(email.value).toBe('ada@gmail.com')
    expect(d.querySelector<HTMLInputElement>('input[type="radio"][value="in_house"]')!.checked).toBe(true)
    expect(d.querySelector<HTMLInputElement>('input[type="radio"][value="firm"]')!.checked).toBe(false)

    await type(email, 'ada@okafor.ng')
    expect(alerts(d).join('|')).not.toContain(FREE_MAIL_REFUSED)
    expect(email.hasAttribute('aria-describedby')).toBe(false)
    expect(labelled(d, 'Password').value, 'editing the email keeps the rest').toBe(FULL.password)
  })

  it('every 202 shows the same check-your-email view', async () => {
    stubFetch(() => json(202, { status: 'verification_pending' }))
    const views: [string, string][] = []
    for (const address of ['ada@okafor.ng', 'tunde@lagos-tax.ng']) {
      await remountApp()
      const d = await openRegistration()
      await fillForm(d, { email: address })
      await submit(d)

      expect(d.querySelectorAll('input').length, 'the form is replaced').toBe(0)
      expect(d.querySelector('button[type="submit"]')).toBeNull()
      const heading = Array.from(d.querySelectorAll('h1, h2, h3')).find((h) => h.textContent?.trim() === 'Check your email')
      expect(heading, 'expected the Check your email heading').toBeDefined()
      expect(d.textContent).toContain(`If this address can be registered, a confirmation link is on its way to ${address}. Open it, then sign in.`)
      expect(byText(d, 'Close'), 'expected a Close button').toBeDefined()
      views.push([address, d.textContent ?? ''])
    }
    const [[a1, t1], [a2, t2]] = views
    expect(t1).not.toBe(t2)
    expect(t1.replaceAll(a1, '<address>')).toBe(t2.replaceAll(a2, '<address>'))
  })

  it('the check-your-email Close button closes the window', async () => {
    stubFetch(() => json(202, { status: 'verification_pending' }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await submit(d)
    expect(d.textContent, 'control: the view shows').toContain('Check your email')

    await clickByText(d, 'Close')

    expect(document.querySelectorAll(DIALOG).length).toBe(0)
  })

  it('closed, throttled, unavailable and gateway-worded answers show fixed copy and re-enable the form', async () => {
    const cases: [string, () => Response | Promise<Response>, string][] = [
      ['503', () => json(503, { error: 'registration is closed' }), 'Registration is not open yet.'],
      ['429', () => json(429, { error: 'rate limited' }), 'Too many attempts. Try again in a minute.'],
      ['502', () => json(502, { error: 'bad gateway' }), 'Registration is unavailable right now. Try again shortly.'],
      ['network', () => Promise.reject(new TypeError('Failed to fetch')), 'Registration is unavailable right now. Try again shortly.'],
      ['other 400', () => json(400, { error: 'Password should be at least 8 characters.' }), 'Password should be at least 8 characters.'],
    ]
    for (const [name, make, copy] of cases) {
      stubFetch(make)
      await remountApp()
      const d = await openRegistration()
      await fillForm(d)

      await submit(d)

      expect(alerts(d), name).toEqual([copy])
      const btn = d.querySelector<HTMLButtonElement>('button[type="submit"]')
      expect(btn, `${name}: the form is still shown`).not.toBeNull()
      expect(btn!.disabled, `${name}: submit re-enabled`).toBe(false)
      expect(btn!.textContent?.trim()).toBe('Create account →')
      expect(labelled(d, 'Work email').disabled, `${name}: fields re-enabled`).toBe(false)
      expect(labelled(d, 'Password').value, `${name}: values kept`).toBe(FULL.password)
    }
  })

  it('a second submit while creating sends nothing', async () => {
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)

    await submit(d)
    const btn = d.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(btn.textContent?.trim()).toBe('Creating…')
    expect(btn.disabled).toBe(true)
    expect(fetchMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      btn.click()
      d.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await settle()
    expect(fetchMock, 'neither a click nor a direct submit sends again').toHaveBeenCalledTimes(1)
  })
})

describe('the window as a dialog', () => {
  it('the cookie notice yields to the registration window, and Escape closes it', async () => {
    await mountApp()
    const notice = document.querySelector(NOTICE)
    expect(notice, 'control: no stored consent shows the notice').not.toBeNull()
    expect(notice!.hasAttribute('inert'), 'control: not suppressed at rest').toBe(false)

    await openRegistration()
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'suppressed while the window is open').toBe(true)

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'the notice returns').toBe(false)
  })

  it('a click inside the card keeps the window open and a scrim click closes it', async () => {
    await mountApp()
    const d = await openRegistration()

    await act(async () => {
      labelled(d, 'Work email').click()
    })
    expect(document.querySelectorAll(REGISTER_DIALOG).length, 'a click in the card does not close').toBe(1)

    await act(async () => {
      d.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
  })

  it('the header Close control closes the window and the entry opens it again', async () => {
    await mountApp()
    const d = await openRegistration()
    await act(async () => {
      d.querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)

    await openRegistration()
    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
  })
})
