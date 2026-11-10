// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { EXAMPLE_MAIL, WHAT_CTA, WHAT_EMAIL, WHAT_EYEBROW, WHAT_H2, WHAT_LIMITS, WHAT_RULES } from './copy'

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

let container: HTMLDivElement
let root: Root

beforeEach(async () => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://api.example.test')
  store.set('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App />))
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  store.clear()
  vi.unstubAllEnvs()
})

const what = () => container.querySelector<HTMLElement>('section#what')!
const text = (el: Element) => el.textContent ?? ''

describe('what you get', () => {
  it('whatYouGet_carriesTheCopyAndNumbers', () => {
    const s = what()
    expect(s.classList.contains('check-what')).toBe(true)
    expect(text(s.querySelector('.t-eyebrow')!)).toBe(WHAT_EYEBROW)
    const h2 = s.querySelector('h2')!
    expect(s.getAttribute('aria-labelledby')).toBe(h2.id)
    expect(text(h2)).toBe(WHAT_H2.join(''))
    expect(h2.querySelector('span')!.textContent).toBe(WHAT_H2[1])
    const h3s = [...s.querySelectorAll('h3')]
    expect(h3s.map(text)).toEqual([WHAT_RULES.title, WHAT_EMAIL.title])
    expect(h3s.map((h) => text(h.nextElementSibling!))).toEqual([WHAT_RULES.text, WHAT_EMAIL.text])
    const cells = [...s.querySelectorAll('.check-limits > div')]
    expect(cells.map((c) => [text(c.querySelector('b')!), text(c.querySelector('span')!)])).toEqual(WHAT_LIMITS.map((l) => [l.n, l.caption]))
    const cta = [...s.querySelectorAll('a')].find((a) => text(a) === WHAT_CTA)!
    expect(cta.getAttribute('href')).toBe('#check')
    // Literal numbers and words: the comparisons above restate WHAT_*.
    expect(cells.map((c) => text(c.querySelector('b')!))).toEqual(['2', '3', '15 MiB', '4'])
    expect(cells.map((c) => text(c.querySelector('span')!))).toEqual([
      'invoices checked per file',
      'free checks per email address',
      'largest file, one at a time',
      'file types: CSV, XLSX, PDF, DOCX',
    ])
    expect(text(s.querySelector('.t-eyebrow')!)).toBe('WHAT YOU GET')
    expect(text(h2)).toBe('Two invoices, checked rule by rule.The results land in your inbox.')
    expect(text(cta)).toBe('Check a file')
  })

  it('whatYouGet_exampleFollowsTheRealMail', () => {
    const fig = what().querySelector('figure[aria-label="Example results email"]')!
    const t = text(fig)
    expect(t).toContain(EXAMPLE_MAIL.from)
    expect(t).toContain(EXAMPLE_MAIL.subject)
    const [bad, good] = [...fig.querySelectorAll('.check-mail-inv')]
    expect(text(bad.querySelector('.check-mail-no')!)).toBe('INV-1041')
    const rows = [...bad.querySelectorAll('.check-mail-row')].map((r) => [text(r.querySelector('b')!), text(r.querySelector('span')!)])
    expect(rows).toEqual([
      ['Supplier TIN', 'Supplier TIN is required.'],
      ['Buyer TIN', 'Buyer TIN, when present, must be NNNNNNNN-NNNN or an RN- incorporation number.'],
    ])
    expect(text(good.querySelector('.check-mail-no')!)).toBe('INV-1042')
    expect(text(good)).toContain('Passes every rule')
    expect(t).toContain(EXAMPLE_MAIL.line)
    expect(EXAMPLE_MAIL.line).toBe('Each failed rule shows its field and the rule’s message.')
    expect(text(fig.querySelector('figcaption')!)).toBe(EXAMPLE_MAIL.caption)
  })

  it('whatYouGet_carriesNoPassFailList', () => {
    const t = text(what())
    for (const banned of ['PASS', 'FAIL', '1 fail', 'All pass', 'which rules pass, which fail']) expect(t).not.toContain(banned)
  })

  it('whatYouGet_namesWarningsAndNotChecked', () => {
    const body = text(what().querySelectorAll('h3')[1].nextElementSibling!)
    expect(body).toContain('Warnings')
    expect(body).toContain('Not checked')
    expect(text(what().querySelector('figcaption')!)).toContain('warnings or rules not checked')
  })
})
