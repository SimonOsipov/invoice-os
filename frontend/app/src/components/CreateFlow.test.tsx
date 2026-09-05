// @vitest-environment jsdom
// RED specs (task-396, BUG-03-07 item 10, Mode A) — CreateFlow.tsx:72 renders a connector
// span after every step unconditionally, so the last step's connector dangles past it with
// nothing after it. Pins the post-fix contract (wrap in idx < steps.length - 1) before the
// executor makes the change. First test file for this component — no ctx-builder existed;
// createFlowCtx follows the repo's per-file local-helper convention (reportsCtx() in
// ReportsView.test.tsx, detailCtx() in InvoiceDetail.test.tsx).
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CreateStep, PlatformCtx } from '../types'
import { CreateFlow } from './CreateFlow'

// Handler surface enumerated by grepping `ctx\.` in CreateUpload.tsx and CreateForm.tsx —
// the two step components a 3-step ('upload') and a 1-step ('form') createStep actually
// render here. Neither mounts a useEffect or calls ctx.authedFetch, so no fetch mock is
// needed. `run.status: 'idle'` keeps runIsActive(run) false so the step router renders,
// not ImportProgress.
//
// EXTR-15-07 (task-833) added the third parameter rather than a second builder: `over` is
// spread LAST, so every existing two-arg call above is byte-for-byte unchanged.
function createFlowCtx(
  createStep: CreateStep,
  runKind: 'spreadsheet' | 'document' | null = null,
  over: Record<string, unknown> = {},
): PlatformCtx {
  const ctx = {
    createStep,
    runKind,
    run: { files: [], cursor: 0, status: 'idle' },
    closeCreate: () => {},
    mode: 'firm',
    active: { short: 'Lagos Freight', tin: '20184412-0001' },
    // CreateUpload
    pickedFiles: [],
    filesRefusal: null,
    importError: null,
    activeEntity: null,
    entitiesState: 'ready',
    entities: [],
    clients: [],
    addPickedFiles: () => {},
    removePickedFile: () => {},
    setSettingsTab: () => {},
    nav: () => {},
    readAllColumns: () => {},
    skipUpload: () => {},
    // CreateForm
    draft: { number: '', buyer: '', buyerTin: '', date: '', currency: 'NGN', items: [] },
    filing: false,
    filingError: null,
    updateDraft: () => {},
    updateItemDesc: () => {},
    updateItem: () => {},
    removeItem: () => {},
    addItem: () => {},
    fileDraft: () => {},
    // ReviewBatch — the document path's step 2. Enumerated by grepping `ctx\.` in
    // ReviewBatch.tsx; an empty id list keeps its own fetch effect from firing.
    reviewBatchIds: [],
    authedFetch: async () => {
      throw new Error('no fetch expected — reviewBatchIds is empty')
    },
    openImportedInvoice: () => {},
    restartImport: () => {},
    ...over,
  }
  return ctx as unknown as PlatformCtx
}

// The connector span is unmarked (no class, no testid) — width: 36 / height: 1 is the
// literal pair CreateFlow.tsx:72 itself is pinned by (the plan's own grep target), and
// jsdom parses the inline style attribute into these, so this selector cannot collide with
// the step-number span (width: 22, height: 22) sitting next to it.
function connectorCount(container: HTMLElement): number {
  return Array.from(container.querySelectorAll('span')).filter(
    (s) => s.style.width === '36px' && s.style.height === '1px',
  ).length
}

describe('CreateFlow — step-strip connector spans (BUG-03-07 item 10)', () => {
  afterEach(() => cleanup())

  it('3-step import path (createStep upload): exactly 2 connectors', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('upload')} />)
    expect(connectorCount(container)).toBe(2)
  })

  it('1-step typed path (createStep form): exactly 0 connectors', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('form')} />)
    expect(connectorCount(container)).toBe(0)
  })
})

