// Go <-> SPA <-> e2e key-set mirrors for wire types nothing links at compile time. One file
// rather than a per-type home in each consumer; `ApprovalRun`'s tri-mirror is the one
// exception and stays in approvals.test.ts.
//
// The three extractors are approvals.test.ts:864-898's, verbatim.

import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { NOT_ACTIVE_MEMBER_MESSAGE } from './authedFetch'

function repoFile(rel: string): string {
  return readFileSync(fileURLToPath(new URL(`../../../../${rel}`, import.meta.url)), 'utf8')
}

// Struct-scoped extraction: a file-wide tag regex would fold a neighbouring struct's json
// tags into this one's count and silently corrupt every floor.
function goStructKeys(source: string, structName: string): string[] {
  const body = new RegExp(`type\\s+${structName}\\s+struct\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
  const keys: string[] = []
  for (const m of body.matchAll(/`json:"([^"]+)"`/g)) {
    const key = m[1].split(',')[0]
    if (key !== '-') keys.push(key)
  }
  return keys
}

// The `extends` clause is optional in the pattern: without it an interface that extends
// anything yields '' -- zero keys, which reads exactly like clean.
function tsInterfaceKeys(source: string, interfaceName: string): string[] {
  const body =
    new RegExp(`export interface\\s+${interfaceName}(?:\\s+extends\\s+[^{]*?)?\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
  const keys: string[] = []
  for (const rawSeg of body.split(/[\n;]/)) {
    const seg = rawSeg.trim()
    if (!seg || seg.startsWith('//')) continue
    const m = /^([A-Za-z_][A-Za-z0-9_]*)\??\s*:/.exec(seg)
    if (m) keys.push(m[1])
  }
  return keys
}

// Symmetric-difference key names; [] means the two sets agree.
function keySetDiff(a: string[], b: string[]): string[] {
  const setA = new Set(a)
  const setB = new Set(b)
  const diff = new Set<string>()
  for (const k of a) if (!setB.has(k)) diff.add(k)
  for (const k of b) if (!setA.has(k)) diff.add(k)
  return [...diff]
}

const E2E_CLIENT = 'e2e/api/client.ts'

// Each anchor is a symbol OTHER than the type being extracted, so a moved or emptied file
// fails loudly instead of yielding [] and passing the equality row on {} === {} === {}.
// Every anchor ends at '(': a bare prefix still matches getAuditLogV2, so a rename would
// leave the control-needle row green over a symbol that no longer exists.
const WIRE_MIRRORS = [
  {
    ts: 'StatusChange',
    go: 'StatusChange',
    goPath: 'internal/invoice/invoice.go',
    goAnchor: 'func (s Status) valid() bool',
    spaPath: 'frontend/app/src/lib/invoices.ts',
    spaAnchor: 'export async function getInvoiceHistory(',
    e2eAnchor: 'export function getInvoiceHistory(',
    floor: 6,
  },
  {
    ts: 'AuditEvent',
    go: 'Event',
    goPath: 'internal/audit/reader.go',
    goAnchor: 'func ScopeOf(',
    spaPath: 'frontend/app/src/lib/audit.ts',
    spaAnchor: 'export async function getAuditLog(',
    e2eAnchor: 'export function getAuditLog(',
    floor: 10,
  },
  // EXTR-11-04, plus EXTR-12-02's ExtractionCandidate and EXTR-12-05's ExtractionCorrected —
  // the review screen's seven. One Go file, distinct anchors, so a deleted struct cannot be
  // masked by a neighbour's still-present symbol. Each anchor ends in '(' — without it a renamed
  // getExtractionDetailX still contains the anchor.
  {
    ts: 'ExtractionCandidate',
    go: 'ExtractionCandidate',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func (r *Reader) PageImageKey(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export async function fetchPageImage(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 2,
  },
  {
    ts: 'ExtractionCorrected',
    go: 'ExtractionCorrected',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func mergeCorrections(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export function highlightStyle(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 3,
  },
  {
    ts: 'ExtractionDetail',
    go: 'ExtractionDetail',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func emptyDetail()',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export async function getExtractionDetail(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 6,
  },
  {
    ts: 'ExtractionDocument',
    go: 'ExtractionDocument',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func detailTx(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export function docMetaLine(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 4,
  },
  {
    ts: 'ExtractionPage',
    go: 'ExtractionPage',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func detailPagesTx(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export function pageFrameStyle(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 3,
  },
  {
    ts: 'ExtractionFieldState',
    go: 'ExtractionFieldState',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func detailFieldsTx(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export function scrollRegionIntoView(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 6,
  },
  {
    ts: 'ExtractionRegion',
    go: 'ExtractionRegion',
    goPath: 'internal/extraction/reader.go',
    goAnchor: 'func (r *Reader) Detail(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export function highlightStyle(',
    e2eAnchor: 'export function getExtractionDetail(',
    floor: 5,
  },
  {
    ts: 'CorrectionRequest',
    go: 'CorrectionRequest',
    goPath: 'internal/extraction/handlers_correction.go',
    goAnchor: 'func CorrectionHandler(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export async function postFieldCorrection(',
    e2eAnchor: 'export function postFieldCorrection(',
    floor: 4,
  },
  {
    ts: 'CorrectionResponse',
    go: 'CorrectionResponse',
    goPath: 'internal/extraction/handlers_correction.go',
    goAnchor: 'func CorrectionHandler(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export async function postFieldCorrection(',
    e2eAnchor: 'export function postFieldCorrection(',
    floor: 7,
  },
  // EXTR-13-06 — the line-items route. LineItemInput's goPath pins internal/extraction, not
  // internal/invoice: that package's own LineItemInput has a fifth key (LineTax) and a bare
  // `go: 'LineItemInput'` without this path would read it and red the row (T4 below).
  {
    ts: 'LineItemInput',
    go: 'LineItemInput',
    goPath: 'internal/extraction/handlers_lineitems.go',
    goAnchor: 'func canonicalLineJSON(',
    spaPath: 'frontend/app/src/lib/lineItems.ts',
    spaAnchor: 'export function linesToPost(',
    e2eAnchor: 'export function postLineItems(',
    floor: 4,
  },
  {
    ts: 'LineItemsRequest',
    go: 'LineItemsRequest',
    goPath: 'internal/extraction/handlers_lineitems.go',
    goAnchor: 'func LineItemsHandler(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export async function postLineItems(',
    e2eAnchor: 'export function postLineItems(',
    floor: 1,
  },
  {
    ts: 'LineItemsResponse',
    go: 'LineItemsResponse',
    goPath: 'internal/extraction/handlers_lineitems.go',
    goAnchor: 'func writeLineItems(',
    spaPath: 'frontend/app/src/lib/extractionReview.ts',
    spaAnchor: 'export async function postLineItems(',
    e2eAnchor: 'export function postLineItems(',
    floor: 4,
  },
] as const

// AUDIT-10-07 — the message mirror.
//
// WIRE_MIRRORS above compares KEY SETS between a Go struct and its TypeScript twins. This
// compares a LITERAL: db.NotActiveMemberMessage is the 403 body every gated handler returns,
// and `isSuspended` matches on it, not on the bare status. Nothing links the two at compile
// time, and Go's own TestHandlerMappingMessageIsNeverRetyped walks internal/, cmd/ and tools/
// only — it cannot see either TypeScript copy.
//
// Two extracted legs, not one: e2e/api/suspension.spec.ts transcribes the same sentence for
// its deployed-wire assertion, so a reword in Go would leave that spec passing against a
// message the server no longer sends.

// Refuses to match a literal containing ANY backslash escape: an escaped literal yields ''
// and trips the non-vacuity row rather than being silently half-read.
function goStringConst(source: string, name: string): string {
  return new RegExp(`\\bconst\\s+${name}\\s*=\\s*"([^"\\\\]*)"`).exec(source)?.[1] ?? ''
}

