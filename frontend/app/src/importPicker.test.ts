// PICKER-1..3 (EXTR-09-04, task-771, Test-first) — RED specs for the widened import
// picker. Mechanical source scans, in the idiom sourceDocumentScope.test.ts already uses:
// the picker itself is unrenderable in this suite (vitest environment is 'node', no jsdom),
// so its attribute and its copy are read out of the source instead.
//
// PICKER-3 carries an ABSENCE claim (no spec pins the accept attribute). Per AC #7 it also
// carries a population floor and a control needle, because a wholesale deletion of the 17
// locator blocks would satisfy the absence half over nothing — the M4-04 failure class.
import { readdirSync, readFileSync, existsSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const SRC_DIR = dirname(fileURLToPath(import.meta.url))
const CREATE_UPLOAD = join(SRC_DIR, 'components', 'CreateUpload.tsx')

// AC #1, verbatim. Eight extensions, seven types (.jpg and .jpeg are one type).
const EXPECTED_ACCEPT = '.csv,.xlsx,.pdf,.png,.jpg,.jpeg,.webp,.docx'
const EXPECTED_EXTENSIONS = EXPECTED_ACCEPT.split(',')
const EXPECTED_COPY_TOKENS = EXPECTED_EXTENSIONS.map((e) => e.replace('.', '').toUpperCase())

// The 17 locators EXTR-09-04 migrates: 14 in import-wizard.spec.ts, 3 in
// invoice-surfaces.spec.ts. The floor is a lower bound, never an equality — adding a
// locator must not red this.
const SWEPT_LOCATOR_FLOOR = 17
const SWEPT_SPECS = ['import-wizard.spec.ts', 'invoice-surfaces.spec.ts']

// Comments in CreateUpload.tsx discuss `accept` and the word "spreadsheet" at length. Every
// scan below reads CODE, so the prose explaining a rule can never satisfy or violate it.
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '')
}

function pickerCode(): string {
  const raw = readFileSync(CREATE_UPLOAD, 'utf8')
  expect(raw.length, 'CreateUpload.tsx must be non-empty').toBeGreaterThan(0)
  // Positive control: proves the read landed on the real picker before anything is parsed
  // out of it.
  expect(raw, 'CreateUpload.tsx must still declare the picker input this story widens').toContain('id="pf-import-file"')
  return stripComments(raw)
}

// Fails loudly rather than returning '' — a silent miss would let every assertion below
// pass over nothing.
function acceptAttribute(): string {
  const code = pickerCode()
  const all = code.match(/accept="[^"]*"/g) ?? []
  expect(all.length, 'CreateUpload.tsx must carry exactly one accept="…" attribute in code; these specs have lost their anchor').toBe(1)
  const m = /accept="([^"]*)"/.exec(code)
  expect(m, 'accept="…" did not parse out of CreateUpload.tsx').not.toBeNull()
  return m![1]
}

