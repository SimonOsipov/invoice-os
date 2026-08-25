// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// AUDIT-09-04's RED specs, written before the component. Harness is AuditView.test.tsx's
// (mockFetch + narrowed ctx + stubbed VITE_GATEWAY_URL), unchanged.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import type { ComponentProps } from 'react'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { AuditEvent, AuditResponse } from '../lib/audit'
import { auditEventView } from '../lib/auditVocabulary'
import { AUDIT_COPY } from '../lib/auditView'
import { createAuthedFetch } from '../lib/authedFetch'
import {
  ACTIVITY_CHIP_LABELS,
  ACTIVITY_CHIP_ORDER,
  ACTIVITY_COPY,
  ACTIVITY_FETCH_LIMIT,
  ACTIVITY_REST_ROWS,
  activityRows,
  activityToggleCopy,
} from '../lib/invoiceActivity'
import type { PlatformCtx } from '../types'

import { AUDIT_TABLE_MIN_WIDTH, AuditRow } from './AuditRow'
import { InvoiceActivityCard } from './InvoiceActivityCard'

const INVOICE_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
const OTHER_INVOICE_ID = 'bbbbbbbb-0000-4000-8000-000000000002'
const SOURCE = join(__dirname, 'InvoiceActivityCard.tsx')

// arch §3's six new ACTIVITY_COPY keys. invoiceActivity.ts is NOT this subtask's file, so
// they cannot be added here -- copyOf() reads the lib the moment the key lands, which keeps
// a user-approved copy revision (F-H) a one-place change instead of a six-spec change.
const EXPECTED_NEW_COPY = {
  cardTitle: 'Activity',
  loading: 'Loading activity…',
  emptyScopedTitle: 'No activity recorded for this invoice',
  emptyScopedBody: 'Edits, validations, approvals and transmissions appear here as they happen.',
  emptyWorkspaceAlso: 'This workspace has not recorded anything at all yet.',
  chipZeroInert: 'A chip with no count is dimmed: this invoice has no events of that kind in the loaded page.',
} as const

type NewCopyKey = keyof typeof EXPECTED_NEW_COPY

function copyOf(key: NewCopyKey): string {
  const live = (ACTIVITY_COPY as unknown as Record<string, string | undefined>)[key]
  return live ?? EXPECTED_NEW_COPY[key]
}

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function mockFetch(reply: MockResponse | ((url: string) => MockResponse)) {
  const fetchMock = vi.fn((url: string) => Promise.resolve(typeof reply === 'function' ? reply(url) : reply))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function auditEvent(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'evt-1',
    created_at: '2026-08-20T09:15:00Z',
    event: 'invoice.created',
    actor: 'c0000000-0000-0000-0000-000000000001',
    actor_name: 'Chinedu Okafor',
    actor_kind: 'person',
    entity_id: 'ent-1',
    company_name: 'Honeywell Group',
    company_scope: 'company',
    payload: { id: INVOICE_ID, invoice_number: 'INV-9' },
    ...over,
  }
}

