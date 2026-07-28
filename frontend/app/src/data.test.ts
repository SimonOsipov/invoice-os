// Pins the SAMPLE_FILES invariant that makes M4-08-FU-01's deletion safe (task-177
// AC#2). CreateForm.tsx:15 and CreateFlow.tsx:24 both map `uploadFile` (only ever set
// from a SAMPLE_FILES id, App.tsx:314) to a display filename; CreateForm hardcodes the
// two names as a ternary with NO fallback arm. Adding a third sample file — or renaming
// one — silently renders '' there and breaks import-wizard.spec.ts's E2E-08/E2E-10
// exact-text assertions on 'lagos-freight-INV-0482.pdf'. Since [import-upload-unify]
// that oracle is weaker than it was: the document card is sandbox-gated, so those
// assertions only run after the spec toggles SANDBOX, and any future test that forgets
// the toggle silently asserts against an absent element. Components need jsdom (vitest
// is environment:'node'), so this pure-data check is the sole unit-level guard.

import { describe, it, expect } from 'vitest'
import { SAMPLE_FILES } from './data'

describe('SAMPLE_FILES (task-177 AC#2 premise)', () => {
  it('carries exactly the pdf and img ids — a csv sample would resurrect the branch CreateForm.tsx deleted', () => {
    expect(SAMPLE_FILES.map((f) => f.id)).toEqual(['pdf', 'img'])
  })

  it('has no id CreateForm.tsx cannot name — its ternary has no fallback, so an unhandled id renders an empty filename', () => {
    const handled = new Set(['pdf', 'img'])
    const unhandled = SAMPLE_FILES.filter((f) => !handled.has(f.id))
    expect(unhandled).toEqual([])
  })

  it('keeps the exact display names CreateForm.tsx hardcodes and the deployed e2e asserts verbatim', () => {
    const byId = Object.fromEntries(SAMPLE_FILES.map((f) => [f.id, f.name]))
    expect(byId.pdf).toBe('lagos-freight-INV-0482.pdf')
    expect(byId.img).toBe('scan-invoice-0482.jpg')
  })

  it('has unique ids — SAMPLE_FILES.find() would otherwise silently shadow a duplicate', () => {
    const ids = SAMPLE_FILES.map((f) => f.id)
    expect(new Set(ids).size).toBe(ids.length)
  })
})
