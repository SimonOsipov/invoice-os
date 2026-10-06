// The checks the import-wizard specs name: the two fixture-freshness scans, the freshened-DOCX
// unzip, the dead-letter sentence read-back and the EXTR36 declaration order.
import { describe, expect, it } from 'vitest'
import { unzipSync } from 'fflate'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { stripComments } from '@invoice-os/api-client/strip-comments'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const E2E_ROOT = dirname(TOPOLOGY_DIR)
const REPO_ROOT = dirname(E2E_ROOT)
const SPEC_1 = 'import-wizard.spec.ts'
const SPEC_2 = 'import-wizard-2.spec.ts'
const source1 = readFileSync(join(TOPOLOGY_DIR, SPEC_1), 'utf8')
const source = readFileSync(join(TOPOLOGY_DIR, SPEC_2), 'utf8')

const BLOCK_START = 'EXTR-18-07 · the deployed proof'
const blockStart = source.indexOf(BLOCK_START)
if (blockStart === -1) throw new Error(`start marker not found in ${SPEC_2}: ${JSON.stringify(BLOCK_START)}`)
const block = source.slice(blockStart)

const EXTR35_E2E_01 = 'EXTR35-E2E-01 (AC-8): the letter-spaced register files its invoice instead of quarantining'
const EXTR36_E2E_02 = 'EXTR36-E2E-02 (AC-3): a typed correction on a Chrome print teaches its twin'
const EXTR36_E2E_01 = 'EXTR36-E2E-01 (AC-1/AC-2): a Chrome-shaped register anchors its printed labels'

// EXTR-15-12 (task-836). The EXTR-15 deployed-proof span runs from its own marker to
// EXTR-18-07's, and is scanned SEPARATELY: two of its documents are DOCX, which no
// unique*PdfBytes() helper mints, so it needs its own allowlist. The rest of
// import-wizard-2.spec.ts is CSV probes with `Buffer.from(...)` bodies and is not scanned at
// all -- freshness only matters where an upload is polled to a settled extraction.
const EXTR15_BLOCK_START = 'EXTR-15 · the deployed proof'
const extr15Start = source.indexOf(EXTR15_BLOCK_START)
if (extr15Start === -1)
  throw new Error(`start marker not found in ${SPEC_2}: ${JSON.stringify(EXTR15_BLOCK_START)}`)
if (extr15Start >= blockStart)
  throw new Error('the EXTR-15 marker no longer precedes the EXTR-18-07 marker -- the span it delimits is empty')
const extr15Block = source.slice(extr15Start, blockStart)

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
})

// --- EXTR-15-12 (task-836): the EXTR-15 deployed-proof span --------------------------------
//
// The failure mode the EXTR-18-07 scan above covers, over the span that carries the EXTR-15
// cases: a fixture reaching setInputFiles unfreshened (which the per-tenant content hash reuses
// and the PERMANENT per-document enqueue key then skips, so the poll settles on a PREVIOUS
// run's job and stays green while extraction is broken).

describe('[extr-15-12] every EXTR-15 fixture upload goes through a fresh-per-call helper', () => {
  // The helpers that mint fresh bytes on every call. A raw module-scope constant reaching
  // setInputFiles is the defect; `Buffer`, runDocuments' own type annotation, is not.
  const FRESH_HELPERS = [
    'uniqueScannedPdfBytes()',
    'uniqueDensePdfBytes()',
    'uniqueGarbageBytes()',
    'uniqueGoldenDocxBytes()',
    'uniqueEmptyDocxBytes()',
    'uniquePdfBytes()',
  ]
  const FORWARDERS = new Set(['Buffer', 'file.buffer'])
  // Comment-stripped: this span's own header documents the rule below in prose, and the
  // prose would otherwise read as an offending call site.
  const bufferArgs = [...stripComments(extr15Block).matchAll(/buffer:\s*([^,}\n]+)/g)].map((m) => m[1].trim())

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
})

describe('[extr-15-12] the freshened DOCX is still a readable DOCX', () => {
  // The ONE thing about EXTR15-E2E-03 that is checkable without a deployment. A PDF takes a
  // trailing comment; a zip cannot, so uniqueGoldenDocxBytes moves the content hash through the
  // archive's own end-of-central-directory comment instead. If that recipe were wrong the
  // deployed case would fail as "the sidecar cannot read DOCX", which is a different and much
  // more expensive conclusion than "the e2e helper corrupts the fixture".
  const FIXTURE = join(E2E_ROOT, 'fixtures/documents/golden_invoice.docx')
  const raw = readFileSync(FIXTURE)

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
})

describe('[extr-15-12] the deployed dead-letter literals track their sole owner', () => {
  // documentRun.ts's deadLetterRefusal owns every terminal sentence (EXTR-15-04). The e2e spec
  // cannot import it -- e2e/ has no dependency on frontend/app -- so this reads its copy back out
  // of the owner.
  const OWNER = join(REPO_ROOT, 'frontend/app/src/lib/documentRun.ts')
  const owner = readFileSync(OWNER, 'utf8')

  function literalOf(name: string): string {
    const m = new RegExp(`const ${name} =\\s*\\n?\\s*'([^']+)'`).exec(source1)
    expect(m, `${name} is gone from ${SPEC_1}`).not.toBeNull()
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

describe('[extr-36] declaration order is load-bearing', () => {
  // The three register fixtures share one fingerprint and all run PERSONAS.A, so a rule taught
  // by an earlier test is live for every later upload. Each failure message below names it.

  it('EXTR36-E2E-02 is declared after EXTR35-E2E-01', () => {
    const at35 = source.indexOf(EXTR35_E2E_01)
    const at02 = source.indexOf(EXTR36_E2E_02)
    expect(at35, `test name not found in ${SPEC_2}: ${JSON.stringify(EXTR35_E2E_01)}`).toBeGreaterThan(-1)
    expect(at02, `test name not found in ${SPEC_2}: ${JSON.stringify(EXTR36_E2E_02)}`).toBeGreaterThan(-1)
    expect(
      at02,
      "EXTR36-E2E-02 sits above EXTR35-E2E-01 -- advisory_register.pdf shares chrome_register.pdf's fingerprint v3:d89450d1..., and both run as PERSONAS.A, so a rule EXTR35-E2E-01 could write would already be live for EXTR36-E2E-02's upload",
    ).toBeGreaterThan(at35)
  })

  it('EXTR36-E2E-01 is declared after EXTR36-E2E-02', () => {
    const at02 = source.indexOf(EXTR36_E2E_02)
    const at01 = source.indexOf(EXTR36_E2E_01)
    expect(at02, `test name not found in ${SPEC_2}: ${JSON.stringify(EXTR36_E2E_02)}`).toBeGreaterThan(-1)
    expect(at01, `test name not found in ${SPEC_2}: ${JSON.stringify(EXTR36_E2E_01)}`).toBeGreaterThan(-1)
    expect(
      at01,
      "EXTR36-E2E-01 sits above EXTR36-E2E-02 -- both upload documents sharing fingerprint v3:d89450d1..., both run as PERSONAS.A, so the buyer_name rule EXTR36-E2E-01 teaches would already be live when EXTR36-E2E-02 uploads its supposedly untaught twin",
    ).toBeGreaterThan(at02)
  })
})
