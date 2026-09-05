// BUG-14 deleted six blocked-reason nodes from the invoice detail page. Every absence
// assertion in the rendered suite passes just as well on a component that was never
// edited; this static scan is the oracle that keeps them gone.
//
// The closed world is PRODUCTION source, not the whole corpus. `InvoiceDetail.test.tsx`
// legitimately names all six -- its absence specs assert them by name and RETIRED_TESTIDS
// declares them -- so a whole-corpus equality would red on any new spec that mentions one.
// The honest durable claim: none of the six may appear in a non-test file under
// frontend/app/src except the two out-of-scope siblings that survive.
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('.', import.meta.url))
const SELF = fileURLToPath(import.meta.url)

// Re-declared, never imported from rowBlockedReasonRemoved.test.ts: importing from a
// .test.ts re-registers that file's describes here (the toggleProofGuards.test.ts precedent).
function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules') continue
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(path))
    else if (/\.tsx?$/.test(entry.name)) out.push(path)
  }
  return out
}

const rel = (path: string): string => relative(SRC, path).split(sep).join('/')

const FILES = sourceFiles(SRC)
const PROD_FILES = FILES.filter((path) => !/\.test\.tsx?$/.test(path))
// The BARE testid value, never the `data-testid="..."` declaration form: the bare value
// also matches every getByTestId locator and a `${r.id}` template literal.
const CORPUS = PROD_FILES.map((path) => [rel(path), readFileSync(path, 'utf8')] as const)

function filesContaining(needle: string): string[] {
  return CORPUS.filter(([, text]) => text.includes(needle))
    .map(([path]) => path)
    .sort()
}

// Lazy: a drifted SRC must red a test with its own message, not crash collection.
const detailSource = (): string => readFileSync(join(SRC, 'components', 'InvoiceDetail.tsx'), 'utf8')

// A legitimate new home must be DECLARED here by whoever adds it -- the same contract
// InvoiceDetail.test.tsx:4423-4431 takes for UNTOUCHED_TESTIDS.
const EXPECTED_HOMES: Record<string, string[]> = {
  'view-ubl-blocked-reason': [],
  'reject-blocked-reason': [],
  'resolve-outside-blocked-reason': [],
  'submit-blocked-reason': [],
  'approve-blocked-reason': ['components/ApprovalsView.tsx'],
  'revalidate-blocked-reason': ['components/ReviewRow.tsx'],
}

const NEEDLES = Object.keys(EXPECTED_HOMES)

describe('BUG-14: the scan population', () => {
  it('control: the scan reaches the SPA source tree', () => {
    expect(
      FILES.length,
      'the scan drifted off frontend/app/src -- every absence check below would be vacuous',
    ).toBeGreaterThanOrEqual(200)
  })

  it('control: the production filter keeps a real population and actually filters', () => {
    expect(PROD_FILES.length, 'the non-test population collapsed').toBeGreaterThanOrEqual(100)
    expect(PROD_FILES.length, 'the .test. filter matched nothing').toBeLessThan(FILES.length)
    expect(PROD_FILES.filter((p) => p.includes('.test.'))).toEqual([])
  })

  it('control: this file is scanned as a source file and excluded as a test file', () => {
    expect(FILES, 'the scan never reached this directory').toContain(SELF)
    expect(PROD_FILES, 'the test exclusion failed -- this file is not production source').not.toContain(SELF)
  })

  it('control: the six control testids are still found', () => {
    for (const id of ['view-ubl', 'detail-approve', 'detail-reject', 'edit-toggle', 'revalidate', 'detail-submit']) {
      expect(filesContaining(id).length, `the control testid ${id} vanished -- the scanner finds nothing`).toBeGreaterThan(0)
    }
  })

  it('control: the out-of-scope sibling reason testids are still found', () => {
    for (const id of ['approval-blocked-reason', 'delegation-blocked-reason', 'pager-blocked-reason']) {
      expect(filesContaining(id).length, `the surviving sibling ${id} is unreachable -- the absence claims are vacuous`).toBeGreaterThan(0)
    }
  })

  it('control: the declared homes are SRC-relative paths, not absolute', () => {
    const scanned = CORPUS.map(([path]) => path)
    for (const home of Object.values(EXPECTED_HOMES).flat()) {
      expect(scanned, `${home} matches no scanned path -- a relative()/separator mistake`).toContain(home)
    }
  })
})