// Same refusal, single-quoted — the e2e transcription's form.
function tsStringConst(source: string, name: string): string {
  return new RegExp(`\\bconst\\s+${name}\\s*=\\s*'([^'\\\\]*)'`).exec(source)?.[1] ?? ''
}

// Each anchor is a symbol OTHER than the constant being extracted, same reason as above.
const MESSAGE_MIRRORS = [
  {
    go: 'NotActiveMemberMessage',
    goPath: 'internal/platform/db/tenant.go',
    goAnchor: 'func WithinRequestTenantTxOpts(',
    spa: NOT_ACTIVE_MEMBER_MESSAGE,
    e2e: 'NOT_ACTIVE_MESSAGE',
    e2ePath: 'e2e/api/suspension.spec.ts',
    e2eAnchor: 'setMembershipStatus',
  },
] as const

describe('wire mirrors: Go <-> the SPA <-> e2e/api/client.ts (AC-5)', () => {
  it('wireMirrors_controlNeedleEverySourceFileWasActuallyRead', () => {
    const e2e = repoFile(E2E_CLIENT)
    expect(e2e.length).toBeGreaterThan(0)

    for (const m of WIRE_MIRRORS) {
      const go = repoFile(m.goPath)
      const spa = repoFile(m.spaPath)
      expect(go, `lost anchor on ${m.goPath}`).toContain(m.goAnchor)
      expect(spa, `lost anchor on ${m.spaPath}`).toContain(m.spaAnchor)
      expect(e2e, `lost anchor on ${E2E_CLIENT} for ${m.ts}`).toContain(m.e2eAnchor)
    }
  })

  it('wireMirrors_populationFloorPerTypePerSource', () => {
    const e2e = repoFile(E2E_CLIENT)

    for (const m of WIRE_MIRRORS) {
      expect(
        goStructKeys(repoFile(m.goPath), m.go).length,
        `Go ${m.go} must clear its floor of ${m.floor}`,
      ).toBeGreaterThanOrEqual(m.floor)
      expect(
        tsInterfaceKeys(repoFile(m.spaPath), m.ts).length,
        `${m.spaPath} ${m.ts} must clear its floor of ${m.floor}`,
      ).toBeGreaterThanOrEqual(m.floor)
      expect(
        tsInterfaceKeys(e2e, m.ts).length,
        `${E2E_CLIENT} ${m.ts} must clear its floor of ${m.floor}`,
      ).toBeGreaterThanOrEqual(m.floor)
    }
  })

  it('wireMirrors_threeWayEqualityRunsAfterTheFloorRowAbove', () => {
    const e2e = repoFile(E2E_CLIENT)

    for (const m of WIRE_MIRRORS) {
      const goKeys = goStructKeys(repoFile(m.goPath), m.go)
      const spaKeys = tsInterfaceKeys(repoFile(m.spaPath), m.ts)
      const e2eKeys = tsInterfaceKeys(e2e, m.ts)

      expect(keySetDiff(goKeys, spaKeys), `${m.ts}: Go ${m.go} vs ${m.spaPath}`).toEqual([])
      expect(keySetDiff(goKeys, e2eKeys), `${m.ts}: Go ${m.go} vs ${E2E_CLIENT}`).toEqual([])
      expect(keySetDiff(spaKeys, e2eKeys), `${m.ts}: ${m.spaPath} vs ${E2E_CLIENT}`).toEqual([])
    }
  })

  it('wireMirrors_plantedPositiveTheComparatorCanReportARealMismatch', () => {
    // Synthetic, in-memory only: 'b' is present on the Go side and missing on the TS side.
    const goFixture = 'type Fixture struct {\n\tA string `json:"a"`\n\tB string `json:"b"`\n}'
    const tsFixtureMissingB = 'export interface Fixture {\n  a: string\n}'

    const goKeys = goStructKeys(goFixture, 'Fixture')
    const tsKeys = tsInterfaceKeys(tsFixtureMissingB, 'Fixture')
    expect(goKeys).toEqual(['a', 'b'])
    expect(tsKeys).toEqual(['a'])

    const diff = keySetDiff(goKeys, tsKeys)
    expect(diff, 'the missing key must surface by name, not just a boolean flag').toContain('b')
  })

  it('wireMirrors_tableIsNonVacuous', () => {
    // The registry of what this file checks. A cleared table would let every loop here pass
    // on zero iterations, so each table is named — a new mirror that skips this row is a
    // mirror nothing runs.
    expect(WIRE_MIRRORS.map((m) => m.ts)).toEqual([
      'StatusChange',
      'AuditEvent',
      'ExtractionCandidate',
      'ExtractionCorrected',
      'ExtractionDetail',
      'ExtractionDocument',
      'ExtractionPage',
      'ExtractionFieldState',
      'ExtractionRegion',
      'CorrectionRequest',
      'CorrectionResponse',
      'LineItemInput',
      'LineItemsRequest',
      'LineItemsResponse',
    ])
    expect(MESSAGE_MIRRORS.map((m) => m.go)).toEqual(['NotActiveMemberMessage'])
  })

  // T3 — closes the blind spot AC-7 names: goStructKeys and
  // TestLineItemsWireTypes_HaveBraceFreeBodies both count `json:"…"` matches, so an exported
  // field with NO tag is invisible to both. Reads the three new structs' own brace-free bodies
  // and demands a tag on every remaining line.
  it('wireMirrors_everyGoFieldInTheNewStructsCarriesAJsonTag', () => {
    const go = repoFile('internal/extraction/handlers_lineitems.go')
    const structs = ['LineItemInput', 'LineItemsRequest', 'LineItemsResponse']

    function fieldLinesOf(structName: string): string[] {
      const body = new RegExp(`type\\s+${structName}\\s+struct\\s*\\{([^{}]*)\\}`).exec(go)?.[1] ?? ''
      return body
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l !== '' && !l.startsWith('//'))
    }

    for (const s of structs) {
      const lines = fieldLinesOf(s)
      expect(lines.length, `${s}: found no field lines to check`).toBeGreaterThan(0)
      for (const line of lines) {
        expect(line, `${s}: field line has no json tag -- invisible to goStructKeys`).toMatch(/`json:"[^"]+"`/)
      }
    }

    // Planted positive: an untagged field must fail the same predicate.
    const untagged = 'LineTax *string'
    expect(untagged).not.toMatch(/`json:"[^"]+"`/)
  })

  // T4 — internal/invoice.LineItemInput is a DIFFERENT, five-field type (adds LineTax) with NO
  // json tags at all -- it is Go-to-Go (Store.Create's input), never marshaled. goStructKeys
  // counts only `json:"…"` matches, so reading it yields 0, not 5. That is the point: a goPath
  // typo pointing this row at invoice.go would not silently agree, it would fail the floor of 4
  // LOUDLY (0 < 4) before the equality row ever ran.
  it('wireMirrors_theLineItemInputRowPointsAtTheExtractionPackage', () => {
    const row = WIRE_MIRRORS.find((m) => m.ts === 'LineItemInput')
    expect(row, 'LineItemInput row must exist').toBeDefined()
    expect(row?.goPath).toBe('internal/extraction/handlers_lineitems.go')

    const invoiceSrc = repoFile('internal/invoice/invoice.go')
    expect(invoiceSrc, 'internal/invoice.LineItemInput must still exist, untagged').toContain(
      'type LineItemInput struct {\n\tDescription *string\n\tQuantity    *string\n\tUnitPrice   *string\n\tLineTotal   *string\n\tLineTax     *string\n}',
    )
    const invoiceKeys = goStructKeys(invoiceSrc, 'LineItemInput')
    expect(invoiceKeys.length, 'untagged fields must not be readable as wire keys').toBe(0)
  })

  it('wireMirrors_theApprovalRunMirrorStillLivesInApprovalsTest', () => {
    // The third tri-mirror AC-5 names. Left where it shipped; this row is what makes all
    // three checkable from one place.
    const src = repoFile('frontend/app/src/lib/approvals.test.ts')
    expect(src, 'the scan read the wrong file').toContain('function goStructKeys')
    expect(src).toContain('wire mirror: Go read_model.go <-> lib/approvals.ts <-> e2e/api/client.ts')
    expect(src, 'ApprovalRun lost its row in WIRE_STRUCTS').toContain("{ go: 'Run', ts: 'ApprovalRun'")
  })
})

