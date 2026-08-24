// AUDIT-08-02 (task-665, EB-02-1..EB-02-15) -- drawer copy, the pre-build refusals, the
// manifest rows and the period wording. Authored RED against a stub, per Stage 2.5.
// Subtask 01's lesson: a scan is only worth its haystack. EB-02-1/2 therefore scan the
// VALUES of EVIDENCE_COPY unioned with every exported function's output, never file text --
// evidenceBundleView.ts's own header contains "signed" while explaining D-08-01.

/// <reference types="node" />

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { auditExportToastCopy } from './auditCsv'
import { AUDIT_COPY } from './auditView'
import {
  bundleRequestFor,
  type BundleCounts,
  type BundlePeriod,
  type BundleRequest,
  type EvidenceBundlePreview,
} from './evidenceBundle'
import {
  BUNDLE_INVOICE_LIMIT,
  EVIDENCE_COPY,
  bundleBasisLine,
  bundleBlockFor,
  bundleBlockReason,
  bundleManifestLines,
  bundlePeriodLabel,
  bundleReadyLine,
  bundleToastCopy,
  type BundleBlock,
  type BundleToastInput,
} from './evidenceBundleView'
import { formatBytes } from './sourceDocument'

const REPO_ROOT = resolve(__dirname, '../../../..')
const ARCHIVE_GO = resolve(REPO_ROOT, 'internal/archive/archive.go')
const ASSEMBLE_GO = resolve(REPO_ROOT, 'internal/archive/assemble.go')
const EXCHANGE_GO = resolve(REPO_ROOT, 'internal/archive/exchange.go')
const MANIFEST_GO = resolve(REPO_ROOT, 'internal/archive/manifest.go')

// U+00B7, byte-identical to AUDIT_COPY.exportCaption. Not U+2022, not a hyphen.
const MID = '·'
const MANIFEST_NEEDLE = `MANIFEST ${MID} SHA-256`
const EN_DASH = '–'

const SIGNED_RE = /signed/i
// Not /sign/i -- rowManifest deliberately carries "signature" (D-08-01's disclaimer).
const ESTIMATE_RE = /estimated|approx|up to/i

const ENTITY_ID = '11111111-1111-1111-1111-111111111111'
const NOW = new Date('2026-08-24T10:15:30.123Z')

const PERIOD: BundlePeriod = {
  from: '2026-07-01T00:00:00Z',
  to: '2026-07-31T23:59:59Z',
  bounds: 'inclusive',
  basis: 'invoices.created_at',
}

// All five counts. The earlier fixture omitted `submissions`, so that row rendered
// `undefined` and EB-02-4 passed against a broken mapping.
const COUNTS: BundleCounts = {
  invoices: 507,
  status_transitions: 2028,
  submissions: 1204,
  exchange_attempts: 1521,
  body_files: 3042,
}

const PREVIEW: EvidenceBundlePreview = {
  entity: { id: ENTITY_ID, name: 'Honeywell Group', tin: '12345678-0001' },
  period: PERIOD,
  filename: 'ASComply_evidence_Honeywell_Group_20260701_20260731.zip',
  counts: COUNTS,
  over_limit: false,
}

const REQ: BundleRequest = { entityId: ENTITY_ID, from: '2026-07-01T00:00:00.000Z', to: '2026-07-31T23:59:59.999Z' }
const PERIOD_LABEL = `1 July 2026 ${EN_DASH} 31 July 2026`

const TOAST: BundleToastInput = {
  filename: PREVIEW.filename,
  invoices: 1500,
  bytes: 624640,
  company: 'Honeywell Group',
  period: PERIOD_LABEL,
}

const ALL_BLOCK_KINDS: NonNullable<BundleBlock>[] = [
  { kind: 'no-company' },
  { kind: 'no-period' },
  { kind: 'invalid-range' },
  { kind: 'empty', company: PREVIEW.entity.name, period: PERIOD_LABEL },
  { kind: 'over-limit', invoices: 12345, limit: BUNDLE_INVOICE_LIMIT },
]

