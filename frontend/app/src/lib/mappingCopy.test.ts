// Static copy guard for the "never guessed" rewording. Mirrors
// reviewCopy.census.test.ts's readSrc idiom -- source-text assertions, not render.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

const CREATE_MAPPING = 'src/components/CreateMapping.tsx'

function readSrc(rel: string): string {
  return readFileSync(path.join(process.cwd(), rel), 'utf8')
}

describe('mappingCopy', () => {
  it(`COPY-01: every "never guessed" line in CreateMapping.tsx also says the invoice number is only restored from this client's earlier import`, () => {
    const src = readSrc(CREATE_MAPPING)
    expect(src, 'control: the file must actually be read').toContain('export function CreateMapping(')

    const lines = src.split('\n').filter((l) => l.includes('never guessed'))
    expect(lines, 'exactly three lines must say "never guessed"').toHaveLength(3)

    const missing = lines.filter((l) => !l.includes("only restored from this client's earlier import"))
    expect(missing).toEqual([])
  })

  it(`COPY-02: the three reworded sites carry System Design §6's exact wording`, () => {
    const src = readSrc(CREATE_MAPPING)
    const needles = [
      `// badged AUTO — except the invoice number, which is never guessed, only restored from this client's earlier import.`,
      `Drag invoice_number onto a column to continue — the invoice number is never guessed, only restored from this client's earlier import.`,
      `— the invoice number is never guessed, only restored from this client's earlier import and marked`,
    ]
    for (const n of needles) {
      expect(src.includes(n), n).toBe(true)
    }

    const after = src.slice(src.indexOf(needles[2]!) + needles[2]!.length).replace(/^\{' '\}\s*/, '')
    expect(
      after.startsWith(`<span className="mono" style={{ fontSize: 10, color: 'var(--action)' }}>RESTORED</span>.`),
      'the RESTORED span, in the action colour, must close the reworded sentence',
    ).toBe(true)
  })
})
