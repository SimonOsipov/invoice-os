// QA Mode B gap-fill for the DemoLeadForm extraction. The Stage 2.5 specs (S1-S17)
// transcribe the plan; these rows cover what the plan's table did not: the popup's
// DOM golden, the panel padding, the CSS split, the hook-order contract
// each slot at a time, and the comment-blind source scans in analytics.test.ts.
/// <reference types="node" />
import { describe, expect, it, vi } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { DemoLeadForm, DEMO_FORM_CSS } from './DemoLeadForm'
import { DemoModal } from './DemoModal'

const HERE = dirname(fileURLToPath(import.meta.url))
const LEAD_FORM_SRC = readFileSync(join(HERE, 'DemoLeadForm.tsx'), 'utf8')
const MODAL_SRC = readFileSync(join(HERE, 'DemoModal.tsx'), 'utf8')
const APP_SRC = readFileSync(join(HERE, '..', 'App.tsx'), 'utf8')

function noop() {}

// Block comments then line comments. A source scan that matches a `//` line can be
// satisfied by a comment quoting the code it guards — see the analytics-scan rows below.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
}

// Seeds one React useState slot by call order, the idiom DemoModal.adversarial.test.tsx
// already relies on, so the panels (which only exist after a state transition) are
// reachable from a static render.
async function renderSeeded(
  slot: number,
  value: unknown,
  props: { idPrefix: string; onDone?: () => void },
): Promise<string> {
  vi.resetModules()
  vi.doMock('react', async (importOriginal) => {
    const actual = await importOriginal<typeof import('react')>()
    let call = 0
    return {
      ...actual,
      useState: <T,>(initial: T) => {
        call += 1
        return call === slot ? actual.useState(value as T) : actual.useState(initial)
      },
    }
  })
  try {
    const { renderToStaticMarkup: render } = await import('react-dom/server')
    const mod = await import('./DemoLeadForm')
    return render(createElement(mod.DemoLeadForm, props))
  } finally {
    vi.doUnmock('react')
    vi.resetModules()
  }
}

// Normalised `selector { body }` pairs, @media wrappers flattened away, so a rule can
// move between blocks (which the extraction does) without this seeing a change.
function cssRules(css: string): string[] {
  const flat = css.replace(/@media\s*\([^)]*\)\s*\{/g, '')
  return Array.from(flat.matchAll(/([^{}]+)\{([^{}]*)\}/g))
    .map((m) => `${m[1].split(/\s+/).filter(Boolean).join(' ')} { ${m[2].split(/\s+/).filter(Boolean).join(' ')} }`)
    .filter((r) => !r.startsWith(' '))
}

// The popup's rule SET is the contract (order aside): MODAL_CHROME_CSS and DEMO_FORM_CSS.
const POPUP_CSS_RULES = [
  'from { opacity: 0; }',
  'to { opacity: 1; }',
  'from { opacity: 0; transform: translateY(8px); }',
  'to { opacity: 1; transform: none; }',
  'to { transform: rotate(360deg); }',
  '.dm-input, .dm-select { transition: border-color var(--dur-fast) var(--ease-out); }',
  '.dm-input:focus, .dm-select:focus { outline: 2px solid var(--ring); outline-offset: 2px; }',
  '.dm-err { border-color: var(--destructive) !important; }',
  '.dm-select { appearance: none; -webkit-appearance: none; }',
  '.si-close { transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out); }',
  '.si-close:hover { background: var(--muted); color: var(--ink); }',
  '.si-close:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }',
  '.dm-row { flex-direction: column !important; align-items: stretch !important; }',
]

const MODAL_PANEL_STYLE = 'padding:36px 24px 26px;text-align:center;display:grid;gap:12px;justify-items:center'