describe('wire mirror: db.NotActiveMemberMessage <-> the SPA <-> e2e/api/suspension.spec.ts (AUDIT-10-07)', () => {
  it('messageMirror_controlNeedleEverySourceFileWasActuallyRead', () => {
    for (const m of MESSAGE_MIRRORS) {
      expect(repoFile(m.goPath), `lost anchor on ${m.goPath}`).toContain(m.goAnchor)
      expect(repoFile(m.e2ePath), `lost anchor on ${m.e2ePath}`).toContain(m.e2eAnchor)
    }
  })

  it('messageMirror_extractionIsNonVacuousBeforeAnythingIsCompared', () => {
    // Zero hits must never read as agreement: '' === '' would pass the equality row below.
    for (const m of MESSAGE_MIRRORS) {
      expect(
        goStringConst(repoFile(m.goPath), m.go),
        `extracted nothing for Go const ${m.go} — the declaration moved, was renamed, or grew an escape`,
      ).not.toBe('')
      expect(
        tsStringConst(repoFile(m.e2ePath), m.e2e),
        `extracted nothing for ${m.e2ePath}'s ${m.e2e}`,
      ).not.toBe('')
      expect(m.spa, 'the SPA constant is empty').not.toBe('')
    }
  })

  it('messageMirror_theGoLiteralEqualsBothTypeScriptCopies', () => {
    for (const m of MESSAGE_MIRRORS) {
      const goValue = goStringConst(repoFile(m.goPath), m.go)

      expect(m.spa, 'isSuspended would stop matching the wire body').toBe(goValue)
      expect(
        tsStringConst(repoFile(m.e2ePath), m.e2e),
        'the deployed-wire assertion would pass against a message the server no longer sends',
      ).toBe(goValue)
    }
  })

  it('messageMirror_plantedPositiveTheExtractorsCanReportARealMismatch', () => {
    // Synthetic, in-memory only.
    const goFixture = 'package db\n\nconst Msg = "the original sentence"\n'
    const tsFixture = "const MSG = 'a reworded sentence'\n"

    expect(goStringConst(goFixture, 'Msg')).toBe('the original sentence')
    expect(tsStringConst(tsFixture, 'MSG')).toBe('a reworded sentence')
    expect(goStringConst(goFixture, 'Msg')).not.toBe(tsStringConst(tsFixture, 'MSG'))

    // A renamed or escaped declaration yields '', which the non-vacuity row rejects.
    expect(goStringConst(goFixture, 'Absent')).toBe('')
    expect(goStringConst('const Msg = "has an \\" escape"', 'Msg')).toBe('')
    expect(tsStringConst(tsFixture, 'ABSENT')).toBe('')
  })

  it('messageMirror_theGoCommentStillPointsHere', () => {
    // internal/platform/db/tenant.go's comment claimed this mirror before it existed
    // (AUDIT-10-04 reworded it to name the owner). A claim about a guard has to be
    // checkable, or the next reader trusts it and skips writing the guard.
    const src = repoFile('internal/platform/db/tenant.go')
    expect(src, 'tenant.go stopped naming the file that pins its message').toContain(
      'frontend/app/src/lib/wireMirrors.test.ts',
    )
  })
})

