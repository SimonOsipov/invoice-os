// The review-copy census (EXTR-15-08, task-834, Mode A / test-first).
//
// The review screens speak spreadsheet — "rows read", "unreadable rows", "One row is one
// line item". A PDF has no rows, so each of those sentences is wrong for a document.
// EXTR-15-09 rewrites all 31 of them behind batchUnit/runUnit. This file is the
// instrument that proves that sweep landed in full.
//
// WHY A POSITIVE CENSUS AND NOT A GREP FOR THE ABSENCE OF "rows".
// A grep asserting an absence returns zero hits when the grep itself breaks — a moved
// file, a renamed export, a typo'd path — and zero hits reads exactly like a clean repo.
// This project has shipped that mistake before. So every row below asserts a PRESENCE,
// and CEN-3/CEN-4 assert that the files being scanned are the real, populated ones.
//
// All four census specs are GREEN since EXTR-15-09. If CEN-2 fails, the table is not the
// failing thing and must not be rewritten to make it pass: either a document branch has
// gone missing from the source, or a spreadsheet one has been edited.
//
// THE DOCUMENT LITERALS ARE THE SHIPPED WORDING, settled by EXTR-15-09. FIVE carry an AC
// of their own: B8, A3, A4, A6 and A7 read "already in the register" for the document
// unit, where the spreadsheet unit keeps "already in your ledger". Those five are marked
// `AC` below. SIX were settled by 09's architecture pass: R4, R5 and R6 (D1, the middle
// CSV column is dropped, not renamed), U4 and A5 (D4, an em dash, not the word
// `Document`) and C1 (D2, one unbranched paragraph carrying both grains).
//
// AC-3 forbids "row"/"rows" as an ENGLISH NOUN in a document branch, not the identifier:
// `{rows.length}` and `${batch.rows_total}` stay, because they name a variable. SW-3 is
// the guard and strips every `{...}` / `${...}` expression before it applies the regex.
//
// BOUNDARY. The census pins the 31 sites it knows about, measured on this branch. It
// CANNOT catch a thirty-second unit-ambiguous string added later, nor a seventh file. Its
// floor is a ratchet against silent deletion, not a discovery instrument.
//
// DELIBERATE EXCLUSION, so the absence is a decision and not an oversight:
// lib/reviewBatch.ts:1069 — `${notReady} of the ${rows.length} row/rows on this page
// cannot be sent.` (bulkBarView) is NOT a site. Its "row" already means a TABLE row, not a
// line of an uploaded file: it is scoped by "on this page" (pagination), it counts
// InvoiceRecords, and even in a spreadsheet run one invoice can come from many file rows —
// so the word is exactly as accurate, and as loose, in both units. Every one of the 31
// sites below fails that test: each uses "row" for a line of the source file, which a PDF
// does not have.
//
// ceiling: needles are matched as plain substrings of the SOURCE, not against rendered
// output. A site split across a JSX expression (C1 is, today) can only be pinned up to
// the split point.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

// process.cwd() (= frontend/app under `pnpm --filter @invoice-os/app test`), never
// `new URL(rel, import.meta.url)`: Vite rewrites that into an asset import and kills the
// module with `Denied ID …`. Same idiom as documentRun.test.ts:1143.
function readSrc(rel: string): string {
  return readFileSync(path.join(process.cwd(), rel), 'utf8')
}

const REVIEW_BATCH = 'src/lib/reviewBatch.ts'
const REVIEW_BATCH_TSX = 'src/components/ReviewBatch.tsx'
const UNREADABLE_TAB = 'src/components/ReviewUnreadableTab.tsx'
const ALREADY_IMPORTED_TAB = 'src/components/ReviewAlreadyImportedTab.tsx'
const IMPORT_PROGRESS = 'src/components/ImportProgress.tsx'
const CREATE_UPLOAD = 'src/components/CreateUpload.tsx'

const FILES = [REVIEW_BATCH, REVIEW_BATCH_TSX, UNREADABLE_TAB, ALREADY_IMPORTED_TAB, IMPORT_PROGRESS, CREATE_UPLOAD]

interface Site {
  id: string
  file: string
  spreadsheetLiteral: string
  documentLiteral: string
}

