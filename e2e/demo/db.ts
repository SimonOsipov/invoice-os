// M3-11 Day-30 wedge demo — env-gated audit_log read (task-85, story Decision
// D2/D7/D8).
//
// There is NO audit-read API/UI surface (audit is a write-only in-process
// module; the gateway routes no `audit` service; the ops-console Audit view is a
// hardcoded mock). AC-7 therefore asserts the real audit_log row directly over
// DATABASE_SUPERUSER_URL_DEV — the superuser secret CI already uses for
// reset+seed. Superuser is BYPASSRLS, so it can read the tenant-scoped,
// FORCE-RLS audit_log (migrations/20260708062657_audit_log.sql).
//
// pg is CommonJS and ships NO ESM-native named exports; under Node ESM (e2e is
// "type":"module", tsconfig verbatimModuleSyntax:true) `import { Client } from
// 'pg'` fails at runtime. The ONLY working form is the default import +
// destructure below.
import pg from 'pg'

const { Client } = pg

// withClient(): the single superuser pg wiring both reads route through (Decision
// D9 — not re-derived per call). Constructs the Client, connects, runs fn, and ALWAYS
// end()s in finally. Railway public Postgres requires TLS; rejectUnauthorized:false
// accepts its managed cert without a local CA bundle (the DSN is a trusted CI secret).
// The bounded connectionTimeoutMillis + query_timeout make a stalled connect/query on
// the cold dev DB fail with a clear DB error instead of hanging until the Playwright
// 60s test timeout. Never call when !dbEnabled().
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

// dbEnabled(): the DSN is a CI secret. Local `test:demo` runs (against the live
// dev URLs, like the other suites) won't have it, so the spec env-gate-skips
// AC-7 when this is false. auditRowExists() must never be called when false.
export function dbEnabled(): boolean {
  return !!process.env.DATABASE_SUPERUSER_URL_DEV
}

// requireDbInCI(): D8's skip is correct LOCALLY (no superuser DSN for a dev) but
// must never happen in CI, where the secret is GUARANTEED present -- the
// reset-seed job (dev-env.yml:243-246) hard-fails the whole run without it and
// the e2e job `needs:` reset-seed. So !dbEnabled() in CI cannot mean "no
// secret"; it can only mean this step stopped being PASSED it, in which case
// AC-7 would skip and the demo would still report green. M4-04-08 hit exactly
// that in the api suite (its test:api step shipped with no env: block, so the
// Day-60 stamp gate silently vanished from a green run); this closes the same
// trap here before it fires. Deliberately DUPLICATED from api/db.ts's
// requireDbInCI rather than shared: demo imports FROM api (api/client.ts), never
// the reverse (see api/db.ts's header), and a third shared module for four
// lines would cost more than the duplication.
export function requireDbInCI(): void {
  if (dbEnabled() || !process.env.CI) return
  throw new Error(
    [
      'DATABASE_SUPERUSER_URL_DEV is not set, but this is CI (process.env.CI) -- refusing to skip.',
      "AC-7 (the kill-switch's audit_log row) CANNOT be proven without it, and skipping it would",
      'make this demo gate an invisible green. reset-seed already requires this same secret, so it',
      'exists -- the "Demo (Day-30 wedge journey)" step in .github/workflows/dev-env.yml is not',
      'passing it. Restore:',
      '  env:',
      '    DATABASE_SUPERUSER_URL_DEV: ${{ secrets.DATABASE_SUPERUSER_URL_DEV }}',
    ].join('\n'),
  )
}

export interface AuditRowQuery {
  event: string
  key: string
  tenantId: string
  // t0 captured (as the DB clock's now()) just before the toggle, so the
  // assertion sees only THIS run's row on the always-on, accumulating dev DB
  // (audit_log has no FK to tenants, so reset never clears it — Decision D7).
  since: string | Date
}

// auditRowExists(): true iff >= 1 audit_log row matches the toggle signature.
// The row the kill-switch writes in-tx (internal/validation/store.go:170-181)
// has event = 'validation.rule.<enabled|disabled>' and payload {key,version,
// from,to}; we match event + payload->>'key' + tenant_id + created_at >= since.
// Never call this when !dbEnabled().
export async function auditRowExists({ event, key, tenantId, since }: AuditRowQuery): Promise<boolean> {
  return withClient(async (client) => {
    const res = await client.query<{ n: number }>(
      `SELECT count(*)::int AS n
         FROM audit_log
        WHERE event = $1
          AND payload->>'key' = $2
          AND tenant_id = $3
          AND created_at >= $4`,
      [event, key, tenantId, since],
    )
    return Number(res.rows[0]?.n ?? 0) >= 1
  })
}

// dbNow(): the DB clock (SELECT now()) — the `t0` baseline AC-7 filters audit_log
// rows against (created_at >= t0). Uses the DATABASE clock, NOT the Node runner
// clock, so a runner/DB skew can neither hide this run's toggle row nor over-match
// an accumulated older one (Decision D7). Routes through withClient like
// auditRowExists, reusing this module's single pg wiring rather than re-deriving the
// connection string + TLS config in the spec (Decision D9). Never call when
// !dbEnabled(). pg maps a timestamptz to a JS Date, so the return is a Date.
export async function dbNow(): Promise<Date> {
  return withClient(async (client) => {
    const res = await client.query<{ now: Date }>('SELECT now() AS now')
    return res.rows[0].now
  })
}
