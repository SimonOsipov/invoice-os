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
// - listInvoices:      GET   `${base}/api/invoice/v1/invoices[?<any of the eleven
//                       filters>]`, resolving the ENVELOPE whole -- `{invoices,
//                       pagination}`, not the bare array it used to unwrap (INVCR-01-08,
//                       task-284 AC-1: `pagination.total` is what every review filter-pill
//                       count and the pager read, and it was unreachable while this
//                       discarded it). Absent options emit no param and the client adds no
//                       defaults of its own (ListHandler, handlers.go:349-451,
//                       [needs-attention-param-strictness], [entity-id-restored]).
// - violationSummary:   GET   `${base}/api/invoice/v1/invoices/violation-summary
//                       ?import_batch_id=...` (repeated per id, BULK-01-02), unwraps
//                       `.rules` -- the review rail's rule-key aggregate
//                       (ViolationSummaryHandler, handlers.go; at least one usable id is
//                       REQUIRED there, zero usable ids is a 400).
// - getInvoice:         GET   `${base}/api/invoice/v1/invoices/{id}`, resolves an
//                       InvoiceDetailRecord and normalizes a missing/undefined
//                       `rule_set_version` to `null` (defensive; the backend now sends
//                       explicit null either way, GetHandler handlers.go:185-211).
//                       `qr_png_base64` (M5-09-01/03) gets the same defensive
//                       normalization once implemented -- the wire never actually omits
//                       either key (getResponse, handlers.go:189-192, no omitempty).
//                       The four action flags normalize FAIL-CLOSED (`=== true`), see
//                       getInvoice itself.
// - getInvoiceHistory:  GET   `${base}/api/invoice/v1/invoices/{id}/history`, resolves
//                       the bare StatusChange[] verbatim (HistoryHandler).
// - createInvoice:      POST  `${base}/api/invoice/v1/invoices`, follows editInvoice's
//                       shape verbatim (CreateHandler, handlers.go:117-165, INVCR-01-02,
//                       task-278) -- the missing caller for a handler that has existed
//                       since M4-02 (cmd/invoice/main.go:56). draftToCreateRequest
//                       (lib/invoiceDraft.ts) is the pure Draft -> body mapper.
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
// Edit / re-validate availability is READ OFF THE WIRE, never mirrored here
// (INVED-01-06, [gates-on-the-wire]): getInvoice surfaces can_edit / can_revalidate /
// revalidate_blocked_reason, which the backend DERIVES from legalTransitions (canEdit,
// store.go:919-942; canRevalidate, :944-960). The status-set predicate this file used to
// export for that job is deleted outright rather than re-pointed -- a second list of
// statuses is exactly the thing that drifts out of sync with the machine, which is what
// happened when the backend widened Edit to accept `rejected` and the SPA did not follow.
// isInFlight (polling) and INVOICE_STATUS_STYLE (pills) are NOT availability gates and
// stay. isRowSelectable became one in APPR-08-09, and -- unlike can_edit/can_revalidate
// above -- it does NOT read its answer off the wire. The server's rule is `TransmitClear`
// (`internal/approval`): `!policyActive || approvedRun`, and NEITHER input rides the row
// wire. `approval.run_state` is a DIFFERENT fact -- the NEWEST run's state (`RowFactsTx`)
// -- from which this file INFERS the gate, so it is a re-derivation from a PROXY. That
// makes TWO mirrored residuals here, not one: the `status === 'validated'` status set,
// and the inference itself. The inference matches the server on every reachable state
// only because of four invariants living outside the SPA: validation always arms
// (`ApplyValidation`), a rejection demotes in the same tx (`decideTx`), every walk back
// to draft cancels the live run (`CancelLiveRunTx`), and `approved` is EXISTS over ALL
// runs while `run_state` is only the newest (`TransmitClearTx`, `GateFactsTx`). Change
// one and this file must be re-decided; invoices.test.ts's A-sel-13 pins the surface and
// names all four. Both residuals are tolerated only because the server's skip is
// authoritative -- a wrong answer costs a checkbox, never a bypass. A gate that cannot
// make that claim reads the wire.
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
import { toDateInputValue } from './format'

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
  // kept_as_is_at/by/reason (INVCR-01-15, D6) -- Invoice's own three new columns
  // (invoice.go), no omitempty, so all three are present as explicit null on an
  // un-kept row, same convention as irn/csid/qr_payload above. `kept_as_is_at` is
  // what verdictPill's kept-invalid badge reads (lib/reviewBatch.ts); `_by`/`_reason`
  // are surfaced so the row-expansion panel can render the persisted reason verbatim.
  kept_as_is_at: string | null
  kept_as_is_by: string | null
  kept_as_is_reason: string | null
  // failure_kind (BUG-06-01/04) -- the reason a submission landed in
  // status='failed' (invoice.go), no omitempty, required (not `?`): the
  // server always says explicitly, so a missing fixture should be a loud
  // typecheck error, not a silent `undefined`.
  failure_kind: string | null
  line_items?: InvoiceLineItem[]
  rule_set_version: number | null
  // approval (APPR-08-08) -- a LIST-wire key ONLY: Go emits it on listItem, never on
  // getResponse. InvoiceDetailRecord therefore Omits it, so a detail-path read is a
  // COMPILE error rather than a runtime answer -- both `undefined?.run_state` and a
  // fabricated `null` make isRowSelectable fail OPEN, and `null` also claims "this
  // invoice has no run", which on a compliance surface is a lie. That is the
  // `rule_set_version` trap AVOIDED (typed present, reads undefined on the wire that
  // omits it -- reviewBatch.ts), not followed. On a list row it is REQUIRED and
  // nullable: normaliseInvoiceRow makes that true for every row.
  approval: InvoiceApproval | null
}

// One list row's approval standing, mirroring approval.RowFacts (gate.go) field for
// field. `due_at` is the wire's RFC3339 string, not a Date.
export interface InvoiceApproval {
  run_state: string
  pending_ord: number | null
  pending_role_title: string | null
  pending_holder_warn: boolean
  due_at: string | null
  overdue: boolean
}

