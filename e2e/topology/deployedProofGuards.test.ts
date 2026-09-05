// EXTR-18-07 (task-851) local guard. The three deployed-proof specs cannot run locally --
// they need EXTRACTOR=docling on production, still a pending operator action (D-35). This
// scans import-wizard.spec.ts and e2e/ as source text and proves the two failure modes that
// ARE checkable without a deployment: a per-tenant content-hash collision silently reusing a
// stale job, and a negation that passes on a reachable-but-empty sidecar.
import { describe, expect, it } from 'vitest'
import { unzipSync } from 'fflate'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const E2E_ROOT = dirname(TOPOLOGY_DIR)
const REPO_ROOT = dirname(E2E_ROOT)
const SPEC_PATH = join(TOPOLOGY_DIR, 'import-wizard.spec.ts')
const source = readFileSync(SPEC_PATH, 'utf8')
const SELF = fileURLToPath(import.meta.url)

const BLOCK_START = 'EXTR-18-07 · the deployed proof'
const blockStart = source.indexOf(BLOCK_START)
if (blockStart === -1) throw new Error(`start marker not found in import-wizard.spec.ts: ${JSON.stringify(BLOCK_START)}`)
const block = source.slice(blockStart)

// EXTR-15-12 (task-836). The EXTR-15 deployed-proof span runs from its own marker to
// EXTR-18-07's, and is scanned SEPARATELY: two of its documents are DOCX, which no
// unique*PdfBytes() helper mints, so it needs its own allowlist. The rest of
// import-wizard.spec.ts is CSV probes with `Buffer.from(...)` bodies and is not scanned at
// all -- freshness only matters where an upload is polled to a settled extraction.
const EXTR15_BLOCK_START = 'EXTR-15 · the deployed proof'
const extr15Start = source.indexOf(EXTR15_BLOCK_START)
if (extr15Start === -1)
  throw new Error(`start marker not found in import-wizard.spec.ts: ${JSON.stringify(EXTR15_BLOCK_START)}`)
if (extr15Start >= blockStart)
  throw new Error('the EXTR-15 marker no longer precedes the EXTR-18-07 marker -- the span it delimits is empty')
const extr15Block = source.slice(extr15Start, blockStart)

// Comments legitimately quote the very patterns these guards scan for -- the EXTR-15 span's
// own header explains its `buffer:` rule, and EXTR18-E2E-01's note quotes the banned
// MOCK-INV-0001 negation. Stripped before scanning so a guard reads code, not documentation
// about code. Line-based, so a literal containing "//" would truncate -- no such literal
// exists among the strings these checks care about.
function stripLineComments(text: string): string {
  return text
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      return idx === -1 ? line : line.slice(0, idx)
    })
    .join('\n')
}

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
  // _DOCX joins _PDF with EXTR-15-12: GOLDEN_INVOICE_DOCX is a module-scope fixture constant
  // read the same way, and a UUID hoisted onto its line would be the same defect.
  const constLines = source.split('\n').filter((l) => /^const \w+_(PDF|DOCX) = readFileSync\(/.test(l))

  it('found module-scope fixture constant lines (control needle)', () => {
    expect(constLines.length, 'no const ..._PDF/..._DOCX = readFileSync(...) line found -- the check below covers nothing').toBeGreaterThanOrEqual(2)
  })

  it('the module-scope fixture constants carry no randomUUID call of their own', () => {
    const offenders = constLines.filter((line) => line.includes('randomUUID'))
    expect(offenders, `fixture constant(s) minting a UUID at module scope: ${offenders.join(', ')}`).toEqual([])
  })
})

