// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/invite" }
/// <reference types="node" />
// The landing accept page (D1, D13, D16, D17). Real invite.ts and register.ts, fetch stubbed.
// Controls are diffed against the sibling windows: RegisterModal.tsx (fields, submit, sent view) and SignInForm.tsx (labels, aria).
import { StrictMode, act, createElement, type ComponentType } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const T = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const ADDRESS = 'tunde@obi.test'
const WORKSPACE = 'Obi Partners'
const PREVIEW_URL = 'https://gw.x/auth/invitation'
const REGISTER_URL = 'https://gw.x/auth/invitation/register'
const RESEND_URL = 'https://gw.x/auth/resend-verification'
const SIGN_IN_HREF = `https://app.x?auth=start#invite=${T}`

// D16 copy, pinned here and not imported: a wording change is a deliberate edit.
const INVALID_HEADING = 'This invite is no longer valid'
const INVALID_TEXT = 'Ask your workspace admin for a new one.'
const UNAVAILABLE = 'We could not load this invite. Reload the page to try again.'
const READY_TEXT = `${ADDRESS} is invited to join ${WORKSPACE} on ASComply as Reviewer.`
const READY_PROMPT = 'New to ASComply? Create an account with this address. Already have one? Sign in.'
const REGISTER_TEXT = `We will email ${ADDRESS} a link to confirm the address and choose your password.`
const SENT_TEXT = `If this address can be registered, a link to confirm it and choose your password is on its way to ${ADDRESS}. Open it and choose your password, then come back to this page and choose Sign in. Already have an account? Sign in now.`
const PRODUCT_EMAIL_NOTICE = 'We will email you about your account and the service. This is part of using ASComply Africa.'
const RESEND_SENT = `If ${ADDRESS} still needs verifying, a new link is on its way. Use the newest one.`
// register.ts copy, as registerOutcome maps the gateway's statuses.
const THROTTLED = 'Too many attempts. Try again in a minute.'
const CLOSED = 'Registration is not open yet.'
const REGISTER_UNAVAILABLE = 'Registration is unavailable right now. Try again shortly.'
// Gateway texts, internal/gateway/invitation.go: msgInviteNotValid, msgInviteLookupDown, msgInviteeFieldsRequired.
const WIRE_NOT_VALID = 'this invite is no longer valid'
const WIRE_LOOKUP_DOWN = 'invitation lookup is unavailable'
const WIRE_FIELDS_REQUIRED = 'token is required'

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let assigned: string[]
let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.stubEnv('VITE_REGISTRATION_OPEN', 'true')
  vi.resetModules()
  // Reads delegate to the real location; only `href` writes are captured.
  assigned = []
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  const real = window.location
  const stub = {
    get href() {
      return real.href
    },
    set href(v: string) {
      assigned.push(v)
    },
    get pathname() {
      return real.pathname
    },
    get search() {
      return real.search
    },
    get hash() {
      return real.hash
    },
    get origin() {
      return real.origin
    },
  }
  Object.defineProperty(window, 'location', { value: stub, writable: true, configurable: true })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

type Route = (init: RequestInit) => Response | Promise<Response>
const previewOk = (over: Partial<Record<'workspace' | 'role' | 'email', string>> = {}): Route => () =>
  json(200, { workspace: WORKSPACE, role: 'reviewer', email: ADDRESS, ...over })

function stubFetch(routes: { preview?: Route; register?: Route; resend?: Route } = {}) {
  const table: Record<string, Route | undefined> = {
    [PREVIEW_URL]: routes.preview ?? previewOk(),
    [REGISTER_URL]: routes.register,
    [RESEND_URL]: routes.resend,
  }
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    const route = table[url]
    if (!route) throw new Error(`unexpected request to ${url}`)
    return route(init)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const callsTo = (fetchMock: ReturnType<typeof stubFetch>, url: string) => fetchMock.mock.calls.filter(([u]) => u === url)
const bodyOf = (call: [string, RequestInit]) => JSON.parse(call[1].body as string) as unknown

async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
  }
}

async function mount(token: string | null = T, strict = true): Promise<void> {
  const { InvitePage } = (await import('./InvitePage')) as { InvitePage: ComponentType<{ token: string | null }> }
  const page = createElement(InvitePage, { token })
  await act(async () => {
    root.render(strict ? createElement(StrictMode, null, page) : page)
  })
  await settle()
}

