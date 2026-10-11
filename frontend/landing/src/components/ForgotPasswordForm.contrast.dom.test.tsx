// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// AC-4: the reset notice's text contrast, resolved from the token values (jsdom applies no CSS).
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LANDING_SRC, contrast, customPropValues, readV2Css, resolveTextContrast, stripSource } from '../cssScan.test.util'
import { ForgotPasswordForm } from './ForgotPasswordForm'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const v2 = readV2Css()
const landingCss = (f: string) => stripSource(f, readFileSync(join(LANDING_SRC, 'styles', f), 'utf8'))
const CSS = [v2['tokens/colors.css'], v2['utilities.css'], landingCss('ds.css'), landingCss('landing.css')]
const TOKENS = customPropValues(v2['tokens/colors.css'])
const hex = (name: string) => TOKENS.get(name)!.toLowerCase()

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('the reset notice contrast', () => {
  it('resetNotice_contrastIsAtLeast4_5', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 202 })))
    await act(async () => root.render(createElement(ForgotPasswordForm, { onBack: vi.fn() })))
    const input = container.querySelector<HTMLInputElement>('input[type="email"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'a@corp.example')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => {
      container.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    })
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })

    const rows = resolveTextContrast(container, CSS).filter((r) => r.el.matches('[role="status"] p'))
    expect(rows, 'the notice is measured once').toHaveLength(1)
    const [row] = rows
    expect([row.fg, row.bg, row.ambiguous]).toEqual([hex('--ink'), hex('--accent'), false])
    expect([row.fg, row.bg]).toEqual(['#0b3032', '#f5bc88'])
    expect(row.ratio).toBeGreaterThanOrEqual(4.5)
    expect(contrast('#f5bc88', '#ffffff'), 'control: peach text on white fails').toBeLessThan(4.5)
  })
})