describe('[extr-18-07] no MOCK-INV-0001 negation anywhere under e2e/', () => {
  const files = listTsFiles(E2E_ROOT).filter((f) => f !== SELF)
  const contents = files.map((f) => ({ path: f, text: readFileSync(f, 'utf8') }))

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

describe('[deployed-proof] every deployed-proof spec sets test.setTimeout() >= 300_000', () => {
  // Scanned over the WHOLE file rather than the EXTR-18-07 block (EXTR-15-12): the floor is a
  // property of a deployed-proof spec wherever it sits, and the EXTR-15 ones sit above that
  // marker. Each body is sliced from its own title to the NEXT top-level `test(` -- not to the
  // next registered title, which would swallow any unregistered test in between.
  const testNames = [
    "EXTR18-E2E-01 (AC-5): the deployed reading is the document's own number",
    'EXTR18-E2E-02 (AC-8): a document with no recoverable text settles unreadable, and its pages still render',
    'EXTR18-E2E-03: an image-only page the OCR can read is NOT unreadable',
    'EXTR15-E2E-01 (AC-10): the hand-off row sits inside its card, and its gutter holds at every width',
    'EXTR15-E2E-02 (AC-6): a two-document run hands off the row that was clicked, not its sibling',
    'EXTR15-E2E-03: the deployed sidecar reads a real DOCX, and reads its printed fields',
    'EXTR15-E2E-04 (T3): a DOCX the reader cannot open dead-letters at text_not_read',
    // EXTR-15-13. The first sits ABOVE the EXTR-15 marker (its fixtures are CSV, which enqueues
    // no extraction, so the span's freshness rule has nothing to protect); this floor is scanned
    // over the whole file, so registering it here still covers it.
    'EXTR15-E2E-05 (AC-1): a spreadsheet run still reads ROWS READ, Rows stored and Row',
    'EXTR15-E2E-06 (AC-2/AC-3): the document review screen says documents and register, and holds its controls at every width',
  ]

  const testStarts = [...source.matchAll(/\ntest\(/g)].map((m) => m.index + 1)

  it('found the file\'s top-level test( declarations (control needle)', () => {
    expect(testStarts.length, 'no top-level test( found -- every body below would run to EOF').toBeGreaterThan(20)
  })

  for (const name of testNames) {
    it(`"${name}" sets test.setTimeout() >= 300_000`, () => {
      const from = source.indexOf(name)
      expect(from, `test name not found in import-wizard.spec.ts: ${JSON.stringify(name)}`).toBeGreaterThan(-1)
      const to = testStarts.find((i) => i > from) ?? source.length
      const testBody = source.slice(from, to)
      // The budget, not a literal: runDocuments/settleOneDocument alone can wait 240s on the
      // extraction and 120s more on the landing. Playwright's 30s default would kill any of
      // these, which is what this floor exists to prevent.
      const call = /test\.setTimeout\((\d[\d_]*)\)/.exec(testBody)
      expect(call, 'no test.setTimeout(...) call in this test body').not.toBeNull()
      expect(
        Number(call![1].replace(/_/g, '')),
        `test.setTimeout(${call![1]}) is below the 300s deployed-proof floor`,
      ).toBeGreaterThanOrEqual(300_000)
    })
  }
})

// --- EXTR-15-12 (task-836): the EXTR-15 deployed-proof span --------------------------------
//
// Same two failure modes the EXTR-18-07 guard above covers, over the span that carries the
// EXTR-15 cases: a fixture reaching setInputFiles unfreshened (which the per-tenant content
// hash reuses and the PERMANENT per-document enqueue key then skips, so the poll settles on a
// PREVIOUS run's job and stays green while extraction is broken), and a copy literal that has
// drifted from the code that emits it.

describe('[extr-15-12] EXTR-15 span population (control needle + floor)', () => {
  it('found the EXTR-15 block marker (control needle)', () => {
    expect(source).toContain(EXTR15_BLOCK_START)
  })

  it('the EXTR-15 block is non-trivial', () => {
    expect(
      extr15Block.length,
      'the span between the two markers is near-empty -- a drifted marker scanned nothing',
    ).toBeGreaterThan(2_000)
  })
})

describe('[extr-15-12] every EXTR-15 fixture upload goes through a fresh-per-call helper', () => {
  // The five helpers that mint fresh bytes on every call. A raw module-scope constant reaching
  // setInputFiles is the defect; `Buffer`, runDocuments' own type annotation, is not.
  const FRESH_HELPERS = [
    'uniqueScannedPdfBytes()',
    'uniqueDensePdfBytes()',
    'uniqueGarbageBytes()',
    'uniqueGoldenDocxBytes()',
    'uniqueEmptyDocxBytes()',
  ]
  const FORWARDERS = new Set(['Buffer', 'file.buffer'])
  // Comment-stripped: this span's own header documents the rule below in prose, and the
  // prose would otherwise read as an offending call site.
  const bufferArgs = [...stripLineComments(extr15Block).matchAll(/buffer:\s*([^,}\n]+)/g)].map((m) => m[1].trim())

  it('found buffer: args to check (control needle)', () => {
    expect(bufferArgs.length, 'no buffer: argument in the EXTR-15 span -- the check below covers nothing').toBeGreaterThanOrEqual(6)
  })

  it('every buffer: arg calls a fresh-per-call helper, never a raw fixture constant', () => {
    const offenders = bufferArgs.filter((a) => !FORWARDERS.has(a) && !FRESH_HELPERS.includes(a))
    expect(offenders, `non-helper buffer arg(s): ${offenders.join(', ')}`).toEqual([])
  })

  // Population floor: without it the check above passes vacuously the moment every real call
  // site is refactored behind a forwarder. Raised 5 -> 8 by EXTR-15-13, whose EXTR15-E2E-06
  // adds three more uploads to the span.
  it('every upload in the span is a real helper call site', () => {
    const calls = bufferArgs.filter((a) => FRESH_HELPERS.includes(a))
    expect(calls.length, `only ${calls.length} helper call site(s) in the span`).toBeGreaterThanOrEqual(8)
  })

  it('every named helper exists and mints its UUID inline', () => {
    for (const call of FRESH_HELPERS) {
      const name = call.slice(0, -2)
      const m = new RegExp(`function\\s+${name}\\s*\\(\\)\\s*:\\s*Buffer\\s*\\{([\\s\\S]*?)\\n\\}`).exec(source)
      expect(m, `no ${name}() helper found in import-wizard.spec.ts`).not.toBeNull()
      expect(m![1].includes('randomUUID()'), `${name}() mints no UUID of its own`).toBe(true)
    }
  })
})

describe('[extr-15-12] the freshened DOCX is still a readable DOCX', () => {
  // The ONE thing about EXTR15-E2E-03 that is checkable without a deployment. A PDF takes a
  // trailing comment; a zip cannot, so uniqueGoldenDocxBytes moves the content hash through the
  // archive's own end-of-central-directory comment instead. If that recipe were wrong the
  // deployed case would fail as "the sidecar cannot read DOCX", which is a different and much
  // more expensive conclusion than "the e2e helper corrupts the fixture".
  const FIXTURE = join(E2E_ROOT, 'fixtures/documents/golden_invoice.docx')
  const raw = readFileSync(FIXTURE)

  it('the committed fixture is byte-identical to the Go golden it borrows', () => {
    const go = readFileSync(join(REPO_ROOT, 'internal/extraction/testdata/invoice.docx'))
    expect(Buffer.compare(raw, go), 'e2e/fixtures/documents/golden_invoice.docx has drifted from internal/extraction/testdata/invoice.docx').toBe(0)
  })

  it('its end-of-central-directory record is where the freshening writes', () => {
    expect(raw.subarray(raw.length - 22, raw.length - 18).toString('hex'), 'the last 22 bytes are not an EOCD record').toBe('504b0506')
    expect(raw.readUInt16LE(raw.length - 2), 'the fixture already carries a zip comment -- the recipe would overwrite its length').toBe(0)
  })

  it('a freshened copy still unzips and still carries the golden\'s printed fields', () => {
    const comment = Buffer.from(`e2e-${crypto.randomUUID()}`, 'utf8')
    const out = Buffer.concat([raw, comment])
    out.writeUInt16LE(comment.length, raw.length - 2)

    const entries = unzipSync(new Uint8Array(out))
    expect(Object.keys(entries).length, 'the freshened archive lost members').toBeGreaterThan(10)
    const xml = Buffer.from(entries['word/document.xml']).toString('utf8')
    // The three values corpus_wired_db_test.go's golden resolves, as they are PRINTED in the
    // document -- so a fixture regenerated with different content reds here, not on the gate.
    expect(xml, 'the freshened DOCX no longer prints the golden invoice number').toContain('ASC-2026-0919')
    expect(xml, 'the freshened DOCX no longer prints the golden issue date').toContain('14 Aug 2026')
    expect(xml, 'the freshened DOCX no longer prints the golden total').toContain('4,300.00')
  })

  it('the spec\'s helper writes the comment length into that same field', () => {
    const m = /function uniqueGoldenDocxBytes\(\): Buffer \{([\s\S]*?)\n\}/.exec(source)
    expect(m, 'uniqueGoldenDocxBytes() is gone from import-wizard.spec.ts').not.toBeNull()
    expect(m![1], 'the helper no longer writes the EOCD comment length').toContain('writeUInt16LE(comment.length, GOLDEN_INVOICE_DOCX.length - 2)')
  })
})

describe('[extr-15-12] the deployed dead-letter literals track their sole owner', () => {
  // documentRun.ts's deadLetterRefusal owns every terminal sentence (EXTR-15-04). The e2e spec
  // cannot import it -- e2e/ has no dependency on frontend/app -- so it pins two literals and
  // this reads them back out of the owner. documentRun.test.ts's TS15-10b covers the shorter
  // DEAD_LETTER_NEEDLE the same way, and discriminates it against all seven kinds.
  const OWNER = join(REPO_ROOT, 'frontend/app/src/lib/documentRun.ts')
  const owner = readFileSync(OWNER, 'utf8')

  function literalOf(name: string): string {
    const m = new RegExp(`const ${name} =\\s*\\n?\\s*'([^']+)'`).exec(source)
    expect(m, `${name} is gone from import-wizard.spec.ts`).not.toBeNull()
    return (m as RegExpExecArray)[1]
  }

  it('DEAD_LETTER_SENTENCE is the pages_not_rendered return, byte for byte', () => {
    const arm = /case 'pages_not_rendered':[\s\S]*?return '([^']+)'/.exec(owner)
    expect(arm, "deadLetterRefusal no longer has a 'pages_not_rendered' arm returning a literal").not.toBeNull()
    expect(literalOf('DEAD_LETTER_SENTENCE')).toBe((arm as RegExpExecArray)[1])
  })

  it('GENERIC_FAILURE_OPENING opens the kind-less arm, and no other', () => {
    const opening = literalOf('GENERIC_FAILURE_OPENING')
    const fallback = /default:[\s\S]*?return `([^`]+)`/.exec(owner)
    expect(fallback, 'deadLetterRefusal no longer has a default arm returning a template literal').not.toBeNull()
    expect((fallback as RegExpExecArray)[1].startsWith(opening), 'the default arm no longer opens with this text').toBe(true)

    // Discrimination: asserting its ABSENCE proves the kind reached the render only if no
    // OTHER arm contains it. Six named arms, matched over the whole switch.
    const arms = [...owner.matchAll(/case '(\w+)':[\s\S]*?return [`']([^`']+)[`']/g)]
    expect(arms.length, 'the named arms of deadLetterRefusal are no longer readable').toBe(6)
    const also = arms.filter(([, , sentence]) => sentence.includes(opening)).map(([, kind]) => kind)
    expect(also, `the opening also appears in: ${also.join(', ')}`).toEqual([])
  })
})