function logResponse(over: Partial<AuditResponse> = {}): MockResponse {
  const events = over.events ?? [auditEvent()]
  const body: AuditResponse = {
    events,
    page: { limit: ACTIVITY_FETCH_LIMIT, has_more: false, next_cursor: null },
    total: events.length,
    log_is_empty: false,
    facets: { event: [], actor: [], company: [] },
    ...over,
  }
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

// Hand-written identifiers -- the lib exposes no event generator. The guard below is what
// stops a vocabulary change silently re-binning a fixture into the wrong chip.
const INVOICES_EVENT = 'invoice.updated'
const APPROVALS_EVENT = 'invoice.approval_approved'

function eventsOf(spec: Array<{ event: string; n: number }>): AuditEvent[] {
  const out: AuditEvent[] = []
  for (const s of spec) {
    for (let i = 0; i < s.n; i++) {
      out.push(auditEvent({ id: `evt-${out.length}`, event: s.event, payload: { id: INVOICE_ID, seq: out.length } }))
    }
  }
  return out
}

// openAuditForInvoice is AUDIT-09-05's PlatformCtx verb; it is a spy here because the card's
// only job is to call it once with the right pair.
type ActivityCtx = PlatformCtx & { openAuditForInvoice: Mock }

function activityCtx(): ActivityCtx {
  return {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    entities: [],
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openAuditForInvoice: vi.fn(),
  } as unknown as ActivityCtx
}

const INVOICE_NUMBER = 'INV-9'

// `invoiceNumber` is not on the component's props until AUDIT-09-05 lands it; the cast is what
// keeps tsc green on the RED commit and is a no-op once the prop exists.
type CardProps = ComponentProps<typeof InvoiceActivityCard>
function cardProps(ctx: ActivityCtx, invoiceId: string, invoiceNumber: string): CardProps {
  return { ctx, invoiceId, invoiceNumber } as unknown as CardProps
}

function renderCard(invoiceId: string = INVOICE_ID, invoiceNumber: string = INVOICE_NUMBER, ctx: ActivityCtx = activityCtx()) {
  return { ctx, ...render(<InvoiceActivityCard {...cardProps(ctx, invoiceId, invoiceNumber)} />) }
}

const rowCount = () => screen.queryAllByTestId('audit-row').length

async function loaded() {
  await screen.findByTestId('invoice-activity-chips')
}

function paramsOf(fetchMock: ReturnType<typeof mockFetch>, call = 0): URLSearchParams {
  return new URL(fetchMock.mock.calls[call]![0]).searchParams
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('InvoiceActivityCard fixtures', () => {
  it('invoiceActivity_fixtureEventsBinWhereTheSpecsAssume', () => {
    // Every count below is derived from these two identifiers landing in these two chips.
    expect(auditEventView(INVOICES_EVENT).domain).toBe('invoices')
    expect(auditEventView(APPROVALS_EVENT).domain).toBe('approvals')
  })
})

describe('InvoiceActivityCard fetch', () => {
  it('invoiceActivity_sendsInvoiceIdAndNoDateWindow', async () => {
    const fetchMock = mockFetch(logResponse())
    renderCard()
    await loaded()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const url = new URL(fetchMock.mock.calls[0]![0])
    expect(url.pathname).toBe('/api/invoice/v1/audit-log')
    const params = url.searchParams
    // The two positives are the control for the five absences: they read the same object.
    expect(params.get('invoice_id')).toBe(INVOICE_ID)
    expect(params.get('limit')).toBe(String(ACTIVITY_FETCH_LIMIT))
    // An invoice's whole life belongs on its own page. The 30-day default is the Audit
    // SCREEN's (auditFilters.ts), and handlers.go skips an absent from/to entirely.
    for (const absent of ['from', 'to', 'cursor', 'event', 'actor', 'q']) {
      expect(params.has(absent), `${absent} must not be sent`).toBe(false)
    }
  })

  it('invoiceActivity_fetchesOncePerInvoiceId', async () => {
    const fetchMock = mockFetch(logResponse())
    const { rerender } = renderCard()
    await loaded()
    expect(fetchMock).toHaveBeenCalledTimes(1)

    // A parent re-render is not a new invoice. useAsync's effect deps are [invoiceId].
    rerender(<InvoiceActivityCard {...cardProps(activityCtx(), INVOICE_ID, INVOICE_NUMBER)} />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    rerender(<InvoiceActivityCard {...cardProps(activityCtx(), OTHER_INVOICE_ID, 'INV-OTHER')} />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(paramsOf(fetchMock, 1).get('invoice_id')).toBe(OTHER_INVOICE_ID)
  })
})

describe('InvoiceActivityCard reuse', () => {
  it('invoiceActivity_reusesAuditRowExpansion', async () => {
    mockFetch(logResponse({ events: [auditEvent({ payload: { id: INVOICE_ID, irn: 'NG-001' } })] }))
    renderCard()
    await loaded()

    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)
    expect(screen.getByTestId('audit-expansion')).toBeTruthy()
    expect(screen.getAllByTestId('audit-payload-field').length).toBeGreaterThanOrEqual(1)

    const src = readFileSync(SOURCE, 'utf8')
    // Control needle: prove the scan read THIS file before trusting its silence.
    expect(src, 'the source scan read the wrong file').toContain('export function InvoiceActivityCard')
    expect(src, 'the card must mount AuditRow, not restate it').toContain('AuditRow')
    for (const owned of ['audit-expansion', 'audit-payload-field', 'audit-event-identifier']) {
      expect(src, `${owned} belongs to AuditRow; a copy here is a parallel expansion body`).not.toContain(owned)
    }
  })

  it('invoiceActivity_isExtractableFromTheAuditScreen', () => {
    const src = readFileSync(SOURCE, 'utf8')
    // Multi-line, not /^import .*$/: a braced import list spans lines, and a line-bounded
    // regex captures only `import {` -- every specifier below would then be silently absent
    // and the ban half of this scan would pass on any file at all.
    const imports = src.match(/^import\b[\s\S]*?from '[^']*'/gm) ?? []
    // Control needle: AuditRow.test.tsx:99-101's idiom.
    expect(imports.length, 'the import scan found nothing -- the regex, not the file, is wrong').toBeGreaterThanOrEqual(5)
    const joined = imports.join('\n')
    for (const want of ['./AuditRow', './AuditTable', '../lib/invoiceActivity', '../lib/audit']) {
      expect(joined, `the card must import ${want}`).toContain(want)
    }
    // auditView last: it holds AUDIT_COPY's workspace-empty language, which would be a lie
    // beside one invoice.
    for (const banned of ['AuditView', 'AuditPager', 'AuditFilterCard', 'auditFilters', 'auditView']) {
      expect(joined, `${banned} drags the Audit SCREEN onto the invoice page`).not.toContain(banned)
    }
  })

  it('invoiceActivity_omitsTheNarrowToInvoiceAffordance', async () => {
    const payload = { id: INVOICE_ID, invoice_number: 'INV-9' }
    // Positive control on the same locator: this payload DOES produce the affordance when
    // the prop is supplied, so the null below is the omission and not an inert fixture.
    render(<AuditRow event={auditEvent({ payload })} expanded onToggle={() => {}} onFilterToInvoice={() => {}} />)
    expect(screen.getByTestId('audit-invoice-affordance')).toBeTruthy()
    cleanup()

    mockFetch(logResponse({ events: [auditEvent({ payload })] }))
    renderCard()
    await loaded()
    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)
    expect(screen.getByTestId('audit-expansion')).toBeTruthy()
    expect(screen.queryByTestId('audit-invoice-affordance')).toBeNull()
  })
})

describe('InvoiceActivityCard chips', () => {
  it('invoiceActivity_chipsAreThePfChipRecipe', async () => {
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 3 }, { event: APPROVALS_EVENT, n: 2 }]) }))
    renderCard()
    await loaded()

    const chips = ACTIVITY_CHIP_ORDER.map((k) => screen.getByTestId(`activity-chip-${k}`))
    expect(chips).toHaveLength(ACTIVITY_CHIP_ORDER.length)
    // Order is a design fact (D-AC-5): read the rendered row, do not re-derive it.
    const rendered = within(screen.getByTestId('invoice-activity-chips')).getAllByRole('button')
    expect(rendered.map((b) => b.getAttribute('data-testid'))).toEqual(ACTIVITY_CHIP_ORDER.map((k) => `activity-chip-${k}`))
    for (const [i, chip] of chips.entries()) {
      const key = ACTIVITY_CHIP_ORDER[i]!
      expect(chip.className, `${key} must wear .pf-chip`).toContain('pf-chip')
      expect(chip.getAttribute('aria-pressed'), `${key} must carry aria-pressed even when disabled`).not.toBeNull()
      expect(chip.textContent, `${key} must show its label`).toContain(ACTIVITY_CHIP_LABELS[key])
    }
    expect(chips.filter((c) => c.getAttribute('aria-pressed') === 'true')).toHaveLength(1)
    expect(screen.getByTestId('activity-chip-all').getAttribute('aria-pressed')).toBe('true')

    fireEvent.click(screen.getByTestId('activity-chip-approvals'))
    expect(screen.getByTestId('activity-chip-approvals').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('activity-chip-all').getAttribute('aria-pressed')).toBe('false')
    expect(rowCount()).toBe(2)
  })

  it('invoiceActivity_inertChipIsDisabledWithAVisibleReason', async () => {
    // document.* is gated out of the invoice-scoped read, so this zero is structural.
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 2 }]) }))
    renderCard()
    await loaded()

    const documents = screen.getByTestId('activity-chip-documents')
    expect(documents).toHaveProperty('disabled', true)
    // Never hidden, never a bare title= -- AUDIT-08 proved a title= on a disabled button is
    // invisible in Chromium and two QA passes missed it.
    expect(documents.getAttribute('title')).toBeNull()
    const describedBy = documents.getAttribute('aria-describedby')
    expect(describedBy, 'the reason must be a text node the control points at').toBeTruthy()
    const reason = document.getElementById(describedBy!)
    expect(reason, `aria-describedby="${describedBy}" points at nothing`).toBeTruthy()
    expect(reason!.textContent).toBe(ACTIVITY_COPY.documentsInert)
    expect(reason!.textContent!.length).toBeGreaterThan(0)
  })

  it('invoiceActivity_incidentallyEmptyChipCarriesItsOwnReason', async () => {
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 3 }]) }))
    renderCard()
    await loaded()

    const reconciliation = screen.getByTestId('activity-chip-reconciliation')
    expect(reconciliation).toHaveProperty('disabled', true)
    const incidentalId = reconciliation.getAttribute('aria-describedby')
    expect(incidentalId).toBe('activity-chip-empty-reason')
    // A different id from the structural one: the two lines say different things.
    expect(incidentalId).not.toBe(screen.getByTestId('activity-chip-documents').getAttribute('aria-describedby'))
    expect(screen.getByTestId('activity-chip-empty-reason').textContent).toBe(copyOf('chipZeroInert'))
    // Positive control for the id split: the enabled chip points at nothing.
    expect(screen.getByTestId('activity-chip-invoices').getAttribute('aria-describedby')).toBeNull()
  })

  it('invoiceActivity_chipChangeClosesAnOpenExpansion', async () => {
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 2 }, { event: APPROVALS_EVENT, n: 2 }]) }))
    renderCard()
    await loaded()

    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)
    expect(screen.getByTestId('audit-expansion')).toBeTruthy()
    fireEvent.click(screen.getByTestId('activity-chip-approvals'))
    expect(screen.queryByTestId('audit-expansion')).toBeNull()
    fireEvent.click(screen.getByTestId('activity-chip-all'))
    // A held-over id would re-open a row the user never opened on this chip.
    expect(screen.queryByTestId('audit-expansion')).toBeNull()
    expect(rowCount()).toBe(4)
  })

  it('invoiceActivity_chipSelectionSurvivesAParentRerender', async () => {
    // arch §6: no `landed` buffer here, because the card holds chip/showAll/expandedId in
    // its own useState and the parent's 2s poll cannot re-run its useAsync.
    const fetchMock = mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 3 }, { event: APPROVALS_EVENT, n: 2 }]) }))
    const { rerender } = renderCard()
    await loaded()
    fireEvent.click(screen.getByTestId('activity-chip-approvals'))
    expect(rowCount()).toBe(2)

    rerender(<InvoiceActivityCard {...cardProps(activityCtx(), INVOICE_ID, INVOICE_NUMBER)} />)
    expect(screen.getByTestId('invoice-activity-chips')).toBeTruthy()
    expect(screen.getByTestId('activity-chip-approvals').getAttribute('aria-pressed')).toBe('true')
    expect(rowCount()).toBe(2)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('InvoiceActivityCard toggle', () => {
  it('invoiceActivity_fiveAtRestThenAll', async () => {
    const events = eventsOf([{ event: INVOICES_EVENT, n: 12 }])
    mockFetch(logResponse({ events }))
    renderCard()
    await loaded()

    expect(rowCount()).toBe(ACTIVITY_REST_ROWS)
    const collapsed = activityToggleCopy({ shown: 12, total: 12, fetched: 12, showAll: false })
    const expanded = activityToggleCopy({ shown: 12, total: 12, fetched: 12, showAll: true })
    expect(screen.getByTestId('activity-toggle').textContent).toBe(collapsed.label)

    fireEvent.click(screen.getByTestId('activity-toggle'))
    expect(rowCount()).toBe(12)
    expect(screen.getByTestId('activity-toggle').textContent).toBe(expanded.label)

    fireEvent.click(screen.getByTestId('activity-toggle'))
    expect(rowCount()).toBe(ACTIVITY_REST_ROWS)
    expect(screen.getByTestId('activity-toggle').textContent).toBe(collapsed.label)
  })

  it('invoiceActivity_toggleIsAbsentAtFiveOrFewer', async () => {
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: ACTIVITY_REST_ROWS }]) }))
    renderCard()
    await loaded()
    // Positive control for the absence: the rows the toggle would govern are on screen.
    expect(rowCount()).toBe(ACTIVITY_REST_ROWS)
    expect(screen.queryByTestId('activity-toggle')).toBeNull()
  })

  it('invoiceActivity_toggleLabelEqualsTheRowsItRenders', async () => {
    // F-B. activityToggleCopy cannot validate `shown`; this fixture is what does. One page
    // of 100 against a server total of 412 kills all three wrong arguments at once:
    // total (412), events.length (100, wrong at Approvals) and rows.length (5 -> no label).
    const events = eventsOf([{ event: INVOICES_EVENT, n: 60 }, { event: APPROVALS_EVENT, n: 40 }])
    mockFetch(logResponse({ events, total: 412 }))
    renderCard()
    await loaded()

    const labelCount = () => {
      const text = screen.getByTestId('activity-toggle').textContent ?? ''
      const m = text.match(/(\d[\d,]*)/)
      expect(m, `the toggle label must name a count: "${text}"`).toBeTruthy()
      return Number(m![1]!.replace(/,/g, ''))
    }

    // (a) chip `all`
    expect(screen.getByTestId('activity-toggle').textContent).not.toContain('412')
    const allClaim = labelCount()
    expect(allClaim).toBe(100)
    fireEvent.click(screen.getByTestId('activity-toggle'))
    expect(rowCount(), 'the label promised rows the card did not render').toBe(allClaim)
    expect(rowCount()).toBe(activityRows(events, 'all', true).length)

    // (b) chip Approvals -- the argument that only this half can tell apart.
    // showAll is STICKY across a chip change: arch §1 clears expandedId only. So the card
    // lands on all 40 approval rows with a 'Show fewer' label, and the honest claim is only
    // readable once collapsed.
    fireEvent.click(screen.getByTestId('activity-chip-approvals'))
    expect(rowCount()).toBe(40)
    expect(screen.getByTestId('activity-toggle').textContent).toBe(
      activityToggleCopy({ shown: 40, total: 412, fetched: 100, showAll: true }).label,
    )
    fireEvent.click(screen.getByTestId('activity-toggle'))
    expect(rowCount()).toBe(ACTIVITY_REST_ROWS)
    const approvalsText = screen.getByTestId('activity-toggle').textContent ?? ''
    expect(approvalsText).not.toContain('412')
    expect(approvalsText).not.toContain('100')
    const approvalsClaim = labelCount()
    expect(approvalsClaim).toBe(40)
    fireEvent.click(screen.getByTestId('activity-toggle'))
    expect(rowCount(), 'the label promised rows the card did not render').toBe(approvalsClaim)
    expect(rowCount()).toBe(activityRows(events, 'approvals', true).length)
  })

  it('invoiceActivity_capNoteRendersBesideTheHonestLabel', async () => {
    const events = eventsOf([{ event: INVOICES_EVENT, n: 60 }, { event: APPROVALS_EVENT, n: 40 }])
    mockFetch(logResponse({ events, total: 412 }))
    renderCard()
    await loaded()

    const note = screen.getByTestId('activity-cap-note').textContent ?? ''
    expect(note).toContain('100')
    expect(note).toContain('412')
    expect(note).toContain(ACTIVITY_COPY.auditLink)
    // The two numbers live in two different sentences: the note carries the cap, the
    // button never claims it.
    expect(screen.getByTestId('activity-toggle').textContent).not.toContain('412')
  })

  it('invoiceActivity_capNoteIsAbsentWhenNothingIsCapped', async () => {
    const events = eventsOf([{ event: INVOICES_EVENT, n: 12 }])
    mockFetch(logResponse({ events, total: 12 }))
    renderCard()
    await loaded()
    // Positive control for the absence: the toggle the note would sit beside is present.
    expect(screen.getByTestId('activity-toggle')).toBeTruthy()
    expect(screen.queryByTestId('activity-cap-note')).toBeNull()
  })
})