// The visible ACCEPTED · … line (CreateUpload.tsx:175), split into its tokens.
function acceptedCopyTokens(): string[] {
  const code = pickerCode()
  const m = /^\s*ACCEPTED\s*·([^\n<{]*)/m.exec(code)
  expect(m, 'no `ACCEPTED · …` line in CreateUpload.tsx; the copy this spec compares is gone').not.toBeNull()
  return m![1]
    .split('·')
    .map((t) => t.trim())
    .filter((t) => t.length > 0)
}

describe('PICKER-1: the accept attribute names all seven accepted types', () => {
  it('PICKER-1: the accept attribute names all seven accepted types', () => {
    const accept = acceptAttribute()
    const exts = accept.split(',').map((e) => e.trim())

    expect(exts.length, 'the accept attribute must list at least one extension').toBeGreaterThan(0)
    // No duplicate: a longer string that repeats an extension accepts nothing extra and
    // makes the copy below impossible to keep honest.
    expect(new Set(exts).size, `accept lists a duplicate extension: ${accept}`).toBe(exts.length)
    expect(exts.slice().sort()).toEqual(EXPECTED_EXTENSIONS.slice().sort())
    // AC #1 pins the ORDER too — the copy line mirrors it.
    expect(accept).toBe(EXPECTED_ACCEPT)
  })
})

describe('PICKER-2: the accepted-types copy matches the accept attribute', () => {
  it('PICKER-2: the accepted-types copy matches the accept attribute', () => {
    const tokens = acceptedCopyTokens()
    expect(tokens.length, 'the ACCEPTED · … line must name at least one type').toBeGreaterThan(0)

    // AC-6 first half: the visible line names every accepted type.
    expect(tokens).toEqual(EXPECTED_COPY_TOKENS)

    // …and it says the same thing the attribute says, in the same order. This is the half
    // that survives a later widening: change one and this reds.
    const exts = acceptAttribute()
      .split(',')
      .map((e) => e.trim().replace('.', '').toUpperCase())
    expect(exts.length, 'the accept attribute parsed to nothing; the comparison below would be vacuous').toBeGreaterThan(0)
    expect(tokens).toEqual(exts)
  })

  // AC-6 second half. The three shipped literals are named exactly, so this spec states
  // what must GO without dictating what replaces it. Comments are stripped first: the file
  // discusses "spreadsheet" in a dozen explanatory blocks that are not visible copy.
  it('PICKER-2 (AC-6 second half): the picker no longer describes itself as spreadsheet-only', () => {
    const code = pickerCode()
    const RETIRED_COPY = [
      'Choose a spreadsheet to import',
      'Drag spreadsheets here, or click to choose',
      'Not a spreadsheet — choose a .csv or .xlsx file.',
    ]
    // Control needle: the surrounding copy this scan reads is still here, so a rewrite
    // that emptied the component could not pass the absences below vacuously.
    expect(code, 'the dropzone copy block must still be present').toContain('The parser extracts')

    for (const literal of RETIRED_COPY) {
      expect(code, `CreateUpload.tsx must no longer ship the spreadsheet-only copy ${JSON.stringify(literal)}`).not.toContain(literal)
    }
  })
})

// --- PICKER-3: the e2e sweep -----------------------------------------------------------

// Walks up to the repo root by its go.mod rather than counting '..' segments, so moving
// this file cannot silently retarget the scan at a directory that does not exist.
function repoRoot(): string {
  let dir = SRC_DIR
  for (let i = 0; i < 10; i++) {
    if (existsSync(join(dir, 'go.mod')) && existsSync(join(dir, 'e2e'))) return dir
    const up = dirname(dir)
    if (up === dir) break
    dir = up
  }
  throw new Error(`no repo root (a directory holding both go.mod and e2e/) above ${SRC_DIR}`)
}

const SKIP_DIRS = new Set(['node_modules', 'test-results', 'playwright-report', '.git'])

function walkE2E(): Array<{ rel: string; content: string }> {
  const root = join(repoRoot(), 'e2e')
  const out: Array<{ rel: string; content: string }> = []
  const stack = [root]
  while (stack.length > 0) {
    const dir = stack.pop()!
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, e.name)
      if (e.isDirectory()) {
        if (!SKIP_DIRS.has(e.name)) stack.push(full)
      } else if (e.isFile() && /\.tsx?$/.test(e.name)) {
        out.push({ rel: relative(root, full), content: readFileSync(full, 'utf8') })
      }
    }
  }
  return out
}

function countOccurrences(hay: string, needle: string): number {
  return hay.split(needle).length - 1
}

describe('PICKER-3: no spec pins the accept attribute, and the swept locators still exist', () => {
  // AC #7, the floor half. It is the control needle for the absence half below: deleting
  // the 17 locator blocks outright would satisfy "no file contains accept=" over nothing,
  // and this is what refuses that.
  it('PICKER-3 floor: the 17 swept locators moved to #pf-import-file rather than vanishing', () => {
    const files = walkE2E()
    expect(files.length, 'the walk visited no e2e file at all').toBeGreaterThanOrEqual(10)

    for (const name of SWEPT_SPECS) {
      const spec = files.find((f) => f.rel.endsWith(name))
      expect(spec, `${name} must still exist under e2e/`).toBeDefined()
      expect(spec!.content.length, `${name} must be non-empty`).toBeGreaterThan(0)
    }

    const total = files.reduce((n, f) => n + countOccurrences(f.content, '#pf-import-file'), 0)
    expect(
      total,
      `#pf-import-file appears ${total} time(s) across e2e/, want at least ${SWEPT_LOCATOR_FLOOR} — the swept locators must MOVE to the id, not be deleted`,
    ).toBeGreaterThanOrEqual(SWEPT_LOCATOR_FLOOR)
  })

  // AC #5, the absence half. Runs its own floor first so it can never pass over an empty
  // or gutted corpus.
  it('PICKER-3 absence: no file under e2e/ pins an accept attribute', () => {
    const files = walkE2E()
    expect(files.length, 'the walk visited no e2e file at all').toBeGreaterThanOrEqual(10)
    // Positive control from the SAME walk: proves file CONTENTS were read, not just names.
    expect(
      files.some((f) => f.content.includes('page.locator(')),
      'no e2e file contains page.locator( — the walk read names but not contents',
    ).toBe(true)
    for (const name of SWEPT_SPECS) {
      expect(
        files.some((f) => f.rel.endsWith(name) && f.content.length > 0),
        `${name} must still exist and be non-empty, or this absence check passes vacuously`,
      ).toBe(true)
    }

    for (const f of files) {
      expect(f.content, `e2e/${f.rel} must not pin an accept attribute — locate the input by #pf-import-file`).not.toContain('accept="')
    }
  })
})