describe('the popup DOM survives the extraction (A1-A3)', () => {
  const popup = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

  it('A1: the popup carries every id it shipped with, plus dm-marketing and no other new one', () => {
    const ids = Array.from(popup.matchAll(/\sid="([^"]+)"/g)).map((m) => m[1])
    expect(ids).toEqual(['dm-name', 'dm-email', 'dm-company', 'dm-role', 'dm-size', 'dm-volume', 'dm-consent', 'dm-marketing'])
  })

  it('A2: the single <style> carries exactly the v2 chrome and form rule set, order aside', () => {
    const styles = Array.from(popup.matchAll(/<style>([\s\S]*?)<\/style>/g))
    expect(styles.length).toBe(1)
    const rules = cssRules(styles[0][1])
    expect(rules.length).toBe(POPUP_CSS_RULES.length)
    expect([...rules].sort()).toEqual([...POPUP_CSS_RULES].sort())
  })

  it('A3: the success panel is the one node that gained markup — id + tabindex, nothing else', async () => {
    const html = await renderSeeded(3, 'success', { idPrefix: 'dm', onDone: noop })
    expect(html).toContain(`<div id="dm-success" tabindex="-1" style="${MODAL_PANEL_STYLE}">`)
    // Pre-extraction the error panel's wrapper carried neither; it must still carry neither.
    const error = await renderSeeded(3, 'error', { idPrefix: 'dm', onDone: noop })
    expect(error).toContain(`<div style="${MODAL_PANEL_STYLE}">`)
  })
})

describe('the form and both panels carry the modal padding (A4)', () => {
  it('A4: the form and both panels are padded', async () => {
    const modalForm = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'dm' }))
    expect(modalForm.startsWith('<form noValidate="" style="padding:24px 24px 22px">')).toBe(true)
    // Through DemoModal too: the popup is the form's only production mount.
    expect(renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))).toContain(
      '<form noValidate="" style="padding:24px 24px 22px">',
    )

    for (const step of ['success', 'error'] as const) {
      const modalPanel = await renderSeeded(3, step, { idPrefix: 'dm', onDone: noop })
      expect(modalPanel).toContain(MODAL_PANEL_STYLE)
    }
  })
})

describe('DEMO_FORM_CSS is the form half only (A5)', () => {
  it('A5: it carries the six form rules and none of the shell rules', () => {
    for (const needle of ['dmSpin', '.dm-input', '.dm-input:focus', '.dm-err', '.dm-select', '.dm-row']) {
      expect(DEMO_FORM_CSS).toContain(needle)
    }
    // Other components render this string beside their own shell CSS; a shell rule here would restyle them.
    for (const shellOnly of ['ovIn', 'cardIn', '.si-close', '.dm-overlay']) {
      expect(DEMO_FORM_CSS).not.toContain(shellOnly)
    }
  })
})

describe('the heading is a slot with no wrapper (A6)', () => {
  it('A6: a given heading is the form’s first child; an absent one emits nothing at all', () => {
    const withHeading = renderToStaticMarkup(
      createElement(DemoLeadForm, { idPrefix: 'zz', heading: createElement('h9' as 'h1', null, 'SLOT') }),
    )
    expect(withHeading.startsWith('<form noValidate="" style="padding:24px 24px 22px"><h9>SLOT</h9><div style="display:flex')).toBe(true)

    const without = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'zz' }))
    expect(without.startsWith('<form noValidate="" style="padding:24px 24px 22px"><div style="display:flex')).toBe(true)
  })

  it('A10: the form still closes on the submit button and the reassurance line', () => {
    const html = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'zz' }))
    expect(html).toMatch(/<button type="submit"[^>]*>Book my demo →<\/button>/)
    expect(html.endsWith('No card required</p></form>')).toBe(true)
  })
})

