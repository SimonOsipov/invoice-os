// M3-11 Day-30 wedge demo — shared helpers + constants (task-85).
//
// Reuses the M3-14 api seam (../api/client) and its golden fixtures
// (../api/fixtures) read-only, plus the topology targets (../topology/targets),
// rather than re-deriving login/create/offboard/TIN/persona logic — pinning the
// demo to the same contract the api suite already guards (story Decision D9).

import { createEntity, listEntities, offboardEntity, type Entity } from '../api/client'
import { freshTin } from '../api/fixtures'
import { TENANTS } from '../topology/targets'
import { withNetworkRetry } from './retry'

// The rule the demo kill-switches in AC-6 (PATCH .../v1/rules/vat-standard-rate
// {enabled:false}) and asserts audited in AC-7 (event validation.rule.disabled,
// payload.key = this). vat-standard-rate is the bad-VAT rule the "Has
// violations" preset fires and that badInvoice isolates.
export const DISABLED_RULE_KEY = 'vat-standard-rate'

// The demo tenant = seeded firm persona A (Okafor & Partners). The browser
// persona "Chinedu Okafor" resolves to this same tenant, so the UI session and
// the headless API/DB token are one tenant (story Decision "demo tenant").
export const DEMO_TENANT_ID = TENANTS.a.id

// Sidebar nav button labels (frontend/app/src/glyphs.tsx NAV_CLIENTS/
// NAV_VALIDATION, rendered in firm mode by Sidebar.tsx). The journey spec (02)
// clicks these to reach the Clients and Validation surfaces.
export const CLIENTS_NAV = 'Clients'
export const VALIDATION_NAV = 'Validation'

// AC-2 portfolio precondition: the demo tenant must list >= 25 business
// entities with a realistic status mix (>= 1 ACTIVE and >= 1 ARCHIVED pill).
const MIN_TOTAL = 25
const MIN_ARCHIVED = 2

// listEntities is issued with limit:200 (mirroring frontend/app/src/lib/
// portfolio.ts:73) so the backend's default-50 clamp (internal/portfolio/
// portfolio.go) never undercounts the portfolio.
const LIST_LIMIT = 200

// Bounded-concurrency batch size for the create round-trips against a possibly
// cold Railway fleet — fresh TINs are order-independent and collision-free, so
// small parallel batches are safe and bound total latency (story Seeding-cost
// watch-item).
const CREATE_BATCH = 5

// ensurePortfolioSeeded(): make the demo tenant satisfy AC-2 IDEMPOTENTLY,
// targeting TOTAL and ARCHIVED counts — never the active count. There is no
// delete endpoint (only offboard = archive, ../api/fixtures.ts:120-121), and an
// un-filtered list returns both active and archived rows, so keying off total +
// archived is what makes repeated calls CONVERGE (once total >= 25 and archived
// >= 2 both branches no-op) with no unbounded growth on a never-reset local DB.
//
// Algorithm (story Decision D1):
//   1. list (limit 200) -> total = all rows, archived = rows with status
//      'archived' (the Entity status enum is lowercase, ../api/client.ts:126).
//   2. if total < 25: create (25 - total) fresh entities (all born active).
//   3. if archived < 2: offboard (2 - archived) of the currently-active rows
//      (original active + the ones just created — no re-list needed, since
//      creates never change the archived count).
export async function ensurePortfolioSeeded(tokenA: string): Promise<void> {
  const { entities, pagination } = await withNetworkRetry(() =>
    listEntities(tokenA, { limit: LIST_LIMIT }),
  )
  // pagination.total is the authoritative UNCLAMPED filtered count across all
  // pages (internal/portfolio/portfolio.go:177-182), so the >=25 gate stays
  // correct even if the portfolio ever grows past the 200-row page on a
  // never-reset local dev DB. archived count + the offboard pool derive from the
  // returned rows (in CI, reset keeps total ~27 so the page holds everything).
  const total = pagination.total
  const archived = entities.filter((e) => e.status === 'archived').length
  const activeRows = entities.filter((e) => e.status === 'active')

  // Step 2: top up total to >= 25 with fresh, uniquely-named entities.
  const created: Entity[] = []
  const toCreate = Math.max(0, MIN_TOTAL - total)
  for (let i = 0; i < toCreate; i += CREATE_BATCH) {
    const batch = Array.from({ length: Math.min(CREATE_BATCH, toCreate - i) }, () => {
      const tin = freshTin()
      return withNetworkRetry(() => createEntity(tokenA, { name: `Demo Client ${tin}`, tin }))
    })
    created.push(...(await Promise.all(batch)))
  }

  // Step 3: ensure >= 2 archived by offboarding active rows (order-independent
  // — offboardEntity only ever archives, so any active row is a valid target).
  const toArchive = Math.max(0, MIN_ARCHIVED - archived)
  const archiveTargets = [...activeRows, ...created].slice(0, toArchive)
  for (const row of archiveTargets) {
    await withNetworkRetry(() => offboardEntity(tokenA, row.id))
  }
}