// EXTR-12-02 — the reason VOCABULARY mirror.
//
// WIRE_MIRRORS compares key sets, so it is blind to a type alias: both copies of
// `ExtractionReason` could name a different five strings and every row above stays green. The
// Go const block is the source and tracks extraction_field_results_reason_code_check, so a
// fifth code would otherwise reach a TypeScript union that silently rejects it.

const REASON_GO_PATH = 'internal/extraction/extractor.go'
const REASON_SPA_PATH = 'frontend/app/src/lib/extractionReview.ts'
const REASON_TS_NAME = 'ExtractionReason'

// Every `ReasonX Reason = "..."` in the const block, values only.
function goReasonValues(source: string): string[] {
  return [...source.matchAll(/\bReason[A-Za-z]*\s+Reason\s*=\s*"([^"]*)"/g)].map((m) => m[1])
}

// The single-quoted members of `export type Name = 'a' | 'b'`.
function tsUnionMembers(source: string, name: string): string[] {
  const body = new RegExp(`export type\\s+${name}\\s*=([^\\n]*)`).exec(source)?.[1] ?? ''
  return [...body.matchAll(/'([^']*)'/g)].map((m) => m[1])
}

describe('wire mirror: extraction Reason <-> both ExtractionReason unions (EXTR-12-02)', () => {
  it('reasonMirror_extractionIsNonVacuousBeforeAnythingIsCompared', () => {
    // Zero hits must never read as agreement: [] equals [] on the row below.
    expect(goReasonValues(repoFile(REASON_GO_PATH)), `no Reason const in ${REASON_GO_PATH}`).toHaveLength(5)
    for (const path of [REASON_SPA_PATH, E2E_CLIENT]) {
      expect(tsUnionMembers(repoFile(path), REASON_TS_NAME), `no ${REASON_TS_NAME} in ${path}`).toHaveLength(5)
    }
  })

  it('reasonMirror_theGoConstsEqualBothTypeScriptUnions', () => {
    const goValues = [...goReasonValues(repoFile(REASON_GO_PATH))].sort()
    for (const path of [REASON_SPA_PATH, E2E_CLIENT]) {
      expect([...tsUnionMembers(repoFile(path), REASON_TS_NAME)].sort(), `${path} ${REASON_TS_NAME}`).toEqual(goValues)
    }
    // The empty reason is what a NULL reason_code becomes, and the member a regex over quoted
    // words drops most easily.
    expect(goValues, "'' is what a clean field carries").toContain('')
  })

  it('reasonMirror_plantedPositiveTheExtractorsCanReportARealMismatch', () => {
    // Synthetic, in-memory only.
    const goFixture = 'const (\n\tReasonNone Reason = ""\n\tReasonOdd  Reason = "odd"\n)'
    const tsFixture = "export type R = '' | 'even'\n"
    expect(goReasonValues(goFixture)).toEqual(['', 'odd'])
    expect(tsUnionMembers(tsFixture, 'R')).toEqual(['', 'even'])
    expect(goReasonValues(goFixture)).not.toEqual(tsUnionMembers(tsFixture, 'R'))
    expect(tsUnionMembers(tsFixture, 'Absent')).toEqual([])
  })
})