// The header forks with the run: wizardHeader(step, runKind) shipped in EXTR-09-06, and
// EXTR-09-07 wired CreateFlow to pass the run kind through. Authored RED against a
// CreateFlow that called wizardHeader with the step alone and rendered the 3-step
// spreadsheet strip for a document run.
describe('CreateFlow — the header follows the run kind (EXTR-09-07, AC-2)', () => {
  afterEach(() => cleanup())

  function stepLabels(container: HTMLElement): string[] {
    // The step-number span is width/height 22 (connectorCount above pins the 36x1
    // connector against exactly this pair); its label is the sibling right after it.
    return Array.from(container.querySelectorAll('span'))
      .filter((s) => s.style.width === '22px' && s.style.height === '22px')
      .map((s) => (s.nextElementSibling as HTMLElement | null)?.textContent ?? '')
  }

  it('FORK-HDR-1: a document run on the upload step renders the 2-step Import · Review strip', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('upload', 'document')} />)
    const labels = stepLabels(container)

    expect(labels.length).toBeGreaterThan(0)
    expect(labels).toEqual(['Import', 'Review'])
    expect(connectorCount(container)).toBe(1)
  })

  it('FORK-HDR-2 (AC-1 control): a spreadsheet run still renders the shipped 3-step strip', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('upload', 'spreadsheet')} />)

    expect(stepLabels(container)).toEqual(['Import', 'Map', 'Review'])
    expect(connectorCount(container)).toBe(2)
  })

  it('FORK-HDR-3: the shared Review step lands at index 1 of 2 on the document path, not 2 of 3', () => {
    const { container } = render(<CreateFlow ctx={createFlowCtx('review', 'document')} />)

    expect(stepLabels(container)).toEqual(['Import', 'Review'])
  })
})

// ============================================================================
// RED specs (EXTR-15-07, task-833, Mode A) — the route out of a dead-lettered document.
//
// A document that dead-letters is stored and can never be re-extracted. Today the run
// lands on the picker with a red sentence and NOTHING to click (CreateFlow.tsx:108-116
// renders each failure as a bare <span> inside one shared flex div). This gives the user
// a route to manual entry, and the invoice they type keeps the document's provenance.
//
// CONTRACT THESE SPECS PIN, and which the executor owes:
//   - runFailures' rows carry `documentId?: string` (importRun.test.ts's HO-7 owns that).
//   - each failure renders as its own bordered row, data-testid="document-failure-row".
//   - the wrapper at CreateFlow.tsx:109 becomes the card, data-testid="document-failures-card".
//   - the control is `ctx.enterByHand(documentId)`. Named after skipUpload, the shipped
//     handler that also lands on createStep 'form'. If EXTR-15-07 wires it under another
//     name, THESE specs are what change — the choice is stated, not assumed.
//
// Copy: 'Enter it by hand' is fixed by AC-3 and pinned literally. The no-entity REASON
// copy is deliberately NOT pinned: no constant for it exists yet, and importing one that
// does not exist would make this file fail to COLLECT rather than fail an assertion. The
// four-layer disabled recipe is asserted structurally instead (disabled + title +
// aria-describedby resolving to a rendered, non-empty node whose text equals the title),
// which is the part APPR-16 proved a `title=` alone cannot satisfy in Chromium.

const HANDOFF_DOCUMENT_ID = '9a4c1e77-2f80-4c53-b6d2-51e0a3f8cc19'

const FAILURE_CARD = 'document-failures-card'
const FAILURE_ROW = 'document-failure-row'
const HAND_OFF_LABEL = 'Enter it by hand'

/**
 * A settled document run whose files all failed — the state markRunFailed leaves behind
 * when applyRoute takes the `none` route, which is the ONLY way this surface is reached.
 *
 * `documentId` is set through a cast: FileOutcome does not carry the field yet, and the
 * one deliberate typecheck red for that lives in importRun.test.ts's HO-2.
 */
function failedDocumentRun(files: { id: string; name: string; message: string; documentId?: string }[]) {
  return {
    files: files.map((f) => ({
      id: f.id,
      name: f.name,
      groupId: 'g1',
      outcome: { kind: 'failed', message: f.message, ...(f.documentId ? { documentId: f.documentId } : {}) },
    })),
    cursor: files.length,
    status: 'failed',
  }
}

