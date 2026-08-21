// Plain node test for actorLabel, the shared GoTrue-subject formatter (task-392,
// BUG-03-03). Moved verbatim from SourceDocumentRail.test.ts's uploaderLabel describe
// block, plus the 'system' case demo data masks the original defect with.
import { describe, expect, it } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { actorLabel } from './actor'

// AUDIT-02-04 (Core AC-4, DEFECT 3): the three assertions below are exact `toEqual`, so
// each gains the new `kind` discriminator. `text` and `mono` are unchanged.
describe('actorLabel', () => {
  it('resolves a persona, passes a raw subject through, and reports null as Not recorded', () => {
    const firm = actorLabel(APP_PERSONAS.firm.subject)
    expect(firm).toEqual({ text: `${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`, mono: false, kind: 'person' })
    expect(firm.text).toContain('Chinedu Okafor')

    // An unknown subject is rendered VERBATIM and flagged mono -- the design's
    // "Amara Okafor · Ayoola & Co. (adviser)" maps to no stored field and is not invented.
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    expect(actorLabel(unknown)).toEqual({ text: unknown, mono: true, kind: 'raw' })

    expect(actorLabel(null)).toEqual({ text: 'Not recorded', mono: false, kind: 'absent' })
  })

  // AUDIT-02-04 (Core AC-4) supersedes the BUG-03-03 rationale this test carried: 'system'
  // was left unresolved on purpose, because special-casing it would have masked the
  // raw-UUID defect in every demo fixture. That defect is what this story fixes -- the
  // server now names every other actor (internal/actor/resolve.go) -- so 'system' no
  // longer hides anything, and rendering the literal to a user is the remaining defect.
  it("renders the literal 'system' as System, unresolved", () => {
    expect(actorLabel('system')).toEqual({ text: 'System', mono: false, kind: 'system' })
  })
})

