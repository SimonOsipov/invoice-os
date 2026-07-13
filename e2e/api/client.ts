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