describe('BUG-14: the six blocked-reason testids live in exactly their declared homes', () => {
  for (const needle of NEEDLES) {
    it(`${needle} lives in exactly its declared homes`, () => {
      const found = filesContaining(needle)
      expect(
        found,
        `${needle} was found in [${found.join(', ')}], declared [${EXPECTED_HOMES[needle].join(', ')}] -- ` +
          'a legitimate new home must be declared in EXPECTED_HOMES by whoever adds it',
      ).toEqual(EXPECTED_HOMES[needle])
    })
  }

  it('InvoiceDetail.tsx names none of the six', () => {
    const src = detailSource()
    expect(src.length, 'the detail component read empty -- the check below is vacuous').toBeGreaterThan(10_000)
    expect(src, 'control: the detail component is the file we think it is').toContain('data-testid="invoice-actions"')
    for (const needle of NEEDLES) {
      expect(src.includes(needle), `${needle} came back to InvoiceDetail.tsx`).toBe(false)
    }
  })
})

// The needles above only bite if a reintroduction reuses an OLD id name. An
// aria-describedby re-added under any other name dangles silently; this catches that.
describe('BUG-14: no control in the detail action cluster wires aria-describedby', () => {
  // Both edges pinned per control, so a match that stopped short cannot read as absent.
  const CONTROLS: Array<{ id: string; first: string; last: string; title: string | null }> = [
    {
      id: 'view-ubl',
      first: 'onClick={() => setUblOpen(true)}',
      last: '...(!inv.can_view_ubl ?',
      title: 'title={!inv.can_view_ubl ? (inv.ubl_blocked_reason ?? undefined) : undefined}',
    },
    {
      id: 'detail-approve',
      first: "onClick={() => toApprovePhase({ type: 'arm' })}",
      last: '...(!inv.can_approve',
      title: 'title={!inv.can_approve ? (inv.approve_blocked_reason ?? undefined) : undefined}',
    },
    {
      id: 'detail-reject',
      first: 'onClick={() => setRejectOpen(true)}',
      last: '...(!inv.can_reject || rejectOpen ?',
      title: 'title={!inv.can_reject ? (inv.reject_blocked_reason ?? undefined) : undefined}',
    },
    {
      id: 'edit-toggle',
      first: 'if (inv.can_edit) setEditing(true)',
      last: '...(!inv.can_edit ?',
      title: null, // [edit-gets-no-reason]: no edit_blocked_reason on the wire.
    },
    {
      id: 'revalidate',
      first: 'onClick={handleRevalidate}',
      last: '...(revalidateDisabled ?',
      title: 'title={!inv.can_revalidate ? (inv.revalidate_blocked_reason ?? undefined) : undefined}',
    },
    {
      id: 'detail-submit',
      first: "onClick={() => toSubmitPhase({ type: 'arm' })}",
      last: '...(!inv.can_submit ?',
      title: 'title={!inv.can_submit ? (inv.submit_blocked_reason ?? undefined) : undefined}',
    },
  ]

  // Terminates on a line holding only `>`; the JSX arrow functions make a bare `>` unusable.
  function attributeBlock(id: string): string | null {
    return detailSource().match(new RegExp(`data-testid="${id}"[\\s\\S]*?\\n\\s*>\\n`))?.[0] ?? null
  }

  for (const c of CONTROLS) {
    it(`control: ${c.id}'s attribute block is extracted whole`, () => {
      const block = attributeBlock(c.id)
      expect(block, `${c.id}'s block no longer matches -- the absence check below is vacuous`).not.toBeNull()
      expect(block, `the ${c.id} match stops short of its first attribute`).toContain(c.first)
      expect(block, `the ${c.id} match ends before its last attribute`).toContain(c.last)
    })
  }

  it('no control carries aria-describedby', () => {
    for (const c of CONTROLS) {
      expect(attributeBlock(c.id), `${c.id} wires an aria-describedby again`).not.toContain('aria-describedby')
    }
  })

  // [title-survives]: nothing else stops a sweep deleting both attributes at once.
  it('control: every control that carried a title still carries it', () => {
    for (const c of CONTROLS) {
      const block = attributeBlock(c.id)
      if (c.title == null) expect(block, `${c.id} gained a title`).not.toContain('title=')
      else expect(block, `${c.id} lost its title -- [title-survives] says it stays`).toContain(c.title)
    }
  })
})
