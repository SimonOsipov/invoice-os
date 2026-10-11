// Pins severityStyle's error->red / warning->amber / info->muted mapping and its
// out-of-enum fallback; the pill colours are asserted nowhere else.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { severityStyle, violationKey, violationLine, type Severity } from './validationApi'

describe('severityStyle', () => {
  const cases: Array<[Severity, string]> = [
    ['error', 'red'],
    ['warning', 'amber'],
    ['info', 'muted'],
  ]

  it('V6: each severity returns a well-formed StatusStyle (bg/border/text/label all truthy)', () => {
    for (const [severity] of cases) {
      const style = severityStyle(severity)
      expect(style.bg).toBeTruthy()
      expect(style.border).toBeTruthy()
      expect(style.text).toBeTruthy()
      expect(style.label).toBeTruthy()
    }
  })

  it("V6: colors map error->red, warning->amber, info->muted (mirrors entityStatusStyle's var(--status-<color>-*) convention)", () => {
    for (const [severity, color] of cases) {
      const style = severityStyle(severity)
      expect(style.bg).toBe(`var(--status-${color}-bg)`)
      expect(style.border).toBe(`var(--status-${color}-border)`)
      expect(style.text).toBe(`var(--status-${color}-text)`)
    }
  })

  it('V6: the three severity styles are mutually distinct', () => {
    const [error, warning, info] = cases.map(([s]) => severityStyle(s))
    expect(error).not.toEqual(warning)
    expect(warning).not.toEqual(info)
    expect(error).not.toEqual(info)
  })

  // The wire `Violation.severity` is JSON.parse'd and never runtime-validated against the
  // `Severity` union, so a future rule-set can send a severity this map has no entry for.
  // `SEVERITY_STYLE[sev] ?? MUTED_STYLE` keeps the mapper total.
  it('QA: an out-of-enum severity (cast) still resolves to the muted style, all four fields truthy — total-mapping fallback required by the story', () => {
    const style = severityStyle('critical' as Severity)

    expect(style).toBeDefined()
    expect(style.bg).toBe('var(--status-muted-bg)')
    expect(style.border).toBe('var(--status-muted-border)')
    expect(style.text).toBe('var(--status-muted-text)')
    expect(style.label).toBeTruthy()
  })
})

describe('violationLine', () => {
  it('violationLine_parsesLinePaths', () => {
    expect(violationLine('line_items[2]')).toEqual({ line: 2, field: null })
    expect(violationLine('line_items[2].unit_price')).toEqual({ line: 2, field: 'unit_price' })
    expect(violationLine('line_items[12].hsn_code')).toEqual({ line: 12, field: 'hsn_code' })
  })

  it('violationLine_rejectsEverythingElse', () => {
    const bad = ['line_items', '', undefined, 'subtotal', 'line_items[0]', 'line_items[01]', 'line_items[1].', ' line_items[1]', 'tax_subtotals[1].x', 'Line_items[1]', 'line_items[-1]', 'line_items[1.5]', 'line_items[abc]', 'line_items[1][2]', 'line_items[1]x', 'line_items[1].a.b', 'line_items[1].a-b', 'line_items[1] ']
    for (const p of bad) expect(violationLine(p), String(p)).toBeNull()
  })

  it('violationLine_parsesTheEngineFixture', () => {
    const rows = JSON.parse(
      readFileSync(fileURLToPath(new URL('../../../../internal/validation/testdata/line_paths.json', import.meta.url)), 'utf8'),
    ) as Array<{ list: string; n: number; field: string; path: string }>
    const lineRows = rows.filter((r) => r.list === 'line_items')
    expect(lineRows.length).toBeGreaterThan(0)
    for (const r of lineRows) expect(violationLine(r.path), r.path).toEqual({ line: r.n, field: r.field || null })
  })

  it('violationKey_distinguishesPaths', () => {
    expect(violationKey({ rule_key: 'r', path: 'line_items[1]' })).not.toBe(violationKey({ rule_key: 'r', path: 'line_items[2]' }))
    expect(violationKey({ rule_key: 'r', path: undefined })).toBe(violationKey({ rule_key: 'r', path: '' }))
  })
})