describe('InvoiceActivityCard ladder', () => {
  it('invoiceActivity_laddersLikeASiblingCard', async () => {
    const pending = vi.fn(() => new Promise<MockResponse>(() => {}))
    vi.stubGlobal('fetch', pending)
    const { unmount } = renderCard()

    expect(screen.getByText(copyOf('loading'))).toBeTruthy()
    expect(screen.queryByTestId('audit-table')).toBeNull()
    expect(screen.queryByTestId('invoice-activity-chips')).toBeNull()
    unmount()

    const failing = vi.fn(() => Promise.reject(new Error('boom')))
    vi.stubGlobal('fetch', failing)
    renderCard()
    await screen.findByText('Something went wrong')
    expect(failing).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByText('Retry'))
    await waitFor(() => expect(failing).toHaveBeenCalledTimes(2))
  })

  it('invoiceActivity_emptyScopedLogIsHonest', async () => {
    mockFetch(logResponse({ events: [], total: 0, log_is_empty: false }))
    renderCard()

    const empty = await screen.findByTestId('invoice-activity-empty')
    expect(empty.textContent).toContain(copyOf('emptyScopedTitle'))
    expect(empty.textContent).toContain(copyOf('emptyScopedBody'))
    expect(screen.queryByTestId('audit-table')).toBeNull()
    expect(screen.queryByText(copyOf('loading'))).toBeNull()
    // The workspace log is NOT empty here, so the card must not say it is.
    expect(screen.queryByTestId('invoice-activity-empty-workspace')).toBeNull()
    const body = screen.getByTestId('invoice-activity-body').textContent ?? ''
    expect(body).not.toContain(AUDIT_COPY.emptyTitle)
    expect(body).not.toContain(AUDIT_COPY.emptyMessage)
  })

  it('invoiceActivity_emptyWorkspaceSaysSoAsWell', async () => {
    mockFetch(logResponse({ events: [], total: 0, log_is_empty: true }))
    renderCard()

    const empty = await screen.findByTestId('invoice-activity-empty')
    expect(empty.textContent).toContain(copyOf('emptyScopedTitle'))
    expect(screen.getByTestId('invoice-activity-empty-workspace').textContent).toContain(copyOf('emptyWorkspaceAlso'))
    // The workspace line is an ADDITION, never a swap -- and never the Audit screen's copy.
    const body = screen.getByTestId('invoice-activity-body').textContent ?? ''
    expect(body).not.toContain(AUDIT_COPY.emptyTitle)
    expect(body).not.toContain(AUDIT_COPY.emptyMessage)
  })
})