const allButtons = () => Array.from(container.querySelectorAll('button'))
const buttonTexts = () => allButtons().map((b) => b.textContent?.trim())
const dsButtons = () => Array.from(container.querySelectorAll<HTMLButtonElement>('button.ds-btn'))
const headings = () => Array.from(container.querySelectorAll('h1, h2, h3'), (h) => h.textContent?.trim())
const text = () => container.textContent ?? ''
const alerts = () => Array.from(container.querySelectorAll('[role="alert"]'), (a) => a.textContent?.trim())

function button(label: string): HTMLButtonElement {
  const b = allButtons().find((x) => x.textContent?.trim() === label)
  expect(b, `expected a "${label}" button`).toBeDefined()
  return b!
}

async function click(el: HTMLElement): Promise<void> {
  await act(async () => el.click())
}

function labelled(label: string): HTMLInputElement {
  const l = Array.from(container.querySelectorAll('label')).find((x) => x.textContent?.trim() === label)
  expect(l, `expected a label "${label}"`).toBeDefined()
  const input = document.getElementById(l!.htmlFor)
  expect(input, `expected the input of "${label}"`).not.toBeNull()
  return input as HTMLInputElement
}

async function toRegisterView(): Promise<void> {
  await click(button('Create account'))
  expect(headings(), 'control: the register view opened').toEqual(['Create your account'])
}

async function submit(): Promise<void> {
  const submitBtn = container.querySelector<HTMLButtonElement>('button[type="submit"]')
  expect(submitBtn, 'expected the submit button').not.toBeNull()
  await click(submitBtn!)
  await settle()
}

function expectInvalidView(): void {
  expect(headings()).toEqual([INVALID_HEADING])
  expect(text()).toContain(INVALID_TEXT)
  expect(allButtons().length, 'the invalid view has no buttons').toBe(0)
  expect(container.querySelectorAll('input').length, 'the invalid view has no fields').toBe(0)
}

