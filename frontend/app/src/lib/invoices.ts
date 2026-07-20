// App-side invoice list/detail data-access helpers (M4-09-03, task-184): fetch
// wrappers + pure helpers over the injected authedFetch, covered by invoices.test.ts
// (I1-I21).
//
// Types mirror the wire shapes in internal/invoice/invoice.go and handlers.go on THIS
// branch: Status is the 7-state CHECK-constrained lifecycle (invoice.go:28-36); Invoice
// (invoice.go:83-114) money fields are *string ([D13]); Violations is the stored
// violations JSONB, reusing validationApi.ts's Violation shape (rule_key/severity/
// message/path?); RuleSetVersion is a *int surfaced ONLY by GetHandler's getResponse
// wrapper (handlers.go:180-183, [read-shape-getresponse-wrapper]) with NO omitempty —
// an un-validated invoice renders an explicit null, never a dropped key. StatusChange
// (invoice.go:133-138) is one invoice_status_history row, returned as a BARE array by
// HistoryHandler (handlers.go:477-505, [history-endpoint-scope]) — no pagination, no
// envelope, unlike every other handler.
//
// Fetch wrappers are thin wrappers around an injected authedFetch (the app-side 401
// seam from M3-07-02, src/lib/authedFetch.ts), mirroring listEntities/updateEntity in
// portfolio.ts. Gateway path prefix confirmed `${base}/api/invoice/v1/…`
// (importApi.ts:248,263):
// - listInvoices:      GET   `${base}/api/invoice/v1/invoices[?needs_attention=true]`,
//                       unwraps `.invoices` (ListHandler, handlers.go:223-292,
//                       [needs-attention-param-strictness]).
// - getInvoice:         GET   `${base}/api/invoice/v1/invoices/{id}`, normalizes a
//                       missing/undefined `rule_set_version` to `null` (defensive; the
//                       backend now sends explicit null either way, GetHandler
//                       handlers.go:185-211).
// - getInvoiceHistory:  GET   `${base}/api/invoice/v1/invoices/{id}/history`, resolves
//                       the bare StatusChange[] verbatim (HistoryHandler).
// - editInvoice:        PATCH `${base}/api/invoice/v1/invoices/{id}`, only the changed
//                       fields in the body (EditHandler, handlers.go:427-475, [D9]).
// - revalidateInvoice:  POST  `${base}/api/invoice/v1/invoices/{id}/validate`, no body
//                       (ValidateHandler, handlers.go:381-425, [gate-endpoint] — the
//                       gate is re-callable at any time; re-calling IS re-validation).
// Non-2xx responses reject with the underlying ApiError unchanged (apiFetch's own
// contract) — these helpers must not swallow or reshape it.
//
// invoiceStatusStyle is a pure StatusStyle mapper over the 7 canonical states, following
// the established var(--status-<color>-{bg,border,text}) + uppercase-label convention
// (entityStatusStyle in portfolio.ts, severityStyle in validationApi.ts): unknown status
// -> muted fallback (total mapping, mirrors severityStyle's `?? MUTED_STYLE`).
//
// isFixable(status) is the edit-surface guard mirror of Store.Edit's own precondition
// (ErrNotFixable, invoice.go:257-269, [A1]/System Design §4 step 3): true for
// draft/validated only, false for every other status.
//
// verdictStatus(staleSinceEdit) is the within-session fix-loop indicator (Core AC #7):
// 'stale' once the currently-displayed validation verdict no longer describes the
// invoice's edited content, else 'current'.
//
// shouldFetchInvoices/invoicesViewState are pure render-decision helpers, mirroring
// shouldFetchEntities/clientsViewState in portfolio.ts: the no-gateway zero-network
// short-circuit (a deployed SPA with no backend behind it must make no network calls)
// means base==null => 'idle' regardless of async status; otherwise the view state
// mirrors async.status.
import type { AuthedFetch } from './portfolio'
import type { Violation } from './validationApi'
import type { StatusStyle } from '../types'
import type { AsyncState, AsyncStatus } from '@invoice-os/api-client'

export type InvoiceStatus =
  | 'draft'
  | 'validated'
  | 'queued'
  | 'submitted'
  | 'accepted'
  | 'rejected'
  | 'failed'

// One line_items row (invoice.go:56-64, LineItem). Optional on InvoiceRecord: Store.List
// leaves LineItems nil ([D7]/[D8]), so list items never carry this key, while Store.Get
// always hydrates it.
export interface InvoiceLineItem {
  id: string
  line_no: number
  description: string | null
  quantity: string | null
  unit_price: string | null
  line_total: string | null
  line_tax: string | null
}

