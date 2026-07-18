// M4-04-08 (task-115): the api/ suite's env-gated superuser DB access, for
// PERF-03/04/05's assertions that have NO API surface today:
//
//   - v2's rule_set_versions.id (uuid) -- /v1/validate only echoes the
//     INTEGER version (client.ts's ValidateResult.rule_set_version); the uuid
//     internal/invoice stamps onto invoices.rule_set_version_id is never
//     returned by any GET/list/validate response, so PERF-03/04's "assert it
//     equals v2's actual uuid, queried live" (never a hardcoded literal, per
//     the task's Stage-1 addendum -- both this story's pin detectors watch
//     for exactly that) has no HTTP path -- only the DB has it.
//
//   - invoice_status_history has no read API at all (mirrors day30.spec.ts's
//     db.ts precedent for audit_log, which likewise has none) -- PERF-05's
//     Day-60 stamp-gate check (draft->validated, per this story's "Ships when
//     true" clause) can only be read from Postgres.
//
//   - PERF-05's "correct a failing invoice" step ALSO has no HTTP path:
//     internal/invoice/store.go's Store.Update exists, but cmd/invoice/main.go
//     wires no PATCH/PUT /v1/invoices/{id} route to it -- verified by reading
//     every app.Mux.HandleFunc/Handle call there (only POST /v1/invoices, GET
//     /v1/invoices/{id}, GET /v1/invoices, POST .../transitions, POST
//     .../validate, and POST /v1/imports are registered). This is a genuine
//     gap between the task's plan text ("fix it via Store.Update") and the
//     shipped surface, not a design choice made here -- flagged in the Mode A
//     return for the PR description; a future subtask should wire the route
//     if the product wants firms to self-correct an invoice over HTTP.
//     correctInvoiceVAT below writes directly, superuser, bypassing that
//     (unreachable-over-HTTP) production update path so PERF-05 can still run
//     as a real, working spec rather than being left unimplemented.
//
// Mirrors e2e/demo/db.ts's pg wiring verbatim (default-import + destructure
// for ESM/CommonJS interop -- pg ships no ESM-native named exports; TLS
// rejectUnauthorized:false for Railway's managed cert; bounded timeouts so a
// stalled cold-DB connect/query fails clearly instead of hanging out to the
// Playwright test timeout) rather than importing that module directly --
// demo/db.ts is a demo-suite-scoped module (its own header says so) and api/
// already has its own project/config boundary (playwright.api.config.ts), so
// a fresh, small file here keeps the two projects' DB access independent
// rather than introducing an api -> demo dependency that runs the wrong
// direction (demo already imports FROM api/client.ts, never the reverse).
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

// dbEnabled(): the DSN is a CI secret, absent on a local run -- PERF-03/04's
// uuid-equality assertion narrows to a self-consistency check, and the whole
// of PERF-05 skips (with a loud annotation), when this is false. Mirrors
// demo/db.ts's dbEnabled/D8 precedent exactly. LOCAL ONLY: in CI a false here
// is a hard failure, never a skip -- see requireDbInCI.
export function dbEnabled(): boolean {
  return !!process.env.DATABASE_SUPERUSER_URL_DEV
}

// requireDbInCI(): the guard that stops a missing DSN from becoming an INVISIBLE
// GREEN. Skipping is correct LOCALLY -- the DSN is a CI secret and a dev has no
// dev-Postgres access -- but in CI the secret is GUARANTEED present: it is a
// GitHub Actions repository secret, always available to any step that
// explicitly maps it via `env:` (M4-21-06: reset-seed is no longer an
// unconditional dependency of the "API E2E" step -- it now runs ONLY on the
// workflow_dispatch path that still targets the persistent `development`
// environment, so it can no longer be cited as proof the secret exists). So
// !dbEnabled() in CI cannot mean "no secret"; it can only mean the workflow
// stopped PASSING it to this step. That is exactly what happened: the
// test:api step shipped with no env: block, PERF-05 -- the Day-60
// draft->validated stamp gate, the M4 "Ships when true" clause -- skipped
// silently, and the job went green anyway. Fail loudly instead, the same way
// reset-seed's own `::error::` guard does, and for the same reason
// scripts/ci/rls-test-gate.sh exists at all: a skipped test must never read
// as a passing one.
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