const STORED = { id: 'f1', name: 'scanned.pdf', message: 'The scan was too poor to read.', documentId: HANDOFF_DOCUMENT_ID }
const NEVER_UPLOADED = { id: 'f2', name: 'lost.pdf', message: 'network: connection reset' }

function renderFailures(
  files: { id: string; name: string; message: string; documentId?: string }[],
  over: Record<string, unknown> = {},
) {
  return render(
    <CreateFlow
      ctx={createFlowCtx('documents', 'document', {
        run: failedDocumentRun(files),
        activeEntity: { id: 'e-1', name: 'Lagos Freight Ltd', tin: '12345678-0001' },
        enterByHand: () => {},
        ...over,
      })}
    />,
  )
}

/**
 * ceiling: jsdom loads no stylesheets, so this reads the inline/attribute layer only. It
 * catches the defect APPR-16 shipped — a `title=` with no rendered text beside it — but a
 * class-driven `display:none` would slip past. The deployed oracle for that is subtask
 * 13's topology sweep (AC-10), which measures real boxes.
 */
function visibleText(el: HTMLElement | null): string {
  if (el === null) return ''
  if (el.hidden) return ''
  const s = el.style
  if (s.display === 'none' || s.visibility === 'hidden' || s.opacity === '0') return ''
  return (el.textContent ?? '').trim()
}

function handOffButtons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button')).filter((b) => (b.textContent ?? '').trim() === HAND_OFF_LABEL)
}

describe('CreateFlow — a dead-lettered document offers manual entry (HO-3/HO-4, AC-3/AC-4/AC-5)', () => {
  afterEach(() => cleanup())

  it('HO-3: two failures, one with a stored document and one without — exactly one button', () => {
    const { container } = renderFailures([STORED, NEVER_UPLOADED])

    // Population floor FIRST: without it "exactly one button" would also be satisfied by a
    // surface that rendered one row and dropped the other entirely.
    const rows = container.querySelectorAll(`[data-testid="${FAILURE_ROW}"]`)
    expect(rows, 'both failures must render before any button is counted').toHaveLength(2)

    expect(handOffButtons(container)).toHaveLength(1)
  })

  it('HO-4a: the row with no document id renders its name and reason but no control at all', () => {
    const { container } = renderFailures([NEVER_UPLOADED])

    // Control needle: the row rendered. An absence-only assertion would pass on a blank
    // screen, which is the very defect this story exists to close.
    const rows = Array.from(container.querySelectorAll<HTMLElement>(`[data-testid="${FAILURE_ROW}"]`))
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain(NEVER_UPLOADED.name)
    expect(rows[0].textContent).toContain(NEVER_UPLOADED.message)

    // No button, not a disabled one: there is nothing to hand off (AC-4).
    expect(rows[0].querySelectorAll('button')).toHaveLength(0)
  })

  it('HO-4b: with no resolved entity the button is DISABLED with a visible reason, never hidden', () => {
    // The predicate is `activeEntity !== null` — the one every filing gate reads
    // (CreateUpload.tsx:98, fileDraftGate). Not entitiesState, not a client id.
    const { container } = renderFailures([STORED], { activeEntity: null })

    const buttons = handOffButtons(container)
    expect(buttons, 'the button must still RENDER without an entity — disabled-with-reason, never hidden').toHaveLength(1)
    const button = buttons[0]
    expect(button.disabled).toBe(true)

    const describedBy = button.getAttribute('aria-describedby')
    expect(describedBy, 'a disabled control must point at its reason').toBeTruthy()

    const reason = container.ownerDocument.getElementById(String(describedBy)) as HTMLElement | null
    expect(reason, `aria-describedby="${describedBy}" resolves to no node in the document`).not.toBeNull()

    // APPR-16: a title= alone is invisible in Chromium. The reason must be RENDERED text
    // beside the button, and the title must say the same thing rather than a second copy.
    const text = visibleText(reason)
    expect(text.length, 'the reason node is empty or hidden — a title= alone is invisible').toBeGreaterThan(0)
    expect(button.getAttribute('title')).toBe(text)

    // And it is scoped to the failure row, not a stray sentence elsewhere on the step
    // (CreateUpload renders its own no-entity prose, which must not satisfy this).
    const row = container.querySelector<HTMLElement>(`[data-testid="${FAILURE_ROW}"]`)
    expect(row, 'the failure row must render').not.toBeNull()
    expect(row!.contains(reason), 'the reason must sit inside the row it refuses').toBe(true)
  })

  it('HO-4c (control): with an entity resolved the same button is ENABLED and drops the reason', () => {
    // Without this leg HO-4b would also pass on a button that is permanently disabled.
    const { container } = renderFailures([STORED])

    const buttons = handOffButtons(container)
    expect(buttons).toHaveLength(1)
    expect(buttons[0].disabled).toBe(false)
    expect(buttons[0].getAttribute('aria-describedby')).toBeNull()
    expect(buttons[0].getAttribute('title')).toBeNull()
  })
})

