// App-side invoice list/detail data-access helpers (M4-09-03, task-184): fetch
// wrappers + pure helpers over the injected authedFetch, covered by invoices.test.ts
// (I1-I21, plus the fiscal/batch-submit/selection/live-refresh specs M5-09-03,
// task-253 adds).
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
// - listInvoices:      GET   `${base}/api/invoice/v1/invoices[?needs_attention=true]
//                       [&entity_id=...]`, unwraps `.invoices` (ListHandler,
//                       handlers.go:223-292, [needs-attention-param-strictness],
//                       [entity-id-restored]).
// - getInvoice:         GET   `${base}/api/invoice/v1/invoices/{id}`, resolves an
//                       InvoiceDetailRecord and normalizes a missing/undefined
//                       `rule_set_version` to `null` (defensive; the backend now sends
//                       explicit null either way, GetHandler handlers.go:185-211).
//                       `qr_png_base64` (M5-09-01/03) gets the same defensive
//                       normalization once implemented -- the wire never actually omits
//                       either key (getResponse, handlers.go:189-192, no omitempty).
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
// (ErrNotFixable, invoice.go:261-273, [A1]/System Design §4 step 3): true for
// draft/validated/rejected, false for every other status. M5-05-01 (task-237) widened
// the BACKEND precondition to this third fixable status (rejected, the rework path);
// M5-09-03 (task-253, Core AC #4) re-synced this mirror to match, closing
// [spa-untouched]. Widening to `rejected` also turns Re-validate on for a rejected
// invoice (InvoiceDetail.tsx's "Fix & re-validate" card) -- correct for the edit form
// (Store.Edit accepts draft/validated/rejected, store.go:592-594), but the validate
// gate itself stays draft-only (gate.go:162 -> ErrNotDraft -> 409), so clicking
// Re-validate before any edit 409s and is caught/surfaced by the existing handler, the
// same known wrinkle already documented for `validated`.
//
// verdictStatus(staleSinceEdit, inv) is the within-session fix-loop indicator (Core AC
// #7) plus the on-load demoted-draft derivation (Core AC #5, task-188 item 4): 'stale'
// iff staleSinceEdit===true OR (inv.status==='draft' AND inv.rule_set_version_id!=null
// AND no violation has severity==='error') — a draft the gate blocked, then edited,
// stamps rule_set_version_id/error-violations that survive the demotion (store.go
// never clears them on Edit), so this is what tells a demoted-and-since-edited draft
// apart from a demoted-but-untouched one on reload. Named residual (accepted, not a
// bug): a draft the gate BLOCKED (error violations + rule_set_version_id stamped) and
// then edited stays 'draft' and reports 'current' on reload -- undetectable
// client-side, since the wire carries no content fingerprint or validated_at.
// `staleSinceEdit` still covers it within the same session; I5d pins this residual
// deliberately so it isn't misread as a bug later.
//
// shouldFetchInvoices/invoicesViewState are pure render-decision helpers, mirroring
// shouldFetchEntities/clientsViewState in portfolio.ts: the no-gateway zero-network
// short-circuit (a deployed SPA with no backend behind it must make no network calls)
// means base==null => 'idle' regardless of async status; otherwise the view state
// mirrors async.status.
//
// gateByActiveEntity ([dashboard-scope-per-client], persona-handoff-fix step 2,
// RENAMED by the step-6 regression fix, [entity-id-restored]): the Invoices/Customers/
// Reports lists are CLIENT-scoped surfaces (Sidebar.tsx's CLIENT nav group). Step 2
// originally did the ENTIRE narrowing here, in the browser, over a still-tenant-wide,
// LIMIT-50 listInvoices() response -- filter-AFTER-paginate, which silently dropped an
// entity's own invoices whenever they weren't inside the newest 50 tenant-wide (CI
// caught this, e2e/topology/import-wizard.spec.ts:208). listInvoices now sends
// `entity_id` itself (ListHandler, handlers.go, [entity-id-restored]) so the AUTHORITATIVE
// narrowing happens server-side, in SQL, before the limit applies -- but the row-level
// `row.entity_id === entityId` check STAYS here too, now as a render-time invariant
// rather than the primary filter: on a company switch, `list.data`/`live` still hold the
// PREVIOUS entity's rows for at least one committed render before the deps-triggered
// refetch resolves (useAsync's `start` dispatch is a passive effect, so it runs AFTER
// that commit) -- without this check that frame would flash the wrong client's
// invoices/KPIs/buyer names. This can only ever DROP rows, never invent wrong ones, so
// it cannot mask a server-side entity_id regression (that's what
// internal/invoice/entity_filter_test.go / TestListHandler_EntityIDParam are for). Also
// bypassed entirely in-house (no business_entities row, [entity-picker] trap 1) --
// its one "client" IS the tenant. A null entityId in firm mode (entities still
// loading/errored, [entity-picker] trap 2) means the active client isn't known yet, so
// this returns [] rather than whatever the network call happened to fetch (the request
// itself omits `entity_id` in that window too, so it would otherwise be tenant-wide).
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

