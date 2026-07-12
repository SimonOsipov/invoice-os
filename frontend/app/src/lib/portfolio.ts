// App-side portfolio entity types + data-access helpers (M3-08-01, task-56). STUB —
// the executor implements the bodies next; every export below throws so the RED specs
// in portfolio.test.ts (P1-P11) fail on a thrown/assertion mismatch, not an import or
// type error.
//
// Types mirror the wire shapes in internal/portfolio/portfolio.go: `Entity`
// (id/name/tin/registration/sector/address/status/created_at, snake_case
// `created_at` on the wire), the GET listResponse envelope ({entities,pagination}),
// and the POST createEntityRequest / PATCH updateEntityRequest bodies (EntityInput /
// Partial<EntityInput>).
//
// listEntities/createEntity/updateEntity are thin wrappers around an injected
// authedFetch (the app-side 401 seam from M3-07-02, src/lib/authedFetch.ts) — these
// helpers receive authedFetch and know nothing about tokens or onUnauthorized:
// - listEntities:  GET  `${base}/api/portfolio/v1/entities?limit=200`, unwraps `.entities`.
// - createEntity:  POST `${base}/api/portfolio/v1/entities`, full EntityInput body.
// - updateEntity:  PATCH `${base}/api/portfolio/v1/entities/{id}`, Partial<EntityInput> body.
// Non-2xx responses reject with the underlying ApiError unchanged (apiFetch's own
// contract, C1-C8 in packages/api-client/src/client.test.ts) — these helpers must not
// swallow or reshape it.
//
// entityStatusStyle is a pure StatusStyle mapper, following the established
// var(--status-<color>-{bg,border,text}) + uppercase-label convention (see
// src/lib/clients.ts's statusStyle/pillFor): active -> green/ACTIVE, archived ->
// muted/ARCHIVED.
//
// shouldFetchEntities/clientsViewState are pure render-decision helpers, extracted so
// the no-gateway zero-network short-circuit (a deployed SPA with no backend behind it
// must make no network calls — Constraint [A-m]) can be node-tested: the deployed
// fleet is always gateway-wired, so there is no deploy-gate oracle for the base==null
// branch (F5). base==null => 'idle' regardless of async status (the short-circuit
// wins); otherwise the view state mirrors async.status.
//
// `apiFetch`/`ApiError`/`AsyncStatus` (as a runtime value) are referenced only by the
// real implementation (next, not this stub) — importing runtime symbols unused here
// would fail noUnusedLocals under this app's strict tsconfig (mirrors the authedFetch.ts
// stub's rationale, M3-07-02). Only the type-only imports below are referenced by this
// stub's signatures.
import type { ApiFetchOptions, AsyncState, AsyncStatus } from '@invoice-os/api-client'
import type { StatusStyle } from '../types'

export type EntityStatus = 'active' | 'archived'

export interface Entity {
  id: string
  name: string
  tin: string | null
  registration: string | null
  sector: string | null
  address: string | null
  status: EntityStatus
  created_at: string
}

export interface EntityListResponse {
  entities: Entity[]
  pagination: { limit: number; offset: number; total: number }
}

export interface EntityInput {
  name: string
  tin: string
  registration?: string
  sector?: string
  address?: string
}

// The authedFetch seam these helpers consume — createAuthedFetch's return type
// (src/lib/authedFetch.ts). No token/onUnauthorized knowledge lives here.
export type AuthedFetch = <T>(url: string, opts?: ApiFetchOptions) => Promise<T>

export async function listEntities(_authedFetch: AuthedFetch, _base: string): Promise<Entity[]> {
  throw new Error('not implemented')
}

export async function createEntity(
  _authedFetch: AuthedFetch,
  _base: string,
  _input: EntityInput,
): Promise<Entity> {
  throw new Error('not implemented')
}

export async function updateEntity(
  _authedFetch: AuthedFetch,
  _base: string,
  _id: string,
  _input: Partial<EntityInput>,
): Promise<Entity> {
  throw new Error('not implemented')
}

export function entityStatusStyle(_status: EntityStatus): StatusStyle {
  throw new Error('not implemented')
}

export function shouldFetchEntities(_base: string | null): boolean {
  throw new Error('not implemented')
}

export function clientsViewState(_base: string | null, _asyncState: AsyncState<Entity[]>): AsyncStatus {
  throw new Error('not implemented')
}
