// @vitest-environment jsdom
// AC 9: the peach band's text contrast, computed from the CSS token values. jsdom applies no CSS,
// so the cascade is resolved by resolveTextContrast.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LANDING_SRC, customPropValues, readV2Css, resolveTextContrast, stripSource, type ContrastRow } from '../cssScan.test.util'
import { mountView, click, show, spyConsoleError, unmountView, type View } from './ds/dsDom.test.util'
import { Coverage } from './Coverage'

const v2 = readV2Css()
const landingCss = (f: string) => stripSource(f, readFileSync(join(LANDING_SRC, 'styles', f), 'utf8'))
const CSS = [v2['tokens/colors.css'], v2['utilities.css'], landingCss('ds.css'), landingCss('landing.css')]
const TOKENS = customPropValues(v2['tokens/colors.css'])
const hex = (name: string) => TOKENS.get(name)!.toLowerCase()

const V_HEADER_PARAGRAPH =
  'Start with a focused launch. Build toward a connected African market, with workflows shaped around local requirements.'

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(() => {
  view = mountView()
  consoleError = spyConsoleError()
})

afterEach(() => {
  expect(consoleError, 'console.error was called').not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

/** Rows below 3:1 inside an h2 and below 4.5:1 elsewhere, plus every ambiguous row. */
function failures(rows: ContrastRow[]): string[] {
  return rows.flatMap((r) => {
    if (r.ambiguous) return [`${r.text}: ambiguous colour`]
    const need = r.el.closest('h2') ? 3 : 4.5
    return r.ratio < need ? [`${r.text}: ${r.fg} on ${r.bg} = ${r.ratio.toFixed(2)}, needs ${need}`] : []
  })
}

function planted(html: string, extraCss: string[] = []): ContrastRow[] {
  const band = document.createElement('section')
  band.className = 'band-peach'
  band.innerHTML = html
  document.body.appendChild(band)
  try {
    return resolveTextContrast(band, [...CSS, ...extraCss])
  } finally {
    band.remove()
  }
}

describe('Coverage band contrast', () => {
  it('CT-01 the peach band’s text meets AC 9', async () => {
    // Controls first: the resolver must see a low ratio, a conflict and an !important.
    const step = planted('<p style="color:var(--step-label)">x</p>')
    expect(step, 'control: one planted row').toHaveLength(1)
    expect([step[0].fg, step[0].bg, step[0].ratio.toFixed(2)], 'control: --step-label on peach').toEqual([hex('--step-label'), hex('--peach-band'), '2.63'])

    const link = planted('<p style="color:var(--link)">x</p><h2><span style="color:var(--link)">y</span></h2>')
    expect(link, 'control: two planted --link rows').toHaveLength(2)
    expect(link[0].ratio.toFixed(2), 'control: --link on peach').toBe('4.35')
    expect(failures([link[0]]), 'control: 4.35 fails 4.5 outside an h2').toHaveLength(1)
    const inH2 = link.find((r) => r.text === 'y')
    expect(inH2, 'control: the span inside the h2 resolves').toBeDefined()
    expect(failures([inH2!]), 'control: 4.35 passes 3 inside an h2').toEqual([])

    const conflict = planted('<p class="cx-a cx-b">x</p>', ['.cx-a { color: #111111; } .cx-b { color: #222222; }'])
    expect(conflict, 'control: one conflict row').toHaveLength(1)
    expect(conflict[0].ambiguous, 'control: two class colours without !important are ambiguous').toBe(true)
    const wins = planted('<p class="cx-a cx-b">x</p>', ['.cx-a { color: #111111 !important; } .cx-b { color: #222222; }'])
    expect(wins, 'control: one row').toHaveLength(1)
    expect([wins[0].ambiguous, wins[0].fg], 'control: the !important colour wins').toEqual([false, '#111111'])

    const cascade = ['.cx-a { color: #111111; } .band-peach .cx-a { color: #222222; } .band-sage .cx-a { color: #333333; }']
    const band = planted('<p class="cx-a">x</p>', cascade)
    expect([band[0].fg, band[0].ambiguous], 'control: a band rule beats a class rule, and only the enclosing band applies').toEqual(['#222222', false])
    const inherit = planted('<div style="color:var(--step-label)"><span>x</span></div><p>y</p>')
    expect(inherit, 'control: two planted rows').toHaveLength(2)
    expect(inherit.map((r) => r.fg), 'control: a span inherits its parent; a bare p takes the band colour').toEqual([hex('--step-label'), hex('--accent-foreground')])

    await show(view, createElement(Coverage))
    const root = view.container.querySelector('section')
    expect(root, 'Coverage renders a section').not.toBeNull()
    const rows = resolveTextContrast(root!, CSS).filter((r) => !r.el.closest('[data-cov-card],[data-cov-panel]'))
    const onPeach = rows.filter((r) => r.bg === hex('--peach-band'))
    expect(hex('--peach-band'), 'control: the token read is the peach band').toBe('#efb684')
    expect(onPeach.length, 'rows on the peach background').toBeGreaterThanOrEqual(6)

    // An unresolved colour would fall back to black on white and pass.
    expect(rows.filter((r) => !/^#[0-9a-f]{6}$/.test(r.fg) || !/^#[0-9a-f]{6}$/.test(r.bg)).map((r) => r.text), 'rows with an unresolved colour').toEqual([])

    const has = (pred: (r: ContrastRow) => boolean) => onPeach.some(pred)
    expect(has((r) => r.el.classList.contains('t-eyebrow')), 'the eyebrow resolves to peach').toBe(true)
    expect(onPeach.find((r) => r.el.classList.contains('t-eyebrow'))?.fg, 'the band rule colours the eyebrow --primary').toBe(hex('--primary'))
    expect(has((r) => r.el.tagName === 'H2'), 'the h2 resolves to peach').toBe(true)
    expect(has((r) => r.el.matches('h2 span.t-hl-peach')), 'the h2 span resolves to peach').toBe(true)
    expect(has((r) => r.text === V_HEADER_PARAGRAPH), 'the header paragraph resolves to peach').toBe(true)
    expect(onPeach.filter((r) => r.el.matches('button[aria-pressed="false"]')), 'the two unpressed tabs resolve to peach').toHaveLength(2)
    expect(failures(rows)).toEqual([])
  })

  it('CT-03 the active tab is measured on its own fill', async () => {
    await show(view, createElement(Coverage))
    const root = view.container.querySelector('section')
    expect(root, 'Coverage renders a section').not.toBeNull()
    const rows = resolveTextContrast(root!, CSS)
    const tabRows = rows.filter((r) => r.el.matches('button[aria-pressed]'))
    expect(tabRows, 'three tab rows').toHaveLength(3)

    const on = tabRows.filter((r) => r.el.getAttribute('aria-pressed') === 'true')
    const off = tabRows.filter((r) => r.el.getAttribute('aria-pressed') === 'false')
    expect([on.length, off.length]).toEqual([1, 2])
    expect(on[0].bg, 'pressed tab fill is --primary, not the band').toBe(hex('--primary'))
    expect(on[0].bg).not.toBe(hex('--peach-band'))
    expect(on[0].fg).toBe(hex('--primary-foreground'))
    expect(on[0].ratio).toBeGreaterThanOrEqual(4.5)
    for (const r of off) {
      expect([r.bg, r.fg], `unpressed tab "${r.text}"`).toEqual([hex('--peach-band'), hex('--primary')])
    }

    // The fill follows the selection.
    click(tabRows[1].el as HTMLElement)
    const after = resolveTextContrast(root!, CSS).filter((r) => r.el.matches('button[aria-pressed="true"]'))
    expect(after.map((r) => r.text), 'the clicked tab is now the filled one').toEqual([tabRows[1].text])
    expect(after[0].bg).toBe(hex('--primary'))
  })
})
