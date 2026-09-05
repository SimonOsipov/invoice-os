// @vitest-environment jsdom
// RED specs (EXTR-15-09, task-835, Mode A / test-first) — AC-4: both review tabs take the
// review unit as a REQUIRED prop.
//
// AN OPTIONAL PROP IS THE WHOLE DEFECT. `unit?: ReviewUnit` compiles at every call site,
// falls back to 'spreadsheet' inside, and a caller that forgot it ships a document run
// still saying "rows" — with a green suite, because no render can tell "the caller passed
// spreadsheet" from "the caller passed nothing". Only the compiler can.
//
// SW-4 IS THEREFORE TWO SPECS AT TWO LAYERS, and neither one alone is the oracle:
//   SW-4a  the compile-time half. `pnpm --filter @invoice-os/app typecheck` is its ONLY
//          oracle — vitest strips types, so it runs green under vitest either way. Today
//          the `@ts-expect-error` directives below are UNUSED (omitting `unit` compiles
//          fine), which tsc reports as TS2578; when 09 lands they become used and tsc
//          goes quiet. That inversion is the spec.
//   SW-4b  the source-scan half, so the vitest leg is not blind to AC-4 entirely. It sees
//          `unit?:` and a default value, which is what an executor reaches for when the
//          existing render call sites start failing to compile.
//
// MODE B WILL HAVE TO EDIT ReviewAlreadyImportedTab.test.tsx. Twelve shipped render calls
// in that file omit `unit`, and every one becomes a tsc error the moment AC-4 lands. That
// is the prop doing its job, not a regression.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

import { ReviewAlreadyImportedTab } from './ReviewAlreadyImportedTab'
import { ReviewUnreadableTab } from './ReviewUnreadableTab'

function readSrc(rel: string): string {
  return readFileSync(path.join(process.cwd(), rel), 'utf8')
}

describe('EXTR-15-09 SW-4 (AC-4): the unit is a required prop on both review tabs', () => {
  it('SW-4a (RED under `typecheck` until EXTR-15-09; always green under vitest): omitting `unit` must not compile', () => {
    const missingOnUnreadable = (
      // @ts-expect-error AC-4: `unit` is required. While this directive is UNUSED, tsc
      // reports TS2578 here and that IS this spec's red.
      <ReviewUnreadableTab rows={[]} rowsTotal={0} batchIds={['b1']} onImportCorrected={() => {}} />
    )
    const missingOnAlreadyImported = (
      // @ts-expect-error AC-4: `unit` is required — see the note above.
      <ReviewAlreadyImportedTab rows={[]} rowsTotal={0} batchIds={['b1']} onOpenInvoice={() => {}} />
    )

    // Runtime is not the oracle here; these two keep the fixtures from being dead code.
    expect(missingOnUnreadable).toBeTruthy()
    expect(missingOnAlreadyImported).toBeTruthy()
  })

  it('SW-4b (RED until EXTR-15-09): each tab declares `unit: ReviewUnit`, neither optional nor defaulted', () => {
    const files = ['src/components/ReviewUnreadableTab.tsx', 'src/components/ReviewAlreadyImportedTab.tsx']
    const wrong: string[] = []

    for (const file of files) {
      const src = readSrc(file)
      // Control, paired with the two absence claims below: a moved or emptied file would
      // otherwise satisfy them by scanning nothing.
      expect(src, `${file} was not read`).toContain('export function Review')

      if (!src.includes('unit: ReviewUnit')) wrong.push(`${file}: no \`unit: ReviewUnit\` in the props type`)
      if (src.includes('unit?:')) wrong.push(`${file}: the unit prop is optional`)
      if (/unit(: ReviewUnit)?\s*=\s*'(spreadsheet|document)'/.test(src)) wrong.push(`${file}: the unit prop is defaulted`)
    }

    expect(wrong).toEqual([])
  })
})
