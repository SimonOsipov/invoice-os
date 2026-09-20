// Mode A (RED) — transcribes Test Specs S6-S10, S16, S17 for BUG-19-01. Each describe
// names its bucket: NEW-BEHAVIOUR rows guard a dynamic import so a missing module fails
// on an assertion, never a collection error; S10b is a CHARACTERIZATION row — it already
// passes against today's DemoModal and must keep passing after the extraction.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { TAXPAYER_SIZE_OPTIONS, CONSENT_TEXT } from './demoForm'
import { DemoModal } from './DemoModal'

const HERE = dirname(fileURLToPath(import.meta.url))
const DEMO_LEAD_FORM_PATH = join(HERE, 'DemoLeadForm.tsx')

function noop() {}

function hasAttr(tag: string, name: string): boolean {
  return new RegExp(`(^|[\\s<])${name}(=|[\\s/>])`).test(tag)
}

// Source-scan rows must not match a `//`/`/* */` comment — strip both before matching.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
}

// Cached across rows: once ./DemoLeadForm resolves, later rows reuse the module
// instead of re-attempting a failed dynamic import each time.
let cachedDemoLeadForm: typeof import('./DemoLeadForm') | null | undefined
async function demoLeadFormModule() {
  if (cachedDemoLeadForm === undefined) {
    cachedDemoLeadForm = await import('./DemoLeadForm').catch(() => null)
  }
  return cachedDemoLeadForm
}

let cachedDemoForm: typeof import('./demoForm') | undefined
async function demoFormModule() {
  if (!cachedDemoForm) cachedDemoForm = await import('./demoForm')
  return cachedDemoForm
}

describe('DemoLeadForm renders every control under any idPrefix (S6, NEW-BEHAVIOUR)', () => {
  it('S6: labelled controls, both option lists and the consent text render under any idPrefix', async () => {
    const mod = await demoLeadFormModule()
    expect(mod, 'expected ./DemoLeadForm.tsx to exist and export DemoLeadForm').not.toBeNull()
    if (!mod) return

    const demoForm = await demoFormModule()
    const html = renderToStaticMarkup(createElement(mod.DemoLeadForm, { idPrefix: 'zz', variant: 'card' }))

    const labelTargets = Array.from(html.matchAll(/<label[^>]*\sfor="([^"]+)"/g)).map((m) => m[1])
    expect(labelTargets.length).toBeGreaterThan(0)
    for (const field of ['name', 'email', 'company']) {
      expect(labelTargets).toContain(`zz-${field}`)
    }

    const optionSets: Array<[string, readonly string[]]> = [
      ['zz-role', demoForm.ROLE_OPTIONS],
      ['zz-size', TAXPAYER_SIZE_OPTIONS],
      ['zz-volume', demoForm.VOLUME_OPTIONS],
    ]
    for (const [id, options] of optionSets) {
      expect(options.length).toBeGreaterThan(0)
      const select = html.match(new RegExp(`<select[^>]*id="${id}"[^]*?</select>`))
      expect(select, `expected #${id}`).not.toBeNull()
      if (!select) continue
      const optionTags = select[0].match(/<option[^>]*>/g) ?? []
      expect(optionTags.length).toBe(options.length + 1)
    }

    const consent = html.match(/<input[^>]*id="zz-consent"[^>]*>/)
    expect(consent, 'expected #zz-consent').not.toBeNull()
    if (consent) {
      expect(consent[0]).toContain('type="checkbox"')
      expect(consent[0]).toContain('aria-required="true"')
    }

    expect(html).toContain(CONSENT_TEXT)
  })
})

describe('DemoLeadForm honeypot hardening under any idPrefix (S7, NEW-BEHAVIOUR)', () => {
  it('S7: the honeypot keeps every hardening attribute and carries no id', async () => {
    const mod = await demoLeadFormModule()
    expect(mod, 'expected ./DemoLeadForm.tsx to exist').not.toBeNull()
    if (!mod) return

    const html = renderToStaticMarkup(createElement(mod.DemoLeadForm, { idPrefix: 'zz', variant: 'card' }))
    const honeypotMatch = html.match(/<input[^>]*name="website"[^>]*>/)
    expect(honeypotMatch, 'expected the honeypot input').not.toBeNull()
    if (!honeypotMatch) return
    const tag = honeypotMatch[0]

    expect(tag).toContain('tabindex="-1"')
    expect(tag).toContain('aria-hidden="true"')
    expect(tag).toContain('autoComplete="new-password"')
    expect(hasAttr(tag, 'required')).toBe(false)
    expect(tag).toContain('data-lpignore="true"')
    expect(tag).toContain('data-1p-ignore="true"')
    expect(tag).toContain('data-bwignore="true"')
    expect(tag).toContain('data-form-type="other"')

    const idMatch = tag.match(/\sid="([^"]+)"/)
    const labelTargets = Array.from(html.matchAll(/<label[^>]*\sfor="([^"]+)"/g)).map((m) => m[1])
    if (idMatch) expect(labelTargets).not.toContain(idMatch[1])
    expect(labelTargets).not.toContain('website')

    const index = html.indexOf(tag)
    const surrounding = html.slice(Math.max(0, index - 400), index)
    expect(surrounding).not.toMatch(/display\s*:\s*none/)
  })
})

