// RED-then-GREEN spec for LAND-02-02 (task-314). Pins DemoModal's five wiring
// changes — taxpayer-size options, consent checkbox, honeypot, isFocusable's
// tabIndex clause, and the gate-aware submit seam's markup surface — via SSR.
//
// No jsdom, no @testing-library in this package (vitest.config.ts: environment
// 'node'). Everything here is asserted against the renderToStaticMarkup string,
// following the Nav.aria-current.test.tsx idiom. Nothing interactive (Tab order,
// focus movement, submit flow) can be observed this way — that is LAND-02-05's
// Playwright spec, not this file.
//
// Commit order (see task-314 Implementation Plan): this file is the RED commit,
// preceded by a micro-commit that adds `export` to isFocusable. Without that
// preceding export, `import { isFocusable } from './DemoModal'` below would fail
// to resolve and the whole file would die at module-collection time — a
// collection error, not a failing assertion, which would also mask R1–R6 since
// the file would never execute at all.
import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DemoModal, isFocusable } from './DemoModal'
import { CONSENT_TEXT, TAXPAYER_SIZE_OPTIONS } from './demoForm'

function noop() {}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// Extracts the first tag matching `tagOpenRegex` from `html`. Uses vitest's own
// `expect` (not a thrown plain Error) so a miss surfaces as a normal failing
// assertion, not a collection error — this is what makes R1/R4/R5's "the field
// doesn't exist yet" state a valid RED rather than a crash.
function extractTag(html: string, tagOpenRegex: RegExp): string {
  const match = html.match(tagOpenRegex)
  expect(match, `expected to find a tag matching ${tagOpenRegex}`).not.toBeNull()
  return match![0]
}

// True if `tag` carries a standalone HTML attribute named `name` — bounded by
// whitespace/tag-start on the left, so "required" doesn't false-match inside
// "aria-required".
function hasAttr(tag: string, name: string): boolean {
  return new RegExp(`(^|[\\s<])${name}(=|[\\s/>])`).test(tag)
}

