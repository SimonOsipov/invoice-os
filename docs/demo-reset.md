# The demo-tenant purge on deploy

**Audience:** anyone who has hit a retention claim elsewhere in this repo and needs to
know what the exception to it actually is, and any operator who has just deployed and is
looking at a demo environment that does not look right.

Every **gated gateway boot** deletes the four seeded demo tenants' rows from 22
tenant-owned tables, then re-seeds them from `db/seed.dev.sql`. It runs inside
`db.Provision`, between `Reset` and `Seed` (`internal/platform/db/provision.go`), and the
primitive is `db.PurgeDemoTenants` (`internal/platform/db/demopurge.go`).

**On the persistent production environment, this is the only place a row covered by a
retention claim is deleted.** Ordinary request handling does delete committed rows —
`line_items` on a re-import, `workflow_role_members` when a role's staffing changes,
`approval_policy_steps` when a policy version is edited, plus River's own job cleanup — but
`invoice_app` holds no `DELETE` grant on any table those claims are about (`audit_log`,
`approval_runs`, `approval_run_steps`, `approval_decisions`, `submission_jobs`,
`app_exchange`, `idempotency_keys`, `invoice_status_history`, `documents`), so no request
can reach one. The purge reaches them because it runs as superuser.

Every retention claim in this repo — `audit_log` is permanently retained, approval
decisions are retained permanently, a submission job is never deleted by the app — is true
of every real tenant, and is scoped by this page and by nothing else.

## Scope: four tenants, by literal uuid

| Tenant | uuid |
|---|---|
| Tenant A (dev) | `aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa` |
| Tenant B (dev) | `bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb` |
| Okafor & Partners | `11111111-1111-1111-1111-111111111111` |
| Honeywell Group | `22222222-2222-2222-2222-222222222222` |

Every statement the purge issues is `DELETE FROM <table> WHERE tenant_id = ANY($1)` with
that list bound. The list is a Go literal (`db.DemoTenants`), never derived from the seed
at runtime — deriving it would let a tenant added to `db/seed.dev.sql` silently widen a
destructive delete. `TestPurgeAllowlistMatchesSeedFileTenants` compares the two in both
directions, and `TestPurgeHasNoUnscopedDeleteStatement` proves the tenant predicate lives
in the same string literal as the `DELETE`.

**The tenant list is the safety boundary, not the environment.** `ENVIRONMENT` reads
`development` on the persistent production environment and is inherited verbatim by a
`pr-<N>` fork, so gating on it would be fail-open — see
[`docs/deploy-model.md`](deploy-model.md), which is where that trap is recorded. The purge
therefore runs on production too. It cannot reach a real tenant's data anywhere, because
no real tenant holds one of those four uuids.

### This is not `db.Reset`

`db.Reset` is destructive in a different shape: it `TRUNCATE`s whole tables — every
tenant's rows, plus River's queue tables — rather than four tenants' rows. But it only
ever runs where `RAILWAY_ENVIRONMENT_NAME` matches `^(?:.+-)?pr-[0-9]+$` — a per-PR
ephemeral fork, which nobody reads as a compliance record. Until this purge shipped,
nothing deleted a committed row on the persistent production environment. That is what
changed, and it changed for the four tenants above only.

## What a deploy resets

Twenty-two tables, deleted leaf-first so every foreign key stays enforced. The order is the
reverse of `db/seed.dev.sql`'s parent-first inserts.

