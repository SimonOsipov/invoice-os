// Once the reason nodes are gone the SPA suite has no oracle that keeps them gone; this
// static scan is it. Test files are scanned too -- a reintroduction inside a spec is
// still a reintroduction. Self-verifying: this file holds the needle as a literal, so a
// broken SELF exclusion turns the absence check RED, never falsely green.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('.', import.meta.url))
const SELF = fileURLToPath(import.meta.url)

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules') continue
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(path))
    else if (/\.tsx?$/.test(entry.name) && path !== SELF) out.push(path)
  }
  return out
}

const FILES = sourceFiles(SRC)
const CORPUS = FILES.map((path) => [path, readFileSync(path, 'utf8')] as const)

// The BARE testid value, never the `data-testid="..."` declaration form: the bare value
// also matches every getByTestId locator and a `${r.id}` template literal.
function filesContaining(needle: string): string[] {
  return CORPUS.filter(([, text]) => text.includes(needle)).map(([path]) => path)
}

describe('BUG-09: the row blocked-reason node stays deleted', () => {
  it('control: the scan reaches the SPA source tree', () => {
    expect(
      FILES.length,
      'the scan drifted off frontend/app/src -- every absence check below would be vacuous',
    ).toBeGreaterThanOrEqual(200)
  })

  it('control: both surviving checkbox testids are still found', () => {
    expect(filesContaining('invoice-select').length).toBeGreaterThan(0)
    expect(filesContaining('review-select').length).toBeGreaterThan(0)
  })

  it('control: the out-of-scope sibling *-blocked-reason testids are still found', () => {
    expect(filesContaining('approval-blocked-reason').length).toBeGreaterThan(0)
    expect(filesContaining('submit-blocked-reason').length).toBeGreaterThan(0)
  })

  it('the testid invoice-blocked-reason appears in no file', () => {
    expect(filesContaining('invoice-blocked-reason')).toEqual([])
  })

  it('the testid review-select-blocked-reason appears in no file', () => {
    expect(filesContaining('review-select-blocked-reason')).toEqual([])
  })
})

// The needles above only bite if a reintroduction reuses the OLD id name. An
// `aria-describedby` re-added under any other name dangles silently: no rendered spec
// reads that attribute off `review-select` any more, by decision (D-6, D-7).
describe('BUG-09: the row select checkbox wires no aria-describedby', () => {
  const reviewRowSrc = readFileSync(join(SRC, 'components', 'ReviewRow.tsx'), 'utf8')
  const selectBlock = reviewRowSrc.match(/data-testid="review-select"[\s\S]*?\/>/)?.[0] ?? null

  it('control: the scan reaches the review-select input and its attributes', () => {
    expect(selectBlock, 'the input block no longer matches -- the absence check below is vacuous').not.toBeNull()
    // Both edges of the attribute list, so a match that stopped early cannot read as absent.
    expect(selectBlock, 'the match stops short of the attribute list').toContain('disabled={!isRowSelectable(r)}')
    expect(selectBlock, 'the match ends before the last attribute').toContain('onChange={(e) => {')
  })

  it("control: the Re-validate button's own aria-describedby is still found", () => {
    expect(reviewRowSrc).toContain('aria-describedby={view.revalidateReason != null ? REVALIDATE_REASON_ID : undefined}')
  })

  it('the select checkbox carries no aria-describedby', () => {
    expect(selectBlock).not.toContain('aria-describedby')
  })
})