// getInvoice's own response shape: InvoiceRecord minus its list-only `approval`, plus
// getResponse's OTHER sibling key `qr_png_base64` (getResponse, handlers.go,
// M5-09-01/03). `rule_set_version` is redeclared here identically, not moved off
// InvoiceRecord -- it stays there (listInvoices' element type / the draftInvoice-style
// fixtures depend on it), this is just co-locating the two getResponse-only keys on the
// type that actually represents a GET response.
// `qr_png_base64` has no `omitempty` either (always present, explicit null when there is
// no qr_payload or rendering failed) -- getInvoice's own `?? null` normalization is
// defensive-only, the same posture already taken for `rule_set_version`.
//
// `can_edit`/`can_revalidate`/`revalidate_blocked_reason` (INVED-01-06, task-267,
// [gates-on-the-wire]) are the backend-DERIVED per-action availability -- canEdit
// (draft/validated/rejected) and canRevalidate (draft only), store.go:940/960 -- so the
// SPA holds no status set of its own. The reason is non-null EXACTLY when
// `canEdit && !canRevalidate` (validated/rejected), null everywhere else, and its copy
// is the backend's verbatim ([revalidate-reason-from-backend]) -- the SPA never authors
// a fallback string.
//
// REQUIRED and nullable (no `?`), matching the two keys above: getResponse tags none of
// the three `omitempty` (handlers.go's getResponse, pinned by
// TestGetHandler_ActionFlagsFalseNotOmitted), so all three are present on every status.
// An optional `?` would compile just as well, which is exactly why it is wrong -- it
// lets a consumer read `undefined` and treat "the server did not say" as an open
// question, the fail-open shape [gates-on-the-wire] exists to remove. The same holds for
// every action key added since, `can_approve`/`can_reject` and their reasons included
// (APPR-08-06): all four are REQUIRED, never `?`. The ACTION FLAGS sit on
// InvoiceDetailRecord ONLY, never InvoiceRecord -- TestListHandler_NoActionFlagKeys
// keeps them off the list wire. `approval` runs the other way -- a list-only key
// (APPR-08-08), Omitted here so it cannot be read off a detail record at all.
export interface InvoiceDetailRecord extends Omit<InvoiceRecord, 'approval'> {
  rule_set_version: number | null
  qr_png_base64: string | null
  can_edit: boolean
  can_revalidate: boolean
  revalidate_blocked_reason: string | null
  can_submit: boolean
  submit_blocked_reason: string | null
  can_view_ubl: boolean
  ubl_blocked_reason: string | null
  // can_resolve_outside/resolve_outside_blocked_reason -- same no-omitempty,
  // fail-closed convention as the four flags above.
  can_resolve_outside: boolean
  resolve_outside_blocked_reason: string | null
  // can_approve/can_reject and their reasons (APPR-08-06) -- same convention again.
  // Approve and reject availability are IDENTICAL (one backend gate feeds both), so
  // the two booleans always agree and the two reasons are the same string; they ship
  // as two pairs because the screen renders two buttons, each needing its own slot.
  can_approve: boolean
  approve_blocked_reason: string | null
  can_reject: boolean
  reject_blocked_reason: string | null
}

// GET /v1/invoices response envelope (listResponse, handlers.go:110-113). Exactly two
// keys -- applied filters are deliberately NOT echoed back
// (TestListHandler_EnvelopeExactKeysAndEffectiveClampedValues pins len == 2), so
// `pagination` reports the EFFECTIVE limit/offset the server used, clamped.
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

// listInvoices's eleven filters (ListFilter, invoice.go:200-217; ListHandler,
// handlers.go:349-451). All eleven AND together server-side, and EVERY one is optional:
// an absent option emits NO query param, so `listInvoices(af, base, {})` is byte-identical
// to the pre-INVCR-01 tenant-wide call.
//
// The four that shipped first: needsAttention ([needs-attention-bool-true-only] —
// absent/false applies no predicate), entityId ([entity-id-restored] — absent/undefined
// applies no filter, tenant-wide), plus limit/offset, which the client never sent until
// now. INVCR-01-06 ([D4]) adds the review screen's five: importBatchIds, status, needsFix,
// ruleKey, q.
//
// importBatchIds (BULK-01-02, [one-review-screen]) widened from a single importBatchId
// to string[] so the review screen can narrow across every batch a multi-file run
// produced, on one query (ListHandler reads the REPEATED import_batch_id query param).
// Emitted as one `import_batch_id` param PER non-empty id (see listInvoices below) —
// `params.append`, never `.set` (which would overwrite all but the last).
//
// `status` is typed InvoiceStatus, NOT string, on purpose: D2 retires 'approved' from
// this flow's vocabulary, and a typed enum makes writing it a compile error at every call
// site — cheaper and earlier than a runtime guard.
//
// NO CLIENT-SIDE DEFAULTS (task-284 §1, pinned by LIST-5 + I7's `not.toContain('?')`).
// The server already defaults limit=50/offset=0 and clamps limit down to 200
// (handlers.go:361-375); an `opts.limit ?? 50` here would emit `?limit=50` on every call
// in the app and silently change the wire for the three shipped screens that send no
// options at all. The server's defaults stay the only defaults.
export interface ListInvoicesOptions {
  needsAttention?: boolean
  entityId?: string
  limit?: number
  offset?: number
  importBatchIds?: string[]
  status?: InvoiceStatus
  needsFix?: boolean
  ruleKey?: string
  q?: string
  // keptAsIs (INVCR-01-15, D6) -- the review shell's "N kept as-is" footer counter
  // query (ListFilter.KeptAsIs, store.go), same [needs-attention-bool-true-only]
  // shape as needsFix/needsAttention. Deliberately NOT one of the four toolbar
  // pills (System Design §7's own table) -- see reviewBatch.ts's ReviewPill union,
  // which is unchanged by this field.
  keptAsIs?: boolean
  // awaitingApproval (APPR-12-01) -- the Approvals screen's own filter, mirroring
  // needsFix/keptAsIs's boolean shape. Server param shipped in APPR-08-07
  // (ListFilter.AwaitingApproval); this is the first caller to send it.
  awaitingApproval?: boolean
}

