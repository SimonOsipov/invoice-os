// QA Mode B gap-fill for the DemoLeadForm extraction. The Stage 2.5 specs (S1-S17)
// transcribe the plan; these rows cover what the plan's table did not: the popup's
// DOM golden, the per-variant panel padding, the CSS split, the hook-order contract
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
  props: { idPrefix: string; variant: 'modal' | 'card'; onDone?: () => void },
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

// Captured from the popup as it rendered at 75f48da4, before the extraction. The rule
// SET is the contract; the order inside the single <style> is the one named exception.
const POPUP_CSS_RULES_BEFORE_EXTRACTION = [
  'from { opacity: 0; }',
  'to { opacity: 1; }',
  'from { opacity: 0; transform: translateY(10px) scale(0.985); }',
  'to { opacity: 1; transform: none; }',
  'to { transform: rotate(360deg); }',
  '.dm-input, .dm-select { transition: border-color 120ms, box-shadow 120ms; }',
  '.dm-input:focus, .dm-select:focus { border-color: var(--action) !important; box-shadow: 0 0 0 3px var(--action-glow); outline: none; }',
  '.dm-err { border-color: var(--status-red-text) !important; }',
  '.dm-select { appearance: none; -webkit-appearance: none; }',
  '.si-close { transition: background 120ms ease-out, color 120ms ease-out; }',
  '.si-close:hover { background: var(--bg-3); color: var(--fg-1); }',
  '.dm-row { flex-direction: column !important; align-items: stretch !important; }',
  '.dm-overlay { padding: 14px !important; }',
]

describe('the popup DOM survives the extraction (A1-A3)', () => {
  const popup = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

  it('A1: the popup still carries every id it shipped with, and no new one', () => {
    const ids = Array.from(popup.matchAll(/\sid="([^"]+)"/g)).map((m) => m[1])
    expect(ids).toEqual(['dm-name', 'dm-email', 'dm-company', 'dm-role', 'dm-size', 'dm-volume', 'dm-consent'])
  })

  it('A2: the single <style> carries exactly the pre-extraction rule set, order aside', () => {
    const styles = Array.from(popup.matchAll(/<style>([\s\S]*?)<\/style>/g))
    expect(styles.length).toBe(1)
    const rules = cssRules(styles[0][1])
    expect(rules.length).toBe(POPUP_CSS_RULES_BEFORE_EXTRACTION.length)
    expect([...rules].sort()).toEqual([...POPUP_CSS_RULES_BEFORE_EXTRACTION].sort())
  })

  it('A3: the success panel is the one node that gained markup — id + tabindex, nothing else', async () => {
    const html = await renderSeeded(3, 'success', { idPrefix: 'dm', variant: 'modal', onDone: noop })
    expect(html).toContain('<div id="dm-success" tabindex="-1" style="padding:32px 22px 24px;text-align:center">')
    // Pre-extraction the error panel's wrapper carried neither; it must still carry neither.
    const error = await renderSeeded(3, 'error', { idPrefix: 'dm', variant: 'modal', onDone: noop })
    expect(error).toContain('<div style="padding:32px 22px 24px;text-align:center">')
  })
})

describe('panel padding follows the variant (A4)', () => {
  it('A4: modal pads the form and both panels; card pads neither', async () => {
    const modalForm = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'dm', variant: 'modal' }))
    const cardForm = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'dc', variant: 'card' }))
    expect(modalForm.startsWith('<form noValidate="" style="padding:20px">')).toBe(true)
    expect(cardForm.startsWith('<form noValidate="" style="padding:0">')).toBe(true)
    // Through DemoModal too: rendering the form alone cannot see which variant the
    // popup asks for.
    expect(renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))).toContain(
      '<form noValidate="" style="padding:20px">',
    )

    for (const step of ['success', 'error'] as const) {
      const modalPanel = await renderSeeded(3, step, { idPrefix: 'dm', variant: 'modal', onDone: noop })
      const cardPanel = await renderSeeded(3, step, { idPrefix: 'dc', variant: 'card' })
      expect(modalPanel).toContain('padding:32px 22px 24px;text-align:center')
      expect(cardPanel).toContain('padding:0;text-align:center')
      expect(cardPanel).not.toContain('32px 22px 24px')
    }
  })
})

describe('DEMO_FORM_CSS is the form half only (A5)', () => {
  it('A5: it carries the six form rules and none of the shell rules', () => {
    for (const needle of ['dmSpin', '.dm-input', '.dm-input:focus', '.dm-err', '.dm-select', '.dm-row']) {
      expect(DEMO_FORM_CSS).toContain(needle)
    }
    // The card (BUG-19-02) renders this string on its own — a leaked overlay or
    // close-button rule would restyle a surface that has neither.
    for (const shellOnly of ['dmOvIn', 'dmCardIn', '.si-close', '.dm-overlay']) {
      expect(DEMO_FORM_CSS).not.toContain(shellOnly)
    }
  })
})

describe('the heading is a slot with no wrapper (A6)', () => {
  it('A6: a given heading is the form’s first child; an absent one emits nothing at all', () => {
    const withHeading = renderToStaticMarkup(
      createElement(DemoLeadForm, { idPrefix: 'zz', variant: 'card', heading: createElement('h9' as 'h1', null, 'SLOT') }),
    )
    expect(withHeading.startsWith('<form noValidate="" style="padding:0"><h9>SLOT</h9><div style="display:flex')).toBe(true)

    const without = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'zz', variant: 'card' }))
    expect(without.startsWith('<form noValidate="" style="padding:0"><div style="display:flex')).toBe(true)
  })

  it('A10: the form still closes on the submit button and the reassurance line', () => {
    const html = renderToStaticMarkup(createElement(DemoLeadForm, { idPrefix: 'zz', variant: 'card' }))
    expect(html).toMatch(/<button type="submit"[^>]*>Book my demo →<\/button>/)
    expect(html.endsWith('No card required</p></form>')).toBe(true)
  })
})

describe('the three useState slots are the ones the adversarial mock assumes (A7)', () => {
  it('A7: slot 1 is form, slot 2 is errors, slot 3 is demoStep', async () => {
    const slot1 = await renderSeeded(
      1,
      { name: 'Seeded Name', email: 'a@b.co', company: 'C', role: 'Other', size: 'Below ₦50m', volume: '100k+', consent: true },
      { idPrefix: 'zz', variant: 'card' },
    )
    expect(slot1).toMatch(/<input id="zz-name"[^>]*value="Seeded Name"/)

    const slot2 = await renderSeeded(2, { consent: 'SEEDED CONSENT ERROR' }, { idPrefix: 'zz', variant: 'card' })
    expect(slot2).toContain('SEEDED CONSENT ERROR')
    expect(slot2).toMatch(/<input[^>]*id="zz-consent"[^>]*aria-describedby="zz-consent-error"/)

    const slot3 = await renderSeeded(3, 'error', { idPrefix: 'zz', variant: 'card' })
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