// Typed through Record so the shape gate is a runtime assertion, not a compile error: with
// EVIDENCE_COPY still `{}`, a dotted key access would fail typecheck instead of going red.
function copyValues(): unknown[] {
  return Object.values(EVIDENCE_COPY as Record<string, unknown>)
}

function copy(key: string): string {
  const value = (EVIDENCE_COPY as Record<string, unknown>)[key]
  expect(typeof value, `EVIDENCE_COPY.${key} must be a string`).toBe('string')
  return value as string
}

function matches(re: RegExp, haystack: string[]): string[] {
  return haystack.filter((s) => re.test(s))
}

// Haystack B: every string an exported function PRODUCES. A literal typed into a function
// body is invisible to Object.values, which is exactly how the banned word would ship.
function functionStrings(): Array<[string, string[]]> {
  const sources: Array<[string, string[]]> = [
    ['bundleReadyLine', [bundleReadyLine(0), bundleReadyLine(TOAST.bytes)]],
    ['bundleToastCopy', [bundleToastCopy(TOAST), bundleToastCopy({ ...TOAST, invoices: 1 })]],
    ['bundlePeriodLabel', [bundlePeriodLabel(PERIOD)]],
    ['bundleBasisLine', [bundleBasisLine(PERIOD), bundleBasisLine({ ...PERIOD, basis: 'invoices.issue_date' })]],
    ['bundleBlockReason', ALL_BLOCK_KINDS.map((b) => bundleBlockReason(b)).filter((s): s is string => s != null)],
    [
      'bundleManifestLines',
      bundleManifestLines(PREVIEW).flatMap((l) => (l.value == null ? [l.label] : [l.label, l.value])),
    ],
  ]

  for (const [name, strings] of sources) {
    expect(strings.length, `${name} must contribute at least one string to the scan`).toBeGreaterThanOrEqual(1)
    expect(
      strings.every((s) => typeof s === 'string' && s.length > 0),
      `${name} contributed an empty or non-string entry`,
    ).toBe(true)
  }
  return sources
}

// The four defences EB-02-1 and EB-02-2 share: floor, shape gate, per-source floor and the
// control needle. Each spec adds its own negative control and zero-match assertion.
function buildHaystack(): string[] {
  const a = copyValues()
  // Floor FIRST: a `{}` copy object scans zero strings and matches zero times, which reads
  // exactly like clean copy. 29 keys are planned; 25 is the floor.
  expect(a.length, 'EVIDENCE_COPY must carry its keys').toBeGreaterThanOrEqual(25)
  // Shape gate: a nested object value hides every string inside it from Object.values.
  expect(
    a.filter((v) => typeof v !== 'string'),
    'every EVIDENCE_COPY value must be a string',
  ).toEqual([])

  const sources = functionStrings()
  expect(sources, 'all six string-producing functions must be scanned').toHaveLength(6)

  const haystack = [...(a as string[]), ...sources.flatMap(([, s]) => s)]

  // Control needle: the scan must still FIND something before its silence means anything.
  expect(AUDIT_COPY.exportCaption, 'MID must be the repo middle dot').toContain(MID)
  expect(
    haystack.filter((s) => s.includes(MANIFEST_NEEDLE)).length,
    `no string carries the ${MANIFEST_NEEDLE} control needle`,
  ).toBeGreaterThanOrEqual(1)

  return haystack
}

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('evidence-bundle drawer copy', () => {
  it('EB-02-1 evidenceCopy_neverSaysSigned', () => {
    const haystack = buildHaystack()

    // Negative control: the same scanner, over a planted string, must still report it.
    expect(matches(SIGNED_RE, ['A signed SHA-256 manifest is attached.'])).toHaveLength(1)

    expect(matches(SIGNED_RE, haystack), 'D-08-01: nothing may call the manifest signed').toEqual([])
    expect(bundleReadyLine(0)).toContain(MANIFEST_NEEDLE)
  })

  it('EB-02-2 evidenceCopy_neverPromisesAnEstimatedSize', () => {
    const haystack = buildHaystack()

    expect(matches(ESTIMATE_RE, ['The bundle is up to 4 MB.'])).toHaveLength(1)

    expect(matches(ESTIMATE_RE, haystack), 'D-08-02: no estimated size may be promised').toEqual([])
  })

  it('EB-02-3 evidenceCopy_agreesWithTheShippedManifestNote', () => {
    const go = readFileSync(MANIFEST_GO, 'utf8')
    // Control needle: prove the read landed on the manifest writer before trusting it.
    expect(go, 'manifest.go must still declare manifestNotes').toContain('manifestNotes')
    expect(go).toContain('not a cryptographic signature')

    expect(copy('rowManifest')).toContain('not a cryptographic signature')
  })

  it('EB-02-11 evidenceCopy_prepareHelperStatesWhatIsTrueAboutSize', () => {
    const helper = copy('prepareHelper')

    expect(helper).toContain('not known until the bundle is built')
    expect(helper).toContain('held in your browser until you save')
    expect(matches(ESTIMATE_RE, [helper])).toEqual([])
    // A digit here is a smuggled estimate; this and EB-02-2 are then un-satisfiable together.
    expect(helper).not.toMatch(/\d/)
  })
})

