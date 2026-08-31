// AC-5's story-level fence: this story changes no wire shape.
//
// `ApprovalRun`'s tri-mirror already lives in approvals.test.ts and is left where it is;
// the two below had no cross-language mirror at all (`StatusChange`) or only a one-way
// Go->SPA substring scan with no SPA<->e2e leg (`AuditEvent`). One file rather than three
// per-type homes: AC-5 is a story-level fence, and lib/invoices.ts and lib/audit.ts carry
// no pointer to it (F-AJ).
//
// The three extractors are approvals.test.ts:864-898's, verbatim.

import { readFileSync } from 'node:fs'
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

function tsInterfaceKeys(source: string, interfaceName: string): string[] {
  const body = new RegExp(`export interface\\s+${interfaceName}\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
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
const WIRE_MIRRORS = [
  {
    ts: 'StatusChange',
    go: 'StatusChange',
    goPath: 'internal/invoice/invoice.go',
    goAnchor: 'func (s Status) valid() bool',
    spaPath: 'frontend/app/src/lib/invoices.ts',
    spaAnchor: 'export async function getInvoiceHistory',
    e2eAnchor: 'export function getInvoiceHistory',
    floor: 6,
  },
  {
    ts: 'AuditEvent',
    go: 'Event',
    goPath: 'internal/audit/reader.go',
    goAnchor: 'func ScopeOf(',
    spaPath: 'frontend/app/src/lib/audit.ts',
    spaAnchor: 'export async function getAuditLog',
    e2eAnchor: 'export function getAuditLog',
    floor: 10,
  },
  // EXTR-11-04 — the review screen's five. One Go file, five distinct anchors, so a
  // deleted struct cannot be masked by a neighbour's still-present symbol. Each anchor
  // ends in '(' — without it a renamed getExtractionDetailX still contains the anchor.
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
    floor: 3,
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
      'ExtractionDetail',
      'ExtractionDocument',
      'ExtractionPage',
      'ExtractionFieldState',
      'ExtractionRegion',
    ])
    expect(MESSAGE_MIRRORS.map((m) => m.go)).toEqual(['NotActiveMemberMessage'])
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