// The server's `q` cap is 200 UTF-8 BYTES, not JS string length (handlers.go
// maxFilterTextLen -- a byte count, enforced via Go's len()). Encode, cut at the byte
// boundary, then back off over any dangling continuation bytes (0b10xxxxxx) so a
// multi-byte character is never split and no `�` can appear.
export function clampFilterText(s: string): string {
  const bytes = new TextEncoder().encode(s)
  if (bytes.length <= 200) return s
  let end = 200
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--
  return new TextDecoder().decode(bytes.subarray(0, end))
}

// One entry of editInvoice's optional `line_items` array (lineItemReq,
// handlers.go:42-48, INVED-01-05). Five nullable strings -- NO `id` and NO `line_no`:
// line_no is system-assigned 1..N by array POSITION ([line-no-by-position]) and a
// client-supplied one is silently ignored, so this array's order IS the only line
// ordering the wire carries.
export interface LineItemEditInput {
  description: string | null
  quantity: string | null
  unit_price: string | null
  line_total: string | null
  line_tax: string | null
}

// editInvoice's PATCH body: the 9 optional header MBS-content fields (editReq,
// handlers.go:70-80, [D9]) — identity/lifecycle are not the edit's job. Reuses
// InvoiceRecord's own field types so the two never drift apart.
//
// `line_items` (INVED-01-06) mirrors editReq.LineItems, a POINTER to a slice, so all
// three states stay distinguishable ([line-items-optional]): key ABSENT -- or set to
// `undefined`, which JSON.stringify drops -- leaves the stored lines exactly as they
// are; `[]` removes every line; a populated array replaces the whole set, renumbered
// 1..N. A type that could not express "absent" would make every header-only save delete
// all of the invoice's lines.
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
> & {
  line_items?: LineItemEditInput[]
}

// One entry of createInvoice's `line_items` array (lineItemReq echo, createRequest.
// LineItems, handlers.go:51-64, INVCR-01-02/task-278). Same five nullable-string fields
// as LineItemEditInput -- deliberately a SEPARATE type, not reused: create and edit are
// different endpoints (createRequest vs editReq) that could diverge independently. NO
// `id`/`line_no`: line_no is system-assigned 1..N by array position (handlers.go:38-41)
// and a client-supplied one is silently ignored.
export interface LineItemCreateInput {
  description: string | null
  quantity: string | null
  unit_price: string | null
  line_total: string | null
  line_tax: string | null
}

// createInvoice's POST body (createRequest, handlers.go:51-64). `entity_id`/
// `invoice_number` are the two REQUIRED fields -- blank rejects 400 before create ever
// runs (handlers.go:138-145); every other header field is nullable. The mapper
// (draftToCreateRequest, lib/invoiceDraft.ts) never emits `buyer_address`/`wht`/
// `doc_type` -- they have no column and no wire field.
export interface InvoiceCreateInput {
  entity_id: string
  invoice_number: string
  issue_date: string | null
  supplier_tin: string | null
  supplier_name: string | null
  buyer_tin: string | null
  buyer_name: string | null
  currency: string | null
  subtotal: string | null
  vat: string | null
  total: string | null
  line_items: LineItemCreateInput[]
}

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

// Returns the envelope WHOLE (INVCR-01-08/task-284 AC-1), not the bare `.invoices` array
// it used to unwrap: `pagination.total` is the source of every review-screen filter-pill
// count and of the "SHOWING 1–50 OF 500" pager, and discarding it here made that number
// unreachable no matter what the server sent.
//
// Three emission rules, one per kind of option — a wrong one here is the only way this
// function can break I7/LIST-1b/LIST-4:
//   - booleans emit iff STRICTLY `=== true` ([needs-attention-bool-true-only]); `false`
//     is absent, and `??`/truthiness would let the string "false" through permissive.
//   - strings emit iff truthy, so `''` is ABSENT — matching the server's own
//     empty-is-absent rule (handlers.go:409-445, `if raw != ""`).
//   - limit/offset emit iff `!= null`, NEVER truthiness: `offset: 0` is falsy, so an
//     `if (opts.offset)` would silently drop the first page's own offset (LIST-4).
//
// importBatchIds (BULK-01-02) is a FOURTH shape: one `import_batch_id` param per
// non-empty id, via `params.append` in a loop — `params.set` in a loop would overwrite
// every earlier value, sending exactly ONE id and silently narrowing a multi-file run's
// review screen to one file's invoices with a plausible-looking total. Each id is still
// empty-is-absent PER VALUE ([''] and [] both emit none), matching every other string
// option above.
export async function listInvoices(
  authedFetch: AuthedFetch,
  base: string,
  opts: ListInvoicesOptions = {},
): Promise<InvoiceListResponse> {
  const params = new URLSearchParams()
  if (opts.needsAttention === true) params.set('needs_attention', 'true')
  if (opts.entityId) params.set('entity_id', opts.entityId)
  if (opts.limit != null) params.set('limit', String(opts.limit))
  if (opts.offset != null) params.set('offset', String(opts.offset))
  for (const id of opts.importBatchIds ?? []) if (id) params.append('import_batch_id', id)
  if (opts.status) params.set('status', opts.status)
  if (opts.needsFix === true) params.set('needs_fix', 'true')
  if (opts.ruleKey) params.set('rule_key', opts.ruleKey)
  if (opts.q) params.set('q', opts.q)
  if (opts.keptAsIs === true) params.set('kept_as_is', 'true')
  if (opts.awaitingApproval === true) params.set('awaiting_approval', 'true')
  const query = params.toString() ? `?${params.toString()}` : ''
  const res = await authedFetch<InvoiceListResponse>(`${base}/api/invoice/v1/invoices${query}`)
  // EVERY row, never just the first (ROW-6). The envelope itself rides through whole --
  // `pagination` is the source of the review screen's counts and pager.
  return { ...res, invoices: res.invoices.map(normaliseInvoiceRow) }
}