describe('InvoiceActivityCard copy', () => {
  it('invoiceActivity_cardTitleIsNotTheBannedString', async () => {
    mockFetch(logResponse())
    renderCard()
    await loaded()

    // import-wizard.spec.ts:576 pins zero matches for 'Audit trail' on this page.
    expect(copyOf('cardTitle')).not.toBe('Audit trail')
    const card = screen.getByTestId('invoice-activity')
    expect(within(card).getByText(copyOf('cardTitle'))).toBeTruthy()
    expect(screen.queryByText('Audit trail')).toBeNull()
  })

  it('invoiceActivity_newCopyLivesInTheLib', () => {
    // [bulk-copy-lives-in-the-lib]: a string in the component is a string node tests cannot
    // reach. Values are not pinned here -- F-H still owes the user an approval pass.
    const live = ACTIVITY_COPY as unknown as Record<string, unknown>
    for (const key of Object.keys(EXPECTED_NEW_COPY) as NewCopyKey[]) {
      expect(typeof live[key], `ACTIVITY_COPY.${key} must exist`).toBe('string')
      expect(String(live[key]).length, `ACTIVITY_COPY.${key} must not be empty`).toBeGreaterThan(0)
    }
    const src = readFileSync(SOURCE, 'utf8')
    expect(src, 'the source scan read the wrong file').toContain('export function InvoiceActivityCard')
    expect((src.match(/ACTIVITY_COPY\./g) ?? []).length, 'the card must read its copy from the lib').toBeGreaterThanOrEqual(3)
  })
})