describe('two mounted instances never collide (S8, NEW-BEHAVIOUR)', () => {
  it('S8: idPrefix="dm" and idPrefix="dc" rendered together produce 14 unique ids', async () => {
    const mod = await demoLeadFormModule()
    expect(mod, 'expected ./DemoLeadForm.tsx to exist').not.toBeNull()
    if (!mod) return

    const dm = renderToStaticMarkup(createElement(mod.DemoLeadForm, { idPrefix: 'dm', variant: 'modal' }))
    const dc = renderToStaticMarkup(createElement(mod.DemoLeadForm, { idPrefix: 'dc', variant: 'card' }))
    const ids = Array.from((dm + dc).matchAll(/\sid="([^"]+)"/g)).map((m) => m[1])
    expect(ids.length).toBe(14)
    expect(new Set(ids).size).toBe(ids.length)
  })
})

describe('no identifier-shaped dm- literal survives in DemoLeadForm.tsx (S9, NEW-BEHAVIOUR)', () => {
  it('S9: only the four exempt class names keep a dm- literal', () => {
    const exists = existsSync(DEMO_LEAD_FORM_PATH)
    expect(exists, 'expected DemoLeadForm.tsx to exist').toBe(true)
    if (!exists) return

    const src = stripComments(readFileSync(DEMO_LEAD_FORM_PATH, 'utf8'))
    expect(src.length).toBeGreaterThan(0)
    expect(src).toMatch(/dm-input/)
    expect(src).toMatch(/dm-err\b/)
    expect(src).toMatch(/dm-select/)
    expect(src).toMatch(/dm-row/)
    expect(/\bdm-(name|email|company|role|size|volume|consent|success|error)/.test(src)).toBe(false)
  })
})

describe('an absent heading emits nothing; the popup heading sits above the first field (S10, mixed)', () => {
  it('S10a (NEW-BEHAVIOUR): DemoLeadForm with no heading renders nothing between <form> and the first field', async () => {
    const mod = await demoLeadFormModule()
    expect(mod, 'expected ./DemoLeadForm.tsx to exist').not.toBeNull()
    if (!mod) return

    const html = renderToStaticMarkup(createElement(mod.DemoLeadForm, { idPrefix: 'zz', variant: 'card' }))
    const formStart = html.indexOf('<form')
    const fieldStart = html.indexOf('id="zz-name"')
    expect(formStart).toBeGreaterThanOrEqual(0)
    expect(fieldStart).toBeGreaterThan(formStart)
    const between = html.slice(formStart, fieldStart)
    expect(between).not.toContain('<h3')
    expect(between).not.toContain('eyebrow')
    expect(between).not.toMatch(/<div><\/div>/)
  })

  it('S10b (CHARACTERIZATION, already green): the popup heading sits between <form> and #dm-name', () => {
    const html = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))
    const formStart = html.indexOf('<form')
    const fieldStart = html.indexOf('id="dm-name"')
    expect(formStart).toBeGreaterThanOrEqual(0)
    expect(fieldStart).toBeGreaterThan(formStart)
    const between = html.slice(formStart, fieldStart)
    expect(between).toContain('BOOK A DEMO')
    expect(between).toContain('<h3')
    expect(between).toContain('See your invoices pass compliance in real time.')
    expect(between).toContain('A 20-minute walkthrough')
  })
})

describe('exactly one style element carries both halves (S16, NEW-BEHAVIOUR)', () => {
  it('S16: DemoModal renders one <style>, and the imported DEMO_FORM_CSS is a substring of it', async () => {
    const mod = await demoLeadFormModule()
    expect(mod, 'expected ./DemoLeadForm.tsx to export DEMO_FORM_CSS').not.toBeNull()
    if (!mod) return
    expect(mod).toHaveProperty('DEMO_FORM_CSS')

    const html = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))
    const styleTags = html.match(/<style/g) ?? []
    expect(styleTags.length).toBe(1)
    for (const needle of ['dmOvIn', 'dmCardIn', '.si-close', '.dm-overlay', 'dmSpin', '.dm-input:focus', '.dm-err', '.dm-row']) {
      expect(html).toContain(needle)
    }
    expect(html).toContain(mod.DEMO_FORM_CSS)
  })
})

describe('the six pure names live only in demoForm.ts (S17, NEW-BEHAVIOUR)', () => {
  it('S17: ROLE_OPTIONS, VOLUME_OPTIONS, DEFAULT_FORM come from demoForm.ts; DemoLeadForm.tsx declares none of the six', async () => {
    const demoForm = await demoFormModule()
    expect(demoForm).toHaveProperty('ROLE_OPTIONS')
    expect(demoForm).toHaveProperty('VOLUME_OPTIONS')
    expect(demoForm).toHaveProperty('DEFAULT_FORM')
    expect(demoForm.ROLE_OPTIONS).toEqual([
      'Owner / Partner',
      'Finance or Accounting lead',
      'Tax / Compliance',
      'Developer / IT',
      'Other',
    ])
    expect(demoForm.VOLUME_OPTIONS).toEqual(['under 1k', '1k–10k', '10k–100k', '100k+'])
    expect(demoForm.DEFAULT_FORM).toMatchObject({ name: '', email: '', company: '' })

    const exists = existsSync(DEMO_LEAD_FORM_PATH)
    expect(exists, 'expected DemoLeadForm.tsx to exist').toBe(true)
    if (!exists) return
    const src = stripComments(readFileSync(DEMO_LEAD_FORM_PATH, 'utf8'))
    expect(/^const (ROLE_OPTIONS|VOLUME_OPTIONS|DEFAULT_FORM)\b/m.test(src)).toBe(false)
    expect(/^type (DemoFormState|DemoFieldKey|DemoStep)\b/m.test(src)).toBe(false)
  })
})