// normaliseInvoiceRow is listInvoices' per-row fail-closed pass over `approval`, the
// same convention getInvoice applies to the action flags below: booleans via `=== true`
// (never `?? false`), nullable fields via `?? null`, a missing or non-object `approval`
// to `null` (never `undefined`). Exported so the specs can drive it directly.
//
// `run_state` is passed through UNTOUCHED, like `pending_role_title`: both are backend
// copy, and a `?? ''` here would be the SPA-authored fallback [gates-on-the-wire]
// forbids. Every other key rides the spread byte-identical (ROW-5).
export function normaliseInvoiceRow(raw: InvoiceRecord): InvoiceRecord {
  const wire = raw.approval
  // An array is `typeof 'object'` too, and a consumer reading `.run_state` off one gets
  // undefined -- the same "the server did not say" case as a string or a number (ROW-1).
  const approval: InvoiceApproval | null =
    typeof wire === 'object' && wire !== null && !Array.isArray(wire)
      ? {
          run_state: wire.run_state,
          pending_ord: wire.pending_ord ?? null,
          pending_role_title: wire.pending_role_title ?? null,
          pending_holder_warn: wire.pending_holder_warn === true,
          due_at: wire.due_at ?? null,
          overdue: wire.overdue === true,
        }
      : null
  return { ...raw, approval }
}

// The register's own page size (mirrors REVIEW_PAGE_SIZE, reviewBatch.ts:702). Stays
// under BATCH_SUBMIT_MAX_IDS (reviewBatch.ts) with zero margin -- a full page selected
// and submitted exactly saturates the batch-submit id cap. reviewBatch.ts already
// imports from this module, so the comparison is asserted in invoices.test.ts (a leaf),
// never imported back here.
export const REGISTER_PAGE_SIZE = 50

// [empty-is-total-zero]: the SET's own total, never `invoices.length` -- a mid-set empty
// PAGE (total>0, this page's slice is []) must resolve 'ready' with the envelope intact,
// not 'empty' (which nulls `data` and loses `pagination`), or the register strands the
// user on a lying zero-state with no pager back to page 1.
export function invoiceListIsEmpty(r: InvoiceListResponse): boolean {
  return r.pagination.total === 0
}

// One row of the violation-summary aggregate (RuleCount, store.go:639-642): a distinct
// rule_key and the number of DISTINCT invoices failing it in this batch -- an invoice
// naming the same rule twice counts ONCE, so these never sum to the invoice total.
export interface RuleCount {
  rule_key: string
  invoices: number
}

// The rows arrive SERVER-ordered (count DESC, then rule_key ASC) and severity-agnostic,
// and are passed through untouched: this rail is a set of filter triggers, so re-sorting
// or re-filtering it here would make the rail disagree with the `rule_key` query each of
// its rows fires ([filters-are-server-side]).
//
// importBatchIds (BULK-01-02) widened from a single id to string[], emitted the same way
// listInvoices' own importBatchIds is: one `import_batch_id` param per non-empty id, via
// URLSearchParams.append (never overwriting), so the rail can span every batch a
// multi-file run produced.
export async function violationSummary(
  authedFetch: AuthedFetch,
  base: string,
  importBatchIds: string[],
): Promise<RuleCount[]> {
  const params = new URLSearchParams()
  for (const id of importBatchIds) if (id) params.append('import_batch_id', id)
  const res = await authedFetch<{ rules: RuleCount[] }>(
    `${base}/api/invoice/v1/invoices/violation-summary?${params.toString()}`,
  )
  return res.rules
}

// The seven action booleans normalize with `=== true`, NOT `?? false`: `??` only defends
// against null/undefined, so any non-boolean truthy the wire might carry (a proxy or mock
// emitting the STRING "false", a 1) would come through permissive. These gate a
// destructive-ish action, so anything that is not literally `true` must deny -- a
// defensive normalization on a permission-shaped flag only earns its keep fail-closed.
// Every key is listed EXPLICITLY even though `...res` is already typed
// InvoiceDetailRecord: an omitted line compiles clean and tsc reports nothing, so the
// APPROVE-1/2/3/5 specs are the only oracle that the fail-closed convention was applied.
// The six `*_blocked_reason` fields all keep `?? null` (their
// declared type is nullable, so `??` is reachable and idiomatic) and are passed through
// BYTE-IDENTICALLY -- no fallback string, no rewriting: that copy is the backend's
// ([revalidate-reason-from-backend]/[gates-on-the-wire]), and an SPA-authored default here
// is exactly the drift that decision forbids. submit_blocked_reason now carries an APPROVAL
// refusal alongside the role and status ones (APPR-08-05): on a validated invoice whose
// approval run is still open, can_submit is false and this field explains why.
//
// `approval` is NOT normalised here: the detail wire never carries the key, so any value
// this function put there would be invented. InvoiceDetailRecord Omits it instead.
export async function getInvoice(authedFetch: AuthedFetch, base: string, id: string): Promise<InvoiceDetailRecord> {
  const res = await authedFetch<InvoiceDetailRecord>(`${base}/api/invoice/v1/invoices/${id}`)
  return {
    ...res,
    rule_set_version: res.rule_set_version ?? null,
    qr_png_base64: res.qr_png_base64 ?? null,
    can_edit: res.can_edit === true,
    can_revalidate: res.can_revalidate === true,
    revalidate_blocked_reason: res.revalidate_blocked_reason ?? null,
    can_submit: res.can_submit === true,
    submit_blocked_reason: res.submit_blocked_reason ?? null,
    can_view_ubl: res.can_view_ubl === true,
    ubl_blocked_reason: res.ubl_blocked_reason ?? null,
    can_resolve_outside: res.can_resolve_outside === true,
    resolve_outside_blocked_reason: res.resolve_outside_blocked_reason ?? null,
    can_approve: res.can_approve === true,
    approve_blocked_reason: res.approve_blocked_reason ?? null,
    can_reject: res.can_reject === true,
    reject_blocked_reason: res.reject_blocked_reason ?? null,
  }
}

// The document, verbatim. Through authedFetch (not a bare fetch) so the 401 -> sign-out
// seam holds ([ubl-fetch-through-authedfetch]); U6 is the row that catches a regression.
export async function getInvoiceUbl(authedFetch: AuthedFetch, base: string, id: string): Promise<string> {
  return authedFetch<string>(`${base}/api/invoice/v1/invoices/${encodeURIComponent(id)}/ubl`, {
    responseType: 'text',
  })
}

export async function getInvoiceHistory(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
): Promise<StatusChange[]> {
  return authedFetch<StatusChange[]>(`${base}/api/invoice/v1/invoices/${id}/history`)
}