describe('CreateFlow — the hand-off carries the document and clears nothing (HO-5, AC-6/AC-7)', () => {
  afterEach(() => cleanup())

  it('HO-5: clicking calls enterByHand once, with THIS row’s document id', () => {
    const enterByHand = vi.fn()
    // Two failures, so a handler that ignores its argument and hard-codes "the first
    // document" cannot pass: the button under test belongs to the SECOND row.
    const second = { id: 'f3', name: 'other.pdf', message: 'The scan was too poor to read.', documentId: 'a0000000-0000-4000-8000-00000000000b' }
    const { container } = renderFailures([NEVER_UPLOADED, second], { enterByHand })

    const buttons = handOffButtons(container)
    expect(buttons, 'only the row with a document offers the hand-off').toHaveLength(1)
    fireEvent.click(buttons[0])

    expect(enterByHand).toHaveBeenCalledTimes(1)
    expect(enterByHand).toHaveBeenCalledWith(second.documentId)
  })

  it('HO-5b (AC-7): the click reaches the hand-off and nothing that would discard the run', () => {
    const enterByHand = vi.fn()
    const restartImport = vi.fn()
    const closeCreate = vi.fn()
    const { container } = renderFailures([STORED], { enterByHand, restartImport, closeCreate })

    const buttons = handOffButtons(container)
    expect(buttons, 'nothing to click — the two absences below would be vacuous').toHaveLength(1)
    fireEvent.click(buttons[0])

    // Positive half first, so the two absences below are not the only claim.
    expect(enterByHand).toHaveBeenCalledTimes(1)
    expect(restartImport, 'restarting the import would wipe run.files and the failure list with it').not.toHaveBeenCalled()
    expect(closeCreate, 'closing the wizard would strand the user off the flow entirely').not.toHaveBeenCalled()
  })

  it('HO-5c (AC-11): the button stays clickable — a second hand-off is permitted and dispatches again', () => {
    // Nothing makes invoices.source_document_id unique (migrations/
    // 20260802163544_documents.sql:60-61 is a PARTIAL, non-unique index) and the
    // spreadsheet path already writes N invoices against one document. The guard against
    // filing the same invoice twice is the shipped duplicate-number outcome, asserted
    // server-side by internal/invoice/source_document_create_test.go's HO-11.
    const enterByHand = vi.fn()
    const { container } = renderFailures([STORED], { enterByHand })

    const buttons = handOffButtons(container)
    expect(buttons).toHaveLength(1)
    const button = buttons[0]
    fireEvent.click(button)
    fireEvent.click(button)

    expect(button.disabled, 'a filing does not consume the hand-off').toBe(false)
    expect(enterByHand).toHaveBeenCalledTimes(2)
    expect(enterByHand.mock.calls).toEqual([[HANDOFF_DOCUMENT_ID], [HANDOFF_DOCUMENT_ID]])
  })
})

