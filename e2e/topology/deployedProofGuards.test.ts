// EXTR-18-07 (task-851) local guard. The three deployed-proof specs cannot run locally --
// they need EXTRACTOR=docling on production, still a pending operator action (D-35). This
// scans import-wizard.spec.ts and e2e/ as source text and proves the two failure modes that
// ARE checkable without a deployment: a per-tenant content-hash collision silently reusing a
// stale job, and a negation that passes on a reachable-but-empty sidecar.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const E2E_ROOT = dirname(TOPOLOGY_DIR)
const SPEC_PATH = join(TOPOLOGY_DIR, 'import-wizard.spec.ts')
const source = readFileSync(SPEC_PATH, 'utf8')
const SELF = fileURLToPath(import.meta.url)

const BLOCK_START = 'EXTR-18-07 · the deployed proof'
const blockStart = source.indexOf(BLOCK_START)
if (blockStart === -1) throw new Error(`start marker not found in import-wizard.spec.ts: ${JSON.stringify(BLOCK_START)}`)
const block = source.slice(blockStart)

function listTsFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules') continue
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listTsFiles(full))
    else if (entry.isFile() && entry.name.endsWith('.ts')) out.push(full)
  }
  return out
}

describe('[extr-18-07] guard population (control needle + floor)', () => {
  it('found the EXTR-18-07 block marker (control needle)', () => {
    expect(source).toContain(BLOCK_START)
  })

  it('the EXTR-18-07 block is non-trivial', () => {
    expect(block.length, 'the block from its start marker to EOF is near-empty -- a drifted marker scanned nothing').toBeGreaterThan(2_000)
  })
})

describe('[extr-18-07] every EXTR18 fixture upload goes through a unique*PdfBytes() helper', () => {
  const bufferArgs = [...block.matchAll(/buffer:\s*([^,}\n]+)/g)].map((m) => m[1].trim())

  it('found buffer: args to check (control needle)', () => {
    expect(bufferArgs.length, 'no buffer: argument found in the EXTR-18-07 block -- the check below covers nothing').toBeGreaterThanOrEqual(2)
  })

  // `Buffer` is settleOneDocument's type annotation and `file.buffer` its forwarding
  // parameter; neither is an upload's byte source. What this guards is a raw module-scope
  // fixture constant (SCANNED_INVOICE_PDF) reaching setInputFiles unfreshened, which would
  // collide on the per-document enqueue key and settle on a PREVIOUS run's job.
  const FORWARDERS = new Set(['Buffer', 'file.buffer'])

  it('every buffer: arg calls a unique*PdfBytes() helper, never a raw fixture constant', () => {
    const offenders = bufferArgs.filter((a) => !FORWARDERS.has(a) && !/^unique\w*PdfBytes\(\)$/.test(a))
    expect(offenders, `non-helper buffer arg(s): ${offenders.join(', ')}`).toEqual([])
  })

  // Population floor: without it the check above passes vacuously the moment every real
  // call site is refactored behind a forwarder.
  it('at least two buffer: args are real unique*PdfBytes() call sites', () => {
    const calls = bufferArgs.filter((a) => /^unique\w*PdfBytes\(\)$/.test(a))
    expect(calls.length, `only ${calls.length} helper call site(s) in the block`).toBeGreaterThanOrEqual(2)
  })

  it('no readFileSync call reaches extractOneDocument directly inside the block', () => {
    expect(block.includes('readFileSync'), 'a raw readFileSync call appears inside the EXTR-18-07 block').toBe(false)
  })
})