// Each spreadsheet literal was verified to occur EXACTLY ONCE in its file on this branch.
// Several are deliberately longer than the sentence they name, because the short form is
// a substring of a sibling: R4 inside R5, B4 inside B7, A3 inside A7.
const SITES: Site[] = [
  // --- lib/reviewBatch.ts (7) ---
  {
    id: 'R1',
    file: REVIEW_BATCH,
    spreadsheetLiteral: '${batch.rows_total} ROWS READ · SERVER VERDICT · RULE SET ',
    documentLiteral: '${batch.rows_total} DOCUMENTS READ · SERVER VERDICT · RULE SET ',
  },
  {
    id: 'R2',
    file: REVIEW_BATCH,
    spreadsheetLiteral: '${rowsTotal} ROWS READ · SERVER VERDICT · RULE SET ',
    documentLiteral: '${rowsTotal} DOCUMENTS READ · SERVER VERDICT · RULE SET ',
  },
  {
    id: 'R3',
    file: REVIEW_BATCH,
    spreadsheetLiteral: 'Unreadable rows (${counts.unreadable})',
    documentLiteral: 'Unreadable documents (${counts.unreadable})',
  },
  {
    id: 'R4',
    file: REVIEW_BATCH,
    spreadsheetLiteral: "'Row,Field,Why it could not be read'",
    documentLiteral: "'Field,Why it could not be read'",
  },
  {
    // 09 D1: the middle column is DROPPED for a document, not renamed to `Document`. In a
    // document run File already holds the name, so a Document column would repeat it on
    // every line while the row number it replaced is always empty
    // (internal/importer/document.go omits Row on all three RowError paths). R6 is the
    // same decision. SW-11 pins that the CELLS follow the header, not just the header.
    id: 'R5',
    file: REVIEW_BATCH,
    spreadsheetLiteral: "'File,Row,Field,Why it could not be read'",
    documentLiteral: "'File,Field,Why it could not be read'",
  },
  {
    id: 'R6', // Same dropped middle column as R5 — see that row.
    file: REVIEW_BATCH,
    spreadsheetLiteral: "'File,Row,Invoice id'",
    documentLiteral: "'File,Invoice id'",
  },
  {
    id: 'R7',
    file: REVIEW_BATCH,
    spreadsheetLiteral: '0 of ${batch.rows_total} rows produced an invoice',
    documentLiteral: '0 of ${batch.rows_total} documents produced an invoice',
  },

  // --- components/ReviewBatch.tsx (8) ---
  {
    id: 'B1',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: 'Built from {rowsValidTotal} rows. Every one of these exists in the ledger',
    documentLiteral: 'Built from {rowsValidTotal} documents. Every one of these exists in the ledger',
  },
  {
    id: 'B2',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: '${tiles.frozen.unreadable} unreadable rows',
    documentLiteral: '${tiles.frozen.unreadable} unreadable documents',
  },
  {
    id: 'B3',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: "'Every row in the file could be read.'",
    documentLiteral: "'Every document in this import could be read.'",
  },
  {
    id: 'B4',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: 'it held no data rows — a spreadsheet with only a header row, for example.',
    documentLiteral: 'nothing invoice-shaped could be found in it — a scan too poor to read, for example.',
  },
  {
    id: 'B5',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: 'caption="Rows stored"',
    documentLiteral: 'caption="Documents stored"',
  },
  {
    id: 'B6',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: 'caption="Rows quarantined"',
    documentLiteral: 'caption="Documents quarantined"',
  },
  {
    id: 'B7',
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: 'a file held no data rows — a spreadsheet with only a header row, for example.',
    documentLiteral: 'nothing invoice-shaped could be found in a file — a scan too poor to read, for example.',
  },
  // B8/B9/B10 are ONE <Tile> (ReviewBatch.tsx:317-319): B8 is its `value`, B9 and B10 are
  // the two arms of its `caption` ternary. Rewriting B8 alone ships "already in the
  // register" directly above "already in your ledger" — which is why all three are pinned
  // and why they sit together here.
  //
  // 09: do NOT collapse them into one sentence. The shipped comment at ReviewBatch.tsx:315
  // states the value counts ROWS and the caption counts INVOICES, and BUG08-BATCH-8 pins
  // both — they are two different numbers, not one fact said twice. That is also why B10's
  // document form keeps the noun "invoices": only "your ledger" becomes "the register".
  {
    id: 'B8', // AC: "already in the register" — the tile's value
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: '${tiles.frozen.alreadyImported} already imported',
    documentLiteral: '${tiles.frozen.alreadyImported} already in the register',
  },
  {
    id: 'B9', // AC: "already in the register" — the caption, zero arm
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: "'Nothing in this file was already in your ledger.'",
    documentLiteral: "'Nothing in this import was already in the register.'",
  },
  {
    id: 'B10', // AC: "already in the register" — the caption, non-zero arm
    file: REVIEW_BATCH_TSX,
    spreadsheetLiteral: '${tiles.frozen.alreadyImportedInvoices} invoices already in your ledger. Nothing to fix.',
    documentLiteral: '${tiles.frozen.alreadyImportedInvoices} invoices already in the register. Nothing to fix.',
  },

  // --- components/ReviewUnreadableTab.tsx (5) ---
  {
    id: 'U1',
    file: UNREADABLE_TAB,
    spreadsheetLiteral: "unreadable-rows-${batchIds.join('-')}.csv",
    documentLiteral: "unreadable-documents-${batchIds.join('-')}.csv",
  },
  {
    id: 'U2',
    file: UNREADABLE_TAB,
    spreadsheetLiteral: '{rows.length} rows never became invoices',
    documentLiteral: '{rows.length} documents never became invoices',
  },
  {
    id: 'U3',
    file: UNREADABLE_TAB,
    spreadsheetLiteral:
      'The importer could not read them, so no rule was ever run against them and nothing was stored. They cannot be fixed here: correct the rows in your file and import again.',
    documentLiteral:
      'The extractor could not read them, so no rule was ever run against them and nothing was stored. They cannot be fixed here: replace the documents and import again.',
  },
  {
    id: 'U4',
    file: UNREADABLE_TAB,
    spreadsheetLiteral: '<span>Row</span>',
    // 09 D4: an em dash, matching the cells beside it, which already render one for a
    // null row. Two WHOLE spans so both literals survive this census and the grid keeps
    // exactly one child per column (AC-6; SW-6 is the parity oracle).
    documentLiteral: '<span>—</span>',
  },
  {
    id: 'U5',
    file: UNREADABLE_TAB,
    spreadsheetLiteral: '{rows.length} of {rowsTotal} rows. The invoices that did import are unaffected.',
    documentLiteral: '{rows.length} of {rowsTotal} documents. The invoices that did import are unaffected.',
  },

  // --- components/ReviewAlreadyImportedTab.tsx (7) ---
  {
    id: 'A1',
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral: "'The matching invoice was not recorded for this row.'",
    documentLiteral: "'The matching invoice was not recorded for this document.'",
  },
  {
    id: 'A2',
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral: "already-imported-rows-${batchIds.join('-')}.csv",
    documentLiteral: "already-imported-documents-${batchIds.join('-')}.csv",
  },
  {
    id: 'A3', // AC: "already in the register"
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral: '{rows.length} rows were already in your ledger',
    documentLiteral: '{rows.length} documents were already in the register',
  },
  {
    // AC: "already in the register". NOTE the spreadsheet sentence contains no "already
    // in your ledger" at all, so this row is a REWRITE, not a substitution.
    id: 'A4',
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral:
      'These rows match invoices this workspace already holds, so the import had nothing new to add. Nothing is wrong with them and there is nothing to correct.',
    documentLiteral:
      'These documents are already in the register, so the import had nothing new to add. Nothing is wrong with them and there is nothing to correct.',
  },
  {
    id: 'A5',
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral: '<span>Row</span>',
    // 09 D4: an em dash, matching the cells beside it, which already render one for a
    // null row. Two WHOLE spans so both literals survive this census and the grid keeps
    // exactly one child per column (AC-6; SW-6 is the parity oracle).
    documentLiteral: '<span>—</span>',
  },
  {
    id: 'A6', // AC: "already in the register"
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral: '<span>Invoice already in your ledger</span>',
    documentLiteral: '<span>Invoice already in the register</span>',
  },
  {
    id: 'A7', // AC: "already in the register"
    file: ALREADY_IMPORTED_TAB,
    spreadsheetLiteral: '{rows.length} of {rowsTotal} rows were already in your ledger.',
    documentLiteral: '{rows.length} of {rowsTotal} documents were already in the register.',
  },

  // --- components/ImportProgress.tsx (1) ---
  {
    // "groups" is spreadsheet-only: nothing groups when one document is one invoice.
    id: 'P1',
    file: IMPORT_PROGRESS,
    spreadsheetLiteral: 'the server reads, groups, stores and validates each file in one request',
    documentLiteral: 'the server reads, extracts and validates each document in one request',
  },

  // --- components/CreateUpload.tsx (1) ---
  {
    // 09 D2 settles this one: it is UNBRANCHED. The site renders at pickedFiles.length
    // === 0, where runKindOf answers null and no unit exists yet, so the paragraph states
    // BOTH grains and the document sentence names the extensions rather than the word
    // "spreadsheet" (AC-3 bans that word in a document branch). The shipped spreadsheet
    // needle stops at the `{' '}` split, not at invoice_number. SW-8 pins the ordering.
    id: 'C1',
    file: CREATE_UPLOAD,
    spreadsheetLiteral:
      'The parser extracts buyer details, line items and totals. One row is one line item; rows group into invoices by the column you map to',
    documentLiteral: 'That grain is CSV and XLSX only: in a PDF or a DOCX, one document is one invoice.',
  },
]

function occurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1
}

describe('review-copy census (EXTR-15-08)', () => {
  // Floor and ceiling in one line: a deleted row and a smuggled-in row both fail here.
  it('CEN-1 (GREEN on landing): the census names exactly 31 sites', () => {
    expect(SITES).toHaveLength(31)
    expect(new Set(SITES.map((s) => s.id)).size).toBe(31)
  })

  // Both halves must be present at once — the unit is a branch, not a replacement.
  // `=== 1`, never `>= 1`: a duplicated sentence is a second site to keep in step, and the
  // census would not see it.
  it('CEN-2 (GREEN since EXTR-15-09): both literals appear exactly once in their file', () => {
    const sources = new Map(FILES.map((f) => [f, readSrc(f)]))
    const wrong: string[] = []

    for (const site of SITES) {
      const src = sources.get(site.file)!
      const s = occurrences(src, site.spreadsheetLiteral)
      const d = occurrences(src, site.documentLiteral)
      if (s !== 1) wrong.push(`${site.id} spreadsheet x${s} in ${site.file}`)
      if (d !== 1) wrong.push(`${site.id} document x${d} in ${site.file}`)
    }

    expect(wrong).toEqual([])
  })

  // Controls. CEN-2 above reads six files by path; if one moved or was emptied, every
  // needle would be absent and CEN-2's failure would say "09 has not landed" when the
  // truth is "the instrument is broken". These two say which.
  it('CEN-3 (GREEN on landing): the control needles are where the census expects them', () => {
    expect(readSrc(REVIEW_BATCH)).toContain('ROWS READ')
    expect(readSrc(UNREADABLE_TAB)).toContain('Download this list (CSV)')
  })

  it('CEN-4 (GREEN on landing): the six scanned files total at least 2,000 source lines', () => {
    const total = FILES.reduce((sum, f) => sum + readSrc(f).split('\n').length, 0)

    expect(total).toBeGreaterThanOrEqual(2000)
  })

  // The instrument checking itself. CEN-2 counts occurrences by plain substring, so a
  // needle that is a substring of a sibling needle counts the sibling's site too and the
  // `=== 1` reads as a duplicate. Several spreadsheet needles are deliberately longer than
  // the sentence they name for exactly this reason (R4 inside R5, B4 inside B7, A3 inside
  // A7). This spec is what keeps that property true after an edit to the table.
  it('CEN-5 (GREEN on landing): no census needle is a substring of another in the same file', () => {
    const needles = SITES.flatMap((s) => [
      { id: `${s.id}.spreadsheet`, file: s.file, text: s.spreadsheetLiteral },
      { id: `${s.id}.document`, file: s.file, text: s.documentLiteral },
    ])
    const collisions: string[] = []

    for (const a of needles) {
      for (const b of needles) {
        if (a === b || a.file !== b.file) continue
        if (b.text.includes(a.text)) collisions.push(`${a.id} is a substring of ${b.id}`)
      }
    }

    expect(collisions).toEqual([])
  })
})