// POST /v1/invoices -- the missing caller for CreateHandler (cmd/invoice/main.go:56,
// INVCR-01-02, task-278: the handler has existed since M4-02 with no caller). Follows
// editInvoice's shape verbatim below -- forwards `input` to apiFetch untouched, no
// wrapping. Non-2xx rejects with the underlying ApiError unchanged.
//
// The 201 body is the bare domain Invoice with its line_items (CreateHandler,
// handlers.go:117-165), NOT the getResponse wrapper -- so like listInvoices/editInvoice/
// revalidateInvoice's responses it carries no `rule_set_version` key, and the resolved
// record reads `undefined` there at runtime despite InvoiceRecord typing it `number |
// null` (see that type's own comment). Deliberately NOT normalized with `?? null` here:
// getInvoice is the one helper that does that, and a fourth variant of the same wire shape
// is worse than the documented gap. If that gap is ever closed it should be closed once,
// for all four helpers, not per-callsite.
export async function createInvoice(
  authedFetch: AuthedFetch,
  base: string,
  input: InvoiceCreateInput,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices`, { method: 'POST', body: input })
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

// POST /v1/invoices/{id}/keep-as-is (INVCR-01-15, D6, KeepAsIsHandler handlers.go) --
// D6's auditable-triage write, never a lifecycle transition. `reason` is sent as
// received; the caller (ReviewRow.tsx) is expected to have already gated the click on
// canKeepAsIs (lib/reviewBatch.ts) so an empty reason never reaches this call at all
// ([must-not-allow-empty-reason], KEEP-2) -- this wrapper does no trimming/validation
// of its own, matching editInvoice/revalidateInvoice's own thin-wrapper shape.
//
// DELETE .../keep-as-is (UnkeepAsIsHandler) has NO caller here: AC #10's named UI
// surface is the keep action only, and the backend endpoint exists with no frontend
// caller yet -- the same precedent createInvoice/POST /v1/invoices itself sat on for
// two stories before INVCR-01-02 wired it up. Flagged in this subtask's notes, not
// silently dropped.
export async function keepInvoiceAsIs(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
  reason: string,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}/keep-as-is`, { method: 'POST', body: { reason } })
}