// EXTR-13-06 — the HeaderFields vocabulary mirror. Three verbatim transcriptions of
// internal/extraction/vocabulary.go's HeaderFields exist and none was ever compared: the
// header/line split now depends on this order (extractionReview.ts's vocabularyRank), so a
// silent drift here misfiles a field into the wrong section rather than merely mislabeling it.

const HEADER_FIELDS_GO_PATH = 'internal/extraction/vocabulary.go'
const HEADER_FIELDS_SPA_PATH = 'frontend/app/src/lib/extractionReview.ts'
const HEADER_FIELDS_TEST_PATH = 'frontend/app/src/components/ExtractionFields.test.tsx'
const HEADER_FIELDS_E2E_PATH = 'e2e/topology/import-wizard.spec.ts'

// var HeaderFields = []string{...}, quoted strings in declaration order.
function goHeaderFields(source: string): string[] {
  const body = /var\s+HeaderFields\s*=\s*\[\]string\{([^}]*)\}/.exec(source)?.[1] ?? ''
  return [...body.matchAll(/"([^"]*)"/g)].map((m) => m[1])
}

// Accepts both `export const NAME = [...]` (extractionReview.ts) and a bare
// `const NAME = [...]` (the two test-file transcriptions), single-quoted members in order.
function tsStringArrayConst(source: string, name: string): string[] {
  const body = new RegExp(`(?:export\\s+)?const\\s+${name}\\s*(?::[^=]*)?=\\s*\\[([^\\]]*)\\]`).exec(source)?.[1] ?? ''
  return [...body.matchAll(/'([^']*)'/g)].map((m) => m[1])
}

const HEADER_FIELDS_TS_COPIES = [
  { path: HEADER_FIELDS_SPA_PATH, name: 'HEADER_FIELDS' },
  { path: HEADER_FIELDS_TEST_PATH, name: 'HEADER_FIELDS' },
  { path: HEADER_FIELDS_E2E_PATH, name: 'VOCABULARY' },
]

describe('wire mirror: HeaderFields vocabulary <-> its three TypeScript transcriptions (EXTR-13-06)', () => {
  it('headerFields_extractionIsNonVacuousBeforeAnythingIsCompared', () => {
    // A floor of ">0" is the classic escape here: if a rename made every extractor return [],
    // an order-comparison row would still pass on [] === []. The vocabulary is a fixed 10.
    expect(goHeaderFields(repoFile(HEADER_FIELDS_GO_PATH)), `no HeaderFields in ${HEADER_FIELDS_GO_PATH}`).toHaveLength(10)
    for (const { path, name } of HEADER_FIELDS_TS_COPIES) {
      expect(tsStringArrayConst(repoFile(path), name), `no ${name} in ${path}`).toHaveLength(10)
    }
  })

  it('headerFields_theGoSliceEqualsAllThreeTranscriptions', () => {
    // In order, not as a set: HEADER_FIELDS' contract is the order one Save writes in
    // (extractionReview.ts's vocabularyRank), which a set comparison cannot see move.
    const goValues = goHeaderFields(repoFile(HEADER_FIELDS_GO_PATH))
    for (const { path, name } of HEADER_FIELDS_TS_COPIES) {
      expect(tsStringArrayConst(repoFile(path), name), `${path} ${name} vs Go, in order`).toEqual(goValues)
    }
  })

  it('headerFields_plantedPositiveTheExtractorsCanReportARealMismatch', () => {
    // Synthetic, in-memory only: reordered AND one member differs ('c' vs 'd').
    const goFixture = 'var HeaderFields = []string{\n\t"a", "b", "c",\n}'
    const tsFixture = "const HEADER_FIELDS = [\n  'b',\n  'a',\n  'd',\n]\n"

    const goValues = goHeaderFields(goFixture)
    const tsValues = tsStringArrayConst(tsFixture, 'HEADER_FIELDS')
    expect(goValues).toEqual(['a', 'b', 'c'])
    expect(tsValues).toEqual(['b', 'a', 'd'])
    expect(goValues, 'the fixtures must actually differ for this test to mean anything').not.toEqual(tsValues)

    // An absent name yields [], not a thrown error and not a false [] === [] agreement.
    expect(tsStringArrayConst(tsFixture, 'ABSENT')).toEqual([])
  })
})