// AUDIT-09-04 QA, F-J. Reusing AuditRow drags a disabled "View transmission evidence ->"
// onto the invoice page for the three submissions-domain types the invoice-scoped read can
// return (submission.accepted/rejected/failed -- the generated column's second event list).
// It is not a defect, but it is NEW on this surface, so it gets an oracle here rather than
// only in AuditRow's own suite: subtask 09's whole-surface diff must LIST it.
describe('InvoiceActivityCard inherited controls (F-J)', () => {
  const SUBMISSION_EVENT = 'submission.failed'

  it('invoiceActivity_fixtureSubmissionEventBinsWhereTheSpecAssumes', () => {
    expect(auditEventView(SUBMISSION_EVENT).domain).toBe('submissions')
  })

  it('invoiceActivity_inheritsTheDisabledEvidenceAffordance', async () => {
    // submission.* takes payload.invoice_id, not payload.id (the generated column's second
    // branch); invoiceRef reads invoice_id first, and a null ref hides the button entirely.
    mockFetch(logResponse({ events: [auditEvent({ event: SUBMISSION_EVENT, payload: { invoice_id: INVOICE_ID, attempt: 3 } })] }))
    renderCard()
    await loaded()
    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)

    const evidence = screen.getByTestId('audit-evidence-affordance')
    expect(evidence, 'the control is present, not hidden').toBeTruthy()
    expect(evidence).toHaveProperty('disabled', true)
    // AUDIT-08: a title= on a disabled button never fires in Chromium.
    expect(evidence.getAttribute('title')).toBeNull()
    const reasonId = evidence.getAttribute('aria-describedby')
    expect(reasonId, 'the block must be a text node the control names').toBeTruthy()
    const reason = document.getElementById(reasonId!)
    expect(reason, `aria-describedby="${reasonId}" points at nothing`).toBeTruthy()
    expect(reason!.textContent!.length).toBeGreaterThan(0)
    // Honest on THIS surface: the invoice page offers no evidence route at all, so the
    // sentence must not promise one somewhere on this page.
    expect(reason!.textContent).toContain('not reachable from this screen')
    // The other inherited affordance stays off: this row is the only one where both could
    // have appeared, since invoiceRef is non-null for exactly the same payloads.
    expect(screen.queryByTestId('audit-invoice-affordance')).toBeNull()
  })

  it('invoiceActivity_evidenceAffordanceIsScopedToSubmissionRows', async () => {
    // The control for the case above: an invoices-domain row expands with no evidence
    // button, so its presence there is the domain gate and not an unconditional render.
    mockFetch(logResponse({ events: [auditEvent({ event: INVOICES_EVENT, payload: { id: INVOICE_ID } })] }))
    renderCard()
    await loaded()
    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)

    expect(screen.getByTestId('audit-expansion')).toBeTruthy()
    expect(screen.queryByTestId('audit-evidence-affordance')).toBeNull()
    expect(screen.queryByTestId('audit-evidence-blocked-reason')).toBeNull()
  })
})

