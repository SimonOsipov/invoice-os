// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Category } from '../data'
import { TEST_DATA } from '../testData'
import { Subscribe } from './Subscribe'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const CATS: Category[] = [
  ...TEST_DATA.categories,
  { id: 'c3', name: 'Tips', headline: 'h', intro: 'i', icon: 'globe', tone: 2, post_count: 0 },
]
const FAILURE = 'We could not send the confirmation email. Try again in a minute.'
const ACCEPTED = () => new Response('{"status":"accepted"}', { status: 202, headers: { 'Content-Type': 'application/json' } })

let container: HTMLDivElement
let root: Root
let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>

async function mount(categories: Category[] = CATS, onHome = () => {}) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () =>
    root.render(
      <main id="blog-main">
        <Subscribe categories={categories} onHome={onHome} />
      </main>,
    ),
  )
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test/')
  fetchMock = vi.fn<typeof fetch>(async () => ACCEPTED())
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const main = () => document.getElementById('blog-main')!
const buttons = () => [...main().querySelectorAll<HTMLButtonElement>('button')]
const byText = (t: string) => buttons().find((b) => b.textContent === t)!
const chips = () => CATS.map((c) => byText(c.name)).filter(Boolean)
const submitButton = () => byText('Subscribe')
const emailInput = () => main().querySelector<HTMLInputElement>('input')!
const text = () => main().textContent ?? ''
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const clickAsync = (el: Element) => act(async () => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))