// --- EXTR-15-09 (task-835, Mode A) ---------------------------------------------------

// AC-3 bans "row"/"rows" as an ENGLISH NOUN from a document branch, never the identifier:
// `{rows.length}` and `${batch.rows_total}` are variable names the branch still reads, and
// `\brows?\b` matches the first of those. Every `{...}` / `${...}` expression is therefore
// removed before the regex runs. `${...}` first, so no orphan `$` survives.
function stripExpressions(literal: string): string {
  return literal.replace(/\$\{[^}]*\}/g, ' ').replace(/\{[^}]*\}/g, ' ')
}

const ROW_NOUN = /\brows?\b/i
const SPREADSHEET_WORD = /spreadsheet/i

describe('EXTR-15-09 SW-3 (AC-3): no document branch speaks spreadsheet', () => {
  // SW-3 IS NOT A SOURCE ORACLE ON ITS OWN. Two of its three claims are about the census
  // table, which this pass can edit; only the presence claim reaches the shipped file. The
  // pair SW-3 ∧ CEN-2 is the oracle: CEN-2 fails if the source does not carry the literal,
  // SW-3 fails if the literal carries the wrong word. Weakening either one alone lets a
  // document branch that still says "rows" through.
  it('SW-3 (GREEN since EXTR-15-09): every document literal ships, and none says row or spreadsheet', () => {
    const sources = new Map(FILES.map((f) => [f, readSrc(f)]))
    const wrong: string[] = []

    for (const site of SITES) {
      const prose = stripExpressions(site.documentLiteral)
      if (!sources.get(site.file)!.includes(site.documentLiteral)) {
        wrong.push(`${site.id} document branch is not in ${site.file}`)
      }
      if (ROW_NOUN.test(prose)) wrong.push(`${site.id} document branch says "row": ${JSON.stringify(prose)}`)
      if (SPREADSHEET_WORD.test(site.documentLiteral)) {
        wrong.push(`${site.id} document branch says "spreadsheet": ${JSON.stringify(site.documentLiteral)}`)
      }
    }

    expect(wrong).toEqual([])
  })

  // The control for the two absence claims above. Run the SAME regexes through the SAME
  // stripper over the spreadsheet column, where both words are everywhere: if this floor
  // is met, a clean document column means the words are gone, not that the scan is broken.
  it('SW-3 control (GREEN on landing): the same regexes fire heavily on the spreadsheet column', () => {
    const rowHits = SITES.filter((s) => ROW_NOUN.test(stripExpressions(s.spreadsheetLiteral)))
    const spreadsheetHits = SITES.filter((s) => SPREADSHEET_WORD.test(s.spreadsheetLiteral))

    expect(rowHits.length, 'the row-noun regex found nothing to match; SW-3 would pass vacuously').toBeGreaterThanOrEqual(20)
    expect(spreadsheetHits.length, 'the spreadsheet regex found nothing to match').toBeGreaterThanOrEqual(2)
  })
})