// One invoices.rejection_reasons element (internal/submission/result.go:27-31, Reason).
// `path` carries Go's `omitempty` -- genuinely absent (undefined), never "", when the
// APP's rejection didn't cite a specific MBS field. The column itself is `jsonb NOT
// NULL DEFAULT '[]'` (migration :61) and MarkRejectedTx normalizes nil -> []Reason{}
// before marshal (actor.go:173-176), so the array is never null on the wire --
// `rejection_reasons` below is typed as a bare array, never `| null`, mirroring how
// `violations: Violation[]` is already typed.
export interface RejectionReason {
  code: string
  message: string
  path?: string
}

// The invoice record shape shared by listInvoices/getInvoice (invoice.go:83-114 plus the
// getResponse/validateResponse sibling key, handlers.go:180-183/376-379).
// `rule_set_version` is typed as always-present (number | null) even though the raw List
// wire payload never carries the key at all (Invoice.RuleSetVersion is json:"-" there) —
// only getInvoice's own normalization (`?? null`) makes that field trustworthy; callers
// reading a list item's `rule_set_version` get `undefined` at runtime despite the type.
//
// `import_batch_id`/`irn`/`csid`/`qr_payload`/`rejection_reasons` (M5-09-03, task-253,
// Core AC #1) re-sync this mirror against invoice.go:83-105 -- all five are `json:"..."`
// with no `omitempty` on the Go struct, so every one of them is present on BOTH the
// list and get wire as an explicit value or explicit null, never an absent key.
export interface InvoiceRecord {
  id: string
  entity_id: string
  import_batch_id: string | null
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
  irn: string | null
  csid: string | null
  qr_payload: string | null
  rejection_reasons: RejectionReason[]
  line_items?: InvoiceLineItem[]
  rule_set_version: number | null
}

