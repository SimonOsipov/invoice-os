// @vitest-environment jsdom
// Component tests for Row; mirrors InvoiceDetail.test.tsx's fetch-mock + ctx-cast idiom.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import {
  selectBlockedReason,
  skipReasonLabel,
  type InvoiceApproval,
  type InvoiceDetailRecord,
  type InvoiceListResponse,
  type InvoiceRecord,
} from '../lib/invoices'
import { ROW_EXPANSION_COPY } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { InvoicesList } from './InvoicesList'
import { Row } from './ReviewRow'

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

function renderRow(over: Partial<InvoiceRecord> = {}) {
  render(
    <Row
      r={row(over)}
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

// RED (APPR-16-02, task-535, Mode A). The selection checkbox gains the same four-layer
// disabled-with-reason recipe the Re-validate button and InvoicesList's own checkbox
// already carry: real `disabled`, an inline mute, a visible reason node, and
// `title`/`aria-describedby` pointing at it with a PER-ROW id. `document.getElementById`
// off the checkbox's own `aria-describedby` is used throughout instead of a guessed
// testid -- it proves the id actually resolves to a rendered node, not just that some
// attribute string exists.
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

  function reasonNodeFor(box: HTMLInputElement): HTMLElement | null {
    const id = box.getAttribute('aria-describedby')
    return id != null ? document.getElementById(id) : null
  }

  it('A16-2a: validated + open run renders the reason in all four layers', () => {
    const shape = { status: 'validated' as const, approval: openRun }
    const reason = selectBlockedReason(shape)
    renderRow(shape)
    const box = selectBox()

    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBe(reason)
    const node = reasonNodeFor(box)
    expect(node).not.toBeNull()
    expect(node?.textContent).toBe(reason)
  })

  it('A16-2b: draft + no run renders the not-validated reason, not the approval one', () => {
    renderRow({ status: 'draft', approval: null })
    const node = reasonNodeFor(selectBox())

    expect(node?.textContent).toBe(skipReasonLabel('not_validated'))
    expect(node?.textContent).not.toBe(skipReasonLabel('awaiting_approval'))
  })

  it('A16-2c: a selectable row renders no reason node at all', () => {
    renderRow({ status: 'validated', approval: null })
    const box = selectBox()

    expect(box.disabled).toBe(false)
    expect(box.getAttribute('title')).toBeNull()
    expect(box.getAttribute('aria-describedby')).toBeNull()
    expect(screen.queryByText(skipReasonLabel('not_validated'))).toBeNull()
    expect(screen.queryByText(skipReasonLabel('awaiting_approval'))).toBeNull()
  })

  it('A16-2d: a post-submission row is disabled and silent -- the status pill is the explanation', () => {
    // selectBlockedReason returns null outside draft/validated (invoices.ts:1213):
    // an accepted row with a lingering open run must render disabled, but no reason.
    renderRow({ status: 'accepted', approval: openRun })
    const box = selectBox()

    expect(box.disabled).toBe(true)
    expect(box.getAttribute('title')).toBeNull()
    expect(box.getAttribute('aria-describedby')).toBeNull()
  })

  it('A16-2e: two blocked rows produce two distinct aria-describedby ids', () => {
    render(
      <>
        <Row r={row({ id: 'inv-a', status: 'draft', approval: null })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={row({ id: 'inv-b', status: 'validated', approval: openRun })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const boxes = screen.getAllByTestId('review-select') as HTMLInputElement[]
    const ids = boxes.map((b) => b.getAttribute('aria-describedby'))

    expect(ids.every((id) => id != null)).toBe(true)
    expect(new Set(ids).size).toBe(2)
    // REVALIDATE_REASON_ID (ReviewRow.tsx:81) is a module const -- reusing it on a
    // per-row control would mint duplicate DOM ids (Decision D-20).
    expect(ids).not.toContain('review-row-revalidate-blocked-reason-text')
  })

  it('A16-2f: parity -- ReviewRow and InvoicesList render a byte-identical string for the same row', async () => {
    const shared = row({ id: 'inv-parity', status: 'validated', approval: openRun })
    const expected = selectBlockedReason(shared)

    render(
      <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const reviewReason = reasonNodeFor(selectBox())?.textContent ?? null
    cleanup()

    // InvoicesList reads its own gateway base from the env (InvoiceDetail.test.tsx's
    // beforeEach precedent) -- Row instead takes `base` as a prop, so no other test
    // here has needed this until now.
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
    mockRegisterFetch([shared])
    render(<InvoicesList ctx={registerCtx()} />)
    await screen.findByText(shared.invoice_number)
    const listReason = screen.getByTestId('invoice-blocked-reason').textContent

    expect(reviewReason).toBe(expected)
    expect(listReason).toBe(expected)
  })
})

// Overruled by APPR-16 Core AC-2 (user, 2026-08-16). Replaces the A06-6 tripwire
// (APPR-12-06, task-531, [selectable-parity-not-new-copy]), which pinned the OPPOSITE
// of this AC: that narrowing kept the reason off ReviewRow for parity with a surface
// that also said nothing. That parity is no longer the goal.
describe('ReviewRow: the checkbox now states its own reason, retargeting [selectable-parity-not-new-copy] (APPR-16-02)', () => {
  it('A16-2g: the retargeted tripwire -- an awaiting-approval row renders title and aria-describedby, not silence', () => {
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
    const describedBy = box.getAttribute('aria-describedby')

    expect(box.getAttribute('title'), 'A06-6 pinned this null; APPR-16 Core AC-2 overrules it').toBe(reason)
    expect(describedBy).not.toBeNull()
    expect(describedBy != null ? document.getElementById(describedBy)?.textContent : null).toBe(reason)
  })
})

// QA adversarial (Stage 4, Mode B, task-535): three cases the RED-phase specs
// (A16-2a..g) didn't cover -- cross-row content pairing (not just distinct ids),
// a live blocked-to-selectable transition, and that aria-describedby never dangles.
describe('ReviewRow: adversarial coverage on the checkbox reason (APPR-16-02, QA Stage 4)', () => {
  const openRun: InvoiceApproval = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  it("A16-2h: two blocked rows with DIFFERENT reasons pair each aria-describedby to its own text, not the sibling's", () => {
    render(
      <>
        <Row r={row({ id: 'inv-draft', status: 'draft', approval: null })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
        <Row r={row({ id: 'inv-awaiting', status: 'validated', approval: openRun })} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />
      </>,
    )
    const [draftBox, awaitingBox] = screen.getAllByTestId('review-select') as HTMLInputElement[]
    const draftId = draftBox.getAttribute('aria-describedby')
    const awaitingId = awaitingBox.getAttribute('aria-describedby')
    expect(draftId).not.toBeNull()
    expect(awaitingId).not.toBeNull()

    const draftReason = draftId != null ? document.getElementById(draftId)?.textContent : null
    const awaitingReason = awaitingId != null ? document.getElementById(awaitingId)?.textContent : null

    // A shared/swapped id would pass one of these two by accident (same text) or fail
    // silently (wrong text, still non-null) -- pinning both directions closes that gap.
    expect(draftReason).toBe(skipReasonLabel('not_validated'))
    expect(awaitingReason).toBe(skipReasonLabel('awaiting_approval'))
    expect(draftReason).not.toBe(awaitingReason)
  })

  it('A16-2i: a row transitioning from blocked to selectable removes the reason node and both attributes, not left stale', () => {
    const shared = row({ id: 'inv-transition', status: 'draft', approval: null })
    const { rerender } = render(
      <Row r={shared} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )
    const box = screen.getByTestId('review-select') as HTMLInputElement
    const staleId = box.getAttribute('aria-describedby')
    expect(staleId).not.toBeNull()

    rerender(
      <Row r={{ ...shared, status: 'validated' }} batches={[]} checked={false} expanded={false} onToggleExpand={() => {}} onToggle={() => {}} ctx={reviewRowCtx()} base="https://gw" onChanged={() => {}} />,
    )

    expect(box.disabled).toBe(false)
    expect(box.getAttribute('title')).toBeNull()
    expect(box.getAttribute('aria-describedby')).toBeNull()
    // Not just the attribute -- the node itself is gone, so the OLD id can't dangle
    // even if something else still held a reference to it.
    expect(staleId != null ? document.getElementById(staleId) : null).toBeNull()
    expect(screen.queryByTestId('review-select-blocked-reason')).toBeNull()
  })

  it.each([
    ['draft, no run', { status: 'draft' as const, approval: null }],
    ['validated, open run', { status: 'validated' as const, approval: openRun }],
    ['validated, no run (selectable)', { status: 'validated' as const, approval: null }],
    ['accepted, open run (silent-disabled)', { status: 'accepted' as const, approval: openRun }],
  ])('A16-2j: %s -- aria-describedby never dangles, in either direction', (_label, shape) => {
    renderRow(shape)
    const box = screen.getByTestId('review-select') as HTMLInputElement
    const describedBy = box.getAttribute('aria-describedby')

    if (describedBy == null) {
      // No attribute -- there must be no orphan reason node sitting in the document either.
      expect(screen.queryByTestId('review-select-blocked-reason')).toBeNull()
    } else {
      // An attribute -- its target must already be in the document in the SAME render,
      // not attached on a later tick.
      expect(document.getElementById(describedBy)).not.toBeNull()
    }
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