describe('EXTR-15-09 SW-10 (AC-9): the run unit is derived once and handed down', () => {
  // A child that re-derives is a second source of truth for one fact, and the two can
  // disagree the moment either derivation changes. ReviewBatch.tsx owns the call; the tabs
  // receive it. Scanning source, not behaviour: a second derivation returning the same
  // answer is invisible to any render.
  it('SW-10 (GREEN since EXTR-15-09): runUnit is called once, in ReviewBatch.tsx, and passed to both tabs', () => {
    const parent = readSrc(REVIEW_BATCH_TSX)
    const unreadable = readSrc(UNREADABLE_TAB)
    const alreadyImported = readSrc(ALREADY_IMPORTED_TAB)

    // Controls: these three needles exist today, so a moved or emptied file cannot satisfy
    // the two zero-count claims below by scanning nothing.
    expect(parent, 'ReviewBatch.tsx was not read').toContain('reviewHeaderAll(')
    expect(unreadable, 'ReviewUnreadableTab.tsx was not read').toContain('export function ReviewUnreadableTab')
    expect(alreadyImported, 'ReviewAlreadyImportedTab.tsx was not read').toContain('export function ReviewAlreadyImportedTab')

    expect(occurrences(parent, 'runUnit(')).toBe(1)
    expect(occurrences(unreadable, 'runUnit(')).toBe(0)
    expect(occurrences(alreadyImported, 'runUnit(')).toBe(0)
    expect(occurrences(unreadable, 'batchUnit(')).toBe(0)
    expect(occurrences(alreadyImported, 'batchUnit(')).toBe(0)
    // RejectedFile and RejectedRun live in ReviewBatch.tsx and take `unit` as a prop. A
    // second derivation there would be `batchUnit`, not `runUnit` — and for RejectedFile,
    // over its ONE batch, it would answer differently from runUnit's all-must-agree rule.
    expect(occurrences(parent, 'batchUnit(')).toBe(0)

    // ...and the DERIVED value reaches all four children, or "called once" is satisfied by a
    // call whose result is dropped. `unit={unit}`, never a bare `unit=`: a hard-coded
    // `unit={'spreadsheet'}` contains `unit=` and would pass, leaving SW-14/SW-17 as the
    // only oracles for a child wired to a constant.
    for (const tag of ['<ReviewUnreadableTab', '<ReviewAlreadyImportedTab', '<RejectedFile', '<RejectedRun']) {
      const start = parent.indexOf(tag)
      expect(start, `${tag} is not rendered by ReviewBatch.tsx`).toBeGreaterThan(-1)
      const element = parent.slice(start, parent.indexOf('/>', start))
      expect(element, `${tag} must be handed the derived run unit`).toContain('unit={unit}')
    }
  })
})
