// Plain node test (no jsdom) for the previewer's pure export -- separate from the jsdom
// SourceDocumentRail.test.tsx render suite. Precedent: ClientsView.test.ts /
// ClientsView.test.tsx, WorkflowParts.test.ts.
import { describe, expect, it } from 'vitest'

import { fileTypeTone } from './SourceDocumentStates'

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