// POST/DELETE /v1/invoices/{id}/resolved-outside -- the `failed`-status sibling of
// keep-as-is. `reason` is sent as received, same thin-wrapper contract as
// keepInvoiceAsIs; the caller gates on canResolveOutside before this is reached.
export async function resolveInvoiceOutside(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
  reason: string,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}/resolved-outside`, {
    method: 'POST',
    body: { reason },
  })
}

export async function unresolveInvoiceOutside(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
): Promise<InvoiceRecord> {
  return authedFetch<InvoiceRecord>(`${base}/api/invoice/v1/invoices/${id}/resolved-outside`, { method: 'DELETE' })
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

export function verdictStatus(
  staleSinceEdit: boolean,
  inv: Pick<InvoiceRecord, 'status' | 'rule_set_version_id' | 'violations'>,
): 'stale' | 'current' {
  if (staleSinceEdit) return 'stale'
  const demotedSinceValidation =
    inv.status === 'draft' && inv.rule_set_version_id != null && !inv.violations.some((v) => v.severity === 'error')
  return demotedSinceValidation ? 'stale' : 'current'
}

// Disclosure of the violation fact only -- never re-derive needs_attention from this.
export function hasBlockingViolation(inv: Pick<InvoiceRecord, 'violations'>): boolean {
  return inv.violations.some((v) => v.severity === 'error')
}

// Shared missing-buyer-TIN signal for the register, review row and Bill-to block --
// blank/whitespace counts as missing, closing the `??` hole all three used to have.
export const BUYER_TIN_MISSING = 'TIN MISSING'
export function isBuyerTinMissing(tin: string | null | undefined): boolean {
  return tin == null || tin.trim() === ''
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

// The five MBS content fields of a line, the ONLY thing diffLineItems compares. `id` and
// `line_no` are deliberately outside this Pick ([fingerprint-excludes-line-ids]).
type LineContent = Pick<InvoiceLineItem, 'description' | 'quantity' | 'unit_price' | 'line_total' | 'line_tax'>

// The line half of editInvoice's PATCH body: `undefined` when `edited` is
// content-identical to `original`, so an untouched line editor sends no `line_items` key
// at all ([fingerprint-excludes-line-ids]) -- replace-all churns line ids, and omitting
// the key is what keeps that churn off edits that never went near a line. `[]` stays
// reachable: emptying a 2-line invoice returns [], not undefined.
//
// Compares BY POSITION over exactly the five fields above, never `id`, never `line_no`
// (line_no is positional, so a reorder correctly reads as a change). Both sides
// canonicalize '' -> null before comparing AND in the emitted array: React inputs hold
// '', never null, so a stored-NULL field seeds the form as ''. Without that
// canonicalization an UNTOUCHED editor reports "changed" on every save -- which churns
// line ids, moves the backend fingerprint for real (NULL -> '' is a genuine content
// change) and so demotes a `validated` invoice on a save that changed nothing -- and
// emits '' into a numeric column, where `$N::text::numeric` raises 22P02 -> ErrValidation
// -> a 400 on a no-op save.
//
const LINE_EDIT_FIELDS = ['description', 'quantity', 'unit_price', 'line_total', 'line_tax'] as const

// '' and null are the SAME absent value on BOTH sides. No trimming: the backend does not
// trim either, and silently rewriting an operator's content is not this helper's job.
function canonField(v: string | null): string | null {
  return v == null || v === '' ? null : v
}

function canonLine(line: LineContent): LineItemEditInput {
  return {
    description: canonField(line.description),
    quantity: canonField(line.quantity),
    unit_price: canonField(line.unit_price),
    line_total: canonField(line.line_total),
    line_tax: canonField(line.line_tax),
  }
}

export function diffLineItems(
  original: ReadonlyArray<LineContent>,
  edited: ReadonlyArray<LineContent>,
): LineItemEditInput[] | undefined {
  // Fresh objects over the five fields only, so `id`/`line_no` cannot leak onto the wire
  // even when the caller hands us whole InvoiceLineItem rows -- and so neither input array
  // nor any of its rows is ever written to.
  const next = edited.map(canonLine)
  if (next.length === original.length) {
    const prev = original.map(canonLine)
    const unchanged = next.every((row, i) => LINE_EDIT_FIELDS.every((k) => row[k] === prev[i][k]))
    if (unchanged) return undefined
  }
  return next
}

// The passive computed line-sum hint for the edit form ([line-sum-hint-semantics], Core
// AC #5): Σ(unit_price × (quantity ?? 1)) over the lines. Display-only -- it never
// derives or rewrites subtotal/vat/total ([totals-ownership]), which is why the
// signature can see nothing but the two fields it multiplies.
//
// Returns an exact decimal STRING, not a number -- a deliberate carrier-only deviation
// from the story's `number | null` (AC #5's arithmetic is unchanged). Money is strings
// end-to-end ([D13]) precisely so no float touches it; the rule this mirrors has a 0.005
// tolerance, so precision below the kobo is load-bearing; and the app's only money
// formatter (fmt(), lib/format.ts:5-7) rounds to WHOLE naira, so a numeric return would
// invite the component to render a hint that reads EQUAL to a subtotal it actually
// differs from -- fabricating the very agreement this hint exists to question.
//
// `null`, never '0.00', when the line set is empty or ANY line's unit_price/quantity is
// absent or present-but-non-numeric: that mirrors lineSumEval's violate-set (an absent
// quantity weights 1; an unparseable one VIOLATES) rather than skipping the line.
//
// A decimal carried EXACTLY, as `u x 10^-s`. `s` is normalized non-negative at parse time
// (an exponent that would drive it below zero is folded into `u` instead), so nothing
// downstream has to reason about a negative scale. bigint throughout -- no float ever
// touches a money value here, which is the whole point of the type.
//
// Exported (INVCR-01-02, task-278, [money-primitives-exported]): draftToCreateRequest
// (lib/invoiceDraft.ts) is the second consumer of this exact-decimal kit, alongside
// computedLineSum below. Zero behaviour change -- these five names were module-private
// until now.
export interface Scaled {
  u: bigint
  s: number
}

// Character-identical to the backend's own jsonNumberRe (internal/invoice/payload.go:42):
// strip the ONE added pair of parens around the sign and this is the Go constant verbatim.
// Capture groups cannot change the accepted language, so the same expression that GATES a
// value also decomposes it -- one pattern, nothing to drift against itself.
//
// Transcribing the evaluator's own grammar is the point, not incidental: this hint's
// refuse-set is then exactly the set putNumber/toFloat treat as a violation, so it never
// renders a number the rule it mirrors would have rejected. (Display-only drift risk
// against an unexported Go constant, deliberately bounded.)
//
// NOT Number()/parseFloat as the gate: Number('') === 0 would fabricate a silent zero out
// of a field the operator just cleared. Verified in node that JS's '$' is strict
// end-of-input here, exactly like Go RE2's -- '1\n' is refused by both -- so the
// transcription does not quietly widen at the anchor.
const DECIMAL_RE = /^(-?)(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/

export function parseScaled(raw: string): Scaled | null {
  // Optional groups are absent at runtime; RegExpExecArray types them all `string`.
  const m: Array<string | undefined> | null = DECIMAL_RE.exec(raw)
  if (m === null) return null
  const fracDigits = m[3] != null ? m[3].slice(1) : ''
  const exponent = m[4] != null ? Number(m[4].slice(1)) : 0
  let u = BigInt(`${m[2] ?? ''}${fracDigits}`)
  let s = fracDigits.length - exponent
  if (s < 0) {
    u *= 10n ** BigInt(-s)
    s = 0
  }
  return { u: m[1] === '-' ? -u : u, s }
}

export function mulScaled(a: Scaled, b: Scaled): Scaled {
  return { u: a.u * b.u, s: a.s + b.s }
}

export function addScaled(a: Scaled, b: Scaled): Scaled {
  const s = Math.max(a.s, b.s)
  return { u: a.u * 10n ** BigInt(s - a.s) + b.u * 10n ** BigInt(s - b.s), s }
}

// Exact digits, no thousands grouping -- that keeps this ICU-independent (format.test.ts:9
// records that toLocaleString('en-NG') varies by node ICU build). Trailing zeros are
// stripped down to, but never below, 2dp: 250.0000 -> '250.00', 0.015 stays '0.015'.
export function renderScaled({ u, s }: Scaled): string {
  let mantissa = u
  let scale = s
  if (scale < 2) {
    mantissa *= 10n ** BigInt(2 - scale)
    scale = 2
  }
  while (scale > 2 && mantissa % 10n === 0n) {
    mantissa /= 10n
    scale -= 1
  }
  const negative = mantissa < 0n
  const digits = (negative ? -mantissa : mantissa).toString().padStart(scale + 1, '0')
  const cut = digits.length - scale
  return `${negative ? '-' : ''}${digits.slice(0, cut)}.${digits.slice(cut)}`
}

export function computedLineSum(lines: ReadonlyArray<Pick<InvoiceLineItem, 'quantity' | 'unit_price'>>): string | null {
  if (lines.length === 0) return null
  let sum: Scaled = { u: 0n, s: 0 }
  for (const line of lines) {
    if (line.unit_price == null) return null
    const amount = parseScaled(line.unit_price)
    if (amount === null) return null
    // An ABSENT quantity weights the line at 1 (putNumber drops a nil, so lineSumEval sees
    // has == false and defaults); a PRESENT but unparseable one VIOLATES rather than
    // falling back to 1. That asymmetry is the rule's own -- mirrored here, not smoothed
    // over, or the hint would show a total on a line the rule refuses to total.
    let weight: Scaled = { u: 1n, s: 0 }
    if (line.quantity != null) {
      const parsed = parseScaled(line.quantity)
      if (parsed === null) return null
      weight = parsed
    }
    sum = addScaled(sum, mulScaled(amount, weight))
  }
  return renderScaled(sum)
}

// The edit form's own field-value state, one string per EDIT_FIELD_KEYS entry (moved from
// InvoiceDetail.tsx, task-391, BUG-03-02).
export type EditFormState = Record<EditFieldKey, string>

export function formFromInvoice(inv: Pick<InvoiceRecord, EditFieldKey>): EditFormState {
  return {
    issue_date: toDateInputValue(inv.issue_date),
    supplier_tin: inv.supplier_tin ?? '',
    supplier_name: inv.supplier_name ?? '',
    buyer_tin: inv.buyer_tin ?? '',
    buyer_name: inv.buyer_name ?? '',
    currency: inv.currency ?? '',
    subtotal: inv.subtotal ?? '',
    vat: inv.vat ?? '',
    total: inv.total ?? '',
  }
}

// diffEditInput only sends changed fields -- PATCHing all 9 on every submit would silently
// blank out the ones the user didn't touch. issue_date is special-cased: Go's *time.Time
// decode rejects a bare date, so a bare YYYY-MM-DD is normalised to midnight UTC, and a
// cleared date is dropped since PATCH cannot represent an explicit clear.
export function diffEditInput(original: Pick<InvoiceRecord, EditFieldKey>, form: EditFormState): InvoiceEditInput {
  const patch: InvoiceEditInput = {}
  for (const key of EDIT_FIELD_KEYS) {
    if (key === 'issue_date') {
      const value = form.issue_date.trim()
      if (value === toDateInputValue(original.issue_date).trim()) continue
      if (!value) continue
      patch.issue_date = /^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00Z` : value
      continue
    }
    if (form[key] === (original[key] ?? '')) continue
    patch[key] = form[key]
  }
  return patch
}

