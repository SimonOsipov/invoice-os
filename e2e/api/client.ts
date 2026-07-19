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

export interface Membership {
  user_id: string
  role: string
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

// Invoice mirrors internal/invoice/invoice.go's Invoice struct exactly (M4-04-08,
// task-115). violations is Go json.RawMessage on the wire -- always a JSON array in
// practice (invoices.violations jsonb NOT NULL DEFAULT '[]', migrations/
// 20260714103137_invoices.sql), so Violation[] is the accurate wire shape, not a raw
// string. rule_set_version_id is the LIVE-STAMPED uuid ([uuid-stamp]) -- distinct from
// ValidateResult.rule_set_version above, which is the plain int the /v1/validate route
// echoes; no route returns both on the same object.
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
  line_items?: unknown[]
}

export interface ListInvoicesQuery {
  limit?: number
  offset?: number
}

export interface ListInvoicesResponse {
  invoices: Invoice[]
  pagination: Pagination
}

// listInvoices(): GET /v1/invoices. NO entity/invoice_number/status filter exists
// ([D8], internal/invoice/handlers.go's ListHandler doc: "No status/entity filters") --
// only limit/offset, mirroring this file's listEntities shape. Callers that need to find
// one particular invoice among a tenant's whole history must page and filter
// client-side (see api/perf.spec.ts's findInvoiceId).
export function listInvoices(token: string, query?: ListInvoicesQuery): Promise<ListInvoicesResponse> {
  const params = new URLSearchParams()
  if (query?.limit !== undefined) params.set('limit', String(query.limit))
  if (query?.offset !== undefined) params.set('offset', String(query.offset))
  const qs = params.toString()
  return apiFetch<ListInvoicesResponse>(`${apiBase()}/api/invoice/v1/invoices${qs ? `?${qs}` : ''}`, { token })
}

export function getInvoice(token: string, id: string): Promise<Invoice> {
  return apiFetch<Invoice>(`${apiBase()}/api/invoice/v1/invoices/${id}`, { token })
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

// ---- Dashboard rollup wire types, mirrored EXACTLY from internal/dashboard/
// dashboard.go's Counts/Bucket/Client/RuleCount/Rollup (M4-07-05, task-159).
// dashboard.go's Client embeds Bucket ANONYMOUSLY so encoding/json promotes
// counts/needs_attention to the row's top level -- DashboardClient below
// models that promotion directly (extends DashboardBucket) rather than
// nesting a "bucket" key. No `omitempty` on any Counts field on the Go side,
// so every key is always present, zeros included. ----

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
