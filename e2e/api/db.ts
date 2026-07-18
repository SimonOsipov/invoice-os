// Superuser DB access for PERF-03/04/05 assertions with no HTTP surface:
// the rule_set_versions uuid (validate echoes only the integer version) and
// invoice_status_history (no read API).
//
// correctInvoiceVAT is NOT in that category any more: PATCH /v1/invoices/{id}
// shipped in M4-05-03 (7bd2a8c). It should move to HTTP.
//
// Duplicates demo/db.ts's pg wiring rather than importing it, to keep the two
// suites independent. pg has no ESM named exports, hence the default import.
import pg from 'pg'
import { ACTIVE_RULE_SET_VERSION } from '../rule-set'

const { Client } = pg

async function withClient<T>(fn: (client: pg.Client) => Promise<T>): Promise<T> {
  const client = new Client({
    connectionString: process.env.DATABASE_SUPERUSER_URL_DEV,
    ssl: { rejectUnauthorized: false },
    connectionTimeoutMillis: 10_000,
    query_timeout: 10_000,
  })
  await client.connect()
  try {
    return await fn(client)
  } finally {
    await client.end()
  }
}

// Absent locally (CI-only DSN): PERF-03/04 narrow to self-consistency and
// PERF-05 skips. In CI a false here is a hard failure — see requireDbInCI.
export function dbEnabled(): boolean {
  return !!process.env.DATABASE_SUPERUSER_URL_DEV
}

// Skipping is right locally, never in CI: a missing DSN there means the
// workflow stopped passing it, not that it doesn't exist. That already
// happened once — PERF-05 skipped silently and the job went green.
export function requireDbInCI(): void {
  if (dbEnabled() || !process.env.CI) return
  throw new Error(
    [
      'DATABASE_SUPERUSER_URL_DEV is not set, but this is CI (process.env.CI) -- refusing to skip.',
      'PERF-05 (the Day-60 draft->validated stamp gate, the M4 "Ships when true" clause) and',
      "PERF-03/04's live rule_set_version_id equality CANNOT be proven without it; skipping them",
      'would make this gate an invisible green. DATABASE_SUPERUSER_URL_DEV is a GitHub Actions repo',
      'secret, guaranteed to exist -- the "API E2E" step in .github/workflows/dev-env.yml is not',
      'passing it. Add:',
      '  env:',
      '    DATABASE_SUPERUSER_URL_DEV: ${{ secrets.DATABASE_SUPERUSER_URL_DEV }}',
    ].join('\n'),
  )
}

// activeRuleSetVersionId(): v2's rule_set_versions.id, queried LIVE against
// the deployed dev DB -- never a hardcoded uuid literal. version is UNIQUE
// (migrations/20260711051711_rule_set_versions.sql: `version integer NOT
// NULL UNIQUE`), so this is exact, not "whichever happens to be active" by
// coincidence. Never call when !dbEnabled().
export async function activeRuleSetVersionId(): Promise<string> {
  return withClient(async (client) => {
    const res = await client.query<{ id: string }>('SELECT id FROM rule_set_versions WHERE version = $1', [
      ACTIVE_RULE_SET_VERSION,
    ])
    if (res.rows.length !== 1) {
      throw new Error(
        `expected exactly one rule_set_versions row for version ${ACTIVE_RULE_SET_VERSION}, got ${res.rows.length}`,
      )
    }
    return res.rows[0].id
  })
}

// statusHistoryHasTransition(): true iff invoice_status_history carries a
// from_status->to_status row for invoiceId at/after `since` -- the PERF-05
// Day-60 stamp-gate check, which has no read API (mirrors day30.spec.ts's
// auditRowExists over audit_log, the same shape one table over). Never call
// when !dbEnabled().
export async function statusHistoryHasTransition(
  invoiceId: string,
  fromStatus: string,
  toStatus: string,
  since: string | Date,
): Promise<boolean> {
  return withClient(async (client) => {
    const res = await client.query<{ n: number }>(
      `SELECT count(*)::int AS n
         FROM invoice_status_history
        WHERE invoice_id = $1 AND from_status = $2 AND to_status = $3 AND changed_at >= $4`,
      [invoiceId, fromStatus, toStatus, since],
    )
    return Number(res.rows[0]?.n ?? 0) >= 1
  })
}

// correctInvoiceVAT(): PERF-05's "fix" step -- a direct superuser UPDATE of
// vat/total, bypassing Store.Update (see the file header: no HTTP route
// exposes it today). Targeted by primary key (id is a uuid PK) so this is a
// single, exact row -- no tenant scoping needed on top of it. Never call when
// !dbEnabled().
export async function correctInvoiceVAT(invoiceId: string, vat: string, total: string): Promise<void> {
  return withClient(async (client) => {
    const res = await client.query('UPDATE invoices SET vat = $1::numeric, total = $2::numeric WHERE id = $3', [
      vat,
      total,
      invoiceId,
    ])
    if (res.rowCount !== 1) {
      throw new Error(`correctInvoiceVAT: expected to update exactly 1 row, updated ${res.rowCount}`)
    }
  })
}

// dbNow(): the DB clock -- the `since` baseline for statusHistoryHasTransition,
// same rationale as demo/db.ts's dbNow (a runner/DB clock skew must never hide
// or over-match a status-history row). Never call when !dbEnabled().
export async function dbNow(): Promise<Date> {
  return withClient(async (client) => {
    const res = await client.query<{ now: Date }>('SELECT now() AS now')
    return res.rows[0].now
  })
}