// AUDIT-09-04 QA, AC-7's unit half. Geometry A asserts the card scrolls and the page does
// not; arch section 7 says this file proves the card ASKED for the scroll container. It did
// not -- replacing <AuditTable> with a bare fragment left all 3152 app tests green, taking
// the overflowX scroller and the 868px floor with it. The import scan cannot see this: an
// unused import is still import text.
describe('InvoiceActivityCard scroll containment (AC-7, unit half)', () => {
  it('invoiceActivity_delegatesContainmentToAuditTablesScroller', async () => {
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 3 }]) }))
    renderCard()
    await loaded()

    const table = screen.getByTestId('audit-table')
    expect(screen.getByTestId('audit-table-head')).toBeTruthy()
    // The 868px floor is what makes the rows refuse to collapse; without it the table
    // shrinks to the column and geometry A3 has nothing to measure.
    expect(table.style.minWidth).toBe(`${AUDIT_TABLE_MIN_WIDTH}px`)
    // AuditTable.tsx wraps the table in the overflowX:'auto' div. jsdom applies no layout,
    // so this is the ask, not the result -- geometry A1/A3 own the result.
    const scroller = table.parentElement
    expect(scroller, 'the table must sit inside a scroll container').toBeTruthy()
    expect(scroller!.style.overflowX).toBe('auto')
    // ...and the card clips at its own rounded border rather than letting the row escape.
    expect(screen.getByTestId('invoice-activity').style.overflow).toBe('hidden')
  })
})