function type(value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(emailInput(), value)
    emailInput().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('Subscribe', () => {
  it('subscribe_rendersPrototypeCopyAndDefaults', async () => {
    await mount()
    expect(main().querySelector('.t-eyebrow')?.textContent).toBe('Newsletter')
    expect(main().querySelector('h1')?.innerHTML).toContain('New posts,')
    expect(main().querySelector('h1 span')?.textContent).toBe('on your schedule.')
    expect(text()).toContain('Choose the topics you follow and how often you hear from us. Every email has a one-click unsubscribe.')
    expect(chips()).toHaveLength(3)
    expect(chips().map((b) => b.getAttribute('aria-pressed'))).toEqual(['true', 'true', 'true'])
    expect(byText('Weekly digest').getAttribute('aria-pressed')).toBe('true')
    expect(byText('Every post').getAttribute('aria-pressed')).toBe('false')
    expect(text()).toContain("One email every Monday with the week's posts.")
    expect(emailInput().placeholder).toBe('name@company.com')
  })

  it('subscribe_frequencyNoteFollowsTheChoice', async () => {
    await mount()
    click(byText('Every post'))
    expect(text()).toContain('An email each time a post is published in your topics.')
    click(byText('Monthly'))
    expect(text()).toContain('One email on the first working day of each month.')
    expect(byText('Monthly').getAttribute('aria-pressed')).toBe('true')
    expect(byText('Every post').getAttribute('aria-pressed')).toBe('false')
  })

  it('subscribe_emptyEmailError', async () => {
    await mount()
    for (const v of ['', '  ']) {
      type(v)
      await clickAsync(submitButton())
      expect(text()).toContain('Enter your work email.')
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('subscribe_malformedEmailError', async () => {
    await mount()
    for (const v of ['a@b', 'a b@c.co']) {
      type(v)
      await clickAsync(submitButton())
      expect(text()).toContain('Enter a valid email address, such as name@company.com.')
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('subscribe_noTopicError', async () => {
    await mount()
    type('a@b.co')
    chips().forEach(click)
    expect(chips().map((b) => b.getAttribute('aria-pressed'))).toEqual(['false', 'false', 'false'])
    await clickAsync(submitButton())
    expect(text()).toContain('Choose at least one topic.')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('subscribe_zeroCategoriesIsNoTopic', async () => {
    await mount([])
    expect(main().querySelectorAll('[aria-pressed]')).toHaveLength(3) // the frequency buttons only
    type('a@b.co')
    await clickAsync(submitButton())
    expect(text()).toContain('Choose at least one topic.')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('subscribe_errorsShowOnlyAfterSubmit', async () => {
    await mount()
    type('x')
    expect(text()).not.toContain('Enter')
    expect(text()).not.toContain('Choose at least one topic.')
  })

  it('subscribe_postsTheChoices', async () => {
    await mount()
    type(' A@B.co ')
    click(chips()[1])
    click(byText('Every post'))
    await clickAsync(submitButton())
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe('https://gw.test/blog/subscriptions')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toEqual({ email: 'A@B.co', category_ids: ['c1', 'c3'], frequency: 'every_post' })
  })

  it('subscribe_doneTextForEachFrequency', async () => {
    const cases: [string, number, string][] = [
      ['Weekly digest', 3, 'a weekly digest across 3 topics.'],
      ['Every post', 2, 'an email for every new post across 2 topics.'],
      ['Monthly', 1, 'a monthly email across 1 topic.'],
    ]
    for (const [label, n, tail] of cases) {
      await mount()
      type(' A@B.co ')
      chips().slice(n).forEach(click)
      click(byText(label))
      await clickAsync(submitButton())
      expect(text()).toContain('Check your inbox.')
      expect(text()).toContain(`We sent a confirmation link to A@B.co. Once confirmed, you will get ${tail}`)
      act(() => root.unmount())
      container.remove()
    }
    await mount() // afterEach unmounts a live root
  })

  it('subscribe_backToTheBlogGoesHome', async () => {
    const onHome = vi.fn()
    await mount(CATS, onHome)
    type('a@b.co')
    await clickAsync(submitButton())
    click(byText('Back to the blog'))
    expect(onHome).toHaveBeenCalledTimes(1)
  })

  it('subscribe_changePreferencesKeepsValues', async () => {
    await mount()
    type('a@b.co')
    click(chips()[0])
    click(byText('Monthly'))
    await clickAsync(submitButton())
    expect(text()).toContain('Check your inbox.')
    click(byText('Change preferences'))
    expect(emailInput().value).toBe('a@b.co')
    expect(chips().map((b) => b.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'true'])
    expect(byText('Monthly').getAttribute('aria-pressed')).toBe('true')
  })

  it('subscribe_failureShowsLineAndKeepsForm', async () => {
    const failures: (() => Promise<Response>)[] = [
      () => Promise.reject(new TypeError('network')),
      async () => new Response('{"error":"x"}', { status: 502 }),
      async () => new Response('{"error":"x"}', { status: 503 }),
      async () => new Response('{"error":"x"}', { status: 400 }),
    ]
    await mount()
    type('a@b.co')
    click(byText('Monthly'))
    for (const fail of failures) {
      fetchMock.mockImplementationOnce(fail)
      await clickAsync(submitButton())
      expect(text()).toContain(FAILURE)
      expect(emailInput().value).toBe('a@b.co')
      expect(byText('Monthly').getAttribute('aria-pressed')).toBe('true')
      expect(submitButton().disabled).toBe(false)
    }
  })

  it('subscribe_postsTopicsInCategoryOrderAfterReselect', async () => {
    await mount()
    type('a@b.co')
    click(chips()[0])
    click(chips()[0])
    await clickAsync(submitButton())
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body)).category_ids).toEqual(['c1', 'c2', 'c3'])
  })

  it('subscribe_secondClickWhileSendingSendsNothing', async () => {
    fetchMock.mockImplementationOnce(() => new Promise<Response>(() => {}))
    await mount()
    type('a@b.co')
    await clickAsync(submitButton())
    await clickAsync(submitButton())
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('subscribe_failureThenRetrySucceedsAndClearsTheLine', async () => {
    await mount()
    type('a@b.co')
    fetchMock.mockImplementationOnce(async () => new Response('{}', { status: 502 }))
    await clickAsync(submitButton())
    expect(text()).toContain(FAILURE)
    let release!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => (release = r)))
    await clickAsync(submitButton())
    expect(text()).not.toContain(FAILURE)
    await act(async () => release(ACCEPTED()))
    expect(text()).toContain('Check your inbox.')
  })

  it('subscribe_noGatewayShowsFailureWithoutFetch', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    try {
      await mount()
      type('a@b.co')
      await clickAsync(submitButton())
      await new Promise((r) => setTimeout(r, 0))
      expect(text()).toContain(FAILURE)
      expect(fetchMock).not.toHaveBeenCalled()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      process.off('unhandledRejection', unhandled)
    }
  })

  it('subscribe_buttonDisabledWhileSending', async () => {
    let release!: (r: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => (release = r)))
    await mount()
    type('a@b.co')
    await clickAsync(submitButton())
    expect(submitButton().disabled).toBe(true)
    await act(async () => release(ACCEPTED()))
    expect(text()).toContain('Check your inbox.')
  })
})
