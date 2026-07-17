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

// Re-exported so specs can do `import { ApiError } from '../api/client'`
// against this one seam, without reaching into the api-client package
// directly — negative specs assert err.kind === 'http' / err.status.
export { ApiError }

// apiBase(): mirrors topology/targets.ts's GATEWAY_URL resolution exactly.
// Deliberately does NOT call the api-client's own gatewayBase() — that reads
// import.meta.env.VITE_GATEWAY_URL, which throws under Node (this package
// has no Vite/browser runtime).
export function apiBase(): string {
  return (
    (process.env.GATEWAY_URL ?? '').trim().replace(/\/+$/, '') || 'https://gateway-development-997b.up.railway.app'
  )
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

// validateInvoice(): POST /v1/invoices/{id}/validate -- THE gate ([gate-endpoint]), the
// only route to `validated` and the on-demand re-validate endpoint. A blocking verdict
// is still a 200 carrying violations as data (internal/invoice/handlers.go's
// ValidateHandler doc), never an HTTP error -- ApiError from this call means 04 was
// unreachable (502) or has no published rule-set (503), never "the invoice has errors".
export function validateInvoice(token: string, id: string): Promise<Invoice> {
  return apiFetch<Invoice>(`${apiBase()}/api/invoice/v1/invoices/${id}/validate`, { method: 'POST', token })
}