// The invoice record shape shared by listInvoices/getInvoice (invoice.go:83-114 plus the
// getResponse/validateResponse sibling key, handlers.go:180-183/376-379).
// `rule_set_version` is typed as always-present (number | null) even though the raw List
// wire payload never carries the key at all (Invoice.RuleSetVersion is json:"-" there) —
// only getInvoice's own normalization (`?? null`) makes that field trustworthy; callers
// reading a list item's `rule_set_version` get `undefined` at runtime despite the type.
export interface InvoiceRecord {
  id: string
  entity_id: string
  invoice_number: string
  status: InvoiceStatus
  issue_date: string | null
  supplier_tin: string | null
  supplier_name: string | null
  buyer_tin: string | null
  buyer_name: string | null
  currency: string | null
  subtotal: string | null
  vat: string | null
  total: string | null
  violations: Violation[]
  rule_set_version_id: string | null
  created_at: string
  line_items?: InvoiceLineItem[]
  rule_set_version: number | null
}

// GET /v1/invoices response envelope (listResponse, handlers.go:91-98).
export interface InvoiceListResponse {
  invoices: InvoiceRecord[]
  pagination: { limit: number; offset: number; total: number }
}

// One invoice_status_history row (invoice.go:133-138, StatusChange). FromStatus is
// nullable: the genesis row (NULL -> 'draft') has no predecessor state.
export interface StatusChange {
  from_status: InvoiceStatus | null
  to_status: InvoiceStatus
  actor: string
  changed_at: string
}

// listInvoices's only filter today (ListFilter.NeedsAttention, invoice.go:196-201,
// [needs-attention-bool-true-only]) — absent/false applies no predicate.
export interface ListInvoicesOptions {
  needsAttention?: boolean
}

// editInvoice's PATCH body: the 9 optional header MBS-content fields (editReq,
// handlers.go:70-80, [D9]) — identity/lifecycle are not the edit's job. Reuses
// InvoiceRecord's own field types so the two never drift apart.
export type InvoiceEditInput = Partial<
  Pick<
    InvoiceRecord,
    | 'issue_date'
    | 'supplier_tin'
    | 'supplier_name'
    | 'buyer_tin'
    | 'buyer_name'
    | 'currency'
    | 'subtotal'
    | 'vat'
    | 'total'
  >
>

export async function listInvoices(
  authedFetch: AuthedFetch,
  base: string,
  opts: ListInvoicesOptions = {},
): Promise<InvoiceRecord[]> {
  const query = opts.needsAttention === true ? '?needs_attention=true' : ''
  const res = await authedFetch<InvoiceListResponse>(`${base}/api/invoice/v1/invoices${query}`)
  return res.invoices
}

export async function getInvoice(authedFetch: AuthedFetch, base: string, id: string): Promise<InvoiceRecord> {
  const res = await authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}`)
  return { ...res, rule_set_version: res.rule_set_version ?? null }
}

export async function getInvoiceHistory(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
): Promise<StatusChange[]> {
  return authedFetch<StatusChange[]>(`${base}/api/invoice/v1/invoices/${id}/history`)
}

export async function editInvoice(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
  patch: InvoiceEditInput,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}`, { method: 'PATCH', body: patch })
}

export async function revalidateInvoice(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}/validate`, { method: 'POST' })
}

// Total-in-practice mapping over the 7 canonical states (typed Partial, mirroring
// severityStyle/SEVERITY_STYLE in validationApi.ts): draft -> muted, validated/accepted
// -> green, queued/submitted -> amber, rejected/failed -> red. Labels are uppercased
// per the entityStatusStyle/statusStyle convention (portfolio.ts / lib/clients.ts).
const MUTED_STYLE: StatusStyle = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'UNKNOWN' }

const INVOICE_STATUS_STYLE: Partial<Record<InvoiceStatus, StatusStyle>> = {
  draft: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'DRAFT' },
  validated: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'VALIDATED' },
  queued: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'QUEUED' },
  submitted: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'SUBMITTED' },
  accepted: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'ACCEPTED' },
  rejected: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'REJECTED' },
  failed: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'FAILED' },
}

// Out-of-enum values reach this at runtime (JSON.parse'd server data, no enum
// validation) despite the InvoiceStatus type -> fall back to MUTED_STYLE rather than
// returning undefined (mirrors severityStyle's `?? MUTED_STYLE`, validationApi.ts:67).
export function invoiceStatusStyle(status: InvoiceStatus): StatusStyle {
  return INVOICE_STATUS_STYLE[status] ?? MUTED_STYLE
}

export function isFixable(status: InvoiceStatus): boolean {
  return status === 'draft' || status === 'validated'
}

export function verdictStatus(staleSinceEdit: boolean): 'stale' | 'current' {
  return staleSinceEdit ? 'stale' : 'current'
}

export function shouldFetchInvoices(base: string | null): boolean {
  return base != null
}

export function invoicesViewState(base: string | null, s: AsyncState<InvoiceRecord[]>): AsyncStatus {
  if (base == null) return 'idle'
  return s.status
}
