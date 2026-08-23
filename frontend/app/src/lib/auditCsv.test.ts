// RED specs (AUDIT-07-09, Stage 2.5/Mode A) -- pin auditCsv.ts's serializer + toast copy
// before the executor implements the bodies. AUDIT_CSV_HEADER is stubbed empty and every
// function throws `new Error('not implemented')`, so every spec below fails on the target
// assertion or that thrown error -- never on a missing module.
//
// FORBIDDEN is a local, unexported const in envPosture.test.ts; this file's ownership
// boundary forbids adding `export` there while another agent is live on that tree, so the
// list is read back out of the source text instead of imported -- never retyped.

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import type { AuditEvent } from './audit'
import { AUDIT_CSV_HEADER, auditCsv, auditCsvFilename, auditExportToastCopy } from './auditCsv'

let eventCounter = 0
function makeEvent(overrides: Partial<AuditEvent> = {}): AuditEvent {
  eventCounter += 1
  return {
    id: `evt-${eventCounter}`,
    created_at: '2026-08-20T09:15:00Z',
    event: 'invoice.created',
    actor: 'actor-uuid-0001',
    actor_name: 'Ada Obi',
    actor_kind: 'person',
    entity_id: null,
    company_name: null,
    company_scope: 'unattributed',
    payload: null,
    ...overrides,
  }
}

// Extracted, not retyped, from envPosture.test.ts's own FORBIDDEN array literal.
function readForbiddenList(): string[] {
  const path = fileURLToPath(new URL('../envPosture.test.ts', import.meta.url))
  const src = readFileSync(path, 'utf8')
  const block = src.match(/const FORBIDDEN = \[([\s\S]*?)\n\]/)
  if (!block) throw new Error('could not locate the FORBIDDEN block in envPosture.test.ts')
  return [...block[1].matchAll(/'([^']*)'/g)].map((m) => m[1])
}

const FORBIDDEN = readForbiddenList()

describe('AUDIT_CSV_HEADER', () => {
  it('auditCsv_headerIsTheNineColumns: the header is the nine columns, in order', () => {
    const expected = [
      'When', 'Who', 'Actor kind', 'Actor id', 'What',
      'Event identifier', 'Company', 'Company scope', 'Event id',
    ]

    expect(AUDIT_CSV_HEADER, 'AUDIT_CSV_HEADER must equal the declared nine-column list, in order').toEqual(expected)
  })
})

describe('auditCsv', () => {
  it('auditCsv_oneLinePerEvent: three events serialize to four lines including the header', () => {
    const events = [makeEvent(), makeEvent(), makeEvent()]

    const csv = auditCsv(events)
    const lines = csv.split('\n')

    expect(lines.length, 'expected the header line plus one line per event (4 total)').toBe(4)
  })

  it('auditCsv_quotesCommasQuotesAndNewlines: a comma, a double quote and a newline are RFC-4180 escaped', () => {
    const event = makeEvent({ actor_name: 'Okafor, "Chi"\nJr' })

    const csv = auditCsv([event])

    expect(csv.length, 'auditCsv produced an empty string').toBeGreaterThan(0)
    expect(
      csv,
      'the Who cell must be quoted, with inner double quotes doubled and the newline preserved literally',
    ).toContain('"Okafor, ""Chi""\nJr"')
  })

  it('auditCsv_companyMirrorsTheRowRule: Company mirrors AuditRow.tsx\'s company_scope rule', () => {
    const events = [
      makeEvent({ id: 'evt-company', company_scope: 'company', company_name: 'Honeywell Group' }),
      makeEvent({ id: 'evt-workspace', company_scope: 'workspace', company_name: null }),
      makeEvent({ id: 'evt-unattributed', company_scope: 'unattributed', company_name: null }),
    ]

    const csv = auditCsv(events)

    expect(csv.length, 'auditCsv produced an empty string').toBeGreaterThan(0)
    expect(csv, 'company_scope "company" must render the company name').toContain('Honeywell Group')
    expect(csv, 'company_scope "workspace" must render the literal Workspace').toContain('Workspace')
    expect(csv, 'company_scope "unattributed" must render the em dash').toContain('—')
  })

  it('auditCsv_noPayloadColumn: the event still serializes, but its payload never leaks in', () => {
    const event = makeEvent({ id: 'evt-sentinel', actor_name: 'Ada Obi', payload: { note: 'PAYLOAD-SENTINEL-9f2' } })

    const csv = auditCsv([event])
    const lines = csv.split('\n').filter((l) => l.length > 0)

    // Positive floor first: prove the event was actually serialised before trusting the
    // absence check below.
    expect(lines.length, 'expected the header line plus exactly one data line').toBe(2)
    expect(lines[1], 'the data line must carry this event\'s actor_name').toContain('Ada Obi')
    expect(lines[1], 'the data line must carry this event\'s id').toContain('evt-sentinel')

    expect(csv, 'the payload sentinel leaked into the CSV').not.toContain('PAYLOAD-SENTINEL-9f2')
    expect(csv, 'raw JSON syntax ("{") leaked into the CSV').not.toContain('{')
  })

  it('auditCsv_whenIsAbsoluteIso: When is the created_at ISO string verbatim, never relative', () => {
    const event = makeEvent({ created_at: '2026-08-20T09:15:00Z' })

    const csv = auditCsv([event])

    expect(csv.length, 'auditCsv produced an empty string').toBeGreaterThan(0)
    expect(csv, 'the exact ISO created_at must appear verbatim').toContain('2026-08-20T09:15:00Z')
    expect(csv.toLowerCase(), 'no relative time phrasing ("ago") may appear').not.toContain('ago')
  })
})