// not_validated/duplicate_request/awaiting_approval are the three reachable
// BatchSubmitResultItem.reason values (the batchSubmitReason* consts,
// internal/invoice/batch_submit.go) -- anything else passes through verbatim rather than
// being swallowed, so an unknown future reason still surfaces something to the operator.
const SKIP_REASON_LABELS: Record<string, string> = {
  not_validated: 'Not validated — validate it first',
  duplicate_request: 'Already submitted with this request',
  awaiting_approval: 'Waiting on approval — an approver must approve it first',
}

export function skipReasonLabel(reason: string): string {
  return SKIP_REASON_LABELS[reason] ?? reason
}

// Mirrors internal/submission/failure.go:11-14 (FK-11 pins this exact set).
export const FAILURE_KINDS = ['payload_not_built', 'never_acknowledged', 'acknowledged_no_verdict'] as const

export type FailureKind = (typeof FAILURE_KINDS)[number]

export interface FailureExplanation {
  headline: string
  detail: string
  nextStep: string
}

// Must never promise notification (M5-08 is unbuilt) or offer recovery (`failed` is
// terminal) -- both are invisible from the code, so they earn this one comment.
const FAILURE_EXPLANATIONS = {
  payload_not_built: {
    headline: 'We could not prepare this invoice for filing.',
    detail:
      'The submission stopped before anything left our system, so this invoice has not been filed. The fault is on our side, not in the invoice — nothing you change here will fix it.',
    nextStep:
      'Report this invoice to ASComply support. Nothing reaches us automatically, so it will not be looked at until you do.',
  },
  never_acknowledged: {
    headline: 'We never got confirmation that this invoice was received for filing.',
    detail:
      'We tried repeatedly for about an hour and never got an answer back. We cannot tell from here whether it arrived, so we cannot say whether it was filed or not.',
    nextStep:
      'Ask ASComply support to confirm the status of this invoice with NRS before you raise another invoice for the same transaction.',
  },
  acknowledged_no_verdict: {
    headline: 'This invoice was received for filing, but no result ever came back.',
    detail:
      'We know it arrived. We never learned whether it was accepted or rejected, so it may already have been filed — raising another invoice for the same transaction could file it twice.',
    nextStep:
      'Do not raise another invoice for the same transaction yet. Ask ASComply support to confirm with NRS whether this one has already been filed.',
  },
} as const satisfies Record<FailureKind, FailureExplanation>

export const FAILURE_EXPLANATION_FALLBACK = {
  headline: 'We do not have a recorded reason for this failure.',
  detail:
    'The reason was not recorded for this invoice, so we cannot tell you from this screen what went wrong — or whether it was filed.',
  nextStep: 'Ask ASComply support to check what happened to this invoice before you raise another one for the same transaction.',
} as const satisfies FailureExplanation

export function failureExplanation(kind: string | null): FailureExplanation {
  if (kind === null) return FAILURE_EXPLANATION_FALLBACK
  return FAILURE_EXPLANATIONS[kind as FailureKind] ?? FAILURE_EXPLANATION_FALLBACK
}

// Static copy for the resolve-outside action's UI, same shape as DETAIL_SUBMIT_COPY;
// tone matches keep-as-is's ROW_EXPANSION_COPY.
export const RESOLVE_OUTSIDE_COPY = {
  label: 'Resolved outside the system',
  reasonPlaceholder: 'How was this invoice resolved outside ASComply? (required)',
  resolvedPrefix: 'Resolved outside the system — ',
  undoLabel: 'Undo',
} as const

export function canResolveOutside(reason: string): boolean {
  return reason.trim() !== ''
}

// `failed` is the only status this action applies to -- a `draft` row with the
// same kept_as_is_* triple is a keep-as-is, not a resolve-outside.
export function resolvedOutside(
  inv: Pick<InvoiceRecord, 'status' | 'kept_as_is_at' | 'kept_as_is_by' | 'kept_as_is_reason'>,
): { at: string; by: string | null; reason: string | null } | null {
  if (inv.status !== 'failed' || inv.kept_as_is_at == null) return null
  return { at: inv.kept_as_is_at, by: inv.kept_as_is_by ?? null, reason: inv.kept_as_is_reason ?? null }
}

// Founder-pinned, verbatim (Core AC #2) -- do not paraphrase or re-tone.
export const DETAIL_SUBMIT_COPY = {
  submit: 'Submit',
  prompt: 'Send this invoice for transmission?',
  detail: 'Nothing here can pull it back.',
  confirm: 'Yes, send it now',
  cancel: 'Cancel',
  sending: 'Sending…',
  unresolved: 'The server did not confirm this invoice was sent. Reload to check its status.',
  notQueued: 'Not queued',
} as const

export type SingleSubmitOutcome =
  | { kind: 'queued' }
  | { kind: 'skipped'; message: string }
  | { kind: 'unresolved'; message: string }