describe('DemoModal SSR render (LAND-02-02)', () => {
  const html = renderToStaticMarkup(createElement(DemoModal, { onClose: noop }))

  it('R1: a required consent checkbox #dm-consent renders with aria-required="true"', () => {
    const consent = extractTag(html, /<input[^>]*id="dm-consent"[^>]*>/)
    expect(consent).toContain('type="checkbox"')
    expect(consent).toContain('aria-required="true"')
  })

  it('R2: the consent label text is the imported CONSENT_TEXT constant verbatim, never retyped', () => {
    // Asserting against the imported constant (not a retyped literal) is the
    // mechanism that makes "the exact wording the visitor was shown" mechanically
    // true rather than a promise — if DemoModal.tsx ever retypes the sentence
    // instead of importing it, CONSENT_TEXT and the markup can diverge silently
    // while this assertion still passes on a stale copy-paste; the import is what
    // ties them together.
    expect(html).toContain(CONSENT_TEXT)
  })

  it('R3: the taxpayer-size select maps TAXPAYER_SIZE_OPTIONS exactly, and never renders Micro', () => {
    const sizeSelect = extractTag(html, /<select[^>]*id="dm-size"[^]*?<\/select>/)
    for (const opt of TAXPAYER_SIZE_OPTIONS) {
      const escaped = escapeRegExp(opt)
      const occurrences = sizeSelect.match(new RegExp(`<option[^>]*value="${escaped}"[^>]*>${escaped}</option>`, 'g')) ?? []
      expect(occurrences.length).toBe(1)
    }
    // Exactly one <option> per TAXPAYER_SIZE_OPTIONS entry, plus the disabled
    // "Select…" placeholder every other select in this form already carries — no
    // stray/duplicate entries.
    const allOptions = sizeSelect.match(/<option[^>]*>/g) ?? []
    expect(allOptions.length).toBe(TAXPAYER_SIZE_OPTIONS.length + 1)
    expect(html).not.toContain('>Micro<')
  })

  it('R4: a Tab-unreachable, screen-reader-hidden honeypot named "website" renders inside the form', () => {
    const honeypot = extractTag(html, /<input[^>]*name="website"[^>]*>/)
    expect(honeypot).toContain('tabindex="-1"')
    expect(honeypot).toContain('aria-hidden="true"')
    // Verified against the actual installed react-dom@19.2.7 SSR output (not
    // assumed): unlike tabIndex (-> lowercase "tabindex"), autoComplete is NOT
    // case-normalized by React and serializes camelCase, same as the existing
    // autoComplete="name"/"email"/"organization" inputs already in this file.
    expect(honeypot).toContain('autoComplete="new-password"')
    expect(hasAttr(honeypot, 'required')).toBe(false)
    // Vendor password-manager ignore attributes: autocomplete alone is not honored
    // by password managers (they ignore autocomplete semantics entirely), so these
    // are the only surgical defence against LastPass/1Password/Bitwarden/Dashlane.
    expect(honeypot).toContain('data-lpignore="true"')
    expect(honeypot).toContain('data-1p-ignore="true"')
    expect(honeypot).toContain('data-bwignore="true"')
    expect(honeypot).toContain('data-form-type="other"')
  })

  it('R5: the honeypot exists (guard), then — given that — no label targets it and its wrapper is not display:none', () => {
    // Mandatory existence guard, asserted first: on today's source there is no
    // honeypot at all, so without this the two checks below would pass vacuously
    // (no honeypot -> trivially "no label targets it" and trivially "no
    // display:none found near it") and this row could never go legitimately RED
    // the way R1-R4 do.
    const honeypot = extractTag(html, /<input[^>]*name="website"[^>]*>/)

    const idMatch = honeypot.match(/\sid="([^"]+)"/)
    const labelTargets = Array.from(html.matchAll(/<label[^>]*\sfor="([^"]+)"/g)).map((m) => m[1])
    if (idMatch) expect(labelTargets).not.toContain(idMatch[1])
    expect(labelTargets).not.toContain('website')

    // The honeypot must be visually off-screen, not display:none (bots skip
    // display:none and are not fooled into filling it in). Scoped to the markup
    // immediately preceding the honeypot tag — its wrapper — rather than the
    // whole document, so this doesn't get confused by unrelated markup.
    const index = html.indexOf(honeypot)
    const surrounding = html.slice(Math.max(0, index - 400), index)
    expect(surrounding).not.toMatch(/display\s*:\s*none/)
  })

  it('R6: the submit button and the three required text inputs are unchanged', () => {
    expect(html).toContain('Book my demo')
    expect(html).toMatch(/<input[^>]*id="dm-name"[^>]*>/)
    expect(html).toMatch(/<input[^>]*id="dm-email"[^>]*>/)
    expect(html).toMatch(/<input[^>]*id="dm-company"[^>]*>/)
  })
})

describe('isFocusable (LAND-02-02) — keeps the honeypot out of the Tab-trap', () => {
  it('R7: tabIndex < 0 is never focusable, even when enabled and rendered', () => {
    const el = { disabled: false, offsetParent: {}, tabIndex: -1 } as unknown as HTMLElement
    expect(isFocusable(el)).toBe(false)
  })

  it('R8: the pre-existing disabled/offsetParent clauses still hold — a regression guard, not a driver', () => {
    const reachable = { disabled: false, offsetParent: {}, tabIndex: 0 } as unknown as HTMLElement
    const disabledEl = { disabled: true, offsetParent: {}, tabIndex: 0 } as unknown as HTMLElement
    const detached = { disabled: false, offsetParent: null, tabIndex: 0 } as unknown as HTMLElement
    expect(isFocusable(reachable)).toBe(true)
    expect(isFocusable(disabledEl)).toBe(false)
    expect(isFocusable(detached)).toBe(false)
  })
})