| # | Table | Why it is purged |
|---|---|---|
| 1 | `approval_decisions` | who approved or rejected a demo invoice; the backlog is re-armed from scratch, so a surviving decision would describe a run that no longer exists |
| 2 | `approval_run_steps` | the per-step state of a demo approval run, meaningless once its run is gone |
| 3 | `approval_runs` | the open/closed run per demo invoice; `internal/demopolicy` re-arms the whole validated backlog on the next invoice-service boot |
| 4 | `app_exchange` | the request/response bodies of demo submissions to the mock APP |
| 5 | `submission_jobs` | one row per demo transmit attempt; a demo left mid-retry would keep retrying against re-seeded invoices |
| 6 | `line_items` | the lines of a demo invoice, re-inserted whole by the seed |
| 7 | `invoice_status_history` | the demo invoice's transitions, which would otherwise describe a previous demo's invoice |
| 8 | `invoices` | the demo register itself — the curated portfolio the seed re-creates |
| 9 | `import_batches` | demo import runs; nothing re-links them, so a survivor would point at deleted invoices |
| 10 | `business_entities` | the curated demo supplier portfolio, re-inserted by the seed |
| 11 | `extraction_anchor_rules` | the anchor rules a demo tenant's corrections taught the extractor; nothing re-seeds them, so a survivor would keep steering reads of documents the seed has just replaced |
| 12 | `extraction_field_results` | the per-field results of a demo document read, meaningless once their job is gone. Purged **before** `extraction_jobs`: the foreign key is `ON DELETE CASCADE`, so purging the parent first would take these rows silently and report a count of 0 |
| 13 | `extraction_field_corrections` | the append-only record of every field a demo persona corrected by hand; the seed re-creates none of them, so a survivor would claim a correction on an invoice that no longer exists. Purged **before** `extraction_jobs` for the same reason as the row above |
| 14 | `extraction_jobs` | one row per demo document read; nothing re-links a survivor, and its RESTRICT foreign key would otherwise block the `documents` delete below |
| 15 | `extraction_page_images` | the rendered-page inventory of a demo document, regenerable and meaningless once its document is gone. The rows go and the stored PNGs do not: the purge issues SQL only, exactly as for `documents` below |
| 16 | `documents` | the source-document records; `internal/demodocs` rebuilds them on the next invoice-service boot (see the checklist below). The purge issues SQL only, so the stored object itself is left in the bucket — the row goes, the bytes do not |
| 17 | `idempotency_keys` | the demo dedupe ledger; a surviving key would make a re-run of a demo submission a silent no-op |
| 18 | `submission_rate_limits` | the demo per-tenant rate window, which would otherwise carry the last demo's budget forward |
| 19 | `invitations` | the seed inserts none, so **zero is their seeded state**; an invitation a demo created must not outlive it |
| 20 | `workflow_role_members` | demo staffing; `db.Seed` restores all 13 rows in the same `Provision` call |
| 21 | `workflow_roles` | demo seats; `db.Seed` restores all 14 under their literal seeded ids |
| 22 | `audit_log` | every audit row the four demo tenants accumulated — see the reading note at the bottom of this page |

This list is deliberately **wider** than `db.Reset`'s, which spares `invitations`,
`workflow_roles` and `workflow_role_members`. It can be wider precisely because `db.Seed`
follows the purge inside the same `Provision` call and restores all three.

`audit_log` is last, and it is the only statement the purge issues under
`session_replication_role = 'replica'`. Its append-only trigger refuses a `DELETE` even
from a superuser, and the bypass is transaction-wide while it is on, so the window opens
around that one statement and closes again. Referential integrity stays enforced for the
other twenty-one deletes.

`TestPurgeTableListCoversEveryTenantOwnedTable` asserts that this list plus the four
exclusions below equals the live schema's full set of `tenant_id`-bearing tables — so a
new tenant-owned table cannot be added without a deliberate decision about which side it
belongs on.

## What it leaves standing

Four tables carry `tenant_id` and are deliberately never purged.

| Table | Why it is spared |
|---|---|
| `memberships` | there is no runtime `INSERT` path, so nothing accumulates; the seed's `DO UPDATE` converges identity and status on every boot anyway |
| `approval_policies` | `internal/demopolicy` rebuilds a policy for the **two persona tenants only**, so purging all four would leave the other two with no policy and nothing to restore one |
| `approval_policy_versions` | same reason — and the sealed version a run pointed at must outlive that run |
| `approval_policy_steps` | same reason; the step tree belongs to a sealed version |

