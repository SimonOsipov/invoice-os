// M3-14-01: the reusable typed API-E2E seam (Core AC 1). Every api/ spec
// (isolation.spec.ts, validation.spec.ts, portfolio.spec.ts — M3-14-02..04)
// drives the deployed gateway headless through this ONE module, built on the
// M3-06 typed client (@invoice-os/api-client/client) so this suite shares the
// exact apiFetch/ApiError seam and normalized error contract the frontend
// does. This is the first repo consumer of a workspace-package .ts subpath
// export through the Playwright runner (see task-74 STEP 0 for the
// resolution proof).
import { apiFetch, ApiError } from '@invoice-os/api-client/client'
import { TENANTS } from '../topology/targets'
import { resolveTarget } from '../targets'

// Re-exported so specs can do `import { ApiError } from '../api/client'`
// against this one seam, without reaching into the api-client package
// directly — negative specs assert err.kind === 'http' / err.status.
export { ApiError }

// apiBase(): shares topology/targets.ts's GATEWAY_URL resolution exactly (both call
// resolveTarget('GATEWAY_URL')). Deliberately does NOT call the api-client's own
// gatewayBase() — that reads import.meta.env.VITE_GATEWAY_URL, which throws under Node
// (this package has no Vite/browser runtime).
export function apiBase(): string {
  return resolveTarget('GATEWAY_URL')
}