// AUDIT-02-04 (Mode A, RED). actorLabel gains ONE optional second parameter -- the wire's
// {actor_name, actor_kind} pair (lib/invoices.ts:317-320, e2e/api/client.ts:483-484). Both
// mirrors type actor_kind as a widened `string`, so the parameter takes `string` and
// narrows inside actor.ts; server-side the only three values are system|person|raw
// (internal/actor/actor.go:12-14). The five callers that pass no pair compile untouched.
describe('actorLabel with the server-resolved pair (Core AC-3/AC-5/AC-9)', () => {
  const KINDS = ['absent', 'system', 'person', 'raw']
  const UNKNOWN = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'

  it('uses the server-resolved name when the wire supplies one', () => {
    expect(actorLabel(UNKNOWN, { name: 'Folake Adesina', kind: 'person' })).toEqual({
      text: 'Folake Adesina',
      mono: false,
      kind: 'person',
    })
  })

  it('a raw kind renders the subject verbatim, mono', () => {
    // Label.Text IS the stored actor byte-for-byte for KindRaw (actor.go:17-18,
    // resolve.go:60), so honouring a raw answer still renders the subject, not a name.
    expect(actorLabel(UNKNOWN, { name: UNKNOWN, kind: 'raw' })).toEqual({
      text: UNKNOWN,
      mono: true,
      kind: 'raw',
    })
  })

  it('a resolved system kind reads System, not the literal', () => {
    expect(actorLabel('system', { name: 'System', kind: 'system' })).toEqual({
      text: 'System',
      mono: false,
      kind: 'system',
    })
  })

  it('null still reads Not recorded, even when a pair is supplied', () => {
    expect(actorLabel(null)).toEqual({ text: 'Not recorded', mono: false, kind: 'absent' })
    expect(actorLabel(null, { name: 'Folake Adesina', kind: 'person' })).toEqual({
      text: 'Not recorded',
      mono: false,
      kind: 'absent',
    })
  })

  // THE test of this subtask. APP_PERSONAS (auth.ts:34-61) holds BOTH tenants' admin
  // subjects, unscoped: c0...001 is Okafor & Partners' admin, c0...002 is Honeywell
  // Group's. Consulting that table AFTER the server has answered discloses the other
  // tenant's admin and employer to a viewer with no visibility into that tenant.
  //
  // The walkthrough: an Okafor viewer opens a history row actored by c0...002. The server,
  // RLS-scoped, finds no membership row and answers kind 'raw' with the subject verbatim.
  // Fall through to APP_PERSONAS from there and the row reads
  // "Ngozi Balogun · Honeywell Group", non-mono, presented as a confidently resolved
  // identity. So: a resolved pair is authoritative, 'raw' included, and NO rung below it
  // may run. Do not reintroduce a `kind !== 'raw'` condition here.
  it('actorLabel_neverConsultsPersonasWhenTheServerAnswered', () => {
    const honeywellAdmin = APP_PERSONAS.inhouse.subject
    const raw = actorLabel(honeywellAdmin, { name: honeywellAdmin, kind: 'raw' })

    expect(raw).toEqual({ text: honeywellAdmin, mono: true, kind: 'raw' })
    expect(raw.text).not.toContain(APP_PERSONAS.inhouse.name)
    expect(raw.text).not.toContain(APP_PERSONAS.inhouse.org)

    // Symmetric: the firm's own admin, seen as raw from the other tenant, is also a bare
    // subject. Both directions, so a one-sided guard cannot pass this.
    const firmAdmin = APP_PERSONAS.firm.subject
    const rawFirm = actorLabel(firmAdmin, { name: firmAdmin, kind: 'raw' })
    expect(rawFirm).toEqual({ text: firmAdmin, mono: true, kind: 'raw' })
    expect(rawFirm.text).not.toContain(APP_PERSONAS.firm.name)

    // And when the server DOES name a persona subject, its name wins over the table's --
    // proof the table was never consulted, not merely that it agreed.
    expect(actorLabel(firmAdmin, { name: 'C. Okafor (server)', kind: 'person' })).toEqual({
      text: 'C. Okafor (server)',
      mono: false,
      kind: 'person',
    })
  })

  // AC-5's blank-cell half, defended at the client even though D-31 already stops a ''
  // reaching the wire. A '' name is the server failing to answer, not an answer: the
  // SUBJECT stands in, in mono. It must NOT fall through to APP_PERSONAS -- that is the
  // leak above by another route, since '' arrives with the subject still in hand.
  it('never renders an empty label, and an empty name falls back to the subject not a persona', () => {
    const subjects = [APP_PERSONAS.firm.subject, APP_PERSONAS.inhouse.subject, 'system', UNKNOWN, 'not-a-uuid']
    const pairs = [
      undefined,
      { name: '', kind: 'person' },
      { name: '', kind: 'raw' },
      { name: '', kind: 'system' },
      { name: 'Folake Adesina', kind: 'person' },
    ]
    const combos = subjects.flatMap((subject) => pairs.map((pair) => ({ subject, pair })))
    expect(combos).toHaveLength(25)

    for (const { subject, pair } of combos) {
      const label = actorLabel(subject, pair)
      expect(label.text, `empty label for ${subject} / ${JSON.stringify(pair)}`).not.toBe('')
      expect(KINDS, `unknown kind for ${subject} / ${JSON.stringify(pair)}`).toContain(label.kind)
    }

    const honeywellAdmin = APP_PERSONAS.inhouse.subject
    expect(actorLabel(honeywellAdmin, { name: '', kind: 'person' })).toEqual({
      text: honeywellAdmin,
      mono: true,
      kind: 'raw',
    })
  })

  // AC-8/D-23/D-24: the five callers that supply no pair keep their behaviour byte for
  // byte -- SourceDocumentStates.tsx:96, InvoiceDetail.tsx:1123, InvoiceDetail.tsx:1264,
  // SourceDocumentRail.tsx:71, approvals.ts:464. Only `kind` is new.
  it('falls back to the persona only when the wire resolved nothing', () => {
    expect(actorLabel(APP_PERSONAS.firm.subject)).toEqual({
      text: `${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`,
      mono: false,
      kind: 'person',
    })
    expect(actorLabel(UNKNOWN)).toEqual({ text: UNKNOWN, mono: true, kind: 'raw' })
  })
})