// -- QA additions (EXTR-13-06, Mode B) ----------------------------------------------------
//
// The rows above compare KEY SETS. Four things they cannot see, each closed here for the
// line-items types only: optionality, nullability, an unmirrored struct in the same file, and
// the gateway path -- which until now failed on the deployed fleet or nowhere.

// The raw interface body, '?' and '| null' intact. tsInterfaceKeys strips both.
function tsInterfaceBody(source: string, interfaceName: string): string {
  return new RegExp(`export interface\\s+${interfaceName}\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
}

function tsFieldLines(body: string): string[] {
  return body
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '' && !l.startsWith('//'))
}

function optionalKeys(body: string): string[] {
  return tsFieldLines(body).flatMap((l) => /^([A-Za-z_][A-Za-z0-9_]*)\?\s*:/.exec(l)?.[1] ?? [])
}

const LINE_ITEM_TYPES = ['LineItemInput', 'LineItemsRequest', 'LineItemsResponse'] as const
const LINE_ITEMS_GO_PATH = 'internal/extraction/handlers_lineitems.go'
const LINE_ITEMS_SPA_PATH = 'frontend/app/src/lib/lineItems.ts'
const REVIEW_SPA_PATH = 'frontend/app/src/lib/extractionReview.ts'

// Which file declares each type on the SPA side: LineItemInput lives in lineItems.ts, the
// layer that builds it; extractionReview.ts re-exports rather than redeclaring.
const SPA_DECL_PATH: Record<string, string> = {
  LineItemInput: LINE_ITEMS_SPA_PATH,
  LineItemsRequest: REVIEW_SPA_PATH,
  LineItemsResponse: REVIEW_SPA_PATH,
}

describe('line-items wire types: what a key-set diff cannot see (EXTR-13-06)', () => {
  it('lineItems_theDetectorFindsTheOptionalKeysThatDoExist', () => {
    // The control for the row below. client.ts's CorrectionRequest really is optional-keyed
    // where the SPA's is not -- the live asymmetry tsInterfaceKeys is blind to. If this row
    // ever finds nothing, the absence assertion under it is asserting nothing.
    const body = tsInterfaceBody(repoFile(E2E_CLIENT), 'CorrectionRequest')
    expect(tsFieldLines(body).length, 'read no CorrectionRequest body').toBe(4)
    expect(optionalKeys(body)).toEqual(['region', 'anchor_label'])
    expect(optionalKeys(tsInterfaceBody(repoFile(REVIEW_SPA_PATH), 'CorrectionRequest'))).toEqual([])
  })

  it('lineItems_noNewInterfaceCarriesAnOptionalKeyOnEitherLeg', () => {
    for (const ts of LINE_ITEM_TYPES) {
      for (const path of [SPA_DECL_PATH[ts], E2E_CLIENT]) {
        const body = tsInterfaceBody(repoFile(path), ts)
        expect(tsFieldLines(body).length, `${path} ${ts}: read no body`).toBeGreaterThan(0)
        expect(
          optionalKeys(body),
          `${path} ${ts}: an optional key compares equal to a required one, so the mirror stays green over the drift`,
        ).toEqual([])
      }
    }
  })

  it('lineItems_theDetectorFindsTheNullableFieldsThatDoExist', () => {
    // The control for the row below: LineItemInput's four cells ARE '| null'.
    const nullable = tsFieldLines(tsInterfaceBody(repoFile(LINE_ITEMS_SPA_PATH), 'LineItemInput')).filter((l) =>
      l.includes('| null'),
    )
    expect(nullable, 'the nullability scan finds nothing, so the absence row below is vacuous').toHaveLength(4)
  })

  it('lineItems_theLinesFieldIsAnArrayNeverNullOrAbsent', () => {
    // AC-8's TypeScript half. Go's normalizeLines guarantees [], so a `| null` here would make
    // every caller branch on a state the server cannot send.
    for (const ts of ['LineItemsRequest', 'LineItemsResponse']) {
      for (const path of [SPA_DECL_PATH[ts], E2E_CLIENT]) {
        const lines = tsFieldLines(tsInterfaceBody(repoFile(path), ts)).filter((l) => l.startsWith('lines'))
        expect(lines, `${path} ${ts}: no lines field`).toHaveLength(1)
        expect(lines[0], `${path} ${ts}: lines must be LineItemInput[], not nullable or optional`).toBe(
          'lines: LineItemInput[]',
        )
      }
    }
  })

  it('lineItemInput_isDeclaredExactlyOnceAcrossTheSpa', () => {
    // The row's spaPath watches ONE file. A second declaration elsewhere is a copy the
    // registry stops watching. Inside extractionReview.ts tsc already refuses it (TS2440 over
    // the type-import, TS2484 over the re-export); nothing refuses it in a third file.
    const root = fileURLToPath(new URL('../', import.meta.url))
    const files = readdirSync(root, { recursive: true, encoding: 'utf8' }).filter((f) => /\.tsx?$/.test(f))
    expect(files.length, 'the SPA source scan read no files').toBeGreaterThan(100)

    // Concatenated so this file's own text cannot self-match the scan.
    const needle = 'export interface ' + 'LineItemInput'
    const declaring = files.filter((f) => readFileSync(join(root, f), 'utf8').includes(needle))
    expect(declaring, 'LineItemInput must be declared once, in the file the mirror row watches').toEqual([
      'lib/lineItems.ts',
    ])
    expect(WIRE_MIRRORS.find((m) => m.ts === 'LineItemInput')?.spaPath).toBe(LINE_ITEMS_SPA_PATH)
  })
})

// Every exported struct in one file that carries a json tag, with its body.
function exportedWireStructs(source: string): string[] {
  return [...source.matchAll(/type\s+([A-Z][A-Za-z0-9_]*)\s+struct\s*\{([^{}]*)\}/g)]
    .filter((m) => /`json:"[^"]+"`/.test(m[2]))
    .map((m) => m[1])
}