// rawFetch(): a raw HTTP seam for M3-15's malformed-request contract specs
// (M3-15-02..05), which need byte-level control over headers/body that the
// typed apiFetch wrapper normalizes away (e.g. a malformed-scheme
// Authorization header, or a genuinely empty request body). Applies
// init.headers verbatim — callers control Authorization exactly (absent /
// "Basic x" / "Bearer not-a-jwt"). JSON-serializes body with Content-Type:
// application/json ONLY when body is present; omitting body sends no
// request body and no Content-Type at all (this is what enables M3-15-04's
// no-body -> io.EOF -> 400 case). Never throws on a non-2xx status — body is
// best-effort parsed JSON, undefined if parsing fails.
export async function rawFetch(
  path: string,
  init?: { method?: string; headers?: Record<string, string>; body?: unknown },
): Promise<{ status: number; body: unknown }> {
  const hasBody = init?.body !== undefined
  const res = await fetch(`${apiBase()}${path}`, {
    method: init?.method,
    headers: hasBody ? { ...init?.headers, 'Content-Type': 'application/json' } : init?.headers,
    body: hasBody ? JSON.stringify(init?.body) : undefined,
  })
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

export interface Persona {
  subject: string
  tenantId: string
  name: string
  kind: string
  role: string
}

// PERSONAS: the two seeded persona tenants (db/seed.dev.sql), imported from
// topology/targets.ts's TENANTS (read-only, DRY — Decision A5/A6) rather than
// duplicated here.
export const PERSONAS: { A: Persona; B: Persona } = {
  A: {
    subject: TENANTS.a.subject,
    tenantId: TENANTS.a.id,
    name: TENANTS.a.name,
    kind: TENANTS.a.kind,
    role: TENANTS.a.role,
  },
  B: {
    subject: TENANTS.b.subject,
    tenantId: TENANTS.b.id,
    name: TENANTS.b.name,
    kind: TENANTS.b.kind,
    role: TENANTS.b.role,
  },
}

// login(): the only way to mint a Bearer token (mock issuer, ephemeral ES256
// key — no shared secret). POST /auth/login is UN-prefixed (outside /api/,
// the mock issuer is mounted outside the proxied prefix). The domain role
// (e.g. "admin") is resolved server-side from `memberships`, never from the
// login request's `role` (always 'authenticated' here).
export async function login(persona: Persona): Promise<string> {
  const { access_token } = await apiFetch<{ access_token: string }>(`${apiBase()}/auth/login`, {
    method: 'POST',
    body: { subject: persona.subject, role: 'authenticated', tenant_id: persona.tenantId },
  })
  return access_token
}

// ---- Wire contract types, declared locally to the verified contract
// (internal/tenancy, internal/portfolio/portfolio.go, internal/validation/
// rule.go + handlers.go). Me mirrors e2e/topology/isolation.spec.ts's Me
// shape exactly. ----

export interface Me {
  tenant: { id: string; name: string; kind: string }
  user: { id: string; role: string }
}

// Membership mirrors internal/tenancy's Membership struct: five keys, none tagged
// omitempty, so display_name/email are present as an explicit value or an explicit null.
// Both the list element and the PATCH response body take this shape.
export interface Membership {
  user_id: string
  role: string
  status: string
  display_name: string | null
  email: string | null
}

export interface MembershipsResponse {
  memberships: Membership[]
}

// Entity mirrors internal/portfolio/portfolio.go's Entity struct: tin/
// registration/sector/address are DB-nullable (`*string` in Go), so `null` is
// a legitimate wire value, not just an absent-field case.
export interface Entity {
  id: string
  name: string
  tin: string | null
  registration: string | null
  sector: string | null
  address: string | null
  status: 'active' | 'archived'
  created_at: string
}

export interface Pagination {
  limit: number
  offset: number
  total: number
}

export interface ListResponse {
  entities: Entity[]
  pagination: Pagination
}

export interface Violation {
  rule_key: string
  severity: string
  message: string
  path?: string
}

export interface ValidateResult {
  rule_set_version: number
  violations: Violation[]
}

// Rule mirrors internal/validation/rule.go's Rule struct (the PATCH
// /v1/rules/{key} response body).
export interface Rule {
  key: string
  type: string
  target: string
  params: unknown
  severity: string
  when?: string | null
  message: string
  scope: string
  enabled: boolean
}

export interface EntityInput {
  name: string
  tin: string
  registration?: string | null
  sector?: string | null
  address?: string | null
}

export interface EntityUpdateInput {
  name?: string
  tin?: string
  registration?: string | null
  sector?: string | null
  address?: string | null
}

export interface ListEntitiesQuery {
  status?: 'active' | 'archived'
  q?: string
  limit?: number
  offset?: number
}

// InvoiceEnvelope is the POST /v1/validate request body shape (Decision
// N19: the engine's resolvePath roots at p["invoice"]). fixtures.ts's
// InvoicePayload is structurally identical — declared separately there so
// fixtures.ts has no dependency on this module, but assignable here as-is.
export interface InvoiceEnvelope {
  invoice: Record<string, unknown>
}

// ---- Typed request wrappers over apiFetch. Each builds the absolute
// /api/<service>/v1/... URL — the gateway strips /api/<service> before
// proxying (System Design "Gateway path convention"; cmd/gateway/main.go,
// internal/gateway/gateway.go), so a bare /v1/... path would 404 at the edge
// — and propagates ApiError, never swallowing it, so negative specs can
// assert err.kind === 'http' / err.status. ----

export function me(token: string): Promise<Me> {
  return apiFetch<Me>(`${apiBase()}/api/tenancy/v1/me`, { token })
}

export function memberships(token: string): Promise<MembershipsResponse> {
  return apiFetch<MembershipsResponse>(`${apiBase()}/api/tenancy/v1/memberships`, { token })
}

// setMembershipStatus(): PATCH /v1/memberships/{user_id}, admin-only, audited. `invited` is
// excluded at the type level rather than by a 400 — it is a column value, not a target.
// Answers the updated row in the same five-key shape as a list element.
export function setMembershipStatus(token: string, userId: string, status: 'active' | 'suspended'): Promise<Membership> {
  return apiFetch<Membership>(`${apiBase()}/api/tenancy/v1/memberships/${userId}`, {
    method: 'PATCH',
    body: { status },
    token,
  })
}

export function createEntity(token: string, body: EntityInput): Promise<Entity> {
  return apiFetch<Entity>(`${apiBase()}/api/portfolio/v1/entities`, { method: 'POST', body, token })
}

export function getEntity(token: string, id: string): Promise<Entity> {
  return apiFetch<Entity>(`${apiBase()}/api/portfolio/v1/entities/${id}`, { token })
}

export function listEntities(token: string, query?: ListEntitiesQuery): Promise<ListResponse> {
  const params = new URLSearchParams()
  if (query?.status) params.set('status', query.status)
  if (query?.q) params.set('q', query.q)
  if (query?.limit !== undefined) params.set('limit', String(query.limit))
  if (query?.offset !== undefined) params.set('offset', String(query.offset))
  const qs = params.toString()
  return apiFetch<ListResponse>(`${apiBase()}/api/portfolio/v1/entities${qs ? `?${qs}` : ''}`, { token })
}

export function updateEntity(token: string, id: string, body: EntityUpdateInput): Promise<Entity> {
  return apiFetch<Entity>(`${apiBase()}/api/portfolio/v1/entities/${id}`, { method: 'PATCH', body, token })
}

export function offboardEntity(token: string, id: string): Promise<Entity> {
  return apiFetch<Entity>(`${apiBase()}/api/portfolio/v1/entities/${id}/offboard`, { method: 'POST', token })
}

export function onboardEntity(token: string, id: string): Promise<Entity> {
  return apiFetch<Entity>(`${apiBase()}/api/portfolio/v1/entities/${id}/onboard`, { method: 'POST', token })
}

export function validate(token: string, invoiceBody: InvoiceEnvelope): Promise<ValidateResult> {
  return apiFetch<ValidateResult>(`${apiBase()}/api/validation/v1/validate`, {
    method: 'POST',
    body: invoiceBody,
    token,
  })
}

export function toggleRule(token: string, key: string, enabled: boolean): Promise<Rule> {
  return apiFetch<Rule>(`${apiBase()}/api/validation/v1/rules/${key}`, {
    method: 'PATCH',
    body: { enabled },
    token,
  })
}

// RejectionReason mirrors internal/submission/result.go's Reason struct (M5-01/M5-03):
// one invoices.rejection_reasons array element. `path` carries Go's `omitempty` --
// genuinely absent, never "", when the APP's rejection didn't cite a specific MBS field.
export interface RejectionReason {
  code: string
  message: string
  path?: string
}

// InvoiceLineItem mirrors internal/invoice/invoice.go's LineItem struct exactly (:56-64,
// INVED-01-08): one line_items row, its numeric columns read via ::text ([D13]) so money
// never round-trips through a float. Store.List leaves LineItems nil ([D7]/[D8]); only
// Store.Get (and therefore only the GET/PATCH responses) hydrates it.
export interface InvoiceLineItem {
  id: string
  line_no: number
  description: string | null
  quantity: string | null
  unit_price: string | null
  line_total: string | null
  line_tax: string | null
}

// Invoice mirrors internal/invoice/invoice.go's Invoice struct exactly (M4-04-08,
// task-115). violations is Go json.RawMessage on the wire -- always a JSON array in
// practice (invoices.violations jsonb NOT NULL DEFAULT '[]', migrations/
// 20260714103137_invoices.sql), so Violation[] is the accurate wire shape, not a raw
// string. rule_set_version_id is the LIVE-STAMPED uuid ([uuid-stamp]) -- distinct from
// ValidateResult.rule_set_version above, which is the plain int the /v1/validate route
// echoes; no route returns both on the same object. irn/csid/qr_payload/rejection_reasons
// (M5-01/M5-03/M5-05) are all `json:"..."` with no `omitempty` on the Go struct
// (invoice.go:101-104) and all four are in invoiceColumns (store.go:46-50), so they are
// present -- as an explicit value or explicit null -- on BOTH the list and get wire.
export interface Invoice {
  id: string
  entity_id: string
  import_batch_id: string | null
  invoice_number: string
  status: 'draft' | 'validated' | 'queued' | 'submitted' | 'accepted' | 'rejected' | 'failed'
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
  // kept_as_is_at/by/reason -- needed to assert the resolved-outside endpoints' 200
  // bodies (a second, mutually exclusive meaning on `failed` invoices).
  kept_as_is_at: string | null
  kept_as_is_by: string | null
  kept_as_is_reason: string | null
  line_items?: InvoiceLineItem[]
}

// InvoiceListItem mirrors internal/invoice/handlers.go's listItem: Invoice embedded plus
// THREE additive siblings. `approval` sits here and NOT on Invoice because Go declares it
// on listItem only -- getResponse does not carry it, so a GET-detail consumer reading it
// off Invoice would get `undefined` where the type promised `InvoiceApproval | null`. Same
// reason the POST/PATCH/transition responses (all plain Invoice) do not carry it.
// can_approve/approve_blocked_reason (APPR-12-09) ride BOTH wires, from one approvalGate
// call, and the reject pair stays detail-only (U5a).
// All three are required, not optional: no omitempty on any Go field, so an invoice with
// no run emits an explicit null (TestListItem_InvoiceKeysUnmovedAndUnrenamed,
// TestListItem_ApproveFlagsCarryNoOmitempty).
export interface InvoiceListItem extends Invoice {
  approval: InvoiceApproval | null
  can_approve: boolean
  approve_blocked_reason: string | null
}

// Mirrors approval.RowFacts (internal/approval/gate.go) key for key -- its six WIRE keys.
// RowFacts also carries PendingRoleKey, tagged json:"-" because it is the list gate's
// input and not wire copy (APPR-12-09), so it has no member here
// (TestListItem_ApprovalObjectHasExactlySixKeys).
export interface InvoiceApproval {
  run_state: string
  pending_ord: number | null
  pending_role_title: string | null
  pending_holder_warn: boolean
  due_at: string | null
  overdue: boolean
}

export interface ListInvoicesQuery {
  limit?: number
  offset?: number
  q?: string
  entity_id?: string
  awaiting_approval?: boolean
}

export interface ListInvoicesResponse {
  invoices: InvoiceListItem[]
  pagination: Pagination
}

// listInvoices(): GET /v1/invoices. q/entity_id added (BUG-01-04) so contract
// specs can exercise the search predicate through the typed seam, not just
// limit/offset -- see api/perf.spec.ts's findInvoiceId for the older
// page-and-filter-client-side workaround this makes unnecessary for q/entity_id.
export function listInvoices(token: string, query?: ListInvoicesQuery): Promise<ListInvoicesResponse> {
  const params = new URLSearchParams()
  if (query?.limit !== undefined) params.set('limit', String(query.limit))
  if (query?.offset !== undefined) params.set('offset', String(query.offset))
  if (query?.q !== undefined) params.set('q', query.q)
  if (query?.entity_id !== undefined) params.set('entity_id', query.entity_id)
  if (query?.awaiting_approval !== undefined) params.set('awaiting_approval', String(query.awaiting_approval))
  const qs = params.toString()
  return apiFetch<ListInvoicesResponse>(`${apiBase()}/api/invoice/v1/invoices${qs ? `?${qs}` : ''}`, { token })
}

// GetInvoiceResult is GetHandler's own response shape: Invoice plus getResponse's two
// GET-only sibling keys (handlers.go's getResponse, no omitempty on
// either). NOT added to Invoice itself -- rule_set_version is json:"-" on the shared Go
// struct (Invoice.RuleSetVersion, internal/invoice/invoice.go), so a list item never
// carries it structurally; only a GET response does.
// CanEdit/CanRevalidate/RevalidateBlockedReason (INVED-01-08, [gates-on-the-wire]):
// getResponse's three additive sibling keys, declared LAST on the Go
// struct and none tagged omitempty -- present, explicit, on every status. Required (not
// optional): a fail-open `?` would let a consumer read `undefined` as "the server didn't
// say", exactly what [gates-on-the-wire] exists to prevent.
// CanSubmit/SubmitBlockedReason: same convention, one call site later.
// submit_blocked_reason carries THREE kinds of refusal, not one (submitGate, handlers.go):
// a ROLE refusal, the status ones, and -- APPR-08-05 -- an APPROVAL refusal on a validated
// invoice whose approval run is still open. So it is non-null on statuses where can_edit is
// false, AND on validated, where can_submit would otherwise be true. Do not narrow it off
// can_edit, and do not read a validated status as proof it is null.
// CanViewUBL/UBLBlockedReason (BUG-04-03): same no-omitempty
// convention; content-derived (ubl.Missing), never status-derived.
export interface GetInvoiceResult extends Invoice {
  rule_set_version: number | null
  qr_png_base64: string | null
  can_edit: boolean
  can_revalidate: boolean
  revalidate_blocked_reason: string | null
  can_submit: boolean
  submit_blocked_reason: string | null
  can_view_ubl: boolean
  ubl_blocked_reason: string | null
  can_resolve_outside: boolean
  resolve_outside_blocked_reason: string | null
  // CanApprove/ApproveBlockedReason/CanReject/RejectBlockedReason (APPR-08-06): same
  // no-omitempty convention. One backend gate feeds both pairs, so can_approve always
  // equals can_reject and the two reasons are the same string. NOT gated by
  // APPROVALS_ENFORCED -- the decision endpoint is unflagged.
  can_approve: boolean
  approve_blocked_reason: string | null
  can_reject: boolean
  reject_blocked_reason: string | null
}

export function getInvoice(token: string, id: string): Promise<GetInvoiceResult> {
  return apiFetch<GetInvoiceResult>(`${apiBase()}/api/invoice/v1/invoices/${id}`, { token })
}

// InvoiceEditInput mirrors internal/invoice/handlers.go's editReq exactly: the 9
// optional header MBS-content fields PATCH /v1/invoices/{id} accepts (M4-05-03) --
// identity/lifecycle are not the edit's job ([D9]). issue_date is a plain string on
// the wire (Go *time.Time unmarshals from/marshals to an RFC3339 string).
export interface InvoiceEditInput {
  issue_date?: string
  supplier_tin?: string
  supplier_name?: string
  buyer_tin?: string
  buyer_name?: string
  currency?: string
  subtotal?: string
  vat?: string
  total?: string
  // line_items (INVED-01-08) mirrors editReq.LineItems, a POINTER to a slice on the Go side
  // (handlers.go:94, editReq.LineItems *[]lineItemReq) -- three states over the wire: the
  // key ABSENT (or `undefined`, which JSON.stringify drops) leaves the stored lines
  // untouched; `[]` replaces the whole set with zero lines; a populated array replaces the
  // whole set, renumbered 1..N by array position. Shape copied verbatim from
  // CreateInvoiceInput's own line_items (:416-422) rather than shared -- the two wire
  // request types (createRequest/editReq) are themselves independent on the Go side.
  line_items?: Array<{
    description?: string
    quantity?: string
    unit_price?: string
    line_total?: string
    line_tax?: string
  }>
}

// editInvoice(): PATCH /v1/invoices/{id} (M4-05-03). Precondition: the invoice must be
// draft OR validated (fixable-state guard) -- editing a validated invoice demotes it to
// draft in the same tx (the fix-loop's demotion edge).
export function editInvoice(token: string, id: string, body: InvoiceEditInput): Promise<Invoice> {
  return apiFetch<Invoice>(`${apiBase()}/api/invoice/v1/invoices/${id}`, { method: 'PATCH', body, token })
}

// StatusChange mirrors internal/invoice/invoice.go's StatusChange exactly: one
// invoice_status_history row (task-160/M4-22-01). from_status is nullable -- the
// genesis row has no predecessor state.
export interface StatusChange {
  from_status: Invoice['status'] | null
  to_status: Invoice['status']
  actor: string
  // AUDIT-02-03: the server-resolved display of actor. actor itself is unchanged.
  actor_name: string
  actor_kind: string
  changed_at: string
}

// getInvoiceHistory(): GET /v1/invoices/{id}/history (task-160/M4-22-01). The success
// body is a BARE JSON array, no pagination/envelope ([history-endpoint-scope]) --
// unlike every other wrapper in this file, whose body is a JSON object. Ordered
// changed_at ASC, id ASC.
export function getInvoiceHistory(token: string, id: string): Promise<StatusChange[]> {
  return apiFetch<StatusChange[]>(`${apiBase()}/api/invoice/v1/invoices/${id}/history`, { token })
}

// validateInvoice(): POST /v1/invoices/{id}/validate -- THE gate ([gate-endpoint]), the
// only route to `validated` and the on-demand re-validate endpoint. A blocking verdict
// is still a 200 carrying violations as data (internal/invoice/handlers.go's
// ValidateHandler doc), never an HTTP error -- ApiError from this call means 04 was
// unreachable (502) or has no published rule-set (503), never "the invoice has errors".
//
// ValidateInvoiceResult is the real wire shape (handlers.go's validateResponse): the
// Invoice fields plus one additive sibling key, rule_set_version -- the plain evaluated
// int (or null when nothing was evaluated), distinct from Invoice.rule_set_version_id's
// live-stamped uuid above; no route returns both names for the same concept.
export interface ValidateInvoiceResult extends Invoice {
  rule_set_version: number | null
}

export function validateInvoice(token: string, id: string): Promise<ValidateInvoiceResult> {
  return apiFetch<ValidateInvoiceResult>(`${apiBase()}/api/invoice/v1/invoices/${id}/validate`, { method: 'POST', token })
}

// CreateInvoiceInput mirrors internal/invoice/handlers.go's createRequest wire body
// (M4-07-05, task-159): entity_id/invoice_number are the only required fields
// (handlers.go's pre-tx-guard non-blank check, handlers.go:118-124) -- everything
// else is optional, so omitting all of it is exactly "missing required MBS content"
// (the dashboard.spec.ts broken-draft fixture).
export interface CreateInvoiceInput {
  entity_id: string
  invoice_number: string
  issue_date?: string
  supplier_tin?: string
  supplier_name?: string
  buyer_tin?: string
  buyer_name?: string
  currency?: string
  subtotal?: string
  vat?: string
  total?: string
  line_items?: Array<{
    description?: string
    quantity?: string
    unit_price?: string
    line_total?: string
    line_tax?: string
  }>
}

// createInvoice(): POST /v1/invoices. Reuses the Invoice interface above -- same
// domain type on read and on create.
export function createInvoice(token: string, body: CreateInvoiceInput): Promise<Invoice> {
  return apiFetch<Invoice>(`${apiBase()}/api/invoice/v1/invoices`, { method: 'POST', body, token })
}

// transitionInvoice(): POST /v1/invoices/{id}/transitions ([D12], body {"target":...}).
// The typed setup wrapper completing the invoice seam. `validated` is guarded (409) —
// earned via validateInvoice, not this endpoint. Contract specs observe the raw code via rawFetch.
export function transitionInvoice(token: string, id: string, target: Invoice['status']): Promise<Invoice> {
  return apiFetch<Invoice>(`${apiBase()}/api/invoice/v1/invoices/${id}/transitions`, { method: 'POST', body: { target }, token })
}

// ---- Dashboard rollup wire types, mirrored from internal/dashboard/
// dashboard.go's Counts/Bucket/Client/RuleCount/Rollup -- covers the fields
// the API specs use. Client embeds Bucket ANONYMOUSLY so encoding/json
// promotes counts/needs_attention/awaiting_approval to the row's top level -- DashboardClient
// extends DashboardBucket rather than nesting a "bucket" key.
// `metrics` and per-client `top_violations` are deliberately unmirrored
// (no-e2e-change): rollup() only returns this type, never builds a request
// literal, so TS excess-property checks don't apply. ----

export interface Counts {
  draft: number
  validated: number
  queued: number
  submitted: number
  accepted: number
  rejected: number
  failed: number
}

export interface DashboardBucket {
  counts: Counts
  needs_attention: number
  awaiting_approval: number
}

export interface DashboardClient extends DashboardBucket {
  entity_id: string
  entity_name: string
}

export interface RuleCount {
  rule_key: string
  invoices: number
}

export interface Rollup {
  totals: DashboardBucket
  clients: DashboardClient[]
  top_violations: RuleCount[]
}

// rollup(): GET /v1/rollup -- the per-tenant dashboard payload.
export function rollup(token: string): Promise<Rollup> {
  return apiFetch<Rollup>(`${apiBase()}/api/dashboard/v1/rollup`, { token })
}

// ---- Workflow-role wire types, mirrored from internal/approval/approval.go's Role:
// exactly four keys, none tagged omitempty, so `desc` is an explicit "" and `members`
// an explicit [] rather than absent. `desc`, NOT `description`. ----

export interface WorkflowRole {
  key: string
  title: string
  desc: string
  members: string[] // user_ids in this role's own `ord` order; [] never null
}

export interface WorkflowRolesResponse {
  workflow_roles: WorkflowRole[]
}

export interface WorkflowRoleInput {
  title: string
  desc?: string // optional so JSON.stringify drops it — genuinely absent on the wire
}

export interface WorkflowRoleUpdateInput {
  title?: string
  desc?: string
}

// listWorkflowRoles(): GET /v1/workflow-roles -- a flat list, no pagination envelope.
// Ungated by design: any caller with a tenant claim may read it.
export function listWorkflowRoles(token: string): Promise<WorkflowRolesResponse> {
  return apiFetch<WorkflowRolesResponse>(`${apiBase()}/api/invoice/v1/workflow-roles`, { token })
}

// createWorkflowRole(): POST /v1/workflow-roles. The key is minted server-side from the
// title, so it can only be learned from the response, never predicted.
export function createWorkflowRole(token: string, body: WorkflowRoleInput): Promise<WorkflowRole> {
  return apiFetch<WorkflowRole>(`${apiBase()}/api/invoice/v1/workflow-roles`, { method: 'POST', body, token })
}

// updateWorkflowRole(): PATCH /v1/workflow-roles/{key}. Answers a FULL Role, staffing
// included -- a rename never re-mints the key.
export function updateWorkflowRole(token: string, key: string, body: WorkflowRoleUpdateInput): Promise<WorkflowRole> {
  return apiFetch<WorkflowRole>(`${apiBase()}/api/invoice/v1/workflow-roles/${key}`, { method: 'PATCH', body, token })
}

// deleteWorkflowRole(): DELETE /v1/workflow-roles/{key}. SOFT -- the row survives with
// deleted_at set and its key is never re-minted. Answers the deleted row.
export function deleteWorkflowRole(token: string, key: string): Promise<WorkflowRole> {
  return apiFetch<WorkflowRole>(`${apiBase()}/api/invoice/v1/workflow-roles/${key}`, { method: 'DELETE', token })
}

// staffWorkflowRole(): PUT /v1/workflow-roles/{key}/members, a whole-set replace. Builds
// the {members} envelope itself, so it cannot express {"members":null} -- that 400 is
// only reachable through rawFetch.
export function staffWorkflowRole(token: string, key: string, members: string[]): Promise<WorkflowRole> {
  return apiFetch<WorkflowRole>(`${apiBase()}/api/invoice/v1/workflow-roles/${key}/members`, {
    method: 'PUT',
    body: { members },
    token,
  })
}

// ---- Approval-policy wire types, mirrored from internal/approval/policy.go's
// Step/PolicyVersion/Policy (:17-80). No omitempty on ANY field, and Step/Policy both
// carry a value-receiver MarshalJSON substituting [] for a nil lane -- so `steps`,
// `versions`, `then` and `else` are always arrays, and `published_at`/`published_by` are
// an explicit null rather than absent. Key sets: Policy 8, Step 10, PolicyVersion 5. ----

export interface ApprovalStep {
  // A server-minted uuid, re-minted on EVERY PUT draft (flattenSteps) -- never assert a value.
  id: string
  kind: 'approval' | 'condition' | 'notify' | 'autoapprove'
  workflow_role_key: string | null
  sla_hours: number | null
  cond_op: '>' | '>=' | '<' | '<=' | null
  cond_amount: string | null // numeric(14,2) read via ::text, so the scale survives ('0.00', not '0')
  notify_target: string | null
  notify_channel: string | null
  then: ApprovalStep[] // [] never null
  else: ApprovalStep[] // [] never null
}

export interface ApprovalPolicyVersion {
  version: number
  sealed: boolean
  // The active slot is TENANT-wide (approval_policy_versions_one_active ON (tenant_id)):
  // publishing any policy clears it on whichever version held it.
  is_active: boolean
  published_at: string | null // RFC3339Nano, always Z-suffixed; null until published
  published_by: string | null // the publisher's subject uuid
}

export interface ApprovalPolicy {
  id: string
  name: string
  scope: string // 'All invoices' is the only value normalizeScope accepts today
  status: 'draft' | 'published' // derived from the TOP version's `sealed`
  version: number // the version `steps` belongs to = the HIGHEST version
  sealed: boolean
  steps: ApprovalStep[] // the TOP version's tree, which is NOT necessarily the active one
  versions: ApprovalPolicyVersion[] // ORDER BY version DESC, newest first
}

export interface ApprovalPoliciesResponse {
  approval_policies: ApprovalPolicy[]
}

// stepInput (policy.go:83-93) declares NO id field, so a client-supplied one is dropped
// at decode -- which is why this request type has none either.
export interface ApprovalStepInput {
  kind: string
  workflow_role_key?: string | null
  sla_hours?: number | null
  cond_op?: string | null
  cond_amount?: string | null
  notify_target?: string | null
  notify_channel?: string | null
  then?: ApprovalStepInput[]
  else?: ApprovalStepInput[]
}

export interface ApprovalPolicyCreateInput {
  name: string
  scope?: string // absent means the default scope, not a value
}

export interface ApprovalPolicyDraftInput {
  name?: string
  scope?: string
  steps: ApprovalStepInput[] // required: a whole-tree replace, never a merge
}

// listApprovalPolicies(): GET /v1/approval-policies -- a flat list, no pagination
// envelope. Ungated by design: any caller holding a tenant claim may read it.
export function listApprovalPolicies(token: string): Promise<ApprovalPoliciesResponse> {
  return apiFetch<ApprovalPoliciesResponse>(`${apiBase()}/api/invoice/v1/approval-policies`, { token })
}

export function getApprovalPolicy(token: string, id: string): Promise<ApprovalPolicy> {
  return apiFetch<ApprovalPolicy>(`${apiBase()}/api/invoice/v1/approval-policies/${id}`, { token })
}

// createApprovalPolicy(): POST /v1/approval-policies. Mints the policy AND its open draft
// version 1, so the answer is version 1, status "draft", steps []. The 201 itself is
// invisible here (apiFetch resolves on any 2xx) -- that claim belongs to rawFetch.
export function createApprovalPolicy(token: string, body: ApprovalPolicyCreateInput): Promise<ApprovalPolicy> {
  return apiFetch<ApprovalPolicy>(`${apiBase()}/api/invoice/v1/approval-policies`, { method: 'POST', body, token })
}

// putApprovalPolicyDraft(): PUT /v1/approval-policies/{id}/draft, a whole-tree replace of
// the open draft -- or of a fresh max+1 version when the policy holds none. Builds the
// {name,scope,steps} envelope itself, so it cannot express {"steps":null} (the staffWorkflowRole
// precedent) -- that 400 is only reachable through rawFetch.
export function putApprovalPolicyDraft(token: string, id: string, body: ApprovalPolicyDraftInput): Promise<ApprovalPolicy> {
  return apiFetch<ApprovalPolicy>(`${apiBase()}/api/invoice/v1/approval-policies/${id}/draft`, {
    method: 'PUT',
    body: { name: body.name, scope: body.scope, steps: body.steps },
    token,
  })
}

// publishApprovalPolicy(): POST /v1/approval-policies/{id}/publish. NO body at all -- the
// handler reads none, and published_by is the caller's subject taken inside the store.
export function publishApprovalPolicy(token: string, id: string): Promise<ApprovalPolicy> {
  return apiFetch<ApprovalPolicy>(`${apiBase()}/api/invoice/v1/approval-policies/${id}/publish`, {
    method: 'POST',
    token,
  })
}

// deleteApprovalPolicy(): DELETE /v1/approval-policies/{id}. SOFT, and the answer is INERT
// -- only id/name/scope are carried through, so a policy published at v3 still answers
// status "draft", version 0, steps [] and versions []. A second delete is 404.
export function deleteApprovalPolicy(token: string, id: string): Promise<ApprovalPolicy> {
  return apiFetch<ApprovalPolicy>(`${apiBase()}/api/invoice/v1/approval-policies/${id}`, { method: 'DELETE', token })
}

// ---- Approval-run wire types, mirrored from internal/approval/read_model.go's
// Resolved/RunStep/RunDecision/Run. RunStep carries no omitempty (fixed key set
// regardless of kind); Run substitutes [] for a nil Steps/Decisions via its own
// MarshalJSON, so both are always arrays. ----

export interface ApprovalResolved {
  text: string
  warn: boolean
}

export interface ApprovalRunStep {
  ord: number
  kind: string
  state: string
  workflow_role_key: string | null
  workflow_role_title: string | null
  holder: ApprovalResolved | null
  sla_hours: number | null
  due_at: string | null
  overdue: boolean
  satisfied_at: string | null
  satisfied_by: string | null
  notify_target: string | null
  notify_channel: string | null
}

export interface ApprovalRunDecision {
  run_step_id: string
  ord: number
  decision: string
  actor: string
  decided_at: string
  reason: string | null
}

export interface ApprovalRun {
  run_id: string
  state: string
  opened_at: string
  closed_at: string | null
  closed_by: string | null
  steps: ApprovalRunStep[]
  decisions: ApprovalRunDecision[]
}

// getInvoiceApproval(): GET /v1/invoices/{id}/approval -- the run read model. Ungated
// by design: RLS is the only tenant scope, no role gate (read_model.go's own comment).
export function getInvoiceApproval(token: string, id: string): Promise<ApprovalRun> {
  return apiFetch<ApprovalRun>(`${apiBase()}/api/invoice/v1/invoices/${id}/approval`, { token })
}

// decideInvoiceApproval(): POST /v1/invoices/{id}/approvals -- approve or reject the
// current pending step. reason is required and non-blank for "rejected", optional for
// "approved"; both capped at 1000 bytes server-side.
export function decideInvoiceApproval(
  token: string,
  id: string,
  body: { decision: 'approved' | 'rejected'; reason?: string },
): Promise<ApprovalRun> {
  return apiFetch<ApprovalRun>(`${apiBase()}/api/invoice/v1/invoices/${id}/approvals`, {
    method: 'POST',
    body,
    token,
  })
}

interface ApprovalTransport {
  read: (token: string, id: string) => Promise<ApprovalRun>
  decide: (token: string, id: string, body: { decision: 'approved' | 'rejected'; reason?: string }) => Promise<ApprovalRun>
}

// decodeJwtSubject(): reads `sub` off a JWT payload without verifying it -- these are
// always mock-issuer tokens in this suite, and it's the only way to name a subject given
// approveUntilClosed's (invoiceId, tokens, max) signature carries none.
function decodeJwtSubject(token: string): string {
  return JSON.parse(Buffer.from(token.split('.')[1] ?? '', 'base64url').toString('utf8')).sub
}

// approveUntilClosed(): drives a run to closed, deciding the first pending approval step
// each iteration with the token staffed to its role. Reads once up front; every step after
// that advances on decideInvoiceApproval's own returned run, not a fresh read.
//
// Guard order: closed state first -- a one-step run closes with no pending step left, and
// comparing that missing step's ord against a previous ord would misfire the stalled guard
// on a correct run. Then no-pending-step, or a stalled open run falls through to
// tokens[undefined] and reports a misleading missing-token error. Then the stalled-ord
// check, keyed on (run_id, ord) rather than ord alone -- a concurrent cancel-and-re-arm is
// the one way ord legitimately goes backward, and run_id turns that would-be false positive
// into a correctly-worded failure instead.
//
// max=6: the deepest reachable policy today needs 4 decisions (a firm invoice over 1bn
// materialises fin_mgr, fin_dir, cfo, compliance -- demopolicy.go:129-139); 6 is headroom.
export async function approveUntilClosed(
  invoiceId: string,
  tokens: Record<string, string>,
  max = 6,
  transport?: ApprovalTransport,
): Promise<ApprovalRun> {
  // Empty tokens against the real transport would reach the initial read unauthenticated
  // and surface a bare 401; fail before that call. A scripted transport doesn't touch the
  // network on the token's account, so this only guards the default pair.
  if (!transport && Object.keys(tokens).length === 0) {
    throw new Error(`approveUntilClosed: tokens is empty; cannot authenticate the initial read (invoice ${invoiceId})`)
  }
  transport = transport ?? { read: getInvoiceApproval, decide: decideInvoiceApproval }

  let run = await transport.read(Object.values(tokens)[0] ?? '', invoiceId)
  let decided = 0
  let lastPending: { runId: string; ord: number } | null = null

  while (run.state === 'open') {
    const pending = run.steps.find((s) => s.kind === 'approval' && s.state === 'pending')
    if (!pending) {
      throw new Error(`approveUntilClosed: run is open but has no pending approval step (invoice ${invoiceId}, run ${run.run_id})`)
    }
    if (lastPending && lastPending.runId === run.run_id && lastPending.ord === pending.ord) {
      throw new Error(
        `approveUntilClosed: pending ord did not advance (invoice ${invoiceId}, run ${run.run_id}, ord ${pending.ord}, role ${pending.workflow_role_key})`,
      )
    }
    if (decided >= max) {
      throw new Error(
        `approveUntilClosed: exceeded max decisions (${max}) for invoice ${invoiceId}; still pending role ${pending.workflow_role_key}`,
      )
    }
    const roleKey = pending.workflow_role_key
    const token = roleKey ? tokens[roleKey] : undefined
    if (!token) {
      throw new Error(`approveUntilClosed: no token for pending role "${roleKey}" (invoice ${invoiceId})`)
    }

    lastPending = { runId: run.run_id, ord: pending.ord }
    try {
      run = await transport.decide(token, invoiceId, { decision: 'approved' })
    } catch (e) {
      const reason = e instanceof Error ? e.message : String(e)
      // decodeJwtSubject throws on a non-JWT token; degrade rather than let that parse
      // error replace the real decide failure being reported here.
      let subject: string
      try {
        subject = decodeJwtSubject(token)
      } catch {
        subject = 'unavailable'
      }
      throw new Error(
        `approveUntilClosed: decide failed for role "${roleKey}" as subject ${subject} (invoice ${invoiceId}): ${reason}`,
      )
    }
    decided += 1
  }

  return run
}

// firmApproverTokens(): mints the seeded firm run's two holder tokens once -- ...0004
// (fin_mgr) and ...0005 (compliance). Memoises the in-flight PROMISE, not the resolved
// value, so two concurrent callers can't double-mint; this is the first module-scope token
// cache in the api suite (every other site uses a per-file beforeAll), which AC-7 mandates.
// The mock issuer's 1h TTL against a 5-15min run means the cache can't go stale.
let firmApproverTokensPromise: Promise<Record<string, string>> | null = null

export function firmApproverTokens(): Promise<Record<string, string>> {
  if (!firmApproverTokensPromise) {
    firmApproverTokensPromise = (async () => {
      const [fin_mgr, compliance] = await Promise.all([
        login({ ...PERSONAS.A, subject: 'c0000000-0000-0000-0000-000000000004' }),
        login({ ...PERSONAS.A, subject: 'c0000000-0000-0000-0000-000000000005' }),
      ])
      return { fin_mgr, compliance }
    })()
  }
  return firmApproverTokensPromise
}

// ---- Audit-reader wire types (AUDIT-04-08), mirrored key-for-key from
// internal/audit/reader.go. e2e/api/client.test.ts's wire-mirror block fails if either side
// gains or loses a key. `payload` is `unknown`, not a named shape: it is jsonb whose keys
// differ per event, and typing it would be a lie the mirror could not catch. ----

export type AuditCompanyScope = 'company' | 'workspace' | 'unattributed'

export interface AuditEvent {
  id: string
  created_at: string
  event: string
  actor: string
  actor_name: string
  actor_kind: string
  entity_id: string | null
  company_name: string | null
  company_scope: AuditCompanyScope
  payload: unknown
}

export interface AuditPageInfo {
  limit: number
  has_more: boolean
  next_cursor: string | null
}

// kind is `json:"kind,omitempty"` on the Go side, so it is absent on event and company
// facets and present only on actor facets — hence optional here, unlike every other key.
export interface AuditFacet {
  value: string | null
  name: string | null
  kind?: string
  count: number
}

export interface AuditFacets {
  event: AuditFacet[]
  actor: AuditFacet[]
  company: AuditFacet[]
}

export interface AuditResponse {
  events: AuditEvent[]
  page: AuditPageInfo
  total: number
  log_is_empty: boolean
  facets: AuditFacets
}

export interface AuditLogQuery {
  limit?: number
  cursor?: string
  from?: string
  to?: string
  event?: string[]
  actor?: string[]
  actor_kind?: 'people' | 'system'
  company?: string
  q?: string
  invoice_id?: string
}

// getAuditLog(): GET /v1/audit-log on the invoice binary — the workspace audit reader.
// `!== undefined` rather than truthiness, so q='' stays sendable and is not silently dropped.
export function getAuditLog(token: string, query?: AuditLogQuery): Promise<AuditResponse> {
  const params = new URLSearchParams()
  if (query?.limit !== undefined) params.set('limit', String(query.limit))
  if (query?.cursor !== undefined) params.set('cursor', query.cursor)
  if (query?.from !== undefined) params.set('from', query.from)
  if (query?.to !== undefined) params.set('to', query.to)
  for (const e of query?.event ?? []) params.append('event', e)
  for (const a of query?.actor ?? []) params.append('actor', a)
  if (query?.actor_kind !== undefined) params.set('actor_kind', query.actor_kind)
  if (query?.company !== undefined) params.set('company', query.company)
  if (query?.q !== undefined) params.set('q', query.q)
  if (query?.invoice_id !== undefined) params.set('invoice_id', query.invoice_id)
  const qs = params.toString()
  return apiFetch<AuditResponse>(`${apiBase()}/api/invoice/v1/audit-log${qs ? `?${qs}` : ''}`, { token })
}

// EXTR-07: GET /v1/extractions on the submission binary -- the extraction job reader.
// Mirrors extraction.JobsResponse / extraction.JobState (internal/extraction/reader.go:23-35).
// documentId is OPTIONAL so a spec can exercise the required-parameter arm, and the guard is
// `!== undefined` (getAuditLog's convention) so '' stays sendable -- the handler treats absent
// and empty alike (internal/extraction/handlers.go:65-69) and both arms must be reachable.
export interface ExtractionJob {
  id: string
  document_id: string
  state: string
  created_at: string
  last_error: string | null
}

export interface ExtractionJobsResponse {
  jobs: ExtractionJob[]
}

export function getExtractions(token: string, documentId?: string): Promise<ExtractionJobsResponse> {
  const params = new URLSearchParams()
  if (documentId !== undefined) params.set('document_id', documentId)
  const qs = params.toString()
  return apiFetch<ExtractionJobsResponse>(
    `${apiBase()}/api/submission/v1/extractions${qs ? `?${qs}` : ''}`,
    { token },
  )
}

// EXTR-11: GET /v1/extractions/{id} on the submission binary -- the review screen's read.
// Mirrored key-for-key from internal/extraction/reader.go; wireMirrors.test.ts fails if this
// side or frontend/app/src/lib/extractionReview.ts gains or loses a key.
export interface ExtractionRegion {
  page: number
  x0: number
  y0: number
  x1: number
  y1: number
}

export interface ExtractionPage {
  page: number
  width_px: number
  height_px: number
}

export type ExtractionReason = '' | 'unreadable' | 'ambiguous' | 'inconsistent' | 'missing'

export interface ExtractionCandidate {
  value: string | null
  region: ExtractionRegion | null
}

export interface ExtractionCorrected {
  method: CorrectionMethod
  was: string | null
  where: string | null
}

export interface ExtractionFieldState {
  name: string
  value: string | null
  region: ExtractionRegion | null
  reason: ExtractionReason
  alternatives: ExtractionCandidate[]
  corrected: ExtractionCorrected | null
}

export interface ExtractionDocument {
  filename: string | null
  content_type: string | null
  size_bytes: number
  stored_at: string
}

export interface ExtractionDetail {
  id: string
  document_id: string
  state: string
  document: ExtractionDocument
  pages: ExtractionPage[]
  fields: ExtractionFieldState[]
}

export function getExtractionDetail(token: string, jobId: string): Promise<ExtractionDetail> {
  return apiFetch<ExtractionDetail>(
    `${apiBase()}/api/submission/v1/extractions/${encodeURIComponent(jobId)}`,
    { token },
  )
}

// EXTR-12: POST /v1/extractions/{id}/fields/{name}/corrections -- one human correction.
// Mirrored key-for-key from internal/extraction/handlers_correction.go; wireMirrors.test.ts
// fails if this side or frontend/app/src/lib/extractionReview.ts gains or loses a key.
export type CorrectionMethod = 'typed' | 'chosen' | 'pointed' | 'undone'

export interface CorrectionResponse {
  id: string
  field_name: string
  value: string
  method: CorrectionMethod
  region: ExtractionRegion | null
  invoice_id: string
  created_at: string
}

export interface CorrectionRequest {
  value: string
  method: CorrectionMethod
  region?: ExtractionRegion | null
  anchor_label?: string
}

export function postFieldCorrection(
  token: string,
  jobId: string,
  field: string,
  body: CorrectionRequest,
): Promise<CorrectionResponse> {
  return apiFetch<CorrectionResponse>(
    `${apiBase()}/api/submission/v1/extractions/${encodeURIComponent(jobId)}/fields/${encodeURIComponent(field)}/corrections`,
    { token, method: 'POST', body },
  )
}
