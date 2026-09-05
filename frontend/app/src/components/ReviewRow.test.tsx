// @vitest-environment jsdom
// Component tests for Row; mirrors InvoiceDetail.test.tsx's fetch-mock + ctx-cast idiom.
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ImportBatch } from '../lib/importApi'
import {
  selectBlockedReason,
  skipReasonLabel,
  type InvoiceApproval,
  type InvoiceDetailRecord,
  type InvoiceListResponse,
  type InvoiceRecord,
} from '../lib/invoices'
import { ROW_EXPANSION_COPY, verdictPill } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { InvoicesList } from './InvoicesList'
import { REVIEW_GRID_COLUMNS, Row } from './ReviewRow'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function row(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-x',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-X',
    status: 'draft',
    issue_date: '2026-07-01T00:00:00Z',
    supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '00000000002',
    buyer_name: 'Beta Ltd',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-07-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    approval: null,
    rule_set_version: null,
    can_approve: false,
    approve_blocked_reason: null,
    ...over,
  }
}

function listRow(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return row({ id: 'inv-1', invoice_number: 'INV-1', status: 'failed', ...over })
}

function detailFixture(over: Partial<InvoiceDetailRecord> = {}): InvoiceDetailRecord {
  return {
    ...listRow(),
    qr_png_base64: null,
    can_edit: false,
    can_revalidate: true,
    revalidate_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
    can_view_ubl: true,
    ubl_blocked_reason: null,
    can_resolve_outside: false,
    resolve_outside_blocked_reason: null,
    can_approve: false,
    approve_blocked_reason: null,
    can_reject: false,
    reject_blocked_reason: null,
    ...over,
  }
}

function reviewRowCtx(): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', () => {}),
    openCreate: () => {},
    openImportedInvoice: () => {},
    invoiceQuery: '',
  }
  return ctx as unknown as PlatformCtx
}

function rowCtx(): PlatformCtx {
  return { authedFetch: createAuthedFetch(() => 'tok', vi.fn()) } as unknown as PlatformCtx
}

function mockGetInvoice(detail: InvoiceDetailRecord) {
  const fetchMock = vi.fn(() =>
    Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(detail) }),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderRow(over: Partial<InvoiceRecord> = {}, batches: ImportBatch[] = []) {
  render(
    <Row
      r={row(over)}
      batches={batches}
      checked={false}
      expanded={false}
      onToggleExpand={() => {}}
      onToggle={() => {}}
      ctx={reviewRowCtx()}
      base="https://gw"
      onChanged={() => {}}
    />,
  )
}

// The submit pair is not declared on InvoiceRecord yet, so the override type names it and
// the gate specs below stay value tests, never type tests.
type SubmitGateOver = Partial<InvoiceRecord> & {
  can_submit?: boolean
  submit_blocked_reason?: string | null
}

function gateRow(over: SubmitGateOver = {}): InvoiceRecord {
  return { ...row(), ...over } as InvoiceRecord
}