describe('every wire struct in handlers_lineitems.go has a mirror row (EXTR-13-06)', () => {
  it('wireMirrors_noTaggedStructInTheLineItemsFileIsUnmirrored', () => {
    // tableIsNonVacuous catches a DELETED row; nothing caught a NEW struct with no row at all.
    // Scoped to this one file -- a repo-wide inventory is internal/audit's shape and is not
    // this story's.
    const found = exportedWireStructs(repoFile(LINE_ITEMS_GO_PATH))
    expect(found, 'the struct scan read nothing').toEqual([...LINE_ITEM_TYPES])

    const mirrored = WIRE_MIRRORS.filter((m) => m.goPath === LINE_ITEMS_GO_PATH).map((m) => m.go)
    for (const name of found) {
      expect(mirrored, `${name} is a tagged wire struct with no WIRE_MIRRORS row`).toContain(name)
    }
  })

  it('wireMirrors_plantedPositiveAnUnmirroredStructIsReported', () => {
    // Synthetic, in-memory only. The untagged one must NOT be reported: it never reaches a wire.
    const fixture =
      'type Mirrored struct {\n\tA string `json:"a"`\n}\n\ntype Newcomer struct {\n\tB string `json:"b"`\n}\n\ntype Internal struct {\n\tC string\n}\n'
    expect(exportedWireStructs(fixture)).toEqual(['Mirrored', 'Newcomer'])
    expect(exportedWireStructs(fixture).filter((n) => !['Mirrored'].includes(n))).toEqual(['Newcomer'])
  })
})

// The mux pattern cmd/submission/main.go registers, with {id} normalized to {}.
function goMuxPattern(source: string, suffix: string): string {
  const m = new RegExp(`"POST (/v1/[^"]*${suffix})"`).exec(source)?.[1] ?? ''
  return m === '' ? '' : `/api/submission${m}`.replace(/\{[^}]*\}/g, '{}')
}

// A template literal's path, with every ${…} normalized to {}.
function tsTemplatePath(source: string, pattern: RegExp): string {
  return (pattern.exec(source)?.[1] ?? '').replace(/\$\{[^}]*\}/g, '{}')
}

