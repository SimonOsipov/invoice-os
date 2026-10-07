// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
/// <reference types="node" />
// The registration window, driven through App: entries, form, outcomes. Setup mirrors App.signIn.dom.test.tsx.
import { act, createElement, type CSSProperties } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'
import { CHECK_INPUT_STYLE, CHECK_LABEL_STYLE, DemoLeadForm } from './components/DemoLeadForm'
import { MARKETING_CONSENT_TEXT } from './components/MarketingConsent'
import { PRODUCT_EMAIL_NOTICE } from './components/RegisterModal'
import { FREE_MAIL_REFUSED } from './register'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const LOGIN = 'Sign in'
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

async function mountApp(seed = true): Promise<void> {
  if (seed && !window.location.search) window.history.replaceState(null, '', `/?state=${STATE}`)
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
  await clickByText(header(), LOGIN)
  await clickByText(document.querySelector<HTMLElement>(DIALOG)!, CREATE)
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
  it('header_showsSignInAndBookADemoOnly', async () => {
    await mountApp()
    expect(buttons(header()).map((b) => b.textContent?.trim() || b.getAttribute('aria-label'))).toEqual([LOGIN, 'Book a demo', 'Menu'])
  })

  it('menu_hasNoCreateEntry', async () => {
    await mountApp()
    const menu = await openMenu()
    expect(buttons(menu).map((b) => b.textContent?.trim())).toEqual([LOGIN])
    expect(menu.textContent).not.toContain(CREATE)
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
    window.history.replaceState(null, '', '/')
    await mountApp(false)
    await clickByText(header(), LOGIN)
    const signIn = document.querySelector<HTMLElement>(DIALOG)!
    expect(byText(signIn, 'Continue with email'), 'control: no held state, the bounce button shows').toBeDefined()
    expect(byText(signIn, CREATE)).toBeDefined()
  })

  it('header_hasNoCreateEntry_whenRegistrationClosed, signInWindow_hasNoCreateRoute_whenRegistrationClosed', async () => {
    await mountApp()
    await clickByText(header(), LOGIN)
    expect(byText(document.querySelector<HTMLElement>(DIALOG)!, CREATE), 'control: open shows the window link').toBeDefined()

    for (const flag of [undefined, 'false']) {
      vi.stubEnv('VITE_REGISTRATION_OPEN', flag)
      await remountApp()
      expect(buttons(header()).map((b) => b.textContent?.trim() || b.getAttribute('aria-label')), `header, flag ${flag}`).toEqual([LOGIN, 'Book a demo', 'Menu'])
      const menu = await openMenu()
      expect(buttons(menu).map((b) => b.textContent?.trim()), `menu, flag ${flag}`).toEqual([LOGIN])
      await clickByText(menu, LOGIN)
      const signIn = document.querySelector<HTMLElement>(DIALOG)!
      expect(signIn.querySelectorAll('input[type="email"]').length, 'control: the sign-in form shows').toBe(1)
      expect(signIn.textContent).not.toContain(CREATE)
      expect(document.body.textContent).not.toContain(CREATE)
    }
  })

  it('the create link sits under the form', async () => {
    await mountApp()
    await clickByText(header(), LOGIN)
    const signIn = document.querySelector<HTMLElement>(DIALOG)!
    const link = byText(signIn, CREATE)
    expect(link, 'expected the link in the sign-in window').toBeDefined()
    const form = signIn.querySelector('form') ?? byText(signIn, 'Continue with email')
    expect(form, 'control: the sign-in form (or its bounce) is in the window').toBeTruthy()
    expect(form!.compareDocumentPosition(link!) & Node.DOCUMENT_POSITION_FOLLOWING, 'the link sits under the sign-in form').toBeTruthy()
    expect(link!.parentElement!.textContent).toContain('New to ASComply?')
    expect(signIn.querySelector('[data-testid="persona-picker"]'), 'no persona picker in the window').toBeNull()
    expect(signIn.textContent).not.toContain(DIVIDER)
    expect(link!.parentElement!.nextElementSibling, 'the link row ends the window body').toBeNull()
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
    expect(d.querySelector('fieldset')!.getAttribute('aria-describedby'), 'the kind message is tied to its fieldset').toBe(d.querySelector('[role="alert"]')!.id)
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
    await fillForm(d, { kind: 'in_house' })

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
      expect(d.textContent).toContain(`If this address can be registered, a confirmation link is on its way to ${address}. Open it, confirm your email, then sign in.`)
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
      expect(labelled(d, 'Work email').hasAttribute('aria-describedby'), `${name}: a form-level message is not tied to the email field`).toBe(false)
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

// The story's [copy] sentences, pinned once: a wording change is a deliberate edit here and in the story.
const STORY_NOTICE = 'We will email you about your account and the service. This is part of using ASComply Africa.'
const STORY_MARKETING = 'Allow marketing communications: ASComply Africa may email me product news and offers. I can unsubscribe at any time.'
const MARKETING = '#reg-marketing'

function marketingBox(d: ParentNode): HTMLInputElement {
  const box = d.querySelector<HTMLInputElement>(MARKETING)
  expect(box, 'expected the marketing checkbox #reg-marketing').not.toBeNull()
  return box as HTMLInputElement
}

function noticeOf(d: ParentNode): HTMLElement {
  expect(PRODUCT_EMAIL_NOTICE.trim(), 'the notice copy is not blank').not.toBe('')
  const n = Array.from(d.querySelectorAll<HTMLElement>('p')).find((p) => p.textContent?.trim() === PRODUCT_EMAIL_NOTICE)
  expect(n, 'expected the product-email notice paragraph').toBeDefined()
  return n as HTMLElement
}

async function tick(d: ParentNode): Promise<void> {
  await act(async () => {
    marketingBox(d).click()
  })
}

function sentBody(fetchMock: { mock: { calls: unknown[][] } }, call = 0): Record<string, unknown> {
  return JSON.parse((fetchMock.mock.calls[call] as [string, RequestInit])[1].body as string) as Record<string, unknown>
}

// The style attribute React writes for a style object, read back from jsdom.
async function renderedStyle(tag: 'label' | 'input', style: CSSProperties): Promise<string> {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const r = createRoot(host)
  await act(async () => {
    r.render(createElement(tag, { style }))
  })
  const attr = host.firstElementChild!.getAttribute('style') ?? ''
  act(() => r.unmount())
  host.remove()
  return attr
}

describe('the product-email notice and the marketing box', () => {
  it('the notice and the marketing sentence are the story copy, in one place each', () => {
    expect(PRODUCT_EMAIL_NOTICE).toBe(STORY_NOTICE)
    expect(MARKETING_CONSENT_TEXT).toBe(STORY_MARKETING)
  })

  it('a registration window shows the product-email notice and an unticked marketing box', async () => {
    await mountApp()
    const d = await openRegistration()

    const box = marketingBox(d)
    expect(box.type).toBe('checkbox')
    expect(box.checked, 'unticked on open').toBe(false)
    expect(box.form, 'inside the form').toBe(d.querySelector('form'))

    const notice = noticeOf(d)
    expect(notice.tagName).toBe('P')
    expect(notice.classList.contains('t-caption')).toBe(true)
    expect(notice.style.textAlign).toBe('center')
    const probe = document.createElement('p')
    probe.style.margin = '14px 0 0'
    expect(notice.style.margin, 'the demo form caption margin').toBe(probe.style.margin)
    expect(notice.closest('label'), 'not inside a label').toBeNull()
    expect(notice.closest('button, a'), 'not inside a control').toBeNull()
    expect(notice.querySelector('input, button, a, label, select, textarea'), 'holds no control').toBeNull()
    expect(d.querySelectorAll('input[type="checkbox"]').length, 'the marketing box is the only checkbox').toBe(1)
    expect(box.labels![0].textContent, 'the notice is not part of the box label').not.toContain(PRODUCT_EMAIL_NOTICE)

    const fieldset = d.querySelector('fieldset')!
    const submitBtn = d.querySelector('button[type="submit"]')!
    expect(fieldset.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING, 'the box follows the kind fieldset').toBeTruthy()
    expect(fieldset.contains(box), 'the box is not inside the kind fieldset').toBe(false)
    expect(box.compareDocumentPosition(submitBtn) & Node.DOCUMENT_POSITION_FOLLOWING, 'the box precedes the submit button').toBeTruthy()
    expect(submitBtn.compareDocumentPosition(notice) & Node.DOCUMENT_POSITION_FOLLOWING, 'the notice sits under the submit button').toBeTruthy()

    const tabbable = Array.from(d.querySelectorAll<HTMLElement>('input, button, select, textarea, a[href]')).filter((el) => el.tabIndex >= 0 && !(el as HTMLInputElement).disabled)
    const order = tabbable.map((el) => (el === box ? 'marketing' : el === submitBtn ? 'submit' : (el as HTMLInputElement).type === 'radio' ? 'radio' : 'other'))
    expect(order.slice(-4), 'tab order ends: both kind radios, the marketing box, submit').toEqual(['radio', 'radio', 'marketing', 'submit'])
    expect(order.filter((o) => o === 'marketing').length).toBe(1)
  })

  it('the notice follows the form error', async () => {
    stubFetch(() => json(503, { error: 'registration is closed' }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await submit(d)
    const alert = d.querySelector('[role="alert"]')
    expect(alert?.textContent?.trim(), 'control: the form error shows').toBe('Registration is not open yet.')

    expect(alert!.compareDocumentPosition(noticeOf(d)) & Node.DOCUMENT_POSITION_FOLLOWING, 'the notice is after the form error').toBeTruthy()
  })

  it('the marketing box is a real labelled checkbox that is never required', async () => {
    await mountApp()
    const d = await openRegistration()
    const box = marketingBox(d)

    expect(box.labels?.length, 'exactly one label').toBe(1)
    const label = box.labels![0]
    expect(label.textContent?.trim(), 'the accessible name is the sentence').toBe(MARKETING_CONSENT_TEXT)
    expect(label.htmlFor === 'reg-marketing' || label.contains(box), 'label tied by htmlFor or wrapping').toBe(true)
    expect(box.id).toBe('reg-marketing')
    expect(box.required).toBe(false)
    expect(box.hasAttribute('required')).toBe(false)
    expect(box.hasAttribute('aria-required'), 'no aria-required').toBe(false)
    expect(box.disabled).toBe(false)
    expect(box.tabIndex, 'in the tab order').toBeGreaterThanOrEqual(0)
    for (const attr of ['aria-label', 'aria-labelledby', 'aria-describedby']) {
      expect(box.hasAttribute(attr), `${attr} would replace or extend the visible sentence`).toBe(false)
    }
    expect(label.querySelectorAll('input').length, 'the label wraps this one control').toBe(1)

    await act(async () => {
      label.click()
    })
    expect(box.checked, 'clicking the label ticks the box').toBe(true)
    await act(async () => {
      label.click()
    })
    expect(box.checked, 'and unticks it').toBe(false)
  })

  it('the marketing box copies the demo consent box treatment', async () => {
    await mountApp()
    const d = await openRegistration()
    const box = marketingBox(d)
    const label = box.labels![0]

    const demoHost = document.createElement('div')
    document.body.appendChild(demoHost)
    const demoRoot = createRoot(demoHost)
    await act(async () => {
      demoRoot.render(createElement(DemoLeadForm, { idPrefix: 'tdemo', variant: 'card' }))
    })
    const demoBox = demoHost.querySelector<HTMLInputElement>('#tdemo-consent')
    expect(demoBox, 'control: the demo consent checkbox renders').not.toBeNull()
    const demoLabel = demoBox!.labels![0]

    const wantLabel = await renderedStyle('label', CHECK_LABEL_STYLE)
    const wantInput = await renderedStyle('input', CHECK_INPUT_STYLE)
    expect(wantLabel.length, 'control: the label style object renders a style').toBeGreaterThan(0)
    expect(wantInput.length, 'control: the input style object renders a style').toBeGreaterThan(0)
    expect(label.getAttribute('style'), 'marketing label').toBe(wantLabel)
    expect(box.getAttribute('style'), 'marketing input').toBe(wantInput)
    expect(demoLabel.getAttribute('style'), 'demo label').toBe(wantLabel)
    expect(demoBox!.getAttribute('style'), 'demo input').toBe(wantInput)
    expect(demoLabel.getAttribute('style'), 'the lift left the demo label values as shipped').toBe(
      'display: flex; align-items: flex-start; gap: 12px; font-size: 13px; line-height: 1.55; color: var(--foreground); cursor: pointer;',
    )
    expect(demoBox!.getAttribute('style'), 'the demo input carries the prototype values (margin: 2px 0 0)').toBe(
      'flex: 0 0 auto; width: 18px; height: 18px; margin: 2px 0px 0px; accent-color: var(--primary); cursor: pointer;',
    )

    act(() => demoRoot.unmount())
    demoHost.remove()
  })

  it('the shown label and the sent sentence are one constant', async () => {
    const fetchMock = stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await tick(d)
    expect(marketingBox(d).checked, 'control: ticked').toBe(true)
    const shown = marketingBox(d).labels![0].textContent?.trim()

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const body = sentBody(fetchMock)
    expect(body).toHaveProperty('marketing_consent_text')
    expect(body.marketing_consent_text, 'the label the person saw').toBe(shown)
    expect(body.marketing_consent_text).toBe(MARKETING_CONSENT_TEXT)
  })

  it('an unticked box never blocks the submit and sends no marketing key', async () => {
    const fetchMock = stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    expect(marketingBox(d).checked, 'control: unticked').toBe(false)

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const body = sentBody(fetchMock)
    expect('marketing_consent_text' in body, 'no key at all').toBe(false)
    expect(Object.keys(body).sort()).toEqual(['display_name', 'email', 'kind', 'password', 'workspace_name'])
    expect(d.textContent).toContain('Check your email')
    expect(alerts(d)).toEqual([])
  })

  it('ticking then unticking before submit sends no marketing key', async () => {
    const fetchMock = stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await tick(d)
    expect(marketingBox(d).checked, 'control: ticked').toBe(true)
    await tick(d)
    expect(marketingBox(d).checked, 'control: unticked again').toBe(false)

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect('marketing_consent_text' in sentBody(fetchMock)).toBe(false)
  })

  it('the marketing box is disabled while creating', async () => {
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    expect(marketingBox(d).disabled, 'control: enabled before the submit').toBe(false)

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(marketingBox(d).disabled).toBe(true)
  })

  it('a refused submit re-enables the marketing box and keeps its tick', async () => {
    const fetchMock = stubFetchSequence(closed, ok)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await tick(d)
    await submit(d)
    expect(alerts(d), 'control: the refusal shows').toEqual(['Registration is not open yet.'])

    expect(marketingBox(d).disabled).toBe(false)
    expect(marketingBox(d).checked, 'the tick survives a refusal').toBe(true)

    await submit(d)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(sentBody(fetchMock, 1).marketing_consent_text).toBe(MARKETING_CONSENT_TEXT)
  })

  it('a form that fails validation keeps the tick and sends nothing', async () => {
    const fetchMock = stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { email: '' })
    await tick(d)
    await submit(d)

    expect(alerts(d), 'control: validation refused').toEqual(['Enter your work email.'])
    expect(fetchMock).not.toHaveBeenCalled()
    expect(marketingBox(d).checked, 'the tick survives a validation refusal').toBe(true)
    expect(marketingBox(d).disabled).toBe(false)
  })

  it('a field refusal from the gateway keeps the tick', async () => {
    stubFetch(() => json(400, { error: FREE_MAIL_REFUSED }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { email: 'ada@gmail.com' })
    await tick(d)
    await submit(d)

    expect(alerts(d), 'control: the free-mail refusal shows').toEqual([FREE_MAIL_REFUSED])
    expect(marketingBox(d).checked).toBe(true)
  })

  it('a second submit while creating sends no second request', async () => {
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await tick(d)
    await submit(d)
    expect(marketingBox(d).disabled, 'control: creating').toBe(true)

    const form = d.querySelector('form')!
    await act(async () => {
      form.requestSubmit()
    })
    await act(async () => {
      form.requestSubmit()
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('a disabled marketing box cannot be toggled while creating', async () => {
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => undefined)))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await submit(d)
    expect(marketingBox(d).disabled, 'control: creating').toBe(true)

    await tick(d)
    await act(async () => {
      marketingBox(d).labels![0].click()
    })

    expect(marketingBox(d).checked, 'neither a click nor its label ticks it').toBe(false)
  })
})

function stubFetchSequence(...makers: (() => Response | Promise<Response>)[]) {
  let i = 0
  const fetchMock = vi.fn().mockImplementation(async () => makers[Math.min(i++, makers.length - 1)]())
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const ok = () => json(202, { status: 'verification_pending' })
const closed = () => json(503, { error: 'registration is closed' })
const pending = () => new Promise<Response>(() => undefined)

describe('adversarial: validation through the window', () => {
  it('each missing or invalid field shows its own message, takes focus and sends nothing', async () => {
    const cases: [string, Fill, string, string][] = [
      ['empty email', { email: '' }, 'Work email', 'Enter your work email.'],
      ['malformed email', { email: 'ada@' }, 'Work email', 'Enter a valid work email address.'],
      ['empty password', { password: '' }, 'Password', 'Choose a password.'],
      ['empty name', { name: '' }, 'Your name', 'Enter your name.'],
      ['blank name', { name: '   ' }, 'Your name', 'Enter your name.'],
      ['empty workspace', { workspace: '' }, 'Workspace name', 'Enter your company or workspace name.'],
      ['over-long workspace', { workspace: 'x'.repeat(201) }, 'Workspace name', 'Use 200 characters or fewer.'],
    ]
    expect(cases.length).toBeGreaterThan(0)
    const fetchMock = stubFetch(ok)
    for (const [name, over, label, message] of cases) {
      await remountApp()
      const d = await openRegistration()
      await fillForm(d, over)

      await submit(d)

      const found = alerts(d)
      expect(found, name).toEqual([message])
      const input = labelled(d, label)
      expect(document.activeElement, `${name}: focus`).toBe(input)
      expect(input.getAttribute('aria-invalid'), name).toBe('true')
      expect(input.getAttribute('aria-describedby'), name).toBe(d.querySelector('[role="alert"]')!.id)
      expect(fetchMock, `${name}: no request`).not.toHaveBeenCalled()
    }
  })

  it('with several fields missing, focus goes to the first in form order, ahead of a missing kind', async () => {
    const fetchMock = stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { name: '', workspace: '', kind: null })

    await submit(d)

    expect(alerts(d).length).toBe(3)
    expect(document.activeElement).toBe(labelled(d, 'Your name'))
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('editing a field clears only its own message', async () => {
    stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    await submit(d)
    expect(alerts(d).length, 'control: five messages after an empty submit').toBe(5)

    await type(labelled(d, 'Work email'), 'ada@okafor.ng')
    expect(alerts(d).length).toBe(4)
    expect(labelled(d, 'Work email').hasAttribute('aria-describedby')).toBe(false)
    expect(labelled(d, 'Password').hasAttribute('aria-describedby'), 'the password message stays').toBe(true)

    await act(async () => {
      d.querySelector<HTMLInputElement>('input[type="radio"][value="firm"]')!.click()
    })
    expect(alerts(d).length, 'picking a kind clears the kind message only').toBe(3)
    expect(alerts(d)).not.toContain('Choose how this workspace files invoices.')
  })

  it('the radios are native, enabled and in the tab order, and a form submit posts', async () => {
    const fetchMock = stubFetch(ok)
    await mountApp()
    const d = await openRegistration()
    const radios = Array.from(d.querySelectorAll<HTMLInputElement>('input[type="radio"]'))
    expect(radios.length).toBe(2)
    for (const r of radios) {
      expect(r.disabled).toBe(false)
      expect(r.closest('fieldset')!.disabled).toBe(false)
      expect(r.tabIndex, 'not removed from the tab order').toBeGreaterThanOrEqual(0)
    }

    const form = d.querySelector('form')!
    const inputs = Array.from(d.querySelectorAll('input'))
    expect(inputs.length, 'four text fields, two radios and the marketing checkbox').toBe(7)
    expect(d.querySelectorAll('input:not([type=radio]):not([type=checkbox])').length, 'the smoke spec locator selects the four text fields').toBe(4)
    expect(inputs.every((i) => i.form === form), 'every field sits in the one form, so Enter in any of them submits it').toBe(true)
    expect(form.querySelectorAll('button[type="submit"]').length).toBe(1)

    await fillForm(d)
    await act(async () => {
      form.requestSubmit()
    })
    await settle()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('adversarial: outcomes through the window', () => {
  it('a free-mail refusal stays while other fields are edited, goes on an email edit, and a resend still posts', async () => {
    const fetchMock = stubFetch(() => json(400, { error: FREE_MAIL_REFUSED }))
    await mountApp()
    const d = await openRegistration()
    await fillForm(d, { email: 'ada@gmail.com' })
    await submit(d)
    const email = labelled(d, 'Work email')
    expect(email.getAttribute('aria-describedby'), 'control: the refusal shows').toBeTruthy()

    await type(labelled(d, 'Password'), 'another-pw-1')
    await type(labelled(d, 'Your name'), 'Ada O.')
    await type(labelled(d, 'Workspace name'), 'Okafor Ltd')
    await act(async () => {
      d.querySelector<HTMLInputElement>('input[type="radio"][value="in_house"]')!.click()
    })
    expect(alerts(d), 'other edits keep the refusal').toEqual([FREE_MAIL_REFUSED])
    expect(email.getAttribute('aria-describedby')).toBe(d.querySelector('[role="alert"]')!.id)

    await submit(d)
    expect(fetchMock, 'the same address posts again and is refused again').toHaveBeenCalledTimes(2)
    expect(alerts(d)).toEqual([FREE_MAIL_REFUSED])

    await type(email, 'ada@okafor.ng')
    expect(alerts(d)).toEqual([])
  })

  it('a 202 after a free-mail refusal or a closed answer shows only the check view', async () => {
    for (const [name, first] of [['free-mail', () => json(400, { error: FREE_MAIL_REFUSED })], ['closed', closed]] as const) {
      stubFetchSequence(first, ok)
      await remountApp()
      const d = await openRegistration()
      await fillForm(d, { email: 'ada@gmail.com' })
      await submit(d)
      expect(alerts(d).length, `control: ${name} showed an error`).toBe(1)

      await type(labelled(d, 'Work email'), 'ada@okafor.ng')
      await submit(d)

      expect(alerts(d), name).toEqual([])
      expect(d.textContent, name).toContain('Check your email')
      expect(d.textContent).toContain('ada@okafor.ng')
      expect(d.querySelectorAll('input').length).toBe(0)
    }
  })

  it('a retry clears the previous form error while it sends', async () => {
    const fetchMock = stubFetchSequence(closed, pending)
    await mountApp()
    const d = await openRegistration()
    await fillForm(d)
    await submit(d)
    expect(alerts(d), 'control: the first answer shows').toEqual(['Registration is not open yet.'])

    await submit(d)

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(d.querySelector('button[type="submit"]')?.textContent?.trim(), 'control: the retry is sending').toBe('Creating…')
    expect(alerts(d), 'no stale error beside Creating…').toEqual([])
  })
})

describe('adversarial: the window among its neighbours', () => {
  it('the window mounts after the cookie notice', async () => {
    await mountApp()
    const notice = document.querySelector(NOTICE)
    expect(notice, 'control: the notice is mounted').not.toBeNull()
    const d = await openRegistration()
    expect(notice!.compareDocumentPosition(d) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('the cookie notice stays inert through the swap from the sign-in window', async () => {
    await mountApp()
    await clickByText(header(), LOGIN)
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'control: inert under sign-in').toBe(true)

    await clickByText(document.querySelector<HTMLElement>(DIALOG)!, CREATE)

    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
    expect(document.querySelector(NOTICE)!.hasAttribute('inert')).toBe(true)
  })

  it('leaving the sign-in window for registration drops its boot error', async () => {
    await bootAt(`/?state=${STATE}&signin=failed`)
    const signIn = document.querySelector<HTMLElement>(`${DIALOG}[aria-label="${LOGIN}"]`)!
    expect(signIn.textContent, 'control: boot shows the error').toContain("We couldn't open your workspace.")
    await clickByText(signIn, CREATE)
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)

    await clickByText(header(), LOGIN)

    const again = document.querySelector<HTMLElement>(`${DIALOG}[aria-label="${LOGIN}"]`)
    expect(again, 'control: the sign-in window reopens').not.toBeNull()
    expect(again!.textContent).not.toContain("We couldn't open your workspace.")
  })
})

describe('the resend control on the check-your-email view', () => {
  const ADA = 'ada@corp.example'
  const RESEND = 'Send the link again'
  const SENDING = 'Sending…'
  // Pinned here, not imported: a wording change is a deliberate edit.
  const SENT = `If ${ADA} still needs verifying, a new link is on its way. Use the newest one.`
  const FAILED = 'The link could not be sent right now. Try again shortly.'
  const RESEND_URL = 'https://gw.x/auth/resend-verification'

  // Registration answers 202; every resend call goes to `resend`.
  function routedFetch(resend: () => Response | Promise<Response>) {
    const fetchMock = vi.fn().mockImplementation(async (url: unknown) => (String(url).endsWith('/auth/register') ? json(202, { status: 'verification_pending' }) : resend()))
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  function resendCalls(fetchMock: { mock: { calls: unknown[][] } }): [string, RequestInit][] {
    return (fetchMock.mock.calls as [string, RequestInit][]).filter(([url]) => String(url).endsWith('/auth/resend-verification'))
  }

  async function checkView(): Promise<HTMLElement> {
    const d = await openRegistration()
    await fillForm(d, { email: ADA })
    await submit(d)
    expect(d.textContent, 'control: the check-your-email view shows').toContain('Check your email')
    return d
  }

  function resendButton(d: ParentNode): HTMLButtonElement {
    const b = Array.from(d.querySelectorAll<HTMLButtonElement>('button.ds-btn')).find((x) => x.textContent?.trim() === RESEND || x.textContent?.trim() === SENDING)
    expect(b, `expected a "${RESEND}" button`).toBeDefined()
    return b as HTMLButtonElement
  }

  async function click(el: HTMLElement): Promise<void> {
    await act(async () => {
      el.click()
    })
    await settle()
  }

  function statusNotes(d: ParentNode): string[] {
    return Array.from(d.querySelectorAll('[role="status"]')).map((n) => n.textContent?.trim() ?? '').filter(Boolean)
  }

  it('the check-your-email view offers Send the link again above Close', async () => {
    routedFetch(() => json(202, { status: 'accepted' }))
    await mountApp()
    const d = await checkView()

    const buttons = Array.from(d.querySelectorAll<HTMLButtonElement>('button.ds-btn'))
    expect(buttons.map((b) => b.textContent?.trim())).toEqual([RESEND, 'Close'])
    for (const b of buttons) {
      expect(b.className).toBe('ds-btn ds-btn--outline ds-btn--md')
      expect(b.style.width).toBe('100%')
    }
    const headerClose = d.querySelector<HTMLButtonElement>('button[aria-label="Close"]')
    expect(headerClose, 'the header Close control stays').not.toBeNull()
    expect(headerClose!.classList.contains('ds-btn')).toBe(false)
  })

  it('Send the link again posts sentTo once and disables only itself while sending', async () => {
    const fetchMock = routedFetch(() => new Promise<Response>(() => undefined))
    await mountApp()
    const d = await checkView()
    const btn = resendButton(d)
    const close = byText(d, 'Close') as HTMLButtonElement

    await click(btn)
    await click(btn)

    const calls = resendCalls(fetchMock)
    expect(calls, 'one POST for two clicks').toHaveLength(1)
    expect(calls[0][0]).toBe(RESEND_URL)
    expect(calls[0][1].method).toBe('POST')
    expect(JSON.parse(calls[0][1].body as string)).toStrictEqual({ email: ADA })
    expect(btn.disabled).toBe(true)
    expect(btn.textContent?.trim()).toBe(SENDING)
    expect(close.disabled, 'Close stays enabled').toBe(false)
  })

  it('the live region exists before the click and receives the notice after it', async () => {
    routedFetch(() => json(202, { status: 'accepted' }))
    await mountApp()
    const d = await checkView()

    const region = d.querySelector('[role="status"]')
    expect(region, 'the region is in the DOM before the click').not.toBeNull()
    expect(region!.textContent).toBe('')

    await click(resendButton(d))

    expect(d.querySelector('[role="status"]'), 'the same node receives the text').toBe(region)
    expect(region!.textContent?.trim()).toBe(SENT)
  })

  it('every 202 shows the same sent notice', async () => {
    const answers = [() => json(202, { status: 'accepted' }), () => json(202, {})]
    let n = 0
    routedFetch(() => answers[n++]())
    await mountApp()
    const d = await checkView()
    const btn = resendButton(d)

    for (const [i] of answers.entries()) {
      await click(btn)
      expect(n, `click ${i + 1} sent a request`).toBe(i + 1)
      expect(statusNotes(d), `answer ${i + 1}`).toEqual([SENT])
      expect(alerts(d), `answer ${i + 1}: no alert`).toEqual([])
      expect(btn.disabled).toBe(false)
      expect(btn.textContent?.trim()).toBe(RESEND)
    }
  })

  it('a failed resend shows the failure alert and re-enables the button', async () => {
    const cases: [string, () => Response | Promise<Response>][] = [
      ['400', () => json(400, { error: 'invalid email address' })],
      ['502', () => json(502, { error: 'bad gateway' })],
      ['503', () => json(503, { error: 'registration is not configured' })],
      ['network', () => Promise.reject(new TypeError('Failed to fetch'))],
      ['gateway unset', () => json(202, { status: 'accepted' })],
    ]
    for (const [name, make] of cases) {
      let held = false
      routedFetch(() => (held ? new Promise<Response>(() => undefined) : make()))
      await remountApp()
      const d = await checkView()
      const btn = resendButton(d)
      if (name === 'gateway unset') vi.stubEnv('VITE_GATEWAY_URL', '')

      await click(btn)

      expect(alerts(d), name).toEqual([FAILED])
      expect(statusNotes(d), `${name}: no sent notice`).toEqual([])
      expect(btn.disabled, `${name}: re-enabled`).toBe(false)
      expect(btn.textContent?.trim(), name).toBe(RESEND)

      held = true
      vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
      await click(btn)
      expect(alerts(d), `${name}: the old alert is gone before the answer`).toEqual([])
      expect(btn.disabled, `${name}: sending`).toBe(true)
    }
  })

  it('closing the window during a resend leaves no late update', async () => {
    const late: [string, (settleFetch: (r: Response | Error) => void) => void][] = [
      ['202', (f) => f(json(202, { status: 'accepted' }))],
      ['network failure', (f) => f(new TypeError('Failed to fetch'))],
    ]
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const unhandled: unknown[] = []
    const onUnhandled = (reason: unknown) => unhandled.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      for (const [name, answer] of late) {
        let settleFetch!: (r: Response | Error) => void
        const pending = new Promise<Response>((resolve, reject) => {
          settleFetch = (r) => (r instanceof Error ? reject(r) : resolve(r))
        })
        const fetchMock = routedFetch(() => pending)
        await remountApp()
        const d = await checkView()
        await click(resendButton(d))
        expect(resendCalls(fetchMock), `${name}: the resend is in flight`).toHaveLength(1)

        await click(byText(d, 'Close')!)
        expect(document.querySelectorAll(DIALOG).length, `${name}: the window closed`).toBe(0)

        answer(settleFetch)
        await settle()
        expect(consoleError, name).not.toHaveBeenCalled()
        expect(unhandled, name).toEqual([])
      }
    } finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })

  it('a later resend clears the earlier outcome, and a notice and an alert replace each other', async () => {
    let next: () => Response | Promise<Response> = () => json(202, { status: 'accepted' })
    routedFetch(() => next())
    await mountApp()
    const d = await checkView()
    const btn = resendButton(d)
    expect(btn.type, 'a plain button, so it never submits anything').toBe('button')

    await click(btn)
    expect(statusNotes(d), 'control: the first notice shows').toEqual([SENT])

    let release!: (r: Response) => void
    next = () =>
      new Promise<Response>((resolve) => {
        release = resolve
      })
    await click(btn)
    expect(statusNotes(d), 'the old notice is gone while the next resend runs').toEqual([])
    expect(alerts(d)).toEqual([])
    expect(btn.disabled).toBe(true)

    await act(async () => release(json(503, { error: 'registration is not configured' })))
    await settle()
    expect(alerts(d), 'a refusal after a notice').toEqual([FAILED])
    expect(statusNotes(d), 'the notice does not stay beside the alert').toEqual([])

    next = () => json(202, { status: 'accepted' })
    await click(btn)
    expect(alerts(d), 'a notice after a refusal').toEqual([])
    expect(statusNotes(d)).toEqual([SENT])
    expect(d.textContent, 'the view stays').toContain('Check your email')
    expect(document.querySelectorAll(REGISTER_DIALOG).length).toBe(1)
  })
})