describe('bundleManifestLines', () => {
  it('EB-02-4 bundleManifestLines_countsComeFromThePreview', () => {
    expect(Object.keys(COUNTS), 'the fixture must carry all five counts').toHaveLength(5)
    expect(Object.values(COUNTS).every((n) => typeof n === 'number')).toBe(true)

    const lines = bundleManifestLines(PREVIEW)
    expect(lines).toHaveLength(7)

    const expected = [
      COUNTS.invoices,
      COUNTS.status_transitions,
      COUNTS.submissions,
      COUNTS.exchange_attempts,
      COUNTS.body_files,
    ].map((n) => n.toLocaleString('en-NG'))

    const values = lines.map((l) => l.value)
    expect(values.filter((v) => v !== null)).toEqual(expected)
    // FIRS references and the manifest row carry no number (D-08-12).
    expect(values.filter((v) => v === null)).toHaveLength(2)

    const labels = lines.map((l) => l.label)
    for (const n of expected) {
      expect(
        labels.some((l) => l.includes(n)),
        `a label bakes in the count ${n}`,
      ).toBe(false)
    }
    for (const label of labels) {
      expect(label, 'no thousands-grouped number belongs in a label').not.toMatch(/\d[\d,]*,\d{3}/)
    }
  })

  it('EB-02-5 bundleManifestLines_namesTheRealZipEntries', () => {
    const assemble = readFileSync(ASSEMBLE_GO, 'utf8')
    expect(assemble, 'assemble.go must still call newCSVEntry').toContain('newCSVEntry')

    const csvEntries = [...assemble.matchAll(/newCSVEntry\("([^"]+)"\)/g)].map((m) => m[1])
    expect(csvEntries, 'the four CSV entries must still be found').toHaveLength(4)

    const labels = bundleManifestLines(PREVIEW).map((l) => l.label)
    expect(labels).toHaveLength(7)
    for (const entry of csvEntries) {
      expect(
        labels.some((l) => l.includes(entry)),
        `no label names ${entry}`,
      ).toBe(true)
    }

    // bodies/ and manifest.json are not CSV entries; their own writers are the oracle.
    const exchange = readFileSync(EXCHANGE_GO, 'utf8')
    expect(exchange, 'exchange.go must still write the body files').toContain('selectExchange')
    expect(exchange).toContain('"bodies/"')
    expect(labels.some((l) => l.includes('bodies/'))).toBe(true)

    const manifest = readFileSync(MANIFEST_GO, 'utf8')
    expect(manifest, 'manifest.go must still write the manifest entry').toContain('writeManifest')
    expect(manifest).toContain('bw.zw.Create("manifest.json")')
    expect(labels.some((l) => l.includes('manifest.json'))).toBe(true)
  })
})

describe('bundleBasisLine', () => {
  it('EB-02-6 bundleBasisLine_statesCreatedAtNotIssueDate', () => {
    expect(PERIOD.basis).toBe('invoices.created_at')
    expect(PERIOD.bounds).toBe('inclusive')

    const line = bundleBasisLine(PERIOD)
    expect(line).toContain('Both dates are included')
    expect(line).toContain('added to ASComply')
    // D-08-11's point is the contrast, so the invoice date may be named -- but only negated.
    expect(line).toContain('not by the date on the invoice')
    expect(line.split('the date on the invoice')).toHaveLength(2)
    expect(line.split('not by the date on the invoice')).toHaveLength(2)
  })

  it('EB-02-6b bundleBasisLine_anUnknownBasisIsNotDressedUp', () => {
    const line = bundleBasisLine({ ...PERIOD, basis: 'invoices.issue_date' })

    expect(line).toContain('invoices.issue_date')
    expect(line).not.toContain('added to ASComply')
    expect(line).not.toContain('not by the date on the invoice')
  })
})

describe('bundleBlockFor', () => {
  it('EB-02-7 bundleBlock_zeroInvoicesBlocksBeforeTheBuild', () => {
    const empty = { ...PREVIEW, counts: { ...COUNTS, invoices: 0 } }

    const block = bundleBlockFor(ENTITY_ID, REQ, empty)
    expect(block).toEqual({ kind: 'empty', company: PREVIEW.entity.name, period: bundlePeriodLabel(PERIOD) })

    const reason = bundleBlockReason(block)
    expect(reason).not.toBeNull()
    expect(reason!).toContain(PREVIEW.entity.name)
    expect(reason!).toContain(bundlePeriodLabel(PERIOD))
  })

  it('EB-02-8 bundleBlock_overLimitBlocksAndNamesTheCount', () => {
    const over = { ...PREVIEW, over_limit: true, counts: { ...COUNTS, invoices: 12345 } }

    const block = bundleBlockFor(ENTITY_ID, REQ, over)
    // EB-02-12 owns the constant's VALUE; this row owns that the block carries it.
    expect(block).toEqual({ kind: 'over-limit', invoices: 12345, limit: BUNDLE_INVOICE_LIMIT })

    const reason = bundleBlockReason(block)
    expect(reason).not.toBeNull()
    // Through the same formatter: toLocaleString('en-NG') varies with the Node ICU build.
    expect(reason!).toContain((12345).toLocaleString('en-NG'))
    expect(reason!).toContain(BUNDLE_INVOICE_LIMIT.toLocaleString('en-NG'))
  })

  it('EB-02-13 bundleBlock_refusalsAreOrdered', () => {
    const over = { ...PREVIEW, over_limit: true, counts: { ...COUNTS, invoices: 12345 } }
    const empty = { ...PREVIEW, counts: { ...COUNTS, invoices: 0 } }
    const inverted: BundleRequest = { entityId: ENTITY_ID, from: REQ.to, to: REQ.from }

    expect(bundleBlockFor(null, REQ, over), 'no-company outranks over-limit').toEqual({ kind: 'no-company' })
    expect(bundleBlockFor(ENTITY_ID, null, empty), 'no-period outranks empty').toEqual({ kind: 'no-period' })
    expect(bundleBlockFor(ENTITY_ID, inverted, PREVIEW), 'invalid-range outranks a landed preview').toEqual({
      kind: 'invalid-range',
    })
    // A pending preview is not a refusal -- bundleBlockFor answers "is there a stated reason
    // to refuse?", not "is Prepare enabled?".
    expect(bundleBlockFor(ENTITY_ID, REQ, null)).toBeNull()
  })

  it('EB-02-14 bundleBlock_invertedCustomRangeIsRefused', () => {
    const req = bundleRequestFor(ENTITY_ID, { preset: 'custom', from: '2026-07-31', to: '2026-07-01' }, NOW)

    // Subtask 01 never compares the endpoints: it returns a populated request.
    expect(req, 'bundleRequestFor must still populate an inverted custom range').not.toBeNull()
    expect(req!.from > req!.to, 'both endpoints are fixed-width RFC3339 UTC, so byte order is time order').toBe(true)

    const block = bundleBlockFor(ENTITY_ID, req, PREVIEW)
    expect(block).toEqual({ kind: 'invalid-range' })
    expect(bundleBlockReason(block)).toBe(AUDIT_COPY.dateRangeInvalidReason)
  })
})

describe('bundleToastCopy', () => {
  it('EB-02-9 bundleToast_namesAllFiveFacts', () => {
    const toast = bundleToastCopy(TOAST)

    expect(toast).toContain(TOAST.filename)
    expect(toast).toContain(TOAST.invoices.toLocaleString('en-NG'))
    // A 3-digit fixture passes against a raw-digit implementation; 1500 does not.
    expect(toast).not.toContain(String(TOAST.invoices))
    expect(toast).toContain(formatBytes(TOAST.bytes))
    expect(toast).toContain(TOAST.company)
    expect(toast).toContain(TOAST.period)
  })

  it('EB-02-9b bundleToast_pluralisesOneInvoice', () => {
    const toast = bundleToastCopy({ ...TOAST, invoices: 1 })

    expect(toast).toMatch(/\b1 invoice\b/)
    expect(toast).not.toContain('1 invoices')
  })

  it('EB-02-10 bundleToast_isNotTheCsvToast', () => {
    const bundle = bundleToastCopy(TOAST)
    const csv = auditExportToastCopy({
      rows: TOAST.invoices,
      bytes: TOAST.bytes,
      filename: 'audit_log_20260824.csv',
      truncated: false,
      cap: 5000,
    })

    const sentences = (s: string) =>
      s
        .split(/(?<=\.)\s+/)
        .map((x) => x.trim())
        .filter(Boolean)

    const bundleSentences = sentences(bundle)
    const csvSentences = sentences(csv)
    expect(bundleSentences.length).toBeGreaterThanOrEqual(1)
    expect(csvSentences.length).toBeGreaterThanOrEqual(2)

    expect(bundleSentences.filter((s) => csvSentences.includes(s))).toEqual([])
    expect(bundle).not.toContain('No attachments, no payloads, no invoices')
    expect(bundle).not.toContain('Exported ')
  })
})

describe('bundleReadyLine and bundlePeriodLabel', () => {
  it('EB-02-12 bundleLimit_matchesTheServersCap', () => {
    const go = readFileSync(ARCHIVE_GO, 'utf8')
    // Control needle: prove the read landed on the file that declares the cap.
    expect(go, 'archive.go must still declare maxBundleInvoices').toContain('maxBundleInvoices')

    const match = /maxBundleInvoices\s*=\s*(\d+)/.exec(go)
    expect(match, 'the cap must still be an integer literal in archive.go').not.toBeNull()
    expect(BUNDLE_INVOICE_LIMIT).toBe(Number(match![1]))
  })

  it('EB-02-15 bundlePeriodLabel_neverShiftsADayByLocale', () => {
    vi.stubEnv('TZ', 'America/New_York')
    // Control: prove the stub took. In UTC this spec would pass against the bug it exists for.
    expect(new Date(PERIOD.from).getDate(), 'the TZ stub did not take effect').toBe(30)

    const label = bundlePeriodLabel(PERIOD)
    expect(label).toBe(PERIOD_LABEL)
    expect(label, 'BUG-03-02: new Date(iso).toLocaleDateString() shifts a day').not.toContain('30 June 2026')
  })
})