Everything outside the four demo tenants is untouched, in every table, on every
environment. So is every table carrying no `tenant_id` at all — the global rule sets,
their immutability locks, and River's queue infrastructure.

## The purge is non-fatal

`Provision` short-circuits fatally on every other step's error. The purge is the one
exception: it runs as a single transaction that rolls back whole, so a failed purge leaves
the database exactly as it was and the boot continues to `Seed`. A crash-loop costs an
environment; a skipped purge costs one demo's cleanliness.

That makes the purge the only provisioning step a green `/healthz` does **not** prove ran.
So the gateway publishes the outcome as `/healthz`'s `demo_purge` field — `true`, `false`
or `error` — and `.github/workflows/dev-env.yml`'s health gate asserts it on every deploy.
That field is the only thing that can turn a swallowed failure into a red deploy.

The gateway also logs one line per purge, `demo purge complete`, carrying `tenants`,
`rows`, `audit_log_rows`, `duration_ms` and a `by_table` map.

## Operator checklist after a deploy

**Restart both services, gateway first.** About one minute. **No CI run is needed** — this
is a service restart, not a redeploy, so nothing rebuilds and no test suite re-runs.

1. **Restart the gateway.** It runs `Provision`: bootstrap, migrate, reset (PR
   environments only), purge, seed. When its `/healthz` returns 200 with
   `demo_purge: "true"`, the demo tenants have been emptied and re-seeded.
2. **Restart the invoice service.** It runs the two seeders the gateway does not have:
   `internal/demodocs` writes each demo invoice's source document, and
   `internal/demopolicy` publishes each persona tenant's approval policy and re-arms its
   validated backlog. Both are non-fatal and both run before the listener opens, so a
   green `/healthz` there means both finished.

Order matters. The invoice service's two seeders write **on top of** the gateway's seed,
so running them first means writing against rows the purge is about to delete.

### Why a gateway-only restart is not enough

The purge and the seed live in the gateway; the two repair seeders live in the invoice
service. They are different processes, and nothing makes one restart the other.

So a gateway restarted **on its own** — a single-service redeploy, an OOM kill, a manual
restart, a platform-initiated replacement — leaves the demo wrong in two visible ways, and
silent in both:

- **Every demo invoice reads "no source document."** `documents` was purged, and
  `db/seed.dev.sql` cannot reach object storage, so every re-seeded invoice carries a
  `NULL` `source_document_id` until `internal/demodocs` runs again.
- **The approval backlog is unarmed.** `approval_runs` was purged while the three policy
  tables were spared, and `awaiting_approval` is satisfied *vacuously* by an invoice with
  zero runs — so the Approvals badge silently reads `counts.validated` instead of a real
  count. That residual, and the fact that it now costs the production demo rather than
  only a PR environment, is recorded in [`docs/approvals.md`](approvals.md) beside the
  seeder's convergence contract; that page is the authority on it and this one does not
  restate it.

**The window is unbounded.** The fleet stays green, both `/healthz` endpoints stay 200,
and nothing raises an alarm. Neither symptom is detected and neither is repaired on a
timer. Both persist until a human restarts the invoice service — which is why step 2 above
is a step and not a note.

## Demo ids are not stable across a deploy

`db/seed.dev.sql` inserts `business_entities` and `invoices` **without** an explicit `id`,
so both take `DEFAULT gen_random_uuid()`. The purge deletes the rows and the seed inserts
fresh ones, which means **every demo entity and every demo invoice gets a new uuid on
every deploy**.

Consequences, all of them intended:

- **A deep link into the demo register does not survive a deploy.** A bookmarked
  `/invoices/<uuid>`, or one pasted into a demo script, answers `404` after the next
  gateway boot. Point a demo at a *list* screen, or reach the invoice by its number.
- An id captured in a screenshot, a support ticket or a hand-written fixture is stale as
  soon as the environment redeploys.