describe('CreateFlow — each failure is its own row inside one card (HO-8/HO-10, AC-8/AC-9)', () => {
  afterEach(() => cleanup())

  it('HO-8: two rows, each containing its OWN name, reason and control — never a shared parent', () => {
    const { container } = renderFailures([STORED, NEVER_UPLOADED])

    const rows = Array.from(container.querySelectorAll<HTMLElement>(`[data-testid="${FAILURE_ROW}"]`))
    expect(rows).toHaveLength(2)

    // Falsification: the shipped flat list (CreateFlow.tsx:109) puts every filename and
    // reason under ONE div. Asserting containment per row is what a shared parent fails —
    // a scan of the card's whole textContent would pass on it.
    expect(rows[0].textContent).toContain(STORED.name)
    expect(rows[0].textContent).toContain(STORED.message)
    expect(rows[0].textContent, "row 0 must not hold row 1's filename").not.toContain(NEVER_UPLOADED.name)

    expect(rows[1].textContent).toContain(NEVER_UPLOADED.name)
    expect(rows[1].textContent).toContain(NEVER_UPLOADED.message)
    expect(rows[1].textContent, "row 1 must not hold row 0's filename").not.toContain(STORED.name)

    // The control belongs to the row that earned it, not to the list.
    expect(handOffButtons(rows[0])).toHaveLength(1)
    expect(handOffButtons(rows[1])).toHaveLength(0)

    // And every row sits inside the card (AC-9). The DOM half of AC-10(a); subtask 13
    // measures the same relationship geometrically on the deployed build.
    const card = container.querySelector<HTMLElement>(`[data-testid="${FAILURE_CARD}"]`)
    expect(card, 'the failures card must render').not.toBeNull()
    for (const [i, row] of rows.entries()) {
      expect(card!.contains(row), `row ${i} is not inside the failures card`).toBe(true)
    }
  })

  it('HO-10: with no failures, NEITHER testid renders — the card is not an always-mounted empty box', () => {
    // Control needle: the documents step really rendered. Without it the two zeros below
    // would also be reported by a CreateFlow that crashed or rendered nothing at all.
    const { container } = renderFailures([])
    expect(container.textContent, 'the documents step did not render — the absences below would be vacuous').toContain(
      'Import invoices',
    )

    expect(container.querySelectorAll(`[data-testid="${FAILURE_CARD}"]`)).toHaveLength(0)
    expect(container.querySelectorAll(`[data-testid="${FAILURE_ROW}"]`)).toHaveLength(0)
    expect(handOffButtons(container)).toHaveLength(0)
  })

  it('HO-10b (population floor): one failure renders exactly one card and one row', () => {
    const { container } = renderFailures([STORED])
    expect(container.querySelectorAll(`[data-testid="${FAILURE_CARD}"]`)).toHaveLength(1)
    expect(container.querySelectorAll(`[data-testid="${FAILURE_ROW}"]`)).toHaveLength(1)
  })
})

// ---------------------------------------------------------------------------
// HO-9 (AC-8/AC-9): the two style recipes are COPIED, not invented.
//
// Both source literals are read from their own files at test time, so a change on either
// side reds rather than drifting. No new design-system value may enter this surface.

// process.cwd(), not `new URL(rel, import.meta.url)`: under the jsdom environment Vite
// rewrites a `new URL(..., import.meta.url)` built from a template literal into an ASSET
// import and refuses the whole module ("Denied ID .../CLAUDE.md?url"), which is a
// collection error rather than a red spec. documentRun.test.ts's readRepoFile already
// uses this idiom; cwd is frontend/app under `pnpm --filter @invoice-os/app test`.
function repoSource(rel: string): string {
  const source = readFileSync(join(process.cwd(), rel), 'utf8')
  expect(source.length, `the scan read nothing from ${rel}`).toBeGreaterThan(500)
  return source
}

/** Wrapping is prettier's, not the author's — compare the declaration, not its line breaks. */
function normalize(literal: string): string {
  return literal.replace(/\s+/g, ' ').trim()
}