describe('the three useState slots are the ones the adversarial mock assumes (A7)', () => {
  it('A7: slot 1 is form, slot 2 is errors, slot 3 is demoStep', async () => {
    const slot1 = await renderSeeded(
      1,
      { name: 'Seeded Name', email: 'a@b.co', company: 'C', role: 'Other', size: 'Below ₦50m', volume: '100k+', consent: true },
      { idPrefix: 'zz' },
    )
    expect(slot1).toMatch(/<input id="zz-name"[^>]*value="Seeded Name"/)

    const slot2 = await renderSeeded(2, { consent: 'SEEDED CONSENT ERROR' }, { idPrefix: 'zz' })
    expect(slot2).toContain('SEEDED CONSENT ERROR')
    expect(slot2).toMatch(/<input[^>]*id="zz-consent"[^>]*aria-describedby="zz-consent-error"/)

    const slot3 = await renderSeeded(3, 'error', { idPrefix: 'zz' })
    expect(slot3).toContain('Something went wrong')
    expect(slot3).not.toContain('id="zz-name"')
  })

  it('A7b: DemoModal itself declares no useState, so the slots above are DemoLeadForm’s', () => {
    const src = stripComments(MODAL_SRC)
    expect(src).toContain('<DemoLeadForm') // control needle: the file was read and parsed
    expect(src).not.toMatch(/\buseState\b/)
  })
})

describe('the dead submit prop is still wired through (A8)', () => {
  it('A8: DemoModal forwards submit, and App.tsx passes none', () => {
    const modal = stripComments(MODAL_SRC)
    expect(modal).toContain('submit?: (lead: DemoLead) => Promise<void>')
    expect(modal).toContain('submit={submit}')

    const app = stripComments(APP_SRC)
    const site = app.split('\n').find((l) => l.includes('<DemoModal'))
    expect(site, 'control needle: App.tsx must still mount <DemoModal').toBeDefined()
    expect(site).not.toContain('submit=')
  })
})

