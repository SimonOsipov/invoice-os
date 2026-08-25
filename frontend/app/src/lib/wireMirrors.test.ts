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
    // A cleared table would let all three loops above pass on zero iterations.
    expect(WIRE_MIRRORS.map((m) => m.ts)).toEqual(['StatusChange', 'AuditEvent'])
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