describe('[extr-18-07] unique*PdfBytes helpers mint their UUID per call, not at module scope', () => {
  const fnPattern = /function\s+(unique\w*PdfBytes)\s*\(\)\s*:\s*Buffer\s*\{([\s\S]*?)\n\}/g
  const helpers = [...source.matchAll(fnPattern)].map((m) => ({ name: m[1], body: m[2] }))

  it('found unique*PdfBytes helper definitions (control needle)', () => {
    expect(helpers.length, 'no unique*PdfBytes() helper found -- the check below covers nothing').toBeGreaterThanOrEqual(4)
  })

  it('includes uniqueScannedPdfBytes and uniqueDensePdfBytes', () => {
    const names = helpers.map((h) => h.name)
    expect(names).toEqual(expect.arrayContaining(['uniqueScannedPdfBytes', 'uniqueDensePdfBytes']))
  })

  it('every helper body calls randomUUID() inline', () => {
    const offenders = helpers.filter((h) => !h.body.includes('randomUUID()')).map((h) => h.name)
    expect(offenders, `helper(s) with no inline randomUUID() call: ${offenders.join(', ')}`).toEqual([])
  })

  // Matched by line PREFIX only (not a balanced-paren suffix): readFileSync(join(...)) nests
  // parens, so a `[^)]*\)$` tail regex can never reach the real closing paren and silently
  // matches zero lines -- a vacuous pass that would wave through a UUID hoisted onto this line.
  const constLines = source.split('\n').filter((l) => /^const \w+_PDF = readFileSync\(/.test(l))

  it('found module-scope fixture constant lines (control needle)', () => {
    expect(constLines.length, 'no const ..._PDF = readFileSync(...) line found -- the check below covers nothing').toBeGreaterThanOrEqual(2)
  })

  it('the module-scope fixture constants carry no randomUUID call of their own', () => {
    const offenders = constLines.filter((line) => line.includes('randomUUID'))
    expect(offenders, `fixture constant(s) minting a UUID at module scope: ${offenders.join(', ')}`).toEqual([])
  })
})

describe('[extr-18-07] no MOCK-INV-0001 negation anywhere under e2e/', () => {
  const files = listTsFiles(E2E_ROOT).filter((f) => f !== SELF)
  const contents = files.map((f) => ({ path: f, text: readFileSync(f, 'utf8') }))

  // Comments (like this spec's own note above EXTR18-E2E-01) legitimately quote the banned
  // negation to explain why it was avoided. Stripped before scanning so the scan reads code,
  // not documentation about code. Line-based, so a literal containing "//" would truncate --
  // no such literal exists among the strings this check cares about.
  function stripLineComments(text: string): string {
    return text
      .split('\n')
      .map((line) => {
        const idx = line.indexOf('//')
        return idx === -1 ? line : line.slice(0, idx)
      })
      .join('\n')
  }

  it('scanned a real population of e2e/ .ts files', () => {
    expect(files.length, 'the walk found suspiciously few .ts files -- it may be scanning the wrong directory').toBeGreaterThan(20)
  })

  it('control needle: MOCK-INV-0001 appears somewhere under e2e/', () => {
    const found = contents.some((c) => c.text.includes('MOCK-INV-0001'))
    expect(found, 'MOCK-INV-0001 was not found anywhere under e2e/ -- the scan below proves nothing').toBe(true)
  })

  it('no file negates MOCK-INV-0001 with !== or != in code (comments excluded)', () => {
    const pattern = /!==?\s*['"]MOCK-INV-0001['"]/
    const offenders = contents
      .filter((c) => pattern.test(stripLineComments(c.text)))
      .map((c) => c.path.slice(E2E_ROOT.length + 1))
    expect(offenders, offenders.join(', ')).toEqual([])
  })
})

describe('[extr-18-07] each deployed-proof spec sets test.setTimeout() >= 300_000', () => {
  const testNames = [
    "EXTR18-E2E-01 (AC-5): the deployed reading is the document's own number",
    'EXTR18-E2E-02 (AC-8): a document with no recoverable text settles unreadable, and its pages still render',
    'EXTR18-E2E-03: an image-only page the OCR can read is NOT unreadable',
  ]

  const starts = testNames.map((name) => {
    const idx = block.indexOf(name)
    if (idx === -1) throw new Error(`test name not found in the EXTR-18-07 block: ${JSON.stringify(name)}`)
    return idx
  })

  it('found all three EXTR18 test name markers (control needle)', () => {
    expect(starts.length).toBe(3)
  })

  for (let i = 0; i < testNames.length; i++) {
    it(`"${testNames[i]}" sets test.setTimeout() >= 300_000`, () => {
      const from = starts[i]
      const to = i + 1 < starts.length ? starts[i + 1] : block.length
      const testBody = block.slice(from, to)
      // The budget, not a literal: settleOneDocument alone can wait 240s on the extraction
      // and 120s on the landing, so -02/-03 carry 600_000. Playwright's 30s default would
      // kill any of them, which is what this floor exists to prevent.
      const call = /test\.setTimeout\((\d[\d_]*)\)/.exec(testBody)
      expect(call, 'no test.setTimeout(...) call in this test body').not.toBeNull()
      expect(
        Number(call![1].replace(/_/g, '')),
        `test.setTimeout(${call![1]}) is below the 300s deployed-proof floor`,
      ).toBeGreaterThanOrEqual(300_000)
    })
  }
})
