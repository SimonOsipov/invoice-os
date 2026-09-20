// Static copy guard for the "never matched by name" rewording. Mirrors
// reviewCopy.census.test.ts's readSrc idiom -- source-text assertions, not render.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

const CREATE_MAPPING = 'src/components/CreateMapping.tsx'
const CLIENTS = 'src/lib/clients.ts'
const CREATE_FORM = 'src/components/CreateForm.tsx'

const SHARED_SENTENCE = `the invoice number is never matched by name: only suggested from the file's own rows, or restored from this client's earlier import, and confirmed with Continue.`

function readSrc(rel: string): string {
  return readFileSync(path.join(process.cwd(), rel), 'utf8')
}

describe('mappingCopy', () => {
  it(`COPY-01: every "never matched by name" line in CreateMapping.tsx also carries the whole rule sentence`, () => {
    const src = readSrc(CREATE_MAPPING)
    expect(src, 'control: the file must actually be read').toContain('export function CreateMapping(')

    const lines = src.split('\n').filter((l) => l.includes('never matched by name'))
    expect(lines, 'exactly three lines must say "never matched by name"').toHaveLength(3)

    const missing = lines.filter((l) => !l.includes(SHARED_SENTENCE))
    expect(missing).toEqual([])
  })

  it(`COPY-02: the three reworded sites carry the exact wording, and the legend names SUGGESTED then RESTORED`, () => {
    const src = readSrc(CREATE_MAPPING)
    const needles = [
      `// badged AUTO. The invoice number is never matched by name: only suggested from the file's own rows, or restored from this client's earlier import, and confirmed with Continue.`,
      `Drag invoice_number onto a column to continue — the invoice number is never matched by name: only suggested from the file's own rows, or restored from this client's earlier import, and confirmed with Continue.`,
      `and confirmed with Continue. A suggestion is marked`,
    ]
    for (const n of needles) {
      expect(src.includes(n), n).toBe(true)
    }

    const suggestedSpan = `<span className="mono" style={{ fontSize: 10, color: 'var(--status-amber-text)' }}>SUGGESTED</span>, a restore`
    const restoredSpan = `<span className="mono" style={{ fontSize: 10, color: 'var(--action)' }}>RESTORED</span>.`

    const afterSuggestedNeedle = src.slice(src.indexOf(needles[2]!) + needles[2]!.length).replace(/^\{' '\}\s*/, '')
    expect(afterSuggestedNeedle.startsWith(suggestedSpan), 'the legend must name SUGGESTED next, in the amber colour').toBe(true)

    const afterSuggestedSpan = afterSuggestedNeedle.slice(suggestedSpan.length).replace(/^\{' '\}\s*/, '')
    expect(
      afterSuggestedSpan.startsWith(restoredSpan),
      'the RESTORED span, in the action colour, must close the reworded sentence',
    ).toBe(true)
  })

  it(`COPY-03: the old "never guessed" wording is gone from CreateMapping.tsx`, () => {
    const src = readSrc(CREATE_MAPPING)
    expect(src.includes('never matched by name'), 'control: the new wording must be present').toBe(true)
    expect(src.includes('never guessed')).toBe(false)
  })

  it(`COPY-04: the manual form's rule is untouched`, () => {
    const clientsSrc = readSrc(CLIENTS)
    expect(clientsSrc, 'control: the file must actually be read').toContain('export function defaultDraft(')
    expect(clientsSrc).toContain('//   invoice number is a fiscal identifier the product never guesses (§9).')
    expect(clientsSrc.includes('never matched by name')).toBe(false)

    const createFormSrc = readSrc(CREATE_FORM)
    expect(createFormSrc, 'control: the file must actually be read').toContain('export function CreateForm(')
    expect(createFormSrc).toContain('identifier the product does not guess. */}')
    expect(createFormSrc.includes('never matched by name')).toBe(false)
  })
})
