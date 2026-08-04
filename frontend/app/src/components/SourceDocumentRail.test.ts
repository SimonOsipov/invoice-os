// Plain node test (no jsdom) for the previewer's two pure exports -- separate from the
// jsdom SourceDocumentRail.test.tsx render suite. Precedent: ClientsView.test.ts /
// ClientsView.test.tsx, WorkflowParts.test.ts.
import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { uploaderLabel } from './SourceDocumentRail'
import { fileTypeTone } from './SourceDocumentStates'

describe('uploaderLabel', () => {
  it('resolves a persona, passes a raw subject through, and reports null as Not recorded', () => {
    const firm = uploaderLabel(APP_PERSONAS.firm.subject)
    expect(firm).toEqual({ text: `${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`, mono: false })
    expect(firm.text).toContain('Chinedu Okafor')

    // An unknown subject is rendered VERBATIM and flagged mono -- the design's
    // "Amara Okafor · Ayoola & Co. (adviser)" maps to no stored field and is not invented.
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    expect(uploaderLabel(unknown)).toEqual({ text: unknown, mono: true })

    expect(uploaderLabel(null)).toEqual({ text: 'Not recorded', mono: false })
  })
})

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
