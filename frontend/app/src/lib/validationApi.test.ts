// Pins severityStyle's error->red / warning->amber / info->muted mapping and its
// out-of-enum fallback; the pill colours are asserted nowhere else.
import { describe, expect, it } from 'vitest'

import { severityStyle, type Severity } from './validationApi'

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
