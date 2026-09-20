// Mode A (RED) — transcribes Test Specs R1-R8. Node/SSR string scans over
// renderToStaticMarkup output; this package has no DOM parser under `node`.
// R1-R5 and R8's markup half are NEW-BEHAVIOUR (DemoCta still renders the fake
// facade); R6/R7's DemoModal half and R8's source-absence half pin behaviour
// that must survive untouched.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { CONSENT_TEXT } from './demoForm'
import { DemoCta } from './DemoCta'
import { DemoModal } from './DemoModal'

const HERE = dirname(fileURLToPath(import.meta.url))
const DEMO_CTA_PATH = join(HERE, 'DemoCta.tsx')
const DEMO_LEAD_FORM_PATH = join(HERE, 'DemoLeadForm.tsx')

function noop() {}

// DemoCta's prop shape is mid-migration (loses onBookDemo); erase it so this
// file typechecks on both sides of that edit.
const Cta = DemoCta as unknown as () => ReactElement

// Source-scan rows must not match a `//`/`/* */` comment — strip both before matching.
// Duplicated from DemoLeadForm.render.test.tsx's local copy per this package's own
// precedent (no shared test-util module exists here).
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
}

describe('R1 (AC-1.1, NEW-BEHAVIOUR): six real controls, each with a real label', () => {
  it('dc-name/email/company are inputs, dc-role/size/volume are selects, each labelled', () => {
    const html = renderToStaticMarkup(createElement(Cta))
    const labelTargets = Array.from(html.matchAll(/<label[^>]*\sfor="([^"]+)"/g)).map((m) => m[1])
    expect(labelTargets.length).toBeGreaterThan(0)

    for (const field of ['name', 'email', 'company']) {
      expect(labelTargets, `expected a label for dc-${field}`).toContain(`dc-${field}`)
      expect(html, `expected an <input id="dc-${field}">`).toMatch(new RegExp(`<input[^>]*\\sid="dc-${field}"`))
    }
    for (const field of ['role', 'size', 'volume']) {
      expect(labelTargets, `expected a label for dc-${field}`).toContain(`dc-${field}`)
      expect(html, `expected a <select id="dc-${field}">`).toMatch(new RegExp(`<select[^>]*\\sid="dc-${field}"`))
    }
  })
})

describe('R2 (AC-1.1, NEW-BEHAVIOUR): the selects offer exactly the popup\'s option lists', () => {
  it('dc-role/size/volume option markup matches dm-role/size/volume, id prefix aside', () => {
    const dcHtml = renderToStaticMarkup(createElement(Cta))
    const dmHtml = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

    for (const field of ['role', 'size', 'volume']) {
      const dmSlice = dmHtml.match(new RegExp(`<select[^>]*id="dm-${field}"[\\s\\S]*?</select>`))
      expect(dmSlice, `expected the popup's dm-${field} select`).not.toBeNull()
      const dcSlice = dcHtml.match(new RegExp(`<select[^>]*id="dc-${field}"[\\s\\S]*?</select>`))
      expect(dcSlice, `expected the card's dc-${field} select`).not.toBeNull()
      if (!dcSlice || !dmSlice) continue

      expect(dcSlice[0].length).toBeGreaterThan(0)
      expect(dmSlice[0]).toContain('Select…')
      expect(dcSlice[0].replaceAll('dc-', 'dm-')).toEqual(dmSlice[0])
    }
  })
})

describe('R3 (AC-1.2, NEW-BEHAVIOUR): consent checkbox and honeypot', () => {
  it('renders a required checkbox carrying CONSENT_TEXT, and a hardened honeypot', () => {
    const html = renderToStaticMarkup(createElement(Cta))

    const consent = html.match(/<input[^>]*\sid="dc-consent"[^>]*>/)
    expect(consent, 'expected #dc-consent').not.toBeNull()
    if (consent) {
      expect(consent[0]).toContain('type="checkbox"')
      expect(consent[0]).toContain('aria-required="true"')
    }
    expect(html).toContain(CONSENT_TEXT)

    const honeypot = html.match(/<input[^>]*\sname="website"[^>]*>/)
    expect(honeypot, 'expected the honeypot input').not.toBeNull()
    if (honeypot) {
      const tag = honeypot[0]
      expect(tag).toContain('tabindex="-1"')
      expect(tag).toContain('aria-hidden="true"')
      expect(tag).toContain('autoComplete="new-password"')
      expect(tag).toContain('data-lpignore="true"')
      expect(tag).toContain('data-1p-ignore="true"')
      expect(tag).toContain('data-bwignore="true"')
      expect(tag).toContain('data-form-type="other"')
    }
  })
})