describe('auditCsvFilename', () => {
  it('auditCsv_filenameIsDated: the filename embeds the export date as audit-log-YYYY-MM-DD.csv', () => {
    const filename = auditCsvFilename(new Date('2026-08-23T10:00:00Z'))

    expect(filename, 'filename must be the dated audit-log CSV name').toBe('audit-log-2026-08-23.csv')
  })
})

describe('auditExportToastCopy', () => {
  it('auditToast_namesFileRowsAndSize: the copy names the filename, row count and a human-readable size', () => {
    const copy = auditExportToastCopy({ rows: 412, bytes: 58211, filename: 'x.csv', truncated: false, cap: 2000 })

    expect(copy.length, 'auditExportToastCopy produced an empty string').toBeGreaterThan(0)
    expect(copy, 'must name the filename').toContain('x.csv')
    expect(copy, 'must name the row count').toContain('412')
    expect(copy, 'must contain a human-readable size in KB or MB').toMatch(/\d+(\.\d+)?\s?(KB|MB)/i)
  })

  it('auditToast_statesWhatIsNotInIt: the copy states the export excludes attachments, payloads and invoices', () => {
    const copy = auditExportToastCopy({ rows: 10, bytes: 1000, filename: 'a.csv', truncated: false, cap: 2000 })

    expect(copy.length, 'auditExportToastCopy produced an empty string').toBeGreaterThan(0)
    expect(copy, 'must state the exact exclusion sentence').toContain('No attachments, no payloads, no invoices.')
  })

  it('auditToast_statesTheCapWhenTruncated: a truncated export names the count and the cap in one sentence', () => {
    const copy = auditExportToastCopy({ rows: 2000, bytes: 500_000, filename: 'a.csv', truncated: true, cap: 2000 })
    const sentences = copy.split(/\.\s+/).filter((s) => s.length > 0)

    expect(sentences.length, 'auditExportToastCopy produced no sentences to check').toBeGreaterThan(0)

    const capSentence = sentences.find((s) => s.includes('2000'))

    expect(capSentence, 'no sentence in the toast copy names the 2000-row count').toBeDefined()
    expect(capSentence, 'the sentence naming the count must also describe the cap').toMatch(/cap|capped|limit|maximum|most/i)
  })

  it('auditToast_carriesNoForbiddenClaim: a normal export never carries an envPosture FORBIDDEN claim', () => {
    // Floor 1: the imported list is not accidentally empty.
    expect(FORBIDDEN.length, 'the FORBIDDEN list read from envPosture.test.ts was empty').toBeGreaterThan(0)

    const copy = auditExportToastCopy({ rows: 10, bytes: 1000, filename: 'a.csv', truncated: false, cap: 2000 })

    // Floor 2: the copy under test is real before scanning it for absence.
    expect(copy.length, 'auditExportToastCopy produced an empty string').toBeGreaterThan(0)
    expect(copy, 'the normal-export toast must state what it excludes').toContain('No attachments')

    const lower = copy.toLowerCase()
    for (const phrase of FORBIDDEN) {
      expect(lower, `toast copy contains forbidden phrase "${phrase}"`).not.toContain(phrase.toLowerCase())
    }
  })

  it('auditToast_oneRowExport: a one-row export names the count 1 and keeps the exclusion sentence intact', () => {
    const copy = auditExportToastCopy({ rows: 1, bytes: 900, filename: 'one.csv', truncated: false, cap: 2000 })

    expect(copy.length, 'auditExportToastCopy produced an empty string').toBeGreaterThan(0)
    expect(copy, 'the row count must appear as the standalone token "1"').toMatch(/\b1\b/)
    expect(copy, 'a one-row export must never report 0 rows').not.toMatch(/\b0\b/)
    expect(copy, 'a one-row export must read "1 row", matching auditView.ts\'s pluralisation').toContain('Exported 1 row to')
    expect(copy, 'must still carry the exclusion sentence at the one-row boundary').toContain('No attachments, no payloads, no invoices.')
  })
})