describe('the ready view', () => {
  it('invitePage_opensWithOnePreviewAndNamesTheInvite', async () => {
    const fetchMock = stubFetch()
    await mount(T, true)

    expect(fetchMock, 'one request in StrictMode').toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(PREVIEW_URL)
    expect(init.method).toBe('POST')
    expect(bodyOf(fetchMock.mock.calls[0])).toStrictEqual({ token: T })
    expect(url, 'the token travels in the body, never the URL').not.toContain(T)

    expect(container.querySelector('.t-eyebrow')?.textContent).toBe('INVITATION')
    expect(headings()).toEqual([`Join ${WORKSPACE}`])
    expect(text()).toContain(READY_TEXT)
    expect(text()).toContain(READY_PROMPT)
    expect(container.querySelector('img.ds-logo-mark')?.getAttribute('width'), 'Logo size 28, as the modal header').toBe('28')

    // D17, as RegisterModal.tsx: ds-btn md buttons, width 100%, the second stacked with marginTop 10.
    expect(buttonTexts(), 'exactly two buttons, no close control').toEqual(['Create account', 'Sign in'])
    const [create, signIn] = allButtons()
    expect(create.className).toBe('ds-btn ds-btn--primary ds-btn--md')
    expect(signIn.className).toBe('ds-btn ds-btn--outline ds-btn--md')
    expect(create.style.width).toBe('100%')
    expect(signIn.style.width).toBe('100%')
    expect(signIn.style.marginTop).toBe('10px')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it.each([
    ['admin', 'Admin'],
    ['preparer', 'Preparer'],
    ['reviewer', 'Reviewer'],
  ])('invitePage_namesTheRole %s as %s', async (role, label) => {
    stubFetch({ preview: previewOk({ role }) })
    await mount()
    expect(text()).toContain(`${ADDRESS} is invited to join ${WORKSPACE} on ASComply as ${label}.`)
  })

  it('invitePage_signInCarriesTheInviteToTheApp', async () => {
    const fetchMock = stubFetch()
    await mount()
    expect(assigned, 'control: nothing navigates before the click').toEqual([])

    await click(button('Sign in'))

    expect(assigned).toEqual([SIGN_IN_HREF])
    expect(fetchMock, 'Sign in only navigates').toHaveBeenCalledTimes(1)
  })

  it('invitePage_signInDoesNothingWithoutAnAppUrl', async () => {
    vi.stubEnv('VITE_APP_URL', '')
    stubFetch()
    await mount()
    await click(button('Sign in'))
    expect(assigned, 'no href write, as SignInForm Continue with email').toEqual([])
  })

  it.each([['false'], [undefined]])('invitePage_createAccountFollowsRegistrationOpen (VITE_REGISTRATION_OPEN=%s)', async (flag) => {
    stubFetch()
    await mount()
    expect(buttonTexts(), 'control: open registration offers both').toEqual(['Create account', 'Sign in'])

    vi.stubEnv('VITE_REGISTRATION_OPEN', flag as string)
    await act(async () => root.unmount())
    root = createRoot(container)
    await mount()

    expect(buttonTexts()).toEqual(['Sign in'])
    expect(text(), 'the invite is still named').toContain(READY_TEXT)
    // Copy choice for the PM: with signup closed the "New to ASComply?" sentence is hidden with its button.
    expect(text()).not.toContain('New to ASComply?')
  })
})

describe('the register view', () => {
  it('invitePage_registerViewHasNoPasswordField', async () => {
    stubFetch()
    await mount()
    await toRegisterView()

    expect(container.querySelectorAll('input[type="password"], [autocomplete="new-password"]')).toHaveLength(0)
    const inputs = Array.from(container.querySelectorAll('input'))
    expect(inputs, 'one field').toHaveLength(1)
    const email = labelled('Work email')
    expect(inputs[0]).toBe(email)
    expect(email.value).toBe(ADDRESS)
    expect(email.readOnly).toBe(true)
    expect(email.type).toBe('email')
    expect(text()).toContain(REGISTER_TEXT)
    expect(text()).not.toContain('Choose a password.')
    expect(container.querySelector('label[for="inv-password"], #inv-password')).toBeNull()
  })

  it('invitePage_registerViewStacksHeadingTextFormWithTheViewsSpacing', async () => {
    stubFetch()
    await mount()
    await toRegisterView()

    const h = container.querySelector<HTMLElement>('h3')!
    const p = Array.from(container.querySelectorAll<HTMLElement>('p')).find((x) => x.textContent === REGISTER_TEXT)!
    expect(p, 'the D16 text line').toBeDefined()
    expect(p.className).toBe('t-body-sm')
    expect(h.style.marginBottom, 'heading to text, as every other view').toBe('10px')
    expect(p.style.margin).toBe('0px 0px 16px')
    expect(h.compareDocumentPosition(p) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(p.compareDocumentPosition(container.querySelector('form')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('invitePage_registerEnterSubmitPostsTheTokenOnly: a form submit without the button posts once', async () => {
    const fetchMock = stubFetch({ register: () => json(200, {}) })
    await mount()
    await toRegisterView()

    // jsdom has no implicit submission; requestSubmit is what Enter in the field runs.
    await act(async () => container.querySelector('form')!.requestSubmit())
    await settle()

    const posts = callsTo(fetchMock, REGISTER_URL)
    expect(posts).toHaveLength(1)
    expect(bodyOf(posts[0] as [string, RequestInit])).toStrictEqual({ token: T })
    expect(headings()).toEqual(['Check your email'])
  })

  it('invitePage_registerPostsTheTokenOnly', async () => {
    const fetchMock = stubFetch({ register: () => json(200, {}) })
    await mount()
    await toRegisterView()

    await submit()

    const posts = callsTo(fetchMock, REGISTER_URL)
    expect(posts).toHaveLength(1)
    expect(posts[0][1].method).toBe('POST')
    expect(bodyOf(posts[0] as [string, RequestInit])).toStrictEqual({ token: T })
    expect(fetchMock, 'one preview and one register').toHaveBeenCalledTimes(2)

    expect(headings()).toEqual(['Check your email'])
    expect(text()).toContain(SENT_TEXT)
    expect(dsButtons().map((b) => b.textContent?.trim()), 'Send the link again above Sign in, as RegisterModal above Close').toEqual(['Send the link again', 'Sign in'])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('invitePage_registerControlsMatchTheRegisterWindow', async () => {
    stubFetch()
    await mount()
    await toRegisterView()

    // RegisterModal.tsx field(): label.label above input.dm-input, aria-required, autoComplete.
    const email = labelled('Work email')
    const l = container.querySelector<HTMLLabelElement>(`label[for="${email.id}"]`)!
    expect(l.className).toBe('label')
    expect(l.style.display).toBe('block')
    expect(email.classList.contains('dm-input')).toBe(true)
    expect(email.autocomplete).toBe('email')
    expect(email.disabled, 'read-only, not disabled: the value stays selectable').toBe(false)
    expect(email.getAttribute('aria-required')).toBe('true')

    const submitBtn = container.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(submitBtn.textContent?.trim()).toBe('Create account →')
    expect(submitBtn.className).toBe('ds-btn ds-btn--primary ds-btn--md')
    expect(submitBtn.style.width).toBe('100%')
    expect(text()).toContain(PRODUCT_EMAIL_NOTICE)
  })

  it('invitePage_registerBackReturnsToTheInviteWithoutAnotherPreview', async () => {
    const fetchMock = stubFetch()
    await mount()
    await toRegisterView()

    const back = button('Back')
    expect(back.type, 'as ForgotPasswordForm Back to sign in').toBe('button')
    await click(back)
    await settle()

    expect(headings()).toEqual([`Join ${WORKSPACE}`])
    expect(buttonTexts()).toEqual(['Create account', 'Sign in'])
    expect(fetchMock, 'the preview is not repeated').toHaveBeenCalledTimes(1)
  })

  it('invitePage_registerBackClearsThePasswordAndItsError: a refused submit leaves no error behind after Back', async () => {
    stubFetch({ register: () => json(400, { error: 'Password should be at least 6 characters.' }) })
    await mount()
    await toRegisterView()
    await submit()
    expect(alerts(), 'control: the refusal is showing before Back').toEqual(['Password should be at least 6 characters.'])

    await click(button('Back'))
    await click(button('Create account'))

    expect(headings(), 'the register view reopened').toEqual(['Create your account'])
    expect(alerts(), 'a stale server error must not greet the reopened form').toEqual([])
  })

  it('invitePage_registerSubmitDisablesWhilePending', async () => {
    let finish!: () => void
    const pending = new Promise<Response>((res) => {
      finish = () => res(json(200, {}))
    })
    const fetchMock = stubFetch({ register: () => pending })
    await mount()
    await toRegisterView()
    const submitBtn = container.querySelector<HTMLButtonElement>('button[type="submit"]')!

    await click(submitBtn)
    await click(submitBtn)
    // A submit that skips the disabled button (a script, a stale handler) is refused by the handler itself.
    await act(async () => {
      container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })

    expect(callsTo(fetchMock, REGISTER_URL), 'one POST for two clicks and a direct submit').toHaveLength(1)
    expect(submitBtn.disabled).toBe(true)
    expect(submitBtn.textContent?.trim()).toBe('Creating…')
    expect(labelled('Work email').disabled, 'RegisterModal disables its fields while submitting').toBe(true)

    await act(async () => finish())
    await settle()
    expect(headings()).toEqual(['Check your email'])
  })

  const refusals: [string, Route | null, string][] = [
    ['400 shows the gateway text', () => json(400, { error: 'Password should be at least 6 characters.' }), 'Password should be at least 6 characters.'],
    ['400 with the missing-fields text shows it', () => json(400, { error: WIRE_FIELDS_REQUIRED }), WIRE_FIELDS_REQUIRED],
    ['429 shows the throttle text', () => json(429, { error: 'slow down' }), THROTTLED],
    ['503 shows the closed text', () => json(503, { error: 'registration is closed' }), CLOSED],
    ['500 shows the unavailable text', () => json(500, { error: 'boom' }), REGISTER_UNAVAILABLE],
    ['a network error shows the unavailable text', () => { throw new TypeError('Failed to fetch') }, REGISTER_UNAVAILABLE],
  ]

  it.each(refusals)('invitePage_registerRefusals: %s', async (_name, route, expected) => {
    const fetchMock = stubFetch({ register: route! })
    await mount()
    await toRegisterView()

    await submit()

    expect(callsTo(fetchMock, REGISTER_URL), 'control: the register request was made').toHaveLength(1)
    expect(alerts()).toEqual([expected])
    expect(headings(), 'the register view stays').toEqual(['Create your account'])
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled, 'the submit is usable again').toBe(false)
    expect(container.querySelector<HTMLButtonElement>('button[type="submit"]')!.textContent?.trim()).toBe('Create account →')
  })

  it('invitePage_registerRefusals: a 404 shows the invalid view', async () => {
    const fetchMock = stubFetch({ register: () => json(404, { error: WIRE_NOT_VALID }) })
    await mount()
    await toRegisterView()

    await submit()

    expect(callsTo(fetchMock, REGISTER_URL), 'control: the register request was made').toHaveLength(1)
    expectInvalidView()
  })
})

describe('the register errors', () => {
  it('invitePage_registerErrorsMapAsBefore', async () => {
    stubFetch({ register: () => json(404, { error: WIRE_NOT_VALID }) })
    await mount()
    await toRegisterView()
    await submit()
    expectInvalidView()

    await act(async () => root.unmount())
    root = createRoot(container)
    stubFetch({ register: () => json(400, { error: 'token is required' }) })
    await mount()
    await toRegisterView()
    await submit()
    expect(alerts()).toEqual(['token is required'])

    await act(async () => root.unmount())
    root = createRoot(container)
    stubFetch({ register: () => json(503, { error: 'registration is closed' }) })
    await mount()
    await toRegisterView()
    await submit()
    expect(alerts()).toEqual([CLOSED])
  })
})

describe('the sent view', () => {
  async function toSentView(register: Route = () => json(200, {}), resend: Route = () => json(202, {})) {
    const fetchMock = stubFetch({ register, resend })
    await mount()
    await toRegisterView()
    await submit()
    expect(headings(), 'control: the sent view opened').toEqual(['Check your email'])
    return fetchMock
  }

  it('invitePage_sentViewResendsToTheInvitedAddress', async () => {
    let finish!: () => void
    const pending = new Promise<Response>((res) => {
      finish = () => res(json(202, {}))
    })
    const fetchMock = await toSentView(undefined, () => pending)

    // RegisterModal.tsx: the status region exists before the click and receives the notice after it.
    const region = container.querySelector('[role="status"]')
    expect(region, 'the live region is in the DOM before the click').not.toBeNull()
    expect(region!.textContent).toBe('')

    const resend = button('Send the link again')
    expect(resend.className).toBe('ds-btn ds-btn--outline ds-btn--md')
    await click(resend)
    await click(resend)

    const posts = callsTo(fetchMock, RESEND_URL)
    expect(posts, 'one POST for two clicks').toHaveLength(1)
    expect(posts[0][1].method).toBe('POST')
    expect(bodyOf(posts[0] as [string, RequestInit])).toStrictEqual({ email: ADDRESS })
    expect(resend.disabled).toBe(true)
    expect(resend.textContent?.trim()).toBe('Sending…')
    expect(button('Sign in').disabled, 'Sign in stays enabled').toBe(false)

    await act(async () => finish())
    await settle()
    expect(container.querySelector('[role="status"]'), 'the same node receives the text').toBe(region)
    expect(region!.textContent?.trim()).toBe(RESEND_SENT)
    expect(resend.disabled).toBe(false)
  })

  it('invitePage_sentViewSaysToChooseThePasswordFromTheMail', async () => {
    await toSentView()
    expect(text()).toContain(`a link to confirm it and choose your password is on its way to ${ADDRESS}`)
    expect(text()).not.toContain('confirmation link')
    expect(container.querySelectorAll('input'), 'the sent view holds no field').toHaveLength(0)
  })

  it('invitePage_sentViewSignInCarriesTheInviteToTheApp', async () => {
    await toSentView()
    expect(assigned).toEqual([])

    await click(button('Sign in'))

    expect(assigned).toEqual([SIGN_IN_HREF])
  })
})

describe('the unusable invite', () => {
  type Case = [string, string | null, Route | null, number, 'invalid' | 'unavailable']
  const cases: Case[] = [
    ['no token', null, null, 0, 'invalid'],
    ['a preview 404', T, () => json(404, { error: WIRE_NOT_VALID }), 1, 'invalid'],
    ['a preview 500', T, () => json(500, { error: 'boom' }), 1, 'unavailable'],
    ['a preview 502 (the gateway when tenancy is down)', T, () => json(502, { error: WIRE_LOOKUP_DOWN }), 1, 'unavailable'],
    ['a preview network error', T, () => { throw new TypeError('Failed to fetch') }, 1, 'unavailable'],
  ]

  it.each(cases)('invitePage_unusableInviteSaysItIsNoLongerValid: %s', async (_name, token, route, requests, view) => {
    const fetchMock = stubFetch(route ? { preview: route } : {})
    await mount(token)

    expect(fetchMock).toHaveBeenCalledTimes(requests)
    if (view === 'invalid') {
      expectInvalidView()
      expect(text(), 'the page words it, not the gateway').not.toContain(WIRE_NOT_VALID)
    } else {
      expect(text()).toContain(UNAVAILABLE)
      expect(text()).not.toContain(INVALID_HEADING)
      expect(text()).not.toContain('Join')
      expect(allButtons().length, 'the unavailable view is text only').toBe(0)
      expect(container.querySelectorAll('input, a').length, 'the unavailable view has no field or link').toBe(0)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('the loading view', () => {
  it('invitePage_loadingViewIsOneStatusLineWithNoControls', async () => {
    let finish!: () => void
    const pending = new Promise<Response>((res) => {
      finish = () => res(json(200, { workspace: WORKSPACE, role: 'reviewer', email: ADDRESS }))
    })
    const fetchMock = stubFetch({ preview: () => pending })
    await mount()

    expect(fetchMock, 'control: the preview is in flight').toHaveBeenCalledTimes(1)
    const status = Array.from(container.querySelectorAll('[role="status"]'), (e) => e.textContent?.trim())
    expect(status).toEqual(['Checking your invite…'])
    expect(allButtons().length, 'no control while loading').toBe(0)
    expect(container.querySelectorAll('input, a').length).toBe(0)
    expect(headings(), 'no heading while loading').toEqual([])
    expect(text()).not.toContain(INVALID_HEADING)

    await act(async () => finish())
    await settle()
    expect(headings(), 'control: the loading view gives way to the ready view').toEqual([`Join ${WORKSPACE}`])
    expect(container.querySelectorAll('[role="status"]').length).toBe(0)
  })
})

describe('the token stays out of everything but the two bodies', () => {
  it('invitePage_tokenStaysOutOfUrlsHeadersLogsAndPage', async () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m).mockImplementation(() => undefined))
    const fetchMock = stubFetch({
      register: () => json(400, { error: 'Password should be at least 6 characters.' }),
      resend: () => json(202, {}),
    })
    await mount()
    expect(text(), 'the ready view does not print the token').not.toContain(T)
    await toRegisterView()
    await submit()
    expect(alerts(), 'control: the refusal ran').toEqual(['Password should be at least 6 characters.'])
    await submit()

    expect(fetchMock.mock.calls.length, 'control: preview and two register posts').toBe(3)
    for (const [url, init] of fetchMock.mock.calls) {
      expect(url, 'no token in a URL').not.toContain(T)
      expect(JSON.stringify(Array.from(new Headers(init.headers).entries())), `no token in the headers of ${url}`).not.toContain(T)
      expect(String(init.credentials ?? ''), 'no credentials sent').not.toBe('include')
    }
    expect(bodyOf(fetchMock.mock.calls[0] as [string, RequestInit])).toStrictEqual({ token: T })
    expect(text()).not.toContain(T)
    expect(JSON.stringify(Array.from(container.querySelectorAll('[value], [href], [title]'), (e) => [e.getAttribute('href'), e.getAttribute('title')]))).not.toContain(T)
    for (const spy of spies) for (const args of spy.mock.calls) expect(args.map(String).join(' ')).not.toMatch(new RegExp(`${T}`))
  })
})

describe('long values', () => {
  // Secondary (D32): jsdom applies no layout, so this pins the style only. The deployed sweep in subtask 08 is the layout oracle.
  const LONG_WORKSPACE = 'W'.repeat(200)
  const LONG_ADDRESS = `${'x'.repeat(111)}@obi.test`

  function textElementsHolding(needle: string): HTMLElement[] {
    const holding = Array.from(container.querySelectorAll<HTMLElement>('h1, h2, h3, p')).filter((e) => e.textContent?.includes(needle))
    expect(holding.length, `expected a heading or paragraph holding ${needle.slice(0, 12)}…`).toBeGreaterThan(0)
    return holding
  }

  it('invitePage_longValuesWrap', async () => {
    stubFetch({ preview: previewOk({ workspace: LONG_WORKSPACE, email: LONG_ADDRESS }), register: () => json(200, {}), resend: () => json(202, {}) })
    await mount()

    expect(headings()).toEqual([`Join ${LONG_WORKSPACE}`])
    for (const e of [...textElementsHolding(LONG_WORKSPACE), ...textElementsHolding(LONG_ADDRESS)]) {
      expect(e.style.overflowWrap, e.tagName).toBe('anywhere')
    }

    await toRegisterView()
    await submit()
    expect(headings()).toEqual(['Check your email'])
    for (const e of textElementsHolding(LONG_ADDRESS)) expect(e.style.overflowWrap, `sent view ${e.tagName}`).toBe('anywhere')
  })
})
