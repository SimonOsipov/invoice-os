// @vitest-environment jsdom
// Component tests for Row; mirrors InvoiceDetail.test.tsx's fetch-mock + ctx-cast idiom.
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ImportBatch } from '../lib/importApi'
import {
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
  const built = {
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
    submit_blocked_reason: null,
    ...over,
  } as InvoiceRecord
  // Stands in for the server's answer on an unarmed tenant. Derived from status ONLY --
  // deriving the approval half too would put the deleted client rule back in a fixture.
  // Specs about the gate set can_submit explicitly.
  return { ...built, can_submit: over.can_submit ?? built.status === 'validated' }
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
    can_correct_invoice_number: false,
    invoice_number_blocked_reason: null,
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

// submitGate's reachable sentences (internal/invoice/handlers.go). Every spec below sets
// one on the row: the SPA authors none of them any more, so there is nothing to derive.
const SUBMIT_REASON = {
  role: 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.',
  notValidated: 'Only validated invoices can be submitted — re-validate this invoice first.',
  awaiting: 'This invoice is waiting on approval — it can be submitted once an approver approves it.',
} as const

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

// A literal, not ROW_EXPANSION_COPY.notValidated, so an edit to the constant reddens RR-nv-1 (U+2014 em dash).
const NOT_VALIDATED = 'Not yet validated — run Re-validate to check compliance.'

describe('ReviewRow row-expansion: an unevaluated invoice never renders as passing (AC-6)', () => {
  it('RR-nv-1: an unvalidated row, expanded, renders the not-validated arm and no green strip', async () => {
    mockGetInvoice(detailFixture({ status: 'draft', violations: [], rule_set_version: null }))

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

    await screen.findByTestId('review-revalidate') // wait for the record to load before asserting

    const notValidated = screen.queryByTestId('review-row-not-validated')
    expect(notValidated).not.toBeNull()
    expect(notValidated?.textContent).toBe(NOT_VALIDATED)
    expect(screen.queryAllByTestId('review-row-passing')).toHaveLength(0)

    const expansion = screen.getByTestId('review-row-expansion')
    expect(expansion.querySelectorAll('.eyebrow')).toHaveLength(0)
    expect(expansion.querySelectorAll('.label'), 'no section label on the not-validated arm').toHaveLength(0)
    expect(expansion.textContent).not.toContain('passed')
  })

  it('RR-nv-2: a validated clean row still renders the green strip', async () => {
    mockGetInvoice(detailFixture({
      status: 'validated',
      violations: [],
      rule_set_version: 3,
      rule_set_version_id: 'rsv-3',
    }))

    render(
      <Row
        r={listRow({ status: 'validated' })}
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

    await screen.findByTestId('review-revalidate')

    expect(screen.queryByTestId('review-row-passing')).not.toBeNull()
    expect(screen.queryByTestId('review-row-not-validated')).toBeNull()
  })

  function renderExpanded(detail: InvoiceDetailRecord) {
    mockGetInvoice(detail)
    render(
      <Row r={listRow({ status: detail.status })} batches={[]} checked={false} expanded onToggleExpand={() => {}} onToggle={() => {}} ctx={rowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    return screen.findByTestId('review-revalidate')
  }

  // The action row has no testid: it is the Re-validate button's grandparent.
  async function actionRowOf(detail: InvoiceDetailRecord) {
    const btn = await renderExpanded(detail)
    return btn.parentElement?.parentElement as HTMLElement
  }

  it('RR-nv-3: the not-validated arm draws no rule above the action row, like the passing strip', async () => {
    const actionRow = await actionRowOf(detailFixture({ status: 'draft', violations: [], rule_set_version: null }))

    expect(actionRow.style.paddingTop).toBe('0px')
    expect(actionRow.style.borderTop).toBe('')
  })

  it('RR-nv-3 control: a failing row keeps the rule, so an empty border can discriminate', async () => {
    const actionRow = await actionRowOf(detailFixture({
      status: 'failed',
      violations: [{ rule_key: 'buyer-tin-required', severity: 'error', message: 'Buyer TIN is required.', path: 'buyer.tin' }],
      rule_set_version: 3,
      rule_set_version_id: 'rsv-3',
    }))

    expect(actionRow.style.paddingTop).toBe('4px')
    expect(actionRow.style.borderTop).toContain('solid')
  })

  it('RR-nv-4: a warning-only row with no version renders its advisory cards, not the not-validated arm', async () => {
    await renderExpanded(detailFixture({
      status: 'draft',
      violations: [{ rule_key: 'advisory-x', severity: 'warning', message: 'Advisory only.', path: 'buyer.tin' }],
      rule_set_version: null,
    }))

    const expansion = screen.getByTestId('review-row-expansion')
    expect(screen.queryByTestId('review-row-not-validated')).toBeNull()
    expect(screen.queryByTestId('review-row-passing')).toBeNull()
    const labels = expansion.querySelectorAll('.label')
    expect(labels).toHaveLength(1)
    expect(labels[0].textContent).toBe(ROW_EXPANSION_COPY.advisorySectionLabel)
    expect(expansion.querySelectorAll('.eyebrow'), 'the section label moved to .label').toHaveLength(0)
    expect(expansion.textContent).toContain('Advisory only.')
    expect(expansion.textContent).not.toContain('passed')
  })

  it('RR-nv-5: the arm says run Re-validate, so Re-validate is enabled and Keep as-is is absent', async () => {
    const btn = await renderExpanded(detailFixture({ status: 'draft', violations: [], rule_set_version: null }))

    expect(screen.getByTestId('review-row-not-validated')).toBeTruthy()
    expect((btn as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('review-keep')).toBeNull()
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
    renderGateRow({ status: 'validated', approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting })
    expect(selectBox().disabled, 'the server refused this row').toBe(true)
    cleanup()

    // The discriminator: a call site reading anything but the row gets the same answer
    // for every row -- which the line above cannot tell apart.
    renderGateRow({ status: 'validated', approval: null, can_submit: true, submit_blocked_reason: null })
    expect(selectBox().disabled, 'the server cleared this row').toBe(false)
    cleanup()

    // Same run state as the enabled leg, opposite answer: the run cannot be what decides.
    renderGateRow({ status: 'validated', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.role })
    expect(selectBox().disabled, 'a clear status and no run do not overrule the server').toBe(true)
  })

  it('RR-appr-2: parity -- the awaiting-approval checkbox is the SAME disabled control a draft row already renders, apart from the reason each one now states for itself', () => {
    renderGateRow({ status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })
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

    renderGateRow({ status: 'validated', approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting })
    const awaitingBox = selectBox()

    expect({
      present: Boolean(awaitingBox),
      disabled: awaitingBox.disabled,
      label: awaitingBox.getAttribute('aria-label'),
    }, 'awaiting-approval renders exactly the draft row shape, apart from its own reason').toEqual(draftShape)
    expect(awaitingBox.getAttribute('title')).toBe(SUBMIT_REASON.awaiting)
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
    const shape = { status: 'validated' as const, approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting }
    renderGateRow(shape)
    const box = selectBox()

    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBe(SUBMIT_REASON.awaiting)
  })

  it('A16-2b: draft + no run renders the not-validated reason, not the approval one', () => {
    renderGateRow({ status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })
    const title = selectBox().getAttribute('title')

    expect(title).toBe(SUBMIT_REASON.notValidated)
    expect(title).not.toBe(SUBMIT_REASON.awaiting)
  })

  it('A16-2c: a selectable row is enabled and carries no title', () => {
    renderGateRow({ status: 'validated', approval: null, can_submit: true, submit_blocked_reason: null })
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
    renderGateRow({ status: 'validated', approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting })
    const box = screen.getByTestId('review-select') as HTMLInputElement

    expect(box.getAttribute('title'), 'A06-6 pinned this null; APPR-16 Core AC-2 overrules it').toBe(SUBMIT_REASON.awaiting)
  })
})

// QA adversarial (Stage 4, Mode B, BUG-12): A16-2f pins parity on ONE sentence and B12-8 on
// the role rung. submitGate can reach five, two of which share every byte before the em
// dash, and the register had never seen the role one at all.
describe('ReviewRow + InvoicesList: all five server sentences reach both titles, verbatim (BUG-12, QA Stage 4)', () => {
  // internal/invoice/handlers.go: notApproverTransmitReason, submitBlockedReason's three
  // arms, awaitingApprovalReason. Byte-for-byte.
  const SERVER_SENTENCES = [
    'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.',
    'Only validated invoices can be submitted — re-validate this invoice first.',
    'Only validated invoices can be submitted — edit this invoice and re-validate it first.',
    'Only validated invoices can be submitted.',
    'This invoice is waiting on approval — it can be submitted once an approver approves it.',
  ]

  it('B12-14a: control -- the table holds five DISTINCT sentences', () => {
    // An empty or deduplicated table would make the loop below assert nothing, and two
    // of these differ only after the dash.
    expect(SERVER_SENTENCES).toHaveLength(5)
    expect(new Set(SERVER_SENTENCES).size).toBe(5)
  })

  it('B12-14: each sentence lands byte-identical on review-select and on invoice-select', async () => {
    let checked = 0

    for (const [i, sentence] of SERVER_SENTENCES.entries()) {
      const shared = gateRow({
        id: `inv-sentence-${i}`,
        invoice_number: `INV-SENTENCE-${i}`,
        status: 'validated',
        approval: null,
        can_submit: false,
        submit_blocked_reason: sentence,
      })

      render(
        <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
      )
      const reviewBox = screen.getByTestId('review-select') as HTMLInputElement
      const reviewTitle = reviewBox.getAttribute('title')
      const reviewDisabled = reviewBox.disabled
      cleanup()

      vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
      mockRegisterFetch([shared])
      render(<InvoicesList ctx={registerCtx()} />)
      await screen.findByText(shared.invoice_number)
      const listBox = screen.getByTestId('invoice-select') as HTMLInputElement

      expect(reviewDisabled, `review row, sentence=${sentence}`).toBe(true)
      expect(listBox.disabled, `register row, sentence=${sentence}`).toBe(true)
      // Verbatim on each surface, then against each other, so a failure names which one
      // substituted and which two disagree.
      expect(reviewTitle, `review row, sentence=${sentence}`).toBe(sentence)
      expect(listBox.getAttribute('title'), `register row, sentence=${sentence}`).toBe(sentence)
      expect(reviewTitle, `surfaces disagree, sentence=${sentence}`).toBe(listBox.getAttribute('title'))
      cleanup()
      checked += 1
    }

    expect(checked, 'the loop skipped a sentence').toBe(5)
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
        <Row r={gateRow({ id: 'inv-draft', status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={gateRow({ id: 'inv-awaiting', status: 'validated', approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [draftBox, awaitingBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]
    const draftReason = draftBox.getAttribute('title')
    const awaitingReason = awaitingBox.getAttribute('title')

    // One sentence reused for both rows would pass whichever assertion happens to match
    // -- pinning both directions closes that gap.
    expect(draftReason).toBe(SUBMIT_REASON.notValidated)
    expect(awaitingReason).toBe(SUBMIT_REASON.awaiting)
    expect(draftReason).not.toBe(awaitingReason)
  })

  it('A16-2i: a row transitioning from blocked to selectable drops its title, not left stale', () => {
    const shared = gateRow({ id: 'inv-transition', status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })
    const { rerender } = render(
      <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const box = screen.getByTestId('review-select') as HTMLInputElement
    // The first render must really be blocked, or the absence half below is vacuous.
    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBe(SUBMIT_REASON.notValidated)

    rerender(
      <Row r={{ ...shared, status: 'validated', can_submit: true, submit_blocked_reason: null }} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
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

  function renderPair(blockedOver: SubmitGateOver, cleanOver: SubmitGateOver) {
    render(
      <>
        <Row r={gateRow({ id: 'inv-blocked', ...blockedOver })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={gateRow({ id: 'inv-clean', ...cleanOver })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [blockedRow, cleanRow] = screen.getAllByTestId('review-row')
    const [blockedBox, cleanBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]
    return { blockedRow, cleanRow, blockedBox, cleanBox }
  }

  it('B09-3: a not-validated review row renders the same grid children as a selectable one, and keeps its title', () => {
    const { blockedRow, cleanRow, blockedBox, cleanBox } = renderPair(
      { status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated },
      { status: 'validated', approval: null, can_submit: true, submit_blocked_reason: null },
    )

    // Non-vacuity: one row really blocked, the other really selectable -- two equally
    // wrong rows would otherwise satisfy the count below.
    expect(blockedBox.disabled).toBe(true)
    expect(blockedBox.getAttribute('title')).toBe(SUBMIT_REASON.notValidated)
    expect(cleanBox.disabled).toBe(false)

    expect(blockedRow.children.length).toBe(cleanRow.children.length)
  })

  it('B09-4: an awaiting-approval review row renders the same grid children as a selectable one', () => {
    const { blockedRow, cleanRow, blockedBox, cleanBox } = renderPair(
      { status: 'validated', approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting },
      { status: 'validated', approval: null, can_submit: true, submit_blocked_reason: null },
    )

    expect(blockedBox.disabled).toBe(true)
    expect(blockedBox.getAttribute('title')).toBe(SUBMIT_REASON.awaiting)
    expect(cleanBox.disabled).toBe(false)

    expect(blockedRow.children.length).toBe(cleanRow.children.length)
  })
})

// B09-3/B09-4 are both RELATIVE: an edit that widens BOTH rows keeps them green, and they
// count row-level children only, so a reason re-added INSIDE a cell is invisible to them.
describe('BUG-09 QA: the deleted line cannot come back through a blind spot', () => {
  const REVIEW_CELLS = 7
  // A reason sentence's clause before the em dash; the whole sentence if it has none.
  // The sentences are the SERVER's now, and submitGate's draft and rejected arms share
  // one lead -- so a pair fed to this helper must be asserted distinct, never assumed.
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
    renderGateRow({ status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })

    // Non-vacuity: the row must really be blocked.
    expect((screen.getByTestId('review-select') as HTMLInputElement).getAttribute('title')).toBe(SUBMIT_REASON.notValidated)

    // A second, independent denominator: the literal cannot drift away from the grid.
    expect(REVIEW_GRID_COLUMNS.trim().split(/\s+/).length, 'the grid declares a track per cell').toBe(REVIEW_CELLS)
    expect(screen.getByTestId('review-row').children.length, 'a blocked row is checkbox + six cells, nothing more').toBe(REVIEW_CELLS)
  })

  it('QA-B09-7: a blocked review row prints its reason nowhere in its own text, at any nesting depth', () => {
    render(
      <>
        <Row r={gateRow({ id: 'inv-draft', status: 'draft', approval: null, can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={gateRow({ id: 'inv-awaiting', status: 'validated', approval: openRun, can_submit: false, submit_blocked_reason: SUBMIT_REASON.awaiting })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [draftRow, awaitingRow] = screen.getAllByTestId('review-row')
    const [draftBox, awaitingBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]

    // Non-vacuity: each row really holds its OWN sentence in `title`, so both strings are
    // on the page -- just never as rendered text.
    expect(draftBox.getAttribute('title')).toBe(SUBMIT_REASON.notValidated)
    expect(awaitingBox.getAttribute('title')).toBe(SUBMIT_REASON.awaiting)

    expect(draftRow.textContent, 'the reason is back on screen, nested somewhere the child count cannot see').not.toContain(SUBMIT_REASON.notValidated)
    expect(awaitingRow.textContent).not.toContain(SUBMIT_REASON.awaiting)

    // The lead phrase too, so a TRUNCATED re-add cannot slip past exact containment. The
    // two leads must DIFFER, or each row is only re-asserting the other's absence.
    // The status pills read DRAFT/VALIDATED, so neither phrase collides with one.
    expect(lead(SUBMIT_REASON.notValidated)).not.toBe(lead(SUBMIT_REASON.awaiting))
    expect(draftRow.textContent).not.toContain(lead(SUBMIT_REASON.notValidated))
    expect(awaitingRow.textContent).not.toContain(lead(SUBMIT_REASON.awaiting))
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
        r={listRow({ status: 'draft', can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })}
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
    expect((screen.getByTestId('review-select') as HTMLInputElement).getAttribute('title')).toBe(SUBMIT_REASON.notValidated)

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

// RESKIN2-04-04: the review prototype's row, verdict and expansion values (D-15, D-16, D-44).
describe('ReviewRow: the review prototype look (RESKIN2-04-04)', () => {
  const ERR = { rule_key: 'buyer-tin-required', severity: 'error' as const, message: 'Buyer TIN is required.', path: 'buyer.tin' }
  const WARN = { rule_key: 'advisory-x', severity: 'warning' as const, message: 'Advisory only.', path: 'buyer.tin' }
  const UNMAPPABLE = { rule_key: 'line-items-required', severity: 'error' as const, message: 'At least one line item is required.', path: 'line_items' }
  const PANEL_PADDING = '4px 18px 16px 54px'

  function failing(over: Partial<InvoiceDetailRecord> = {}) {
    return detailFixture({ status: 'failed', violations: [ERR], rule_set_version: 3, rule_set_version_id: 'rsv-3', ...over })
  }

  // Non-GET calls answer 500 with `mutationError`; GETs answer the detail record.
  function stubDetail(detail: InvoiceDetailRecord, mutationError?: string) {
    vi.stubGlobal(
      'fetch',
      vi.fn((_url: string, init?: { method?: string }) => {
        if ((init?.method ?? 'GET') !== 'GET') {
          return Promise.resolve<MockResponse>({ ok: false, status: 500, json: () => Promise.resolve({ error: mutationError ?? 'boom' }) })
        }
        return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(detail) })
      }),
    )
  }

  async function renderOpen(detail: InvoiceDetailRecord, mutationError?: string) {
    stubDetail(detail, mutationError)
    render(
      <Row r={listRow({ status: detail.status })} batches={[]} checked={false} expanded onToggleExpand={() => {}} onToggle={() => {}} ctx={rowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    return (await screen.findByTestId('review-revalidate')) as HTMLButtonElement
  }

  const styleOf = (el: Element) => (el as HTMLElement).style

  it('the verdict pill is a 4px pill with no dot', () => {
    const r = { status: 'failed' as const, violations: [ERR] }
    renderRow(r)
    const pill = screen.getByTestId('review-verdict').firstElementChild as HTMLElement

    expect(pill.textContent, 'the first child is the status pill').toBe(verdictPill(r).status.label)
    expect(pill.style.borderRadius).toBe('var(--radius-sm)')
    expect(pill.style.fontSize).toBe('9px')
    expect(pill.style.fontWeight).toBe('700')
    expect(pill.style.letterSpacing).toBe('0.04em')
    expect(pill.style.padding).toBe('2px 7px')
    expect(pill.children, 'no dot or inner span').toHaveLength(0)
    expect(pill.className).toContain('mono')
  })

  it('the rule badge is bare text', () => {
    const r = {
      status: 'failed' as const,
      violations: [ERR, { rule_key: 'vat-standard-rate', severity: 'error' as const, message: 'bad rate' }],
    }
    renderRow(r)
    const cell = screen.getByTestId('review-verdict')
    const badge = cell.children[1] as HTMLElement
    const expected = verdictPill(r).badges[0]

    expect(expected.label, 'two failing rules').toBe('2 RULES FAILED')
    expect(badge.textContent).toBe(expected.label)
    expect(badge.style.border).toBe('')
    expect(badge.style.background).toBe('')
    expect(badge.style.fontSize).toBe('8.5px')
    expect(badge.style.fontWeight).toBe('700')
    expect(badge.style.letterSpacing).toBe('0.04em')
    expect(badge.style.color).toBe(expected.tone.text)
  })

  it('row cells follow the prototype', async () => {
    await renderOpen(failing())
    const rowEl = screen.getByTestId('review-row')

    expect(rowEl.style.background).toBe('var(--bg-1)')
    expect(rowEl.style.gap, 'REVIEW_GRID_GAP').toBe('10px')
    expect(styleOf(within(rowEl).getByText('INV-1')).fontWeight).toBe('600')
    const buyer = within(rowEl).getByText('Beta Ltd')
    expect(buyer.style.fontSize).toBe('13px')
    expect(buyer.style.fontWeight, 'no inline weight').toBe('')
    const tin = screen.getByTestId('buyer-tin')
    expect(tin.style.display).toBe('block')
    expect(tin.style.fontSize).toBe('10.5px')
    const issueDate = rowEl.children[3] as HTMLElement
    expect(issueDate.textContent, 'the 4th cell is the issue date').not.toBe('')
    expect(issueDate.style.fontSize).toBe('11.5px')
    expect(issueDate.style.color).toBe('var(--fg-2)')
    expect(styleOf(rowEl.querySelector('.money') as Element).fontSize).toBe('13px')
    const chevron = rowEl.lastElementChild as HTMLElement
    expect(chevron.style.color).toBe('var(--fg-3)')
    expect(chevron.style.transform).toBe('rotate(180deg)')

    cleanup()
    renderRow()
    const collapsed = screen.getByTestId('review-row')
    expect(collapsed.style.background, 'only an expanded row takes --bg-1').not.toBe('var(--bg-1)')
    expect((collapsed.lastElementChild as HTMLElement).style.transform).toBe('none')
    expect((collapsed.lastElementChild as HTMLElement).style.color).toBe('var(--fg-3)')
  })

  it('the row checkbox is 15px teal and a blocked one keeps its 0.5 dim', () => {
    renderGateRow({ status: 'validated', can_submit: true })
    const box = screen.getByTestId('review-select') as HTMLInputElement
    expect(box.disabled).toBe(false)
    expect(box.style.width).toBe('15px')
    expect(box.style.height).toBe('15px')
    expect(box.style.accentColor).toBe('var(--action)')
    expect(box.style.opacity, 'an enabled box is not dimmed').toBe('')
    cleanup()

    renderGateRow({ status: 'draft', can_submit: false, submit_blocked_reason: SUBMIT_REASON.notValidated })
    const blocked = screen.getByTestId('review-select') as HTMLInputElement
    expect(blocked.disabled).toBe(true)
    expect(blocked.style.width).toBe('15px')
    expect(blocked.style.accentColor).toBe('var(--action)')
    expect(blocked.style.opacity).toBe('0.5')
    expect(blocked.style.cursor).toBe('not-allowed')
  })

  it('the expansion follows the prototype', async () => {
    await renderOpen(failing())
    const expansion = screen.getByTestId('review-row-expansion')
    const card = screen.getByTestId('review-fix-card')
    const pill = card.firstElementChild?.firstElementChild as HTMLElement
    const input = screen.getByTestId('review-fix-input') as HTMLInputElement

    expect(expansion.style.padding).toBe(PANEL_PADDING)
    const sectionLabels = expansion.querySelectorAll('.label')
    expect(sectionLabels, 'the section label is the only .label').toHaveLength(1)
    expect(sectionLabels[0].textContent).toBe(ROW_EXPANSION_COPY.sectionLabel)
    expect(expansion.querySelectorAll('.eyebrow')).toHaveLength(0)
    expect(card.style.border).toBe('1px solid var(--status-red-border)')
    expect(pill.textContent, 'the first head child is the severity pill').toBe('Error')
    expect(pill.style.borderRadius).toBe('var(--radius-sm)')
    expect(pill.children, 'no dot').toHaveLength(0)
    expect(pill.style.fontSize).toBe('9px')
    expect(pill.style.fontWeight).toBe('700')
    expect(pill.style.letterSpacing).toBe('0.06em')
    const fieldLabel = within(card).getByText('Buyer TIN')
    expect(fieldLabel.classList.contains('label'), 'the field label is a plain div').toBe(false)
    expect(input.style.maxWidth).toBe('240px')
    expect(input.style.height).toBe('34px')
    expect(input.style.fontSize).toBe('13px')
    expect(input.style.fontFamily).toBe('var(--font-mono)')
  })

  it('actions are 34px', async () => {
    const revalidate = await renderOpen(failing())
    fireEvent.change(screen.getByTestId('review-fix-input'), { target: { value: '99999999-0001' } })
    const save = await screen.findByTestId('review-fix-save')
    const keep = screen.getByTestId('review-keep')
    const reason = screen.getByTestId('review-keep-reason')

    expect(styleOf(save).height).toBe('34px')
    expect(styleOf(revalidate).height).toBe('34px')
    expect(styleOf(keep).height).toBe('34px')
    expect(styleOf(reason).minWidth).toBe('260px')
    expect(styleOf(reason).maxWidth).toBe('460px')
    expect(styleOf(reason).height).toBe('34px')
  })

  it('a blocked Re-validate dims (#114)', async () => {
    const blocked = await renderOpen(failing({ can_revalidate: false, revalidate_blocked_reason: 'Re-validation is not available for this invoice.' }))

    expect(blocked.disabled).toBe(true)
    expect(blocked.style.opacity).toBe('0.45')
    expect(blocked.style.cursor).toBe('not-allowed')
    expect(blocked.style.background, 'inline rest fill so .v2-btn-ghost:hover cannot repaint').toBe('transparent')
    cleanup()

    const enabled = await renderOpen(failing())
    expect(enabled.disabled).toBe(false)
    expect(enabled.style.opacity, 'an enabled button is not dimmed').toBe('')
    expect(enabled.style.background, 'hover stays live').toBe('')
  })

  it('a warning card takes its severity border', async () => {
    await renderOpen(detailFixture({ status: 'draft', violations: [WARN], rule_set_version: 3, rule_set_version_id: 'rsv-3' }))
    const card = screen.getByTestId('review-fix-card')

    expect(card.style.border).toBe('1px solid var(--status-amber-border)')
    expect(card.style.border).not.toBe('1px solid var(--status-red-border)')
  })

  it('the passing strip carries a tick and plain text', async () => {
    await renderOpen(detailFixture({ status: 'validated', violations: [], rule_set_version: 3, rule_set_version_id: 'rsv-3' }))
    const strip = screen.getByTestId('review-row-passing')
    const tick = strip.querySelector('[aria-hidden]') as HTMLElement | null
    const text = screen.getByText(/passed\.$/)

    expect(strip.style.display).toBe('flex')
    expect(strip.style.gap).toBe('10px')
    expect(strip.style.background).toBe('var(--status-green-bg)')
    expect(tick, 'an aria-hidden tick glyph').not.toBeNull()
    expect(tick?.style.color).toBe('var(--status-green-text)')
    expect(strip.contains(text)).toBe(true)
    expect(text.style.fontSize).toBe('12.5px')
    expect(text.style.color).toBe('var(--fg-2)')
  })

  it('the kept banner is the prototype shape', async () => {
    await renderOpen(detailFixture({
      status: 'draft',
      violations: [ERR],
      kept_as_is_at: '2026-07-30T00:00:00Z',
      kept_as_is_by: 'someone',
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    }))
    const banner = screen.getByTestId('review-kept-banner')

    expect(banner.style.padding).toBe('9px 12px')
    expect(banner.style.borderRadius).toBe('var(--radius-md)')
  })

  it('loading and error expansions use the panel padding', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<MockResponse>(() => {})))
    render(
      <Row r={listRow()} batches={[]} checked={false} expanded onToggleExpand={() => {}} onToggle={() => {}} ctx={rowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const loading = screen.getByTestId('review-row-expansion')
    expect(loading.textContent, 'the read is pending').toContain('Loading this invoice')
    expect(loading.style.padding).toBe(PANEL_PADDING)
    cleanup()

    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve<MockResponse>({ ok: false, status: 500, json: () => Promise.resolve({ error: 'read boom' }) })))
    render(
      <Row r={listRow()} batches={[]} checked={false} expanded onToggleExpand={() => {}} onToggle={() => {}} ctx={rowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    await screen.findByText('Something went wrong')
    const errored = screen.getByTestId('review-row-expansion')
    expect(errored.textContent).not.toContain('Loading this invoice')
    expect(errored.style.padding).toBe(PANEL_PADDING)
  })

  it('the not-validated strip takes the passing strip\'s shape', async () => {
    await renderOpen(detailFixture({ status: 'draft', violations: [], rule_set_version: null }))
    const strip = screen.getByTestId('review-row-not-validated')

    expect(strip.style.display).toBe('flex')
    expect(strip.style.gap).toBe('10px')
    expect(strip.style.background).toBe('var(--bg-3)')
    expect(strip.style.border).toBe('1px solid var(--line-2)')
    expect(strip.querySelector('[aria-hidden]'), 'no tick on this arm').toBeNull()
  })

  it('an unmappable card is an error card without an input', async () => {
    await renderOpen(failing({ violations: [UNMAPPABLE] }))
    const card = screen.getByTestId('review-fix-card')

    expect(card.textContent).toContain(UNMAPPABLE.message)
    expect(card.style.border).toBe('1px solid var(--status-red-border)')
    expect(card.querySelector('[data-testid="review-fix-input"]')).toBeNull()
  })

  it('the revalidate reason stays the hint line (pin)', async () => {
    await renderOpen(failing({ can_revalidate: false, revalidate_blocked_reason: 'Re-validation is not available for this invoice.' }))
    const reason = screen.getByTestId('review-revalidate-reason')

    expect(reason.textContent).toBe('Re-validation is not available for this invoice.')
    expect(reason.style.fontSize).toBe('11.5px')
    expect(reason.style.color).toBe('var(--fg-3)')
  })

  it('save, keep and revalidate errors take the banner shape in red', async () => {
    const cases: Array<{ label: string; message: string; act: () => void }> = [
      {
        label: 'save',
        message: 'save boom',
        act: () => {
          fireEvent.change(screen.getByTestId('review-fix-input'), { target: { value: '99999999-0001' } })
          fireEvent.click(screen.getByTestId('review-fix-save'))
        },
      },
      {
        label: 'keep',
        message: 'keep boom',
        act: () => {
          fireEvent.change(screen.getByTestId('review-keep-reason'), { target: { value: 'because' } })
          fireEvent.click(screen.getByTestId('review-keep'))
        },
      },
      { label: 'revalidate', message: 'revalidate boom', act: () => fireEvent.click(screen.getByTestId('review-revalidate')) },
    ]

    let checked = 0
    for (const { label, message, act } of cases) {
      await renderOpen(failing(), message)
      act()
      const box = await screen.findByText(message)

      expect(box.style.padding, label).toBe('9px 12px')
      expect(box.style.fontSize, label).toBe('12.5px')
      expect(box.style.background, label).toBe('var(--status-red-bg)')
      expect(box.style.borderRadius, label).toBe('var(--radius-md)')
      cleanup()
      checked += 1
    }
    expect(checked).toBe(3)
  })
  it('a fix card hint sits inline beside its input; an unmappable card keeps it as its own block', async () => {
    await renderOpen(failing({ violations: [{ ...ERR, expected: '12345678-0001', actual: '123' }] }))
    const input = screen.getByTestId('review-fix-input')
    const hint = screen.getByTestId('review-fix-hint')
    const row = input.parentElement as HTMLElement

    expect(hint.textContent).toBe('Expected 12345678-0001 · got 123')
    expect(hint.parentElement, 'the hint shares the input row').toBe(row)
    expect(row.style.display).toBe('flex')
    expect(row.style.gap).toBe('11px')
    expect(row.style.flexWrap).toBe('wrap')
    expect(hint.className).toContain('mono')
    expect(hint.style.fontSize).toBe('10.5px')
    expect(hint.style.color).toBe('var(--fg-3)')
    cleanup()

    await renderOpen(failing({ violations: [{ ...UNMAPPABLE, expected: '1', actual: '0' }] }))
    const card = screen.getByTestId('review-fix-card')
    const block = screen.getByTestId('review-fix-hint')
    expect(card.querySelector('[data-testid="review-fix-input"]')).toBeNull()
    expect(block.parentElement, 'no input to sit beside: the hint is a direct child of the card').toBe(card)
    expect(block.style.fontSize).toBe('10.5px')
  })

  it('the section label sits 10px above the first card (margin plus flex gap)', async () => {
    await renderOpen(failing())
    const label = screen.getByTestId('review-row-expansion').querySelector('.label') as HTMLElement
    const column = label.parentElement as HTMLElement
    const card = screen.getByTestId('review-fix-card')

    expect(column.contains(card), 'the label and the cards share one flex column').toBe(true)
    expect(label.style.marginTop).toBe('12px')
    expect(parseFloat(label.style.marginBottom) + parseFloat(column.style.gap), 'prototype: label margin-bottom 10 above the card list').toBe(10)
  })

  it('an info card takes the muted severity border', async () => {
    await renderOpen(detailFixture({ status: 'draft', violations: [{ ...WARN, rule_key: 'note-x', severity: 'info' as const }], rule_set_version: 3, rule_set_version_id: 'rsv-3' }))
    const card = screen.getByTestId('review-fix-card')

    expect(card.style.border).toBe('1px solid var(--status-muted-border)')
  })
  it('the scope note line height is 1.5', async () => {
    await renderOpen(failing())
    const note = screen.getByTestId('review-row-note')

    expect(note.textContent).not.toBe('')
    expect(note.style.fontSize).toBe('11.5px')
    expect(note.style.lineHeight).toBe('1.5')
  })

  it('the passing and not-validated strips take the prototype 12px 0 10px margin (bottom net of the 14px column gap)', async () => {
    for (const [detail, id] of [
      [detailFixture({ status: 'validated', violations: [], rule_set_version: 3, rule_set_version_id: 'rsv-3' }), 'review-row-passing'],
      [detailFixture({ status: 'draft', violations: [] }), 'review-row-not-validated'],
    ] as const) {
      cleanup()
      await renderOpen(detail)
      const strip = screen.getByTestId(id)
      const column = strip.parentElement as HTMLElement

      expect(strip.style.marginTop, id).toBe('12px')
      expect(parseFloat(strip.style.marginBottom) + parseFloat(column.style.gap), `${id}: prototype margin-bottom 10`).toBe(10)
    }
  })
})
