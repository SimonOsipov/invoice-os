// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Confirm } from './Confirm'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const KEY = 'ascomply.blogConfirmToken'
const TOKEN = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const RETRY = 'We could not confirm right now. Try again in a minute.'
const GONE = 'This link has expired or was already used.'
const reply = (status: number, body: unknown = {}) => () =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let container: HTMLDivElement
let root: Root
let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>
const onHome = vi.fn()
const onSubscribe = vi.fn()

async function mount(token: string | null = TOKEN) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () =>
    root.render(
      <main id="blog-main">
        <Confirm token={token} onHome={onHome} onSubscribe={onSubscribe} />
      </main>,
    ),
  )
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test/')
  sessionStorage.setItem(KEY, TOKEN)
  fetchMock = vi.fn<typeof fetch>(async () => reply(200, { status: 'confirmed', unsubscribed_everywhere: false })())
  vi.stubGlobal('fetch', fetchMock)
  onHome.mockClear()
  onSubscribe.mockClear()
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  sessionStorage.clear()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const text = () => document.getElementById('blog-main')!.textContent ?? ''
const buttons = () => [...document.querySelectorAll<HTMLButtonElement>('#blog-main button')]
const byText = (t: string) => buttons().find((b) => b.textContent === t)
const clickAsync = (el: Element) => act(async () => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))

describe('Confirm', () => {
  it('confirm_noRequestOnLoad', async () => {
    await mount()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(byText('Confirm subscription')).toBeTruthy()
    expect(text()).toContain('Confirm your subscription.')
  })

  it('confirm_clickPostsTokenOnce', async () => {
    await mount()
    const button = byText('Confirm subscription')!
    await act(async () => {
      button.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      button.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toBe('https://gw.test/blog/subscriptions/confirm')
    expect(JSON.parse(String(init?.body))).toEqual({ token: TOKEN })
  })

  it('confirm_successState', async () => {
    await mount()
    await clickAsync(byText('Confirm subscription')!)
    expect(text()).toContain('You are subscribed.')
    expect(byText('Confirm subscription')).toBeUndefined()
    expect(sessionStorage.getItem(KEY)).toBeNull()
    await clickAsync(byText('Back to the blog')!)
    expect(onHome).toHaveBeenCalledTimes(1)
  })

  it('confirm_unsubscribedEverywhereState', async () => {
    fetchMock.mockImplementation(async () => reply(200, { status: 'confirmed', unsubscribed_everywhere: true })())
    await mount()
    await clickAsync(byText('Confirm subscription')!)
    expect(text()).toContain('Your topics are saved, but blog emails will not reach you.')
    expect(text()).toContain('This address is unsubscribed from all ASComply email, so we cannot send you blog posts.')
    expect(text()).not.toContain('You are subscribed.')
    expect(byText('Back to the blog')).toBeTruthy()
    expect(sessionStorage.getItem(KEY)).toBeNull()
  })

  it('confirm_410State', async () => {
    fetchMock.mockImplementation(async () => reply(410, { error: 'link expired or used' })())
    await mount()
    await clickAsync(byText('Confirm subscription')!)
    expect(text()).toContain(GONE)
    expect(byText('Confirm subscription')).toBeUndefined()
    expect(byText('Back to the blog')).toBeTruthy()
    await clickAsync(byText('Subscribe again')!)
    expect(onSubscribe).toHaveBeenCalledTimes(1)
  })

  it('confirm_noTokenState', async () => {
    await mount(null)
    expect(text()).toContain(GONE)
    expect(byText('Confirm subscription')).toBeUndefined()
    expect(byText('Subscribe again')).toBeTruthy()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('confirm_retryableFailure', async () => {
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    const cases: [string, () => Promise<Response>][] = [
      ['429', async () => reply(429, { error: 'too many requests' })()],
      ['502', async () => reply(502, { error: 'bad gateway' })()],
      ['reject', async () => Promise.reject(new TypeError('network'))],
    ]
    for (const [name, impl] of cases) {
      fetchMock.mockImplementation(impl)
      await mount()
      await clickAsync(byText('Confirm subscription')!)
      expect(text(), name).toContain(RETRY)
      expect(byText('Confirm subscription')?.disabled, name).toBe(false)
      expect(sessionStorage.getItem(KEY), name).toBe(TOKEN)
      act(() => root.unmount())
      container.remove()
    }
    vi.stubEnv('VITE_GATEWAY_URL', '')
    fetchMock.mockClear()
    await mount()
    await clickAsync(byText('Confirm subscription')!)
    expect(text()).toContain(RETRY)
    expect(fetchMock).not.toHaveBeenCalled()
    await new Promise((r) => setTimeout(r, 0))
    process.off('unhandledRejection', unhandled)
    expect(unhandled).not.toHaveBeenCalled()
  })
})