// getInvoice's own response shape: InvoiceRecord plus getResponse's OTHER sibling key,
// `qr_png_base64` (handlers.go:189-192, M5-09-01/03). `rule_set_version` is redeclared
// here identically, not moved off InvoiceRecord -- it stays there (listInvoices' element
// type / the draftInvoice-style fixtures depend on it), this is just co-locating the two
// getResponse-only keys on the type that actually represents a GET response.
// `qr_png_base64` has no `omitempty` either (always present, explicit null when there is
// no qr_payload or rendering failed) -- getInvoice's own `?? null` normalization is
// defensive-only, the same posture already taken for `rule_set_version`.
export interface InvoiceDetailRecord extends InvoiceRecord {
  rule_set_version: number | null
  qr_png_base64: string | null
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

// listInvoices's two filters (ListFilter, invoice.go:200-217): needsAttention
// ([needs-attention-bool-true-only] — absent/false applies no predicate) and entityId
// ([entity-id-restored] — absent/undefined applies no filter, tenant-wide). Both AND
// together server-side when set together.
export interface ListInvoicesOptions {
  needsAttention?: boolean
  entityId?: string
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

// The 9 editable header fields (editReq, handlers.go:70-80, [D9]) -- moved here from
// InvoiceDetail.tsx (M5-09-03, task-253, addendum A2) so mbsPathToEditField and the
// component share one definition; EditFieldKey travels with the const it's derived
// from, not alone.
export const EDIT_FIELD_KEYS = [
  'issue_date',
  'supplier_tin',
  'supplier_name',
  'buyer_tin',
  'buyer_name',
  'currency',
  'subtotal',
  'vat',
  'total',
] as const

export type EditFieldKey = (typeof EDIT_FIELD_KEYS)[number]

// One BatchSubmit result item (batch_submit.go:85-91, BatchSubmitResultItem). `status`
// is a bare string, NOT InvoiceStatus -- batch_submit.go:206-211 hard-codes a
// known-wrong value there (tracked M5-11). `reason` carries Go's `omitempty`: ABSENT
// (undefined) on the wire when `enqueued` is true, never `""`.
export interface BatchSubmitResultItem {
  invoice_id: string
  enqueued: boolean
  status: string
  reason?: string
}

export async function listInvoices(
  authedFetch: AuthedFetch,
  base: string,
  opts: ListInvoicesOptions = {},
): Promise<InvoiceRecord[]> {
  const params = new URLSearchParams()
  if (opts.needsAttention === true) params.set('needs_attention', 'true')
  if (opts.entityId) params.set('entity_id', opts.entityId)
  const query = params.toString() ? `?${params.toString()}` : ''
  const res = await authedFetch<InvoiceListResponse>(`${base}/api/invoice/v1/invoices${query}`)
  return res.invoices
}

export async function getInvoice(authedFetch: AuthedFetch, base: string, id: string): Promise<InvoiceDetailRecord> {
  const res = await authedFetch<InvoiceDetailRecord>(`${base}/api/invoice/v1/invoices/${id}`)
  return { ...res, rule_set_version: res.rule_set_version ?? null, qr_png_base64: res.qr_png_base64 ?? null }
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

// POST /v1/invoices/submissions -- the batch-submit trigger ([trigger-surface] /
// [batch-key-in-the-body], task-231 System Design; batchSubmitReq, handlers.go:534-536;
// route cmd/invoice/main.go:115). Body is `{invoice_ids, idempotency_key}`; the
// envelope is `{"results":[...]}`, never null (BatchSubmitResult, batch_submit.go:95-100),
// so this unwraps `.results` unconditionally, mirroring listInvoices' `.invoices`
// unwrap. Non-2xx rejects with the underlying ApiError unchanged.
//
// apiFetch JSON.stringifies the body object as-is, so its key order follows
// object-literal insertion order -- `invoice_ids` is declared before `idempotency_key`
// below to match the exact wire string I-submit-1 pins.
export async function submitInvoices(
  authedFetch: AuthedFetch,
  base: string,
  invoiceIds: string[],
  idempotencyKey: string,
): Promise<BatchSubmitResultItem[]> {
  const res = await authedFetch<{ results: BatchSubmitResultItem[] }>(
    `${base}/api/invoice/v1/invoices/submissions`,
    { method: 'POST', body: { invoice_ids: invoiceIds, idempotency_key: idempotencyKey } },
  )
  return res.results
}

// crypto.randomUUID() -- present in this runtime with no polyfill (lib: DOM,
// tsconfig.json), 36 chars, requires a secure context in the browser (HTTPS/localhost
// only; verified against node v25.2.1 / vitest 4.1.10, M5-09-03 addendum A10). Every
// submit path calls this once per batch-submit attempt to derive the idempotency key
// (Core AC #3).
export function newIdempotencyKey(): string {
  return crypto.randomUUID()
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
  return status === 'draft' || status === 'validated' || status === 'rejected'
}

export function verdictStatus(staleSinceEdit: boolean, inv: InvoiceRecord): 'stale' | 'current' {
  if (staleSinceEdit) return 'stale'
  const demotedSinceValidation =
    inv.status === 'draft' && inv.rule_set_version_id != null && !inv.violations.some((v) => v.severity === 'error')
  return demotedSinceValidation ? 'stale' : 'current'
}

// A total mapper over the APP's dotted MBS payload vocabulary (MBSPayload,
// internal/invoice/payload.go:85-125; party(), :152-157) -- NOT the snake_case column
// names (the fiscal-outcome migration header's `"path": "supplier_tin"` example is
// illustrative and wrong about the separator). `null` means "no editable field to
// flag", never "drop the reason" -- the caller still renders the reason in full
// (M5-09-05). `invoice_number` -> null is correct: editReq excludes it
// (handlers.go:70-80), so there is no field to flag; `line_items[...]` and any APP-only
// vocabulary (e.g. `customer.taxIdentifier`, which appears only in the synthesized
// error body) likewise have no SPA edit-form counterpart.
const MBS_PATH_TO_EDIT_FIELD: Record<string, EditFieldKey> = {
  issue_date: 'issue_date',
  currency: 'currency',
  subtotal: 'subtotal',
  vat: 'vat',
  total: 'total',
  'supplier.tin': 'supplier_tin',
  'supplier.name': 'supplier_name',
  'buyer.tin': 'buyer_tin',
  'buyer.name': 'buyer_name',
}

export function mbsPathToEditField(path: string | undefined): EditFieldKey | null {
  if (path == null) return null
  return MBS_PATH_TO_EDIT_FIELD[path] ?? null
}

// Field-flag map for the edit form (task-251 AC #3/#5; extracted from InvoiceDetail.tsx
// in response to a QA finding so this decision has a test oracle). Reduces a rejection
// list to editable-field -> reason code, one entry per field that some reason's MBS path
// maps to. When two or more reasons map to the SAME field, the FIRST one (in `reasons`
// order) wins -- a deliberate tie-break, not an accident: the flag only needs to point
// the operator at the field, and the reason list above it already shows every reason in
// full, so which of several colliding codes gets shown on the flag itself is cosmetic. A
// reason with an unmapped (or absent) path contributes no entry; it is never swallowed,
// just not flagged -- it still renders in full on the rejection card.
export function reasonFieldFlags(reasons: RejectionReason[]): Map<EditFieldKey, string> {
  const flags = new Map<EditFieldKey, string>()
  for (const reason of reasons) {
    const field = mbsPathToEditField(reason.path)
    if (field != null && !flags.has(field)) flags.set(field, reason.code)
  }
  return flags
}

// not_validated/duplicate_request are the two reachable BatchSubmitResultItem.reason
// values (batchSubmitReasonNotValidated/batchSubmitReasonDuplicate, handlers.go) --
// anything else passes through verbatim rather than being swallowed, so an unknown
// future reason still surfaces something to the operator.
const SKIP_REASON_LABELS: Record<string, string> = {
  not_validated: 'Not validated — validate it first',
  duplicate_request: 'Already submitted with this request',
}

export function skipReasonLabel(reason: string): string {
  return SKIP_REASON_LABELS[reason] ?? reason
}

// Selection helpers for the batch-submit list surface (M5-09-06). Only `validated`
// invoices can be batch-submitted (Store.ApplyValidation is the only path into
// `queued`), so selection is scoped to that one status throughout.
export function isRowSelectable(status: InvoiceStatus): boolean {
  return status === 'validated'
}

export function selectableIds(rows: InvoiceRecord[]): string[] {
  return rows.filter((row) => isRowSelectable(row.status)).map((row) => row.id)
}

export function toggleSelection(sel: string[], id: string): string[] {
  return sel.includes(id) ? sel.filter((s) => s !== id) : [...sel, id]
}

// Keeps only ids that are both still present in `rows` (didn't scroll/filter away) AND
// still `validated` (didn't get submitted/edited/re-validated out from under a stale
// selection since it was last computed).
export function pruneSelection(sel: string[], rows: InvoiceRecord[]): string[] {
  const selectable = new Set(selectableIds(rows))
  return sel.filter((id) => selectable.has(id))
}

// The list header checkbox's tri-state (task-257). LOAD-BEARING EDGE: `every()` over an
// empty array is vacuously true, so a naive `selected.length === selectableIds(rows)
// .length` (or an unguarded `.every()`) implementation renders a CHECKED select-all on a
// page with zero selectable rows -- guarded below by checking `selectable.length === 0`
// first and never using `.every()`; a stale id in `selected` that isn't in `selectable`
// also can't inflate the intersection count into a false 'all'.
export function selectAllState(selected: string[], rows: InvoiceRecord[]): 'none' | 'some' | 'all' {
  const selectable = selectableIds(rows)
  if (selectable.length === 0) return 'none'
  const selectedSet = new Set(selected)
  const matched = selectable.filter((id) => selectedSet.has(id)).length
  if (matched === 0) return 'none'
  return matched === selectable.length ? 'all' : 'some'
}

// Live-refresh gate (M5-09-07's useLiveRefresh hook consumes these; every decision it
// makes is one of these pure predicates, tested here since the hook itself isn't).
export function isInFlight(status: InvoiceStatus): boolean {
  return status === 'queued' || status === 'submitted'
}

export function shouldPollInvoice(status: InvoiceStatus, visible: boolean): boolean {
  return isInFlight(status) && visible
}

export function shouldPollList(rows: InvoiceRecord[], visible: boolean): boolean {
  return rows.some((row) => isInFlight(row.status)) && visible
}

export const LIVE_POLL_MS = 2000

// Decides whether a polled tick should also re-fetch the status timeline (Decision
// [history-refresh-predicate]): true only on a real change from a KNOWN previous
// status. `prev === null` covers the very first observation, which the initial
// `history.run()` on mount already handles -- firing here too would double-fetch.
export function shouldRefreshHistory(prev: InvoiceStatus | null, next: InvoiceStatus): boolean {
  return prev !== null && prev !== next
}

// Detail rejection-card visibility + provenance label (task-251 ACs #3/#4/#7).
// `status !== 'accepted'` in shouldShowRejectionCard is a BACKSTOP against a
// server-side bug (AC #7) -- an accepted invoice should never carry rejection_reasons,
// but the card must not show one if it somehow does.
export function shouldShowRejectionCard(inv: Pick<InvoiceRecord, 'status' | 'rejection_reasons'>): boolean {
  return inv.rejection_reasons.length > 0 && inv.status !== 'accepted'
}

// 'current' iff the invoice is presently `rejected` (this IS the live verdict);
// 'historical' for everything else, including a demoted draft whose rejection_reasons
// are carried over from before the rejected->draft demotion (M5-09-02).
export function rejectionProvenance(status: InvoiceStatus): 'current' | 'historical' {
  return status === 'rejected' ? 'current' : 'historical'
}

// Fiscal-record card visibility (task-251 AC #1): only an accepted invoice that
// actually has an IRN has a fiscal record to show.
export function shouldShowFiscalRecord(inv: Pick<InvoiceRecord, 'status' | 'irn'>): boolean {
  return inv.status === 'accepted' && inv.irn != null
}

export function shouldFetchInvoices(base: string | null): boolean {
  return base != null
}

export function invoicesViewState(base: string | null, s: AsyncState<InvoiceRecord[]>): AsyncStatus {
  if (base == null) return 'idle'
  return s.status
}

// See the file-header comment for why this exists (listInvoices now does the real
// narrowing server-side, via `entity_id` -- this is a render-time INVARIANT, not the
// primary filter anymore). isInhouse bypasses it entirely (in-house's "client" is the
// whole tenant); entityId === null in firm mode (no entity resolved yet) yields [],
// never every row. The row-level `entity_id === entityId` check stays -- advisor
// review (product-advisor, pre-commit) caught that dropping it reintroduces a real bug:
// on a company switch, `list.data`/`live` still hold the PREVIOUS entity's rows for at
// least one committed render before the deps-triggered refetch (useAsync's `start`
// dispatch runs in a passive effect, after that commit) resolves -- without this row
// check, that frame renders the wrong client's invoices/KPIs/buyer names. A client-side
// filter can only ever DROP rows, never invent wrong ones, so it cannot mask the
// CI-caught filter-after-paginate regression (entity_filter_test.go's
// TestStoreList_EntityIDNarrowsBeforeLimit / TestListHandler_EntityIDParam already
// cover "does the server actually apply entity_id" directly).
export function gateByActiveEntity(rows: InvoiceRecord[], isInhouse: boolean, entityId: string | null): InvoiceRecord[] {
  if (isInhouse) return rows
  if (entityId == null) return []
  return rows.filter((row) => row.entity_id === entityId)
}