describe('R4 (AC-1.3, NEW-BEHAVIOUR): no CTA prop, one submit button', () => {
  it('R4a: DemoCta.tsx source carries no onBookDemo/onClick=, and does render DemoLeadForm', () => {
    const exists = existsSync(DEMO_CTA_PATH)
    expect(exists, 'expected DemoCta.tsx to exist').toBe(true)
    if (!exists) return

    const stripped = stripComments(readFileSync(DEMO_CTA_PATH, 'utf8'))
    expect(stripped.length).toBeGreaterThan(0)
    // Matches the ELEMENT, not the bare name: `import … from './DemoLeadForm'` alone
    // satisfies a `toContain('DemoLeadForm')` even when nothing is rendered.
    expect(stripped, 'control needle: expected DemoCta.tsx to render <DemoLeadForm').toMatch(/<DemoLeadForm\b/)
    expect(stripped).not.toMatch(/onBookDemo/)
    expect(stripped).not.toMatch(/onClick=/)
  })

  it('R4b: SSR renders exactly one button, type="submit", labelled "Book my demo →"', () => {
    const html = renderToStaticMarkup(createElement(Cta))
    const buttons = html.match(/<button[^>]*>/g) ?? []
    expect(buttons.length, 'expected exactly one <button>').toBe(1)

    const submit = html.match(/<button[^>]*>[^<]*Book my demo →[^<]*<\/button>/)
    expect(submit, 'expected a "Book my demo →" button').not.toBeNull()
    if (submit) expect(submit[0]).toContain('type="submit"')
  })
})

describe('R5 (AC-1.3, NEW-BEHAVIOUR): the facade is gone, not supplemented', () => {
  it('no caret glyph, no div-as-label; a real <label> with the field copy survives', () => {
    const html = renderToStaticMarkup(createElement(Cta))
    expect(html).not.toContain('▾')
    expect(html).not.toMatch(/<div[^>]*class="label"/)
    expect(html).toMatch(/<label[^>]*class="label"/)
    expect(html).toContain('Book my demo')
  })
})

describe('R6 ([panel-padding-by-variant], NEW-BEHAVIOUR): card form renders no padding, popup\'s does', () => {
  it("DemoCta's <form> is padding:0; DemoModal's stays padding:20px", () => {
    const dcHtml = renderToStaticMarkup(createElement(Cta))
    const dcForm = dcHtml.match(/<form[^>]*style="[^"]*"/)
    expect(dcForm, 'expected DemoCta to render a <form>').not.toBeNull()
    if (dcForm) expect(dcForm[0]).toContain('padding:0')

    const dmHtml = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))
    const dmForm = dmHtml.match(/<form[^>]*style="[^"]*"/)
    expect(dmForm, 'expected DemoModal to render a <form>').not.toBeNull()
    if (dmForm) expect(dmForm[0]).toContain('padding:20px')
  })
})

describe("R7 ([card-takes-popup-spacing], NEW-BEHAVIOUR): the card's submit takes the popup's 18px", () => {
  it('the submit button carries margin-top:18px', () => {
    const html = renderToStaticMarkup(createElement(Cta))
    const submit = html.match(/<button[^>]*type="submit"[^>]*>/)
    expect(submit, 'expected a type="submit" button').not.toBeNull()
    if (submit) expect(submit[0]).toContain('margin-top:18px')
  })
})

describe('R8 (AC #6 corrected, mixed): one reassurance line, from the shared form', () => {
  it('SSR carries exactly one "No card required"; DemoCta.tsx no longer duplicates it', () => {
    const html = renderToStaticMarkup(createElement(Cta))
    const occurrences = html.match(/No card required/g) ?? []
    expect(occurrences.length).toBe(1)

    const exists = existsSync(DEMO_CTA_PATH)
    expect(exists, 'expected DemoCta.tsx to exist').toBe(true)
    if (!exists) return
    const stripped = stripComments(readFileSync(DEMO_CTA_PATH, 'utf8'))
    expect(stripped).not.toContain('No card required')
  })

  it('the reassurance line lives only in DemoLeadForm.tsx (breaklist TOTAL 1, down from 2)', () => {
    const demoCtaExists = existsSync(DEMO_CTA_PATH)
    const demoLeadFormExists = existsSync(DEMO_LEAD_FORM_PATH)
    expect(demoCtaExists, 'expected DemoCta.tsx to exist').toBe(true)
    expect(demoLeadFormExists, 'expected DemoLeadForm.tsx to exist').toBe(true)
    if (!demoCtaExists || !demoLeadFormExists) return

    const NEEDLE = /Data resident in-region/g
    const inLeadForm = (readFileSync(DEMO_LEAD_FORM_PATH, 'utf8').match(NEEDLE) ?? []).length
    const inCta = (readFileSync(DEMO_CTA_PATH, 'utf8').match(NEEDLE) ?? []).length

    // Control needle: DemoLeadForm.tsx must keep carrying it regardless of DemoCta.tsx's state.
    expect(inLeadForm, 'control needle: DemoLeadForm.tsx must carry the reassurance line').toBe(1)
    expect(inCta, 'DemoCta.tsx must not duplicate the reassurance line').toBe(0)
  })
})