- Anything that must stay addressable across deploys has to key on something the seed
  states literally — an invoice number, an entity name, a `workflow_roles.key` — never on
  a generated uuid.

`documents` behaves the same way one step later. The purge deletes the row, and
`internal/demodocs` re-`Put`s the object on the next invoice-service boot; `Upsert`'s
`ON CONFLICT (tenant_id, content_hash)` finds nothing to resolve, so it inserts a fresh
row under a new id and records `document.created` — not `document.reused` — again. The
stored bytes are the same bytes; the row that points at them is a new one every cycle.

Tenant ids, by contrast, **are** stable: all four are literals in the seed, which is what
makes the allowlist above possible in the first place.

## Reading the `audit_log` purge count

The purge log's `by_table` map and its `audit_log_rows` field have **different
denominators**, which is why the log lifts `audit_log` out of the map rather than leaving
it there as one more entry.

For every other table the seed re-inserts a curated baseline, so the deleted count is best
read as *excess over that baseline* — what a demo accumulated beyond the fixture.

**`audit_log` has no seeded baseline.** `db/seed.dev.sql` writes no audit rows at all: it
inserts through direct SQL rather than through `audit.Record`, so nothing in the fixture
produces one. Its count is therefore **all audit activity the four demo tenants
accumulated since the last purge** — not excess over anything.

A large `audit_log_rows` therefore means the demo environment was used a lot between
deploys. It does not indicate that anything went wrong, and it is not comparable to the
counts beside it in `by_table`.

## Demo mode flag

The purge above runs regardless of whether the demo persona switcher is visible — the two are
independent. This section documents that switch: `VITE_DEMO_MODE`, the flag gating the demo
persona-switcher UI (`frontend/app/src/demo/`) in the sidebar footer.

`VITE_DEMO_MODE` is a Vite build-time flag, not a runtime one: `import.meta.env.VITE_DEMO_MODE
=== 'true'` is folded into the bundle at `vite build` (`frontend/app/src/demo/flag.ts:3`), so an
off build tree-shakes `src/demo/` out entirely rather than shipping the switcher hidden. It
**defaults to off** — an unset build arg resolves to an empty string, which is not `'true'`.

CI sets it on every non-draft PR environment, **app service only**. `reconcile_url_variables`
in `scripts/ci/railway-env.sh` upserts and independently re-verifies `app.VITE_DEMO_MODE = true`
(`:1242`, `:1252`) against `$RAILWAY_SVC_APP_ID`, the same `ARG`/`ENV` build-arg mechanism
`frontend/app/Dockerfile` already uses for `VITE_GATEWAY_URL` and `VITE_LANDING_URL` (`:17-26`).
It runs from the `Point the fork's URL variables at the fork` step
(`.github/workflows/dev-env.yml:514-525`), gated `github.event_name == 'pull_request'` and, at
the enclosing `prepare-env` job, on the PR being non-draft (`:229-239`) — so a draft PR's
environment never gets the flag and never deploys at all.

**Owed: production.** `VITE_DEMO_MODE` is not set on the persistent environment. Until an
operator sets it on the `app` service and redeploys, the persona switcher is absent from
`app.ascomply.com`. CI is refused write access to that environment by design —
`reconcile_url_variables` exits 1 the moment `env_id` matches `$RAILWAY_DEV_ENVIRONMENT_ID`
(`railway-env.sh:1227-1230`), the same refusal that protects every other URL variable this
function reconciles.

## Related

- [`docs/approvals.md`](approvals.md) — the approval-policy seeder's convergence contract,
  and the unarmed-backlog residual this page's checklist prevents.
- [`docs/deploy-model.md`](deploy-model.md) — why `ENVIRONMENT` is not a usable boundary,
  and how a per-PR environment differs from the persistent one.
- [`docs/migrations.md`](migrations.md) — the append-only grant posture the purge bypasses
  for `audit_log`, and for `audit_log` only.