describe('the line-items gateway path equals the registered mux pattern (EXTR-13-06)', () => {
  // Nothing in Go reads the mux pattern and nothing in TypeScript reads the URL, so a
  // misspelled path went green through tsc, vitest and the whole Go suite, and failed only on
  // the deployed fleet. Three TypeScript legs, one Go source.
  const MAIN = 'cmd/submission/main.go'

  it('gatewayPath_bothTypeScriptCallersBuildThePathTheMuxRegistered', () => {
    const want = goMuxPattern(repoFile(MAIN), '/line-items')
    expect(want, `no POST …/line-items pattern in ${MAIN}`).toBe('/api/submission/v1/extractions/{}/line-items')

    expect(
      tsTemplatePath(repoFile(REVIEW_SPA_PATH), /`\$\{base\}([^`]*line-items)`/),
      'the SPA posts to a path the submission mux does not register',
    ).toBe(want)
    expect(
      tsTemplatePath(repoFile(E2E_CLIENT), /`\$\{apiBase\(\)\}([^`]*line-items)`/),
      'e2e/api/client.ts posts to a path the submission mux does not register',
    ).toBe(want)
  })

  it('gatewayPath_theApiSpecBuildsTheSamePath', () => {
    const spec = repoFile('e2e/api/extractions.spec.ts')
    const base = /const EXTRACTIONS_PATH = '([^']*)'/.exec(spec)?.[1] ?? ''
    expect(base, 'EXTRACTIONS_PATH moved or was renamed').toBe('/api/submission/v1/extractions')

    const built = (/const lineItemsPath = \(id: string\) => `([^`]*)`/.exec(spec)?.[1] ?? '')
      .replace('${EXTRACTIONS_PATH}', base)
      .replace(/\$\{[^}]*\}/g, '{}')
    expect(built, 'the refusal specs would exercise a different route than the SPA calls').toBe(
      goMuxPattern(repoFile(MAIN), '/line-items'),
    )
  })

  it('gatewayPath_plantedPositiveAMisspelledPatternIsReported', () => {
    // Synthetic, in-memory only.
    const goFixture = 'app.Mux.HandleFunc("POST /v1/extractions/{id}/lineitems", h)'
    const tsFixture = 'return authedFetch(`${base}/api/submission/v1/extractions/${jobId}/line-items`, {})'
    expect(goMuxPattern(goFixture, '/lineitems')).toBe('/api/submission/v1/extractions/{}/lineitems')
    expect(tsTemplatePath(tsFixture, /`\$\{base\}([^`]*line-items)`/)).toBe(
      '/api/submission/v1/extractions/{}/line-items',
    )
    expect(goMuxPattern(goFixture, '/lineitems')).not.toBe(tsTemplatePath(tsFixture, /`\$\{base\}([^`]*line-items)`/))

    // An absent pattern yields '', which the non-vacuity assertion above rejects.
    expect(goMuxPattern(goFixture, '/absent')).toBe('')
  })
})

// -- copies this file deliberately does NOT guard (EXTR-13-06, AC-9) ----------------------
//
// LOCKED_FIELDS (ExtractionFields.test.tsx) vs handlers_correction.go's lockedFields -- a Go
//   map[string]string, not a slice; it needs a different extractor, and refuseField's 422 is
//   already pinned by the correction e2e.
// ExtractionJob / ExtractionJobsResponse (e2e/api/client.ts) -- no SPA copy exists, so a
//   three-way row is impossible; the SPA does not read the jobs list.
// CorrectionRequest's optionality asymmetry -- tsInterfaceKeys strips '?', so the SPA's
//   all-required keys compare equal to client.ts's optional region/anchor_label. A live
//   pre-existing defect, not this story's; the three line-items interfaces are non-optional on
//   both legs, and lineItems_noNewInterfaceCarriesAnOptionalKeyOnEitherLeg holds them there.
//   The asymmetry itself is the live control that test runs against.
// import-wizard.spec.ts's WIRE_FIELD_SET -- transcribed from internal/extraction/mock.go, a
//   fixture oracle rather than a wire type.
// An exported wire struct with no row at all -- closed for handlers_lineitems.go by
//   wireMirrors_noTaggedStructInTheLineItemsFileIsUnmirrored; the rest of internal/extraction
//   is still unenumerated, which needs internal/audit's pinned-inventory shape.
// A field added to all three legs at once -- goStructKeys compares SETS, so the registry is
//   blind by construction. Go's TestLineItemsWireTypes_HaveBraceFreeBodies pins 4/1/4 exactly,
//   which catches an addition; a same-count swap and wrong semantics stay open.

// The submit pair rides BOTH wires from one submitGate call, so it is declared once on
// InvoiceRecord and inherited. Nothing links the two interfaces at compile time --
// InvoiceDetailRecord extends Omit<InvoiceRecord, 'approval'>, so a redeclaration compiles
// clean and drifts silently.
describe('the submit pair is declared once on the SPA invoice mirror (BUG-12)', () => {
  const SPA = 'frontend/app/src/lib/invoices.ts'
  const SUBMIT_PAIR = ['can_submit', 'submit_blocked_reason']

  it('B12-9a: control needle + floor for the declared-once scan', () => {
    const source = repoFile(SPA)
    expect(source.length, `${SPA} was not read`).toBeGreaterThan(0)

    const record = tsInterfaceKeys(source, 'InvoiceRecord')
    const detail = tsInterfaceKeys(source, 'InvoiceDetailRecord')

    // Anchors this story never moves: an extractor that broke would empty both sets and
    // let the claim below pass on nothing.
    expect(record.length, 'InvoiceRecord under its floor').toBeGreaterThanOrEqual(30)
    expect(record).toContain('invoice_number')
    expect(record).toContain('can_approve')

    // InvoiceDetailRecord carries an `extends` clause: this row is what proves the
    // extractor sees past one at all.
    expect(detail.length, 'InvoiceDetailRecord under its floor').toBeGreaterThanOrEqual(11)
    expect(detail).toContain('qr_png_base64')
    expect(detail).toContain('can_reject')
  })

  it('B12-9b: the pair is declared once', () => {
    const source = repoFile(SPA)
    const record = tsInterfaceKeys(source, 'InvoiceRecord')
    const detail = tsInterfaceKeys(source, 'InvoiceDetailRecord')

    expect(SUBMIT_PAIR).toHaveLength(2)
    // Absence first: the redeclaration is the hit the fixed extractor already finds in the
    // tree, so this leg is what proves the scan bites before the floor can mask it.
    for (const key of SUBMIT_PAIR) {
      expect(detail, `${key} is redeclared on InvoiceDetailRecord instead of inherited`).not.toContain(key)
      expect(record, `${key} must be declared on InvoiceRecord`).toContain(key)
    }
    expect(record.length, 'InvoiceRecord must carry the pair on top of its floor').toBeGreaterThanOrEqual(32)
  })

  it('B12-9c: planted positive — the extractor reads an extending interface and a plain one alike', () => {
    const extending = 'export interface Fixture extends Base {\n  a: string\n  b: string | null\n}'
    const plain = 'export interface Fixture {\n  a: string\n  b: string | null\n}'

    expect(tsInterfaceKeys(extending, 'Fixture')).toEqual(['a', 'b'])
    expect(tsInterfaceKeys(plain, 'Fixture')).toEqual(['a', 'b'])
    // And it still reports a real absence rather than agreeing with everything.
    expect(tsInterfaceKeys('export interface Fixture extends Base {\n  a: string\n}', 'Fixture')).not.toContain('b')
  })
})
