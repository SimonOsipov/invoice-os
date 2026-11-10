// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { CHECK_EYEBROW, CHECK_INTRO, COMPANY_LINE, TRUST_CUES } from './copy'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  },
})

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const SHEET = { columns: ['Inv', 'Seller'], sample_rows: [['v0', 'v1']], header_row: 1, rows_total: 1, mapping: { invoice_number: 'Inv' }, source: 'heuristic' }

let container: HTMLDivElement
let root: Root

async function mount(answers: Record<string, () => Response> = {}) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const route = Object.keys(answers).find((r) => String(input).endsWith(r))
      if (!route) throw new Error(`unexpected request ${String(input)}`)
      return answers[route]()
    }),
  )
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App />))
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://api.example.test')
  store.set('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  store.clear()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))))
}
const q = <T extends Element>(sel: string) => container.querySelector<T>(sel)!
const items = () => [...container.querySelectorAll<HTMLElement>('.check-progress li')]
const item = (label: string) => items().find((li) => li.querySelector('.check-step-lbl')!.textContent === label)!
const current = () => items().filter((li) => li.getAttribute('aria-current') === 'step').map((li) => li.querySelector('.check-step-lbl')!.textContent)
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === name)!
async function pick(f: File) {
  const input = q<HTMLInputElement>('input[type="file"]')
  Object.defineProperty(input, 'files', { value: [f], configurable: true })
  await act(async () => void input.dispatchEvent(new Event('change', { bubbles: true })))
  await settle()
}
async function type(el: HTMLInputElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(async () => {
    set.call(el, value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('check page frame', () => {
  it('frame_headerLinksTheLanding', async () => {
    await mount()
    const head = q('header.check-head')
    const logo = head.querySelector('a[aria-label="ASComply Africa home"]')!
    expect(logo.getAttribute('href')).toBe('https://www.ascomply.com/')
    const link = (name: string) => [...head.querySelectorAll('a')].find((a) => a.textContent === name)!
    expect(link('What you get').getAttribute('href')).toBe('#what')
    expect(link('Platform').getAttribute('href')).toBe('https://www.ascomply.com/')
    expect(link('Sign up').getAttribute('href')).toBe('https://www.ascomply.com/?register')
  })

  it('frame_heroCarriesTheCopyAndTrustCues', async () => {
    await mount()
    expect(q('.check-hero .t-eyebrow').textContent).toBe(CHECK_EYEBROW)
    expect(CHECK_EYEBROW).toBe('FREE E-INVOICE READINESS CHECK')
    const lis = [...q('ul[aria-label="About this check"]').querySelectorAll('li')]
    expect(lis.map((li) => [li.querySelector('strong')!.textContent, li.textContent])).toEqual(
      TRUST_CUES.map((c) => [c.title, c.title + c.text]),
    )
    expect(container.textContent!.split(CHECK_INTRO)).toHaveLength(2)
  })

  it('frame_stripFollowsASpreadsheet', async () => {
    await mount({ '/readiness-check/mapping': () => json(200, SHEET) })
    expect(current()).toEqual(['Upload'])
    await pick(new File(['a'], 'a.csv'))
    expect(current()).toEqual(['Match columns'])
    expect(item('Upload').className).toBe('is-done')
    await click(button('Continue'))
    expect(current()).toEqual(['Email'])
  })

  it('frame_stripSkipsMatchColumnsForAPdf', async () => {
    await mount()
    await pick(new File(['a'], 'a.pdf'))
    expect(item('Match columns').className).toBe('is-skipped')
    expect(item('Match columns').textContent).toContain('Not needed for PDF')
    expect(current()).toEqual(['Email'])
    const m = q('.check-progress-m')
    expect(m.textContent).toContain('Step 2 of 3')
    expect(m.querySelector('.check-progress-m-name')!.textContent).toBe('Email')
  })

  it('frame_sentLeavesNoCurrentStep', async () => {
    await mount({ '/readiness-check/run': () => json(200, { status: 'sent', checks_left: 2 }) })
    await pick(new File(['a'], 'a.pdf'))
    await type(q<HTMLInputElement>('input[type="email"]'), 'a@example.com')
    await click(q('input[type="checkbox"]'))
    await click(button('Send'))
    await settle()
    expect(current()).toEqual([])
    expect(items().map((li) => li.className)).toEqual(['is-done', 'is-skipped', 'is-done', 'is-done'])
  })

  it('frame_onlyTheMapStepCompactsTheHero', async () => {
    await mount({ '/readiness-check/mapping': () => json(200, SHEET) })
    const wide = () => q('.check-split').classList.contains('check-split--wide')
    expect(wide()).toBe(false)
    await pick(new File(['a'], 'a.csv'))
    expect(wide()).toBe(true)
    await click(button('Continue'))
    expect(wide()).toBe(false)
  })

  it('frame_footerCarriesLogoCompanyAndLinks', async () => {
    await mount()
    const foot = q('footer')
    expect(foot.querySelector('.ds-logo')).not.toBeNull()
    expect(foot.textContent).toContain(COMPANY_LINE)
    expect(foot.querySelector('a')!.getAttribute('href')).toBe('https://www.ascomply.com/privacy')
    expect(foot.querySelector('a')!.textContent).toBe('Privacy')
    expect([...foot.querySelectorAll('button')].map((b) => b.textContent)).toEqual(['Cookie choices'])
  })
})