// `enqueued === true` strictly, never truthiness or the item's own `status` --
// duplicate_request reports status:'queued' while nothing was enqueued, which is
// exactly the trap the "duplicate_request is skipped" test below closes.
export function singleSubmitOutcome(
  invoiceId: string,
  items: BatchSubmitResultItem[] | undefined,
): SingleSubmitOutcome {
  const item = items?.find((i) => i.invoice_id === invoiceId)
  if (item === undefined) return { kind: 'unresolved', message: DETAIL_SUBMIT_COPY.unresolved }
  if (item.enqueued === true) return { kind: 'queued' }
  return {
    kind: 'skipped',
    message: item.reason != null ? skipReasonLabel(item.reason) : DETAIL_SUBMIT_COPY.notQueued,
  }
}

// Selection helpers for the batch-submit list surface (M5-09-06). Selectable means
// `validated` (Store.ApplyValidation is the only path into `queued`) AND no open approval
// run -- the server skips an awaiting-approval row with `awaiting_approval` (APPR-08-09),
// so selecting one can only buy a dead result. Fail-open on any other run_state is a
// pinned decision, not an oversight: see A-sel-8..11.
export function isRowSelectable(row: Pick<InvoiceRecord, 'status' | 'approval'>): boolean {
  return row.status === 'validated' && row.approval?.run_state !== 'open'
}

export function selectableIds(rows: InvoiceRecord[]): string[] {
  return rows.filter((row) => isRowSelectable(row)).map((row) => row.id)
}

export function toggleSelection(sel: string[], id: string): string[] {
  return sel.includes(id) ? sel.filter((s) => s !== id) : [...sel, id]
}

// Keeps only ids that are both still present in `rows` (didn't scroll/filter away) AND
// still selectable (didn't get submitted/edited/re-validated, or fall under a newly
// opened approval run, out from under a stale selection since it was last computed).
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

// Compliance-panel kept-as-is banner visibility + payload (task-392, BUG-03-03). `by`/
// `reason` are `?? null`, never a fabricated fallback string -- the server's absence of a
// value stays absent.
export function keptAsIs(
  inv: Pick<InvoiceDetailRecord, 'kept_as_is_at' | 'kept_as_is_by' | 'kept_as_is_reason'>,
): { at: string; by: string | null; reason: string | null } | null {
  if (inv.kept_as_is_at == null) return null
  return { at: inv.kept_as_is_at, by: inv.kept_as_is_by ?? null, reason: inv.kept_as_is_reason ?? null }
}

// Fiscal-record card visibility (task-251 AC #1): only an accepted invoice that
// actually has an IRN has a fiscal record to show.
export function shouldShowFiscalRecord(inv: Pick<InvoiceRecord, 'status' | 'irn'>): boolean {
  return inv.status === 'accepted' && inv.irn != null
}

export function shouldFetchInvoices(base: string | null): boolean {
  return base != null
}

export function invoicesViewState(base: string | null, s: AsyncState<unknown>): AsyncStatus {
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

// Documented mirror of the server's own ceiling (handlers.go:401-404, `if limit > 200`) --
// the same drift-guard idiom as BATCH_SUBMIT_MAX_IDS (reviewBatch.ts). NOT a runtime
// clamp: the server clamps an over-200 limit silently rather than rejecting it, so
// fetchAllInvoices below reads the ECHOED pagination.limit, never this constant.
export const AGGREGATE_PAGE_SIZE = 200

export const AGGREGATE_MAX_PAGES = 10 // 2000 invoices

// Offsets for pages 2..N (page 1 is already fetched), capped at maxPages. `limit < 1`
// returns no offsets rather than NaN/Infinity -- same guard pagerLabels carries
// (reviewBatch.ts:283-286), unreachable from a real response but not a case to crash on.
export function remainingPageOffsets(
  total: number,
  limit: number,
  maxPages: number,
): { offsets: number[]; truncated: boolean } {
  if (limit < 1) return { offsets: [], truncated: false }
  const pages = Math.min(Math.ceil(total / limit), maxPages)
  const offsets: number[] = []
  for (let page = 2; page <= pages; page++) offsets.push((page - 1) * limit)
  return { offsets, truncated: pages === maxPages && total > maxPages * limit }
}

// The whole-set result of fetchAllInvoices: every row across the fetched pages, plus the
// set's own total/truncated so a consumer can disclose a capped fetch rather than silently
// under-report.
export interface AllInvoices {
  invoices: InvoiceRecord[]
  total: number
  fetched: number
  truncated: boolean
}

// [empty-is-total-zero]: AllInvoices is a plain object, so resolveStatus's default isEmpty
// (Array.isArray-gated, async-state.ts) can never fire on it -- same trap invoiceListIsEmpty
// closes for InvoiceListResponse above.
export function allInvoicesIsEmpty(r: AllInvoices): boolean {
  return r.total === 0
}

// Fetches a client's whole invoice set across pages: page 1 at AGGREGATE_PAGE_SIZE, then
// the rest of the set (per the echoed limit/total) concurrently via Promise.all, capped at
// AGGREGATE_MAX_PAGES. Concurrent because every offset past page 1 is independent once page
// 1 resolves, and Promise.all preserves array order regardless of resolution order, so the
// concatenation is deterministic ascending-offset. One rejected page rejects the whole call
// (Promise.all default), matching listInvoices' own non-2xx-rejects-unchanged contract.
export async function fetchAllInvoices(
  authedFetch: AuthedFetch,
  base: string,
  opts: Omit<ListInvoicesOptions, 'limit' | 'offset'>,
): Promise<AllInvoices> {
  const first = await listInvoices(authedFetch, base, { ...opts, limit: AGGREGATE_PAGE_SIZE, offset: 0 })
  const { limit, total } = first.pagination
  const { offsets, truncated } = remainingPageOffsets(total, limit, AGGREGATE_MAX_PAGES)
  const rest = await Promise.all(
    offsets.map((offset) => listInvoices(authedFetch, base, { ...opts, limit, offset })),
  )
  const invoices = [first, ...rest].flatMap((page) => page.invoices)
  return { invoices, total, fetched: invoices.length, truncated }
}