/** The `{ ... }` that starts at `open`, balanced, exclusive of the outer braces. */
function braced(source: string, open: number): string {
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{') depth++
    else if (source[i] === '}') {
      depth--
      if (depth === 0) return source.slice(open + 1, i)
    }
  }
  throw new Error(`unbalanced style literal at offset ${open}`)
}

/** Every `style={{ ... }}` literal in a source file, in source order. */
function styleLiterals(source: string): string[] {
  const out: string[] = []
  const needle = 'style={{'
  for (let at = source.indexOf(needle); at !== -1; at = source.indexOf(needle, at + 1)) {
    out.push(normalize(braced(source, at + 'style={'.length)))
  }
  return out
}

/**
 * The style literal on the JSX opening tag that carries `data-testid="<id>"`.
 *
 * Walks back to the tag's own `<` and forward to its `>`, so attribute ORDER does not
 * matter — a spec that assumed `style` follows `data-testid` would red on a reorder that
 * changed nothing.
 */
function styleOfTestid(source: string, testid: string): string {
  const marker = source.indexOf(`data-testid="${testid}"`)
  expect(marker, `no element carries data-testid="${testid}"`).toBeGreaterThan(-1)

  const open = source.lastIndexOf('<', marker)
  expect(open, `data-testid="${testid}" is not inside a JSX tag`).toBeGreaterThan(-1)

  let depth = 0
  let close = -1
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{') depth++
    else if (source[i] === '}') depth--
    else if (source[i] === '>' && depth === 0) {
      close = i
      break
    }
  }
  expect(close, `the tag carrying data-testid="${testid}" never closes`).toBeGreaterThan(open)

  const tag = source.slice(open, close)
  const at = tag.indexOf('style={{')
  expect(at, `the element carrying data-testid="${testid}" has no inline style literal`).toBeGreaterThan(-1)
  return normalize(braced(tag, at + 'style={'.length))
}

describe('CreateFlow — the row and card recipes are byte-copies of their shipped siblings (HO-9, AC-8/AC-9)', () => {
  it('HO-9a: the failure row reuses ReviewBatch’s files-strip row literal verbatim', () => {
    const reviewBatch = repoSource('src/components/ReviewBatch.tsx')
    const createFlow = repoSource('src/components/CreateFlow.tsx')

    const shipped = styleOfTestid(reviewBatch, 'review-files-strip-row')
    // Self-check: a gutted or restyled source must fail here rather than make the
    // comparison below a comparison of two empty strings.
    expect(shipped, 'the shipped row recipe lost its border').toContain("border: '1px solid var(--line-1)'")
    expect(shipped, 'the shipped row recipe lost its padding').toContain('padding:')

    expect(styleOfTestid(createFlow, FAILURE_ROW)).toBe(shipped)
  })

  it('HO-9b: the failures card reuses CreateUpload’s own outer card literal verbatim', () => {
    const createUpload = repoSource('src/components/CreateUpload.tsx')
    const createFlow = repoSource('src/components/CreateFlow.tsx')

    // Selected semantically, not by line number: CreateUpload's card is the one literal in
    // that file declaring both a --radius-md corner and an overflow. The uniqueness
    // assertion is what makes this an anchor rather than a guess.
    const cards = styleLiterals(createUpload).filter(
      (s) => s.includes("borderRadius: 'var(--radius-md)'") && s.includes('overflow:'),
    )
    expect(cards, 'CreateUpload.tsx no longer has exactly one card literal to copy').toHaveLength(1)
    expect(cards[0], 'the shipped card recipe lost its surface colour').toContain("background: 'var(--bg-2)'")

    // Byte-equal means byte-equal: the card element carries the recipe and NOTHING else.
    // The wrapper's current display/flexDirection/gap/marginTop belong on a child, not
    // here — otherwise no two cards in this app can be said to share a recipe.
    expect(styleOfTestid(createFlow, FAILURE_CARD)).toBe(cards[0])
  })
})
