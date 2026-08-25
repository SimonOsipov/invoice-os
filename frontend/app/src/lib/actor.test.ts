// Plain node test for actorLabel, the shared GoTrue-subject formatter (task-392,
// BUG-03-03). Moved verbatim from SourceDocumentRail.test.ts's uploaderLabel describe
// block, plus the 'system' case -- which AUDIT-02-04 inverted (note at the second describe).
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

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

// AUDIT-02-04 QA (Mode B). Coverage the AC-derived specs above do not reach. The theme is
// one property: once the server has answered, APP_PERSONAS -- which holds BOTH tenants'
// subjects unscoped (auth.ts:43, :56) -- must stay unread on EVERY input shape, not just
// the well-formed ones the wire promises.
describe('actorLabel adversarial coverage (AUDIT-02-04 QA)', () => {
  const UNKNOWN = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
  const FIRM = APP_PERSONAS.firm.subject
  const INHOUSE = APP_PERSONAS.inhouse.subject
  const LEAKS = [
    APP_PERSONAS.firm.name,
    APP_PERSONAS.firm.org,
    APP_PERSONAS.inhouse.name,
    APP_PERSONAS.inhouse.org,
  ]

  function expectNoPersonaLeak(text: string, why: string) {
    for (const needle of LEAKS) expect(text, `${why}: leaked ${needle}`).not.toContain(needle)
  }

  // CHARACTERISATION, not an endorsement. The '' guard is exact, so a name of one space
  // is an ANSWER and renders as one space -- a visually blank cell that AC-5's
  // `.not.toBe('')` sweep cannot see. Reachable end to end: actor.go:36 stops on a
  // display_name of ' ' for the same reason (it is not ''), so a membership row with a
  // whitespace display_name puts a blank actor on the card. D-31 settled '' by decision;
  // whitespace is the open finding from AUDIT-02-01 and is NOT settled here. Pinned so a
  // future fix is a deliberate change with a red test, not a silent drift.
  it('a whitespace-only name is honoured verbatim and still never consults the personas', () => {
    // TAB, LF and NBSP as codepoints: a literal one in the source is invisible to review.
    const cases = [' ', '  ', String.fromCharCode(9), String.fromCharCode(10), String.fromCharCode(160)]
    expect(cases.length).toBeGreaterThan(0)

    for (const name of cases) {
      for (const subject of [FIRM, INHOUSE, UNKNOWN]) {
        const label = actorLabel(subject, { name, kind: 'person' })
        expect(label, `whitespace name ${JSON.stringify(name)} for ${subject}`).toEqual({
          text: name,
          mono: false,
          kind: 'person',
        })
        expectNoPersonaLeak(label.text, `whitespace name ${JSON.stringify(name)}`)
      }
    }

    // The security half holds where it matters most: whitespace does NOT reopen the
    // fall-through, so a whitespace answer for the other tenant's admin stays blank
    // rather than becoming "Ngozi Balogun BULLET Honeywell Group".
    expect(actorLabel(INHOUSE, { name: ' ', kind: 'raw' })).toEqual({ text: ' ', mono: true, kind: 'raw' })
  })

  // The narrowing at actor.ts:30 is the ONLY thing standing between a wire that widens
  // actor_kind to `string` (invoices.ts:323, e2e/api/client.ts:485) and an ActorKind that
  // callers switch on. Every value outside system|person must land on 'raw' -- mono, with
  // the name verbatim -- and none may fall through to the persona table.
  it('an unexpected kind narrows to raw, in mono, and never reaches APP_PERSONAS', () => {
    const kinds = ['Person', 'PERSON', 'person ', ' person', 'System', 'SYSTEM', '', 'absent', 'raw ', 'null', 'undefined', '__proto__', '{}']
    expect(kinds.length).toBeGreaterThan(0)

    for (const kind of kinds) {
      for (const subject of [FIRM, INHOUSE, UNKNOWN]) {
        const label = actorLabel(subject, { name: 'Folake Adesina', kind })
        expect(label, `kind ${JSON.stringify(kind)} for ${subject}`).toEqual({
          text: 'Folake Adesina',
          mono: true,
          kind: 'raw',
        })
        expectNoPersonaLeak(label.text, `kind ${JSON.stringify(kind)}`)
      }
    }

    // 'absent' is reserved for a null subject and must never come back for a real one.
    expect(actorLabel(FIRM, { name: 'Folake Adesina', kind: 'absent' }).kind).not.toBe('absent')
  })

  // The two kinds the narrowing DOES accept, on a subject the persona table also holds,
  // with a name that disagrees with the table's. Proof the table was not consulted rather
  // than proof it happened to agree -- the existing 'C. Okafor (server)' case covers
  // 'person'; this covers 'system' and the other tenant's subject too.
  it('a resolved pair beats APP_PERSONAS for both accepted kinds, on both tenants', () => {
    const combos = [
      { subject: FIRM, name: 'Someone Else', kind: 'person', mono: false },
      { subject: INHOUSE, name: 'Someone Else', kind: 'person', mono: false },
      { subject: FIRM, name: 'Automated rule', kind: 'system', mono: false },
      { subject: INHOUSE, name: 'Automated rule', kind: 'system', mono: false },
    ]
    expect(combos).toHaveLength(4)

    for (const c of combos) {
      const label = actorLabel(c.subject, { name: c.name, kind: c.kind })
      expect(label, `${c.kind} for ${c.subject}`).toEqual({ text: c.name, mono: c.mono, kind: c.kind })
      expectNoPersonaLeak(label.text, `${c.kind} for ${c.subject}`)
    }
  })

  // The literal 'system' subject is special-cased ONLY on the unresolved path. A member
  // literally named System, or any server answer at all, still wins -- the special case
  // is a fallback for the five pairless callers, never an override of the server.
  it('the literal system subject does not override a server answer', () => {
    expect(actorLabel('system', { name: 'System Adeyemi', kind: 'person' })).toEqual({
      text: 'System Adeyemi',
      mono: false,
      kind: 'person',
    })
    expect(actorLabel('system', { name: 'system', kind: 'raw' })).toEqual({
      text: 'system',
      mono: true,
      kind: 'raw',
    })
    // And unresolved, it is still the friendly literal.
    expect(actorLabel('system')).toEqual({ text: 'System', mono: false, kind: 'system' })
  })

  // CHARACTERISATION of two shapes the type system forbids but a wire can still deliver.
  // Neither is a live defect today -- reported, not fixed, because fixing implementation
  // is not QA's to do:
  //   1. subject '' -- kept off the history card by a DB CHECK, not by this function
  //      (migrations/20260714111246_invoice_status_history.sql:53,
  //      `CHECK (char_length(actor) > 0)`). actorLabel itself returns an empty label.
  //   2. a missing wire field -- `{name: undefined}` is not '', so the guard passes it
  //      through and the cell renders nothing. Only reachable under API/SPA version skew,
  //      where a new bundle reads an old server's history rows.
  // Both stay non-leaking, which is the property that actually matters.
  it('pins the two blank-label shapes the wire contract forbids', () => {
    expect(actorLabel('')).toEqual({ text: '', mono: true, kind: 'raw' })
    expect(actorLabel('', { name: '', kind: 'raw' })).toEqual({ text: '', mono: true, kind: 'raw' })

    const skewed = actorLabel(FIRM, { name: undefined as unknown as string, kind: undefined as unknown as string })
    expect(skewed.text, 'a missing actor_name renders nothing rather than a persona').toBeUndefined()
    expect(skewed).toEqual({ text: undefined, mono: true, kind: 'raw' })
  })

  // AC #6's mechanism, asserted directly: the parameter is OPTIONAL, so passing nothing
  // and passing undefined are the same call, and the five pairless callers keep compiling.
  it('omitting the pair and passing undefined are the same call', () => {
    const subjects = [FIRM, INHOUSE, 'system', UNKNOWN, 'not-a-uuid', '']
    expect(subjects.length).toBeGreaterThan(0)
    for (const subject of subjects) {
      expect(actorLabel(subject, undefined), `undefined pair for ${subject}`).toEqual(actorLabel(subject))
    }
    expect(actorLabel(null, undefined)).toEqual(actorLabel(null))
    // NOT actorLabel.length -- a TS optional parameter still counts toward it (it is 2),
    // so that reads as a required parameter. tsc is the real optionality oracle; the
    // source scan below is the arity one.
  })

  // AC #6 (D-23/D-24), mechanically, over the WHOLE app tree: four call sites pass the
  // resolved pair, four pass the subject alone. Scanning a hand-picked file list cannot see
  // a FIFTH surface acquiring a pair silently, so the caller set is asserted too -- a new
  // caller fails here instead of hiding.
  it('every actorLabel call site under src: four pass a resolved pair, four pass one argument', () => {
    const SRC_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

    function walk(dir: string): string[] {
      return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
        const full = join(dir, e.name)
        if (e.isDirectory()) return e.name === 'node_modules' ? [] : walk(full)
        return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [full] : []
      })
    }

    // Test files are excluded because they are not surfaces; actor.ts stays IN, its own
    // declaration renamed so it cannot read as a call.
    const scrub = (src: string) =>
      src
        .replace(/\/\*[\s\S]*?\*\//g, '')
        .replace(/\/\/.*$/gm, '')
        .replace(/function\s+actorLabel\s*\(/g, 'function DECLARED(')

    const expected: Record<string, { one: number; two: number; pair: string }> = {
      'components/ActorCell.tsx': { one: 0, two: 1, pair: 'actor_name' },
      'components/AuditFilterCard.tsx': { one: 0, two: 1, pair: 'f.name' },
      'components/InvoiceDetail.tsx': { one: 2, two: 0, pair: '' },
      'components/SourceDocumentRail.tsx': { one: 1, two: 0, pair: '' },
      'components/SourceDocumentStates.tsx': { one: 0, two: 1, pair: 'createdByResolved' },
      'lib/invoiceStrip.ts': { one: 1, two: 1, pair: 'row.actor_name' },
    }

    const files = walk(SRC_DIR)
    expect(files.length, 'vacuity floor: the walk actually found the tree').toBeGreaterThan(50)

    const found = new Map<string, string[]>()
    for (const full of files) {
      const calls = [...scrub(readFileSync(full, 'utf8')).matchAll(/actorLabel\(([^)]*)\)/g)].map((m) => m[1])
      if (calls.length > 0) found.set(relative(SRC_DIR, full).split(sep).join('/'), calls)
    }

    expect([...found.keys()].sort(), 'the complete caller set -- a new one belongs in `expected`').toEqual(
      Object.keys(expected).sort(),
    )

    let totalOne = 0
    let totalTwo = 0
    for (const [rel, calls] of found) {
      const two = calls.filter((a) => a.includes(','))
      const one = calls.filter((a) => !a.includes(','))
      expect(one.length, `one-argument actorLabel calls in ${rel}`).toBe(expected[rel].one)
      expect(two.length, `two-argument actorLabel calls in ${rel}`).toBe(expected[rel].two)
      // Every pair-passing call names its own file's wire-derived source rather than
      // rebuilding one locally.
      for (const args of two) expect(args, `resolved pair in ${rel}`).toContain(expected[rel].pair)
      totalOne += one.length
      totalTwo += two.length
    }
    expect(totalOne, 'the four pairless callers').toBe(4)
    expect(totalTwo, 'the four pair-passing callers').toBe(4)
  })
})
