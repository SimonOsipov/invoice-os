// Plain node test (no jsdom) for the previewer's pure export -- separate from the jsdom
// SourceDocumentRail.test.tsx render suite. Precedent: ClientsView.test.ts /
// ClientsView.test.tsx, WorkflowParts.test.ts.
import { describe, expect, it } from 'vitest'

import { fileTypeTone, formatLabel } from './SourceDocumentStates'

describe('fileTypeTone', () => {
  it('maps xlsx/pdf/jpg/unknown to four distinct tones', () => {
    const tones = [
      fileTypeTone('june-sales.xlsx', null),
      fileTypeTone('scan.pdf', null),
      fileTypeTone('photo.jpg', null),
      fileTypeTone('ledger.dat', null),
    ]

    for (const tone of tones) {
      expect(tone.bg.length).toBeGreaterThan(0)
      expect(tone.fg.length).toBeGreaterThan(0)
      // `--accent-tint` is undefined in the rebuilt design system and resolves silently to
      // nothing, with no build error (trap recorded at ImportProgress.tsx:104).
      expect(tone.bg).not.toContain('--accent-tint')
      expect(tone.fg).not.toContain('--accent-tint')
    }

    expect(new Set(tones.map((t) => t.bg)).size).toBe(4)
  })
})

// The restyle leaves both helpers alone: the ExtractionCanvas tile and the modal header read them.
describe('the tone helpers are unchanged', () => {
  it('fileTypeTone keeps its four status tones', () => {
    expect(fileTypeTone('june-sales.xlsx', null)).toEqual({ bg: 'var(--status-green-bg)', fg: 'var(--status-green-text)' })
    expect(fileTypeTone('scan.pdf', null)).toEqual({ bg: 'var(--status-red-bg)', fg: 'var(--status-red-text)' })
    expect(fileTypeTone('photo.jpg', null)).toEqual({ bg: 'var(--status-amber-bg)', fg: 'var(--status-amber-text)' })
    expect(fileTypeTone('ledger.dat', null)).toEqual({ bg: 'var(--bg-3)', fg: 'var(--fg-3)' })
  })

  it('formatLabel keeps extension, declared type, UNKNOWN', () => {
    expect(formatLabel('June-Sales.XLSX', null)).toBe('XLSX')
    expect(formatLabel(null, 'application/pdf; charset=binary')).toBe('application/pdf')
    expect(formatLabel(null, null)).toBe('UNKNOWN')
  })
})