describe('the analytics source scans see code, not comments (A9)', () => {
  // S1/S2/S5 in analytics.test.ts match raw source. A commented-out copy of the
  // guarded line satisfies every one of them while the real call is gone — verified
  // by mutation, so these rows re-assert the same three facts against stripped source.
  const stripped = stripComments(LEAD_FORM_SRC)

  it('A9a: the trackedHubSpotSubmit call site is live code', () => {
    expect(stripped).toContain('trackedHubSpotSubmit(') // control: the wrapper is called
    const calls = stripped.match(/trackedHubSpotSubmit\(/g) ?? []
    expect(calls.length).toBe(1)
    expect(stripped).toMatch(/await trackedHubSpotSubmit\(\(\) => submitDemoLead\(/)
  })

  it('A9b: the honeypot branch is live code', () => {
    expect(stripped).toContain('if (trap) await runStub()')
    const delays = stripped.match(/setTimeout\(resolve, 1300\)/g) ?? []
    expect(delays.length).toBe(1)
  })

  it('A9c: no identifier-shaped dm- literal survives once comments are stripped', () => {
    expect(stripped).toContain('dm-input') // control: the four exempt class names stay
    expect(/\bdm-(name|email|company|role|size|volume|consent|success|error)/.test(stripped)).toBe(false)
  })
})

// Declaration map of one inline style, so `border-radius:var(--radius)` is not
// satisfied by `border-radius:var(--radius-input)`.
function declarations(style: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const d of style.split(';')) {
    const i = d.indexOf(':')
    if (i > 0) out[d.slice(0, i).trim()] = d.slice(i + 1).trim()
  }
  return out
}

const tagsOf = (html: string, re: string) => Array.from(html.matchAll(new RegExp(`<(?:${re})\\b[^>]*>`, 'g'))).map((m) => m[0])
const attrOf = (tag: string, name: string) => tag.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1] ?? ''
const classTokens = (tag: string) => attrOf(tag, 'class').split(/\s+/).filter(Boolean)
const buttonByText = (html: string, text: string) =>
  Array.from(html.matchAll(/<button\b[^>]*>(?:(?!<\/button>)[\s\S])*<\/button>/g))
    .map((m) => m[0])
    .find((b) => b.replace(/<[^>]*>/g, '').trim() === text)

const popupSeed = (slot: number, value: unknown) =>
  renderSeeded(slot, value, { idPrefix: 'dm', onDone: noop })

describe('the demo form wears the v2 field, ring and buttons (FL rows)', () => {
  const popup = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

  it('FL-01: every demo field wears the v2 field', () => {
    const fields = tagsOf(popup, 'input|select').filter((t) => /^dm-/.test(attrOf(t, 'id')) && !['dm-consent', 'dm-marketing'].includes(attrOf(t, 'id')))
    expect(fields.length).toBe(6)
    for (const tag of fields) {
      const d = declarations(attrOf(tag, 'style'))
      const id = attrOf(tag, 'id')
      expect(d.height, id).toBe('42px')
      expect(d.background, id).toBe('var(--card)')
      expect(d.border, id).toBe('1px solid var(--input)')
      expect(d['border-radius'], id).toBe('var(--radius)')
      expect(d.color, id).toBe('var(--ink)')
    }
  })

  it('FL-03: the focus ring is the v2 ring', () => {
    const rules = cssRules(DEMO_FORM_CSS)
    const focus = rules.find((r) => r.startsWith('.dm-input:focus, .dm-select:focus {'))
    expect(focus, 'the focus rule exists').toBeDefined()
    expect(focus).toContain('outline: 2px solid var(--ring);')
    expect(focus).toContain('outline-offset: 2px;')
    expect(focus).not.toContain('box-shadow')
    const err = rules.find((r) => r.startsWith('.dm-err {'))
    expect(err, 'the error rule exists').toBeDefined()
    expect(err).toContain('var(--destructive)')
    expect(err).not.toContain('--status-red-text')
  })

  it('FL-04a: every demo primary button is the DS md primary', async () => {
    const buttons = [
      buttonByText(popup, 'Book my demo →'),
      buttonByText(await popupSeed(3, 'success'), 'Done'),
      buttonByText(await popupSeed(3, 'error'), 'Try again'),
    ].filter((b): b is string => Boolean(b))
    expect(buttons.length, 'floor: three buttons found').toBe(3)
    for (const b of buttons) {
      const tokens = classTokens(b)
      for (const want of ['ds-btn', 'ds-btn--primary', 'ds-btn--md']) expect(tokens, b).toContain(want)
      expect(tokens.some((t) => t.startsWith('v2-btn')), b).toBe(false)
      // Layout comes from .ds-btn--md; an inline height or flex rule would override it.
      const inline = Object.keys(declarations(attrOf(b.match(/<button\b[^>]*>/)![0], 'style')))
      expect(inline, b).toContain('width')
      for (const k of ['height', 'justify-content', 'gap', 'cursor', 'background', 'color']) expect(inline, `${k} ${b}`).not.toContain(k)
    }
  })

  it('FL-05a: the demo spinner has no oklch and the busy button is disabled', async () => {
    const html = await popupSeed(3, 'submitting')
    expect(html, 'control: the seed took').toContain('Booking…')
    expect(html).not.toContain('oklch(')
    const spinner = tagsOf(html, 'span').find((t) => attrOf(t, 'style').includes('dmSpin'))
    expect(spinner, 'the spinner exists').toBeDefined()
    const d = declarations(attrOf(spinner!, 'style'))
    expect(d['border-radius']).toBe('var(--radius-pill)')
    expect(d.border).toContain('color-mix(in srgb, var(--primary-foreground) 40%, transparent)')
    const submit = tagsOf(html, 'button').find((t) => attrOf(t, 'type') === 'submit')!
    expect(submit).toContain('disabled=""')
    expect(classTokens(submit)).toContain('ds-btn')
  })

  it('FL-06: the success panel follows V605-611', async () => {
    const tiles = (html: string) => tagsOf(html, 'span').filter((t) => classTokens(t).includes('ds-icontile'))
    const success = await popupSeed(3, 'success')
    const good = tiles(success)
    expect(good.length).toBe(1)
    expect(classTokens(good[0])).toContain('ds-icontile--primary')
    const g = declarations(attrOf(good[0], 'style'))
    expect([g.width, g.height]).toEqual(['48px', '48px'])
    expect(success).not.toContain('border-radius:99')

    const error = await popupSeed(3, 'error')
    expect(error).not.toContain('border-radius:99')
    const tile = tagsOf(error, 'span').find((t) => attrOf(t, 'style').includes('var(--status-red-bg)'))
    expect(tile, 'the error tile exists').toBeDefined()
    const e = declarations(attrOf(tile!, 'style'))
    expect(e['border-radius']).toBe('var(--radius-md)')
    expect([e.width, e.height]).toEqual(['48px', '48px'])
    expect(e.color).toBe('var(--destructive)')
  })

  it('FL-07a: the demo field errors are the v2 destructive colour', async () => {
    const html = await renderSeeded(
      2,
      { name: 'E1', email: 'E2', company: 'E3', consent: 'E4' },
      { idPrefix: 'dm', onDone: noop },
    )
    const alerts = Array.from(html.matchAll(/<div\b[^>]*role="alert"[^>]*>[\s\S]*?<\/div>/g)).map((m) => m[0])
    const stars = Array.from(html.matchAll(/<span style="([^"]*)">\*<\/span>/g)).map((m) => m[1])
    expect(alerts.length).toBe(4)
    expect(stars.length).toBe(3)
    for (const a of alerts) {
      expect(declarations(attrOf(a.match(/<div\b[^>]*>/)![0], 'style')).color, a).toBe('var(--destructive)')
      expect(a, 'the glyph stays (D-30)').toContain('<svg')
    }
    for (const s of stars) expect(declarations(s).color).toBe('var(--destructive)')
    expect(html).not.toContain('--status-red-text')
  })
})

describe('the rest of the demo form surface follows § Design (FL-08, FL-09, FL-11, FL-12)', () => {
  const popup = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))
  const countOf = (html: string, needle: string) => html.split(needle).length - 1

  it('FL-08: column gap, optional marks, carets, consent row, submit and caption', () => {
    expect(countOf(popup, '<div style="display:flex;flex-direction:column;gap:14px">'), 'field column gap 14').toBe(1)
    expect(countOf(popup, '<span style="color:var(--text-copy)">(opt.)</span>'), 'three (opt.) marks').toBe(3)
    const carets = tagsOf(popup, 'span').filter((t) => attrOf(t, 'style').includes('pointer-events:none'))
    expect(carets.length, 'floor: three carets').toBe(3)
    for (const c of carets) expect(declarations(attrOf(c, 'style')).color).toBe('var(--muted-foreground)')

    const label = tagsOf(popup, 'label').find((t) => attrOf(t, 'for') === 'dm-consent')
    expect(label, 'the consent label exists').toBeDefined()
    const l = declarations(attrOf(label!, 'style'))
    expect([l.gap, l['font-size'], l['line-height'], l.color]).toEqual(['12px', '13px', '1.55', 'var(--foreground)'])
    const box = tagsOf(popup, 'input').find((t) => attrOf(t, 'id') === 'dm-consent')!
    const b = declarations(attrOf(box, 'style'))
    expect([b.width, b.height, b['accent-color']]).toEqual(['18px', '18px', 'var(--primary)'])

    const submit = tagsOf(popup, 'button').find((t) => attrOf(t, 'type') === 'submit')!
    const s = declarations(attrOf(submit, 'style'))
    expect([s.width, s['margin-top']]).toEqual(['100%', '24px'])
    const caption = tagsOf(popup, 'p').find((t) => classTokens(t).includes('t-caption'))
    expect(caption, 'the caption wears t-caption').toBeDefined()
    expect(attrOf(caption!, 'style')).toBe('text-align:center;margin:14px 0 0')
  })

  it('FL-09: success and error panels share the v2 heading, copy and button', async () => {
    const cases = [
      { step: 'success', copy: 'You&#x27;re booked', button: 'dm-success-done', label: 'Done' },
      { step: 'error', copy: 'Something went wrong', button: 'dm-error-retry', label: 'Try again' },
    ] as const
    for (const c of cases) {
      const html = await popupSeed(3, c.step)
      const h3 = tagsOf(html, 'h3')
      expect(h3.length, `${c.step}: one heading`).toBe(1)
      expect(html, c.step).toContain(`${c.copy}</h3>`)
      expect(declarations(attrOf(h3[0], 'style')), c.step).toEqual({
        'font-size': '24px',
        'font-weight': '700',
        'letter-spacing': 'var(--tracking-h3)',
        margin: '4px 0 0',
        color: 'var(--ink)',
      })
      const p = tagsOf(html, 'p')
      expect(p.length, `${c.step}: one paragraph`).toBe(1)
      expect(classTokens(p[0]), c.step).toContain('t-body-sm')
      expect(declarations(attrOf(p[0], 'style')), c.step).toEqual({ 'line-height': '1.6', margin: '0 auto 8px', 'max-width': '360px' })
      const button = tagsOf(html, 'button')
      expect(button.length, `${c.step}: one button`).toBe(1)
      expect(attrOf(button[0], 'id'), c.step).toBe(c.button)
      expect(declarations(attrOf(button[0], 'style')), c.step).toEqual({ width: '100%' })
      expect(buttonByText(html, c.label), c.step).toBeDefined()
      expect(html, `${c.step}: 24px glyph in the tile`).toMatch(/<svg width="24" height="24"/)
    }
  })

  it('FL-11: the heading slot follows V559-560, and no v1 colour or eyebrow class survives in any step', async () => {
    const start = popup.indexOf('<form')
    const slot = popup.slice(start, popup.indexOf('id="dm-name"'))
    expect(slot.length, 'control: the slot was sliced').toBeGreaterThan(100)
    expect(slot).toContain('<div style="margin-bottom:14px"><span class="t-eyebrow">BOOK A DEMO</span></div>')
    expect(slot).not.toContain('class="eyebrow"')
    expect(tagsOf(slot, 'h3').length).toBe(1)
    expect(declarations(attrOf(tagsOf(slot, 'h3')[0], 'style'))).toEqual({
      'font-size': '28px',
      'line-height': '1.2',
      'letter-spacing': 'var(--tracking-h3)',
      'font-weight': '700',
      margin: '0 0 8px',
      color: 'var(--ink)',
    })
    const p = tagsOf(slot, 'p')
    expect(p.length).toBe(1)
    expect(classTokens(p[0])).toContain('t-body-sm')
    expect(declarations(attrOf(p[0], 'style'))).toEqual({ margin: '0 0 20px', 'line-height': '1.6' })

    const steps = [
      popup,
      await popupSeed(2, { name: 'E1', email: 'E2', company: 'E3', consent: 'E4' }),
      await popupSeed(3, 'submitting'),
      await popupSeed(3, 'success'),
      await popupSeed(3, 'error'),
    ]
    expect(steps.length).toBe(5)
    for (const html of steps) {
      expect(html.length).toBeGreaterThan(500)
      expect(html).toContain('var(--ink)')
      expect(html).not.toMatch(/var\(--(bg-\d|line-\d|fg-\d|action|radius-input|status-red-text|text-on-dark)/)
    }
  })

  it('FL-12: an invalid field carries dm-err beside dm-input; a valid one does not', async () => {
    const html = await popupSeed(2, { name: 'E1', email: 'E2', company: 'E3' })
    const cls = (id: string) => classTokens(tagsOf(html, 'input').find((t) => attrOf(t, 'id') === id)!)
    for (const id of ['dm-name', 'dm-email', 'dm-company']) {
      expect(cls(id), id).toEqual(['dm-input', 'dm-err'])
    }
    expect(classTokens(tagsOf(popup, 'input').find((t) => attrOf(t, 'id') === 'dm-name')!)).toEqual(['dm-input'])
  })
})
