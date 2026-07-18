# Demo Reset (M3-12)

**Audience:** whoever is about to run a firm call on the demo tenant.

One command restores the demo tenant (`Okafor & Partners`) to a curated, deterministic,
presentable state — undoing the drift the Day-30 demo suite (M3-11) and prior calls leave
behind: a kill-switched validation rule and accumulated generic `Demo Client <TIN>` rows.

## What it does

`db/demo-reset.sql`, applied as the Postgres superuser in one transaction:

1. **Guards** — refuses (rolls back, no writes) unless the target DB actually has the
   seeded demo tenant fixture (`11111111-…` / `'Okafor & Partners'`). This is what stops
   the command from ever being pointed at the wrong environment.
2. **Re-enables every validation rule** (`UPDATE rules SET enabled = true WHERE enabled =
   false`) — rules are global, so this restores any rule a prior demo kill-switched.
3. **Clears and re-curates the demo tenant's portfolio** — deletes every
   `business_entities` row under the demo tenant and inserts the fixed 27-row curated set
   (21 active / 6 archived named Nigerian businesses). Other tenants are untouched.

It is idempotent — re-running always converges to the same 27 rows, never accumulates.

## Usage

```bash
DATABASE_SUPERUSER_URL_DEV="<deployed-dev superuser DSN>" make demo-reset
```

**The one env var:** `DATABASE_SUPERUSER_URL_DEV` — the deployed `development` Postgres
superuser DSN (Railway → Postgres → Connect → Public Network), supplied by hand on the
command line each time. This is a **local-only** variable, unrelated to the
same-named `DATABASE_SUPERUSER_URL_DEV` GitHub Actions secret that `dev-env.yml` used to
read — no workflow reads that secret anymore (M4-21-10): `dev-env.yml` discovers this DSN
fresh per run via the Railway API instead (see [topology-e2e.md](./topology-e2e.md)), so
this local variable is unaffected regardless of whether that now-unused secret is deleted
from the repo. Superuser is required
because `tenants`/`business_entities` are `FORCE ROW LEVEL SECURITY`, and re-enabling a
rule or curating another tenant's-worth of client rows needs `BYPASSRLS`.

**When to run it:** right before a firm call — it takes seconds and needs no other setup.

## Guard behavior

`make demo-reset` refuses to run — with a clear error and a non-zero exit, before ever
invoking `psql` — if `DATABASE_SUPERUSER_URL_DEV` is unset. If it *is* set but points at a
DB that doesn't have the seeded demo tenant (e.g. an empty CI Postgres, or the wrong
project), the SQL file's own guard raises and rolls back with zero writes. Either way,
nothing is touched unless the target is unambiguously the seeded demo/dev DB.

## Out of scope

This is a manual, on-demand operator command only — there is no cron job, pre-call hook,
or CI wiring that runs it automatically. Nobody is meant to trigger it from the app UI.

## Related

- `db/demo-reset.sql` — the guarded SQL itself.
- `internal/platform/db/demo_reset_test.go` — the DB-backed test suite proving the guard,
  rule re-enable, and clear/curate behavior.
- [topology-e2e.md](./topology-e2e.md) — the sibling `development`-only, dispatch-path-only
  reset/seed flow (`db/reset.dev.sql` + `db/seed.dev.sql`, still present — M4-21-06 scoped
  it to `workflow_dispatch` only, never deleted it) this command deliberately does not
  reuse (that one wipes *all* tenants; this one is scoped to the demo tenant only).
