import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { RotateConfirm } from './components/RotateConfirm'
import { Toast } from './components/Toast'
import { TopBar } from './components/TopBar'

// jsdom drops backdrop-filter and Chromium aliases the prefixed one, so SSR markup is the oracle.
const openTags = (html: string) => html.match(/<[a-z][^>]*>/g) ?? []
const attr = (tag: string, name: string) => tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? ''

describe('v2 shell', () => {
  it('SH-01 the header declares the blur token on both filter properties', () => {
    const html = renderToStaticMarkup(createElement(TopBar, { screen: 'overview', env: 'sandbox', onSetEnv: () => {} }))
    const header = openTags(html).find((t) => t.startsWith('<header'))
    expect(header, `no <header in ${html}`).toBeDefined()
    const style = attr(header!, 'style')
    expect(style.length).toBeGreaterThan(0)

    expect.soft(style, 'header background').toContain('background:var(--header-bg)')
    expect.soft(style, 'unprefixed filter').toContain('backdrop-filter:blur(var(--header-blur))')
    expect.soft(style, '-webkit-backdrop-filter missing or not the token').toContain('-webkit-backdrop-filter:blur(var(--header-blur))')
    expect.soft(style, 'header holds oklch').not.toContain('oklch')
  })

  it('SH-02 the modal scrim mixes --surface and blurs on both properties', () => {
    const html = renderToStaticMarkup(createElement(RotateConfirm, { env: 'LIVE', onClose: () => {}, onConfirm: () => {} }))
    const [scrim = '', panel = ''] = openTags(html)
    expect(scrim, `no scrim in ${html}`).not.toBe('')
    expect(panel, `no panel in ${html}`).not.toBe('')
    const scrimStyle = attr(scrim, 'style')
    const panelStyle = attr(panel, 'style')
    expect(scrimStyle.length).toBeGreaterThan(0)
    expect(panelStyle.length).toBeGreaterThan(0)

    expect.soft(scrimStyle, 'scrim background').toContain('color-mix(in srgb, var(--surface) 55%, transparent)')
    expect.soft(scrimStyle, 'unprefixed filter').toContain('backdrop-filter:blur(6px)')
    expect.soft(scrimStyle, '-webkit-backdrop-filter missing').toContain('-webkit-backdrop-filter:blur(6px)')
    expect.soft(panelStyle, 'panel radius').toContain('border-radius:var(--radius-lg)')
    expect.soft(panelStyle, 'panel shadow').not.toContain('box-shadow')
  })

  it('SH-03 the toast is a dark scope for both tones', () => {
    const cases = [
      { tone: 'ok', icon: 'var(--teal-300)' },
      { tone: 'red', icon: 'var(--status-red-text)' },
    ] as const
    for (const { tone, icon } of cases) {
      const html = renderToStaticMarkup(createElement(Toast, { toast: { msg: 'm', tag: '', tone } }))
      const [root = '', iconSpan = ''] = openTags(html)
      expect(root, `${tone}: no root in ${html}`).not.toBe('')
      expect(iconSpan, `${tone}: no icon span in ${html}`).not.toBe('')
      const rootStyle = attr(root, 'style')
      expect(rootStyle.length).toBeGreaterThan(0)

      expect.soft(attr(root, 'class').split(/\s+/), `${tone}: root class`).toContain('asc-dark')
      expect.soft(rootStyle, `${tone}: root background`).toContain('background:var(--surface)')
      expect.soft(rootStyle, `${tone}: root shadow`).toContain('box-shadow:var(--shadow-card)')
      expect.soft(attr(iconSpan, 'style'), `${tone}: icon colour`).toContain(`color:${icon}`)
    }
  })
})