// AUDIT-09-05 Mode A: the "Open in Audit →" control. The card's whole job is to call
// ctx.openAuditForInvoice(invoiceId, invoiceNumber) once; the atom, the nav and the seed are
// App.auditPrefilter.test.tsx's and AuditView.test.tsx's.
describe('InvoiceActivityCard "Open in Audit →" hand-off (AUDIT-09-05)', () => {
  // Naive: it would also cut a `//` inside a string literal, and this file's component has
  // none. The paired control needles below fail loudly if it eats too much or too little.
  function stripComments(src: string): string {
    return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '')
  }

  it('invoiceActivity_openInAuditCallsTheHandoff', async () => {
    mockFetch(logResponse())
    const { ctx } = renderCard(INVOICE_ID, INVOICE_NUMBER)
    await loaded()

    const btn = screen.queryByTestId('activity-open-in-audit')
    expect(btn, 'the loaded rung must carry the hand-off control').not.toBeNull()
    // Read from the lib, never retyped: the cap note interpolates the same constant.
    expect(btn!.textContent).toBe(ACTIVITY_COPY.auditLink)
    expect(ctx.openAuditForInvoice, 'nothing may call the hand-off before the click').not.toHaveBeenCalled()

    fireEvent.click(btn!)

    // Exactly once, with the id AND the number: the pill reads the number, and the reader 400s
    // on anything that is not a uuid, so the two are not interchangeable.
    expect(ctx.openAuditForInvoice).toHaveBeenCalledTimes(1)
    expect(ctx.openAuditForInvoice).toHaveBeenCalledWith(INVOICE_ID, INVOICE_NUMBER)
  })

  it('invoiceActivity_openInAuditPassesTheInvoiceItIsMounted', async () => {
    // A hardcoded id or a number read off a payload would pass the case above. This one moves
    // both props off their defaults.
    mockFetch(logResponse())
    const { ctx } = renderCard(OTHER_INVOICE_ID, 'INV-OTHER')
    await loaded()

    const btn = screen.queryByTestId('activity-open-in-audit')
    expect(btn, 'the loaded rung must carry the hand-off control').not.toBeNull()
    fireEvent.click(btn!)
    expect(ctx.openAuditForInvoice).toHaveBeenCalledWith(OTHER_INVOICE_ID, 'INV-OTHER')
  })

  it('invoiceActivity_openInAuditRendersWithAndWithoutTheToggle', async () => {
    // One row: activityToggleCopy returns no label, so the hand-off holds the row alone.
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: 1 }]) }))
    renderCard()
    await loaded()
    expect(screen.queryByTestId('activity-toggle'), 'control: one row means no toggle').toBeNull()
    expect(screen.queryAllByTestId('activity-open-in-audit'), 'the hand-off must not depend on the toggle').toHaveLength(1)
    cleanup()

    // Six rows: the toggle appears beside it, and the hand-off is still exactly one control.
    mockFetch(logResponse({ events: eventsOf([{ event: INVOICES_EVENT, n: ACTIVITY_REST_ROWS + 1 }]) }))
    renderCard()
    await loaded()
    expect(screen.queryByTestId('activity-toggle'), 'control: six rows means a toggle').not.toBeNull()
    expect(screen.queryAllByTestId('activity-open-in-audit'), 'exactly one hand-off, never one per row').toHaveLength(1)
  })

  it('invoiceActivity_openInAuditIsAbsentWhenThereIsNothingToOpen', async () => {
    // Positive control on the same locator first: the loaded rung DOES carry it.
    mockFetch(logResponse())
    renderCard()
    await loaded()
    expect(screen.queryByTestId('activity-open-in-audit'), 'control: the loaded rung carries the hand-off').not.toBeNull()
    cleanup()

    // An empty feed would hand off to an Audit screen pre-filtered to an invoice with no
    // events -- an empty screen. The control has nothing to offer there.
    mockFetch(logResponse({ events: [], total: 0 }))
    renderCard()
    await screen.findByTestId('invoice-activity-empty')
    expect(screen.queryByTestId('activity-open-in-audit'), 'the empty rung must not offer the hand-off').toBeNull()
  })

  it('invoiceActivity_openInAuditLabelIsNeverHardcoded', () => {
    const raw = readFileSync(SOURCE, 'utf8')
    const src = stripComments(raw)
    expect(src, 'the scan read the wrong file').toContain('export function InvoiceActivityCard')
    // Paired control needles for the stripper itself.
    expect(raw, 'control: the reference comment must exist to be stripped').toContain('the ONLY owner of the toggle')
    expect(src, 'control: the stripper removed nothing').not.toContain('the ONLY owner of the toggle')

    // The positive half is what stops this being a guard that is vacuously green forever.
    expect(src, 'the label must be READ from the lib').toContain('ACTIVITY_COPY.auditLink')
    expect(src, 'a second copy of the string drifts from the note that interpolates it').not.toContain(
      ACTIVITY_COPY.auditLink,
    )
  })

  it('invoiceActivity_theMountSuppliesTheInvoiceNumber', () => {
    // C-4: the card has no invoice number on its props, so InvoiceDetail owes it a fifth edit.
    // Nothing else in this suite can see a missing prop -- it arrives as `undefined` and the
    // hand-off silently loses the pill's label.
    const src = readFileSync(join(__dirname, 'InvoiceDetail.tsx'), 'utf8')
    expect(src, 'the scan read the wrong file').toContain('<InvoiceActivityCard')
    const mount = src.slice(src.indexOf('<InvoiceActivityCard'))
    const end = mount.indexOf('/>')
    expect(end, 'the card mount has no self-closing tag').toBeGreaterThan(-1)
    const tag = mount.slice(0, end)
    // Control needle: the slice must have captured the props, not an empty span.
    expect(tag, 'the slice missed the mount\'s props').toContain('invoiceId=')
    expect(tag, 'the card mount must pass the invoice number down').toContain('invoiceNumber={inv.invoice_number}')
  })
})
