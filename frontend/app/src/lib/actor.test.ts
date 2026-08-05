// Plain node test for actorLabel, the shared GoTrue-subject formatter (task-392,
// BUG-03-03). Moved verbatim from SourceDocumentRail.test.ts's uploaderLabel describe
// block, plus the 'system' case demo data masks the original defect with.
import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { actorLabel } from './actor'

describe('actorLabel', () => {
  it('resolves a persona, passes a raw subject through, and reports null as Not recorded', () => {
    const firm = actorLabel(APP_PERSONAS.firm.subject)
    expect(firm).toEqual({ text: `${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`, mono: false })
    expect(firm.text).toContain('Chinedu Okafor')

    // An unknown subject is rendered VERBATIM and flagged mono -- the design's
    // "Amara Okafor · Ayoola & Co. (adviser)" maps to no stored field and is not invented.
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    expect(actorLabel(unknown)).toEqual({ text: unknown, mono: true })

    expect(actorLabel(null)).toEqual({ text: 'Not recorded', mono: false })
  })

  // The literal 'system' actor every demo-data status-history row carries -- masks the
  // raw-UUID defect in every fixture, so it must pass through unresolved like any other
  // unrecognised subject rather than getting special-cased.
  it("passes the literal 'system' through unresolved, in mono", () => {
    expect(actorLabel('system')).toEqual({ text: 'system', mono: true })
  })
})