function renderGateRow(over: SubmitGateOver = {}) {
  render(
    <Row
      r={gateRow(over)}
      batches={[]}
      checked={false}
      expanded={false}
      onToggleExpand={() => {}}
      onToggle={() => {}}
      ctx={reviewRowCtx()}
      base="https://gw"
      onChanged={() => {}}
    />,
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

// QA gap-fill (task-413, BUG-05-04): the buyer-tin testid/colour on this surface was
// verified only by lib unit tests and code inspection (mutation-verify: deleting
// data-testid="buyer-tin" from ReviewRow.tsx reddened nothing). These render with
// `expanded=false` so the ExpandedFixPanel's own getInvoice fetch never engages.
describe('ReviewRow buyer TIN signal (task-413, BUG-05-04, AC-4)', () => {
  it('AC-4: null, empty and whitespace-only buyer TIN all read TIN MISSING in red', () => {
    const cases: Array<{ label: string; buyer_tin: string | null }> = [
      { label: 'null', buyer_tin: null },
      { label: 'empty string', buyer_tin: '' },
      { label: 'whitespace-only', buyer_tin: '   ' },
    ]

    for (const { label, buyer_tin } of cases) {
      renderRow({ buyer_tin })

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe('TIN MISSING')
      expect(tin.style.color, label).toBe('var(--status-red-text)')

      cleanup()
    }
  })

  it('AC-4/AC-5: a present buyer TIN, malformed or well-formed, renders the value in grey', () => {
    const cases: Array<{ label: string; buyer_tin: string }> = [
      { label: 'malformed', buyer_tin: 'BADTIN' },
      { label: 'well-formed', buyer_tin: '87654321-0002' },
    ]

    for (const { label, buyer_tin } of cases) {
      renderRow({ buyer_tin })

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe(buyer_tin)
      expect(tin.style.color, label).toBe('var(--fg-3)')

      cleanup()
    }
  })
})

// rowExpansionView (lib/reviewBatch.ts) sets keptReason from kept_as_is_at presence alone
// -- it structurally cannot gate on status (no status in its input) -- so the CONSUMER
// (ReviewRow.tsx) must gate the banner render itself.
describe('ReviewRow row-expansion: the kept banner is a draft-only concept, not resolved-failed', () => {
  it('T6-7: a resolved failed row, expanded, never shows review-kept-banner', async () => {
    mockGetInvoice(detailFixture({
      status: 'failed',
      kept_as_is_at: '2026-08-06T00:00:00Z',
      kept_as_is_by: 'someone',
      kept_as_is_reason: 'Filed manually with the tax authority.',
    }))

    render(
      <Row
        r={listRow({ status: 'failed' })}
        batches={[]}
        checked={false}
        expanded
        onToggleExpand={() => {}}
        onToggle={() => {}}
        ctx={rowCtx()}
        base="https://gw"
        onChanged={() => {}}
      />,
    )

    await screen.findByTestId('review-revalidate') // wait for the record to load before asserting absence
    expect(screen.queryAllByTestId('review-kept-banner')).toHaveLength(0)
  })

  it('T6-8: a kept blocked draft row, expanded, still shows review-kept-banner', async () => {
    mockGetInvoice(detailFixture({
      status: 'draft',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error', message: 'bad rate' }],
      kept_as_is_at: '2026-07-30T00:00:00Z',
      kept_as_is_by: 'someone',
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    }))

    render(
      <Row
        r={listRow({ status: 'draft' })}
        batches={[]}
        checked={false}
        expanded
        onToggleExpand={() => {}}
        onToggle={() => {}}
        ctx={rowCtx()}
        base="https://gw"
        onChanged={() => {}}
      />,
    )

    const banner = await screen.findByTestId('review-kept-banner')
    expect(banner.textContent).toContain(ROW_EXPANSION_COPY.keptPrefix)
    expect(banner.textContent).toContain('Buyer confirmed the discrepancy is intentional.')
  })
})

// QA Stage 4 gap-fill (task-500, APPR-08-09). ReviewRow.tsx's isRowSelectable call site
// had NO render oracle: reverting it alone to `r.status` reddened nothing but tsc, while
// the same revert in InvoicesList.tsx reddens its own parity spec. This is that spec's
// twin -- the other half of AC #3's two-call-site claim.
describe('ReviewRow: an open approval run disables the row checkbox (APPR-08-09, AC-3)', () => {
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  function selectBox(): HTMLInputElement {
    return screen.getByTestId('review-select') as HTMLInputElement
  }

  it('RR-appr-1: awaiting-approval disables, clear-validated enables -- the enabled leg is what pins the call site to the ROW', () => {
    renderRow({ status: 'validated', approval: openRun })
    expect(selectBox().disabled, 'validated + open run is not selectable').toBe(true)
    cleanup()

    // The discriminator: a status-only call site reads `.status` off a string, gets
    // undefined, and disables EVERY checkbox -- which the line above cannot tell apart.
    renderRow({ status: 'validated', approval: null })
    expect(selectBox().disabled, 'validated + no run stays selectable (AC #5)').toBe(false)
    cleanup()

    renderRow({ status: 'validated', approval: { ...openRun, run_state: 'approved' } })
    expect(selectBox().disabled, 'validated + approved run stays selectable').toBe(false)
  })

  it('RR-appr-2: parity -- the awaiting-approval checkbox is the SAME disabled control a draft row already renders, apart from the reason each one now states for itself', () => {
    renderRow({ status: 'draft', approval: null })
    const draftBox = selectBox()
    // `title` dropped from the shared shape: overruled by APPR-16 Core AC-2 (user,
    // 2026-08-16) -- draft and awaiting-approval now state two DIFFERENT reasons, so a
    // title still belongs in the parity claim but not with one shared value.
    const draftShape = {
      present: Boolean(draftBox),
      disabled: draftBox.disabled,
      label: draftBox.getAttribute('aria-label'),
    }
    cleanup()

    renderRow({ status: 'validated', approval: openRun })
    const awaitingBox = selectBox()

    expect({
      present: Boolean(awaitingBox),
      disabled: awaitingBox.disabled,
      label: awaitingBox.getAttribute('aria-label'),
    }, 'awaiting-approval renders exactly the draft row shape, apart from its own reason').toEqual(draftShape)
    expect(awaitingBox.getAttribute('title')).toBe(selectBlockedReason({ status: 'validated', approval: openRun }))
  })
})

// A blocked selection checkbox carries real `disabled`, an inline mute and its reason in
// `title` (APPR-16-02 Core AC-2). Each spec below pins the reason to the row that owns it.
describe('ReviewRow: the checkbox states its own blocked reason (APPR-16-02, Core AC-2 overrule)', () => {
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  function selectBox(): HTMLInputElement {
    return screen.getByTestId('review-select') as HTMLInputElement
  }

  it('A16-2a: validated + open run disables the checkbox and carries the reason in its title', () => {
    const shape = { status: 'validated' as const, approval: openRun }
    const reason = selectBlockedReason(shape)
    renderRow(shape)
    const box = selectBox()

    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBe(reason)
  })

  it('A16-2b: draft + no run renders the not-validated reason, not the approval one', () => {
    renderRow({ status: 'draft', approval: null })
    const title = selectBox().getAttribute('title')

    expect(title).toBe(skipReasonLabel('not_validated'))
    expect(title).not.toBe(skipReasonLabel('awaiting_approval'))
  })

  it('A16-2c: a selectable row is enabled and carries no title', () => {
    renderRow({ status: 'validated', approval: null })
    const box = selectBox()

    expect(box.disabled).toBe(false)
    expect(box.getAttribute('title')).toBeNull()
  })

  it('A16-2d: a post-submission row is disabled and silent -- the status pill is the explanation', () => {
    // The silence is the SERVER's own null: submitBlockedReason returns nil on every
    // status where canEdit is false (handlers.go), so no SPA status list is needed to
    // keep an accepted row -- even one with a lingering open run -- disabled and quiet.
    renderGateRow({ status: 'accepted', approval: openRun, can_submit: false, submit_blocked_reason: null })
    const box = selectBox()

    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBeNull()
  })

  it('A16-2f: parity -- ReviewRow and InvoicesList set a byte-identical title for the same row', async () => {
    // BOTH sides read the row's own sentence. Deriving `expected` from selectBlockedReason
    // compares the function under test with itself, and moving only one side would compare
    // a node against an attribute and pass while proving nothing.
    const PARITY_REASON = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'
    const shared = gateRow({
      id: 'inv-parity',
      status: 'validated',
      approval: openRun,
      can_submit: false,
      submit_blocked_reason: PARITY_REASON,
    })
    const expected = PARITY_REASON

    render(
      <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const reviewTitle = selectBox().getAttribute('title')
    cleanup()

    // InvoicesList reads its own gateway base from the env (InvoiceDetail.test.tsx's
    // beforeEach precedent) -- Row instead takes `base` as a prop, so no other test
    // here has needed this until now.
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
    mockRegisterFetch([shared])
    render(<InvoicesList ctx={registerCtx()} />)
    await screen.findByText(shared.invoice_number)
    const listTitle = (screen.getByTestId('invoice-select') as HTMLInputElement).getAttribute('title')

    expect(reviewTitle).toBe(expected)
    expect(listTitle).toBe(expected)
    // D-8: compared directly too, so a failure names WHICH two surfaces disagree.
    expect(reviewTitle).toBe(listTitle)
  })
})

// RED specs (Stage 2.5, Mode A) — the review row reads the wire, and it is the same wire
// the register reads.
describe('ReviewRow: the review row reads the wire submit gate (BUG-12)', () => {
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }
  // submitGate's role rung (internal/invoice/handlers.go) — new to both row surfaces.
  const ROLE_REASON = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'

  function selectBox(): HTMLInputElement {
    return screen.getByTestId('review-select') as HTMLInputElement
  }

  it('B12-7: the review row renders the same', () => {
    // Both polarities, each contradicting the status/approval rule.
    renderGateRow({ status: 'validated', approval: openRun, can_submit: true, submit_blocked_reason: null })
    expect(selectBox().disabled, 'the server cleared this row while its newest run is open').toBe(false)
    expect(selectBox().getAttribute('title')).toBeNull()
    cleanup()

    renderGateRow({ status: 'validated', approval: null, can_submit: false, submit_blocked_reason: ROLE_REASON })
    expect(selectBox().disabled, 'the server refused this row despite a clear status and no run').toBe(true)
    expect(selectBox().getAttribute('title')).toBe(ROLE_REASON)
  })

  it('B12-8: the role refusal reaches both surfaces', async () => {
    const shared = gateRow({
      id: 'inv-role',
      invoice_number: 'INV-ROLE',
      status: 'validated',
      approval: null,
      can_submit: false,
      submit_blocked_reason: ROLE_REASON,
    })

    render(
      <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const reviewTitle = selectBox().getAttribute('title')
    const reviewDisabled = selectBox().disabled
    cleanup()

    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
    mockRegisterFetch([shared])
    render(<InvoicesList ctx={registerCtx()} />)
    await screen.findByText(shared.invoice_number)
    const listBox = screen.getByTestId('invoice-select') as HTMLInputElement

    expect(reviewDisabled).toBe(true)
    expect(reviewTitle).toBe(ROLE_REASON)
    expect(listBox.disabled).toBe(true)
    expect(listBox.getAttribute('title')).toBe(ROLE_REASON)
  })
})

// Replaces the A06-6 tripwire (APPR-12-06, [selectable-parity-not-new-copy]), which
// pinned the opposite: silence on this row. APPR-16 Core AC-2 overrules it.
describe('ReviewRow: the checkbox now states its own reason, retargeting [selectable-parity-not-new-copy] (APPR-16-02)', () => {
  it('A16-2g: the retargeted tripwire -- an awaiting-approval row states its reason in title, not silence', () => {
    const openRun: InvoiceApproval = {
      run_state: 'open',
      pending_ord: 1,
      pending_role_title: 'Reviewer',
      pending_holder_warn: false,
      due_at: null,
      overdue: false,
    }
    const shape = { status: 'validated' as const, approval: openRun }
    const reason = selectBlockedReason(shape)
    renderRow(shape)
    const box = screen.getByTestId('review-select') as HTMLInputElement

    expect(box.getAttribute('title'), 'A06-6 pinned this null; APPR-16 Core AC-2 overrules it').toBe(reason)
  })
})

// QA adversarial (Stage 4, Mode B, task-535): two cases A16-2a..g don't cover --
// cross-row reason pairing, and a live blocked-to-selectable transition.
describe('ReviewRow: adversarial coverage on the checkbox reason (APPR-16-02, QA Stage 4)', () => {
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  it("A16-2h: two blocked rows with DIFFERENT reasons each carry their own title, not the sibling's", () => {
    render(
      <>
        <Row r={row({ id: 'inv-draft', status: 'draft', approval: null })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={row({ id: 'inv-awaiting', status: 'validated', approval: openRun })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [draftBox, awaitingBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]
    const draftReason = draftBox.getAttribute('title')
    const awaitingReason = awaitingBox.getAttribute('title')

    // One sentence reused for both rows would pass whichever assertion happens to match
    // -- pinning both directions closes that gap.
    expect(draftReason).toBe(skipReasonLabel('not_validated'))
    expect(awaitingReason).toBe(skipReasonLabel('awaiting_approval'))
    expect(draftReason).not.toBe(awaitingReason)
  })

  it('A16-2i: a row transitioning from blocked to selectable drops its title, not left stale', () => {
    const shared = row({ id: 'inv-transition', status: 'draft', approval: null })
    const { rerender } = render(
      <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const box = screen.getByTestId('review-select') as HTMLInputElement
    // The first render must really be blocked, or the absence half below is vacuous.
    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBe(skipReasonLabel('not_validated'))

    rerender(
      <Row r={{ ...shared, status: 'validated' }} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )

    expect(box.disabled).toBe(false)
    expect(box.getAttribute('title')).toBeNull()
  })
})

// Element children only -- a text node is neither a child element nor a grid item.
// Counted against a sibling row, so these two say nothing about the absolute width.
describe('BUG-09: a blocked review row costs no extra grid line', () => {
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  function renderPair(blockedOver: Partial<InvoiceRecord>, cleanOver: Partial<InvoiceRecord>) {
    render(
      <>
        <Row r={row({ id: 'inv-blocked', ...blockedOver })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={row({ id: 'inv-clean', ...cleanOver })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [blockedRow, cleanRow] = screen.getAllByTestId('review-row')
    const [blockedBox, cleanBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]
    return { blockedRow, cleanRow, blockedBox, cleanBox }
  }

  it('B09-3: a not-validated review row renders the same grid children as a selectable one, and keeps its title', () => {
    const { blockedRow, cleanRow, blockedBox, cleanBox } = renderPair(
      { status: 'draft', approval: null },
      { status: 'validated', approval: null },
    )

    // Non-vacuity: one row really blocked, the other really selectable -- two equally
    // wrong rows would otherwise satisfy the count below.
    expect(blockedBox.disabled).toBe(true)
    expect(blockedBox.getAttribute('title')).toBe(skipReasonLabel('not_validated'))
    expect(cleanBox.disabled).toBe(false)

    expect(blockedRow.children.length).toBe(cleanRow.children.length)
  })

  it('B09-4: an awaiting-approval review row renders the same grid children as a selectable one', () => {
    const { blockedRow, cleanRow, blockedBox, cleanBox } = renderPair(
      { status: 'validated', approval: openRun },
      { status: 'validated', approval: null },
    )

    expect(blockedBox.disabled).toBe(true)
    expect(blockedBox.getAttribute('title')).toBe(skipReasonLabel('awaiting_approval'))
    expect(cleanBox.disabled).toBe(false)

    expect(blockedRow.children.length).toBe(cleanRow.children.length)
  })
})

// B09-3/B09-4 are both RELATIVE: an edit that widens BOTH rows keeps them green, and they
// count row-level children only, so a reason re-added INSIDE a cell is invisible to them.
describe('BUG-09 QA: the deleted line cannot come back through a blind spot', () => {
  const REVIEW_CELLS = 7
  // A reason sentence's clause before the em dash; the whole sentence if it has none.
  const lead = (reason: string) => reason.split('—')[0].trim()
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  it("QA-B09-6: a blocked review row renders exactly SEVEN grid children, pinned as a literal and against the grid's own tracks", () => {
    renderRow({ status: 'draft', approval: null })

    // Non-vacuity: the row must really be blocked.
    expect((screen.getByTestId('review-select') as HTMLInputElement).getAttribute('title')).toBe(skipReasonLabel('not_validated'))

    // A second, independent denominator: the literal cannot drift away from the grid.
    expect(REVIEW_GRID_COLUMNS.trim().split(/\s+/).length, 'the grid declares a track per cell').toBe(REVIEW_CELLS)
    expect(screen.getByTestId('review-row').children.length, 'a blocked row is checkbox + six cells, nothing more').toBe(REVIEW_CELLS)
  })

  it('QA-B09-7: a blocked review row prints its reason nowhere in its own text, at any nesting depth', () => {
    render(
      <>
        <Row r={row({ id: 'inv-draft', status: 'draft', approval: null })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={row({ id: 'inv-awaiting', status: 'validated', approval: openRun })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [draftRow, awaitingRow] = screen.getAllByTestId('review-row')
    const [draftBox, awaitingBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]

    // Non-vacuity: each row really holds its OWN sentence in `title`, so both strings are
    // on the page -- just never as rendered text.
    expect(draftBox.getAttribute('title')).toBe(skipReasonLabel('not_validated'))
    expect(awaitingBox.getAttribute('title')).toBe(skipReasonLabel('awaiting_approval'))

    expect(draftRow.textContent, 'the reason is back on screen, nested somewhere the child count cannot see').not.toContain(skipReasonLabel('not_validated'))
    expect(awaitingRow.textContent).not.toContain(skipReasonLabel('awaiting_approval'))

    // The lead phrase too, so a TRUNCATED re-add cannot slip past exact containment.
    // Derived, never authored here -- skipReasonLabel stays the sole source of the copy.
    // The status pills read DRAFT/VALIDATED, so neither phrase collides with one.
    expect(draftRow.textContent).not.toContain(lead(skipReasonLabel('not_validated')))
    expect(awaitingRow.textContent).not.toContain(lead(skipReasonLabel('awaiting_approval')))
  })

  it('QA-B09-8: the KEPT badge and the source-file line nest inside their own cells, so the busiest row is still seven wide', () => {
    const busiest = {
      status: 'draft' as const,
      approval: null,
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Client accepted as-is',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error' as const, message: 'bad vat' }],
      import_batch_id: 'b1',
    }
    // showsSourceFile needs more than one batch; only b1's filename is ever rendered.
    const batches = [
      { id: 'b1', filename: 'july-run.csv' },
      { id: 'b2', filename: 'august-run.csv' },
    ] as ImportBatch[]
    renderRow(busiest, batches)

    const rowEl = screen.getByTestId('review-row')
    const verdictCell = screen.getByTestId('review-verdict')
    const sourceFile = screen.getByTestId('review-row-source-file')
    // Derived, never authored here -- verdictPill owns every badge label.
    const badgeLabel = verdictPill(busiest).badges[0].label

    // Non-vacuity: this row really does carry both extras.
    expect(within(verdictCell).getByText(badgeLabel)).not.toBeNull()
    expect(sourceFile.textContent).toBe('july-run.csv')

    // Containment, not a child index -- the verdict cell is index 5 of 7, the chevron follows it.
    expect(verdictCell.parentElement, 'the verdict cell is a direct child of the row').toBe(rowEl)
    expect(rowEl.contains(sourceFile)).toBe(true)
    expect(Array.from(rowEl.children), 'the source-file line must stay inside the invoice-number cell').not.toContain(sourceFile)
    expect(rowEl.children.length, 'two extras that both nest cannot widen the row').toBe(REVIEW_CELLS)
  })

  // QA-B09-6 pins the COLLAPSED row only. ExpandedFixPanel is a sibling of the row div
  // today, so the count is expansion-independent -- nothing pinned that, and nesting the
  // panel inside the row would put an eighth child on a blocked row unseen.
  it('QA-B09-9: an EXPANDED blocked row is still exactly SEVEN grid children -- the fix panel is a sibling, not a cell', async () => {
    mockGetInvoice(detailFixture({
      status: 'draft',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error', message: 'bad rate' }],
    }))
    render(
      <Row
        r={listRow({ status: 'draft' })}
        batches={[]}
        checked={false}
        expanded
        onToggleExpand={() => {}}
        onToggle={() => {}}
        ctx={rowCtx()}
        base="https://gw"
        onChanged={() => {}}
      />,
    )

    // Non-vacuity, both halves: the panel really rendered, and the row really is blocked.
    await screen.findByTestId('review-revalidate')
    expect((screen.getByTestId('review-select') as HTMLInputElement).getAttribute('title')).toBe(skipReasonLabel('not_validated'))

    expect(screen.getByTestId('review-row').children.length, 'expanding a blocked row widened it').toBe(REVIEW_CELLS)
  })
})

// Minimal register ctx/fetch for the ReviewRow/InvoicesList parity check (A16-2f) --
// mirrors InvoiceDetail.test.tsx's own local pair; InvoicesList is otherwise only
// exercised by InvoicesList.test.tsx.
function registerCtx(): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
    openImportedInvoice: () => {},
    invoiceQuery: '',
  }
  return ctx as unknown as PlatformCtx
}

function mockRegisterFetch(invoices: InvoiceRecord[]) {
  const body: InvoiceListResponse = { invoices, pagination: { limit: 50, offset: 0, total: invoices.length } }
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve(body) }))
}
