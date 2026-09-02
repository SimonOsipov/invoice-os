# Read-Path Suspension

**Audience:** anyone adding an HTTP route, an operator CLI, or a background worker that touches
tenant data. AUDIT-10 made membership status bite on **reads**, not only on writes: a caller
whose `memberships` row in the current tenant exists and is not `active` is refused before the
handler's closure runs. Before this story, a suspended member kept full read access to every
screen until their token expired — the suspension was a label on a list. AUDIT-12 closed the
one gap AUDIT-10 shipped deliberately: a caller with no `memberships` row at all is refused the
same way (§5).

This page is the contract that change created: where the gate sits, what it costs, which
endpoints it covers, which are exempt and why, and the one hole it still leaves open. Every
figure here was measured against Postgres 18.6 on Docker Desktop loopback; the reasoning behind
each choice is in AUDIT-10's and AUDIT-12's `## Decisions`.

**No migration.** The gate is a `SELECT` against an index that already existed
(`memberships_tenant_user_uq`), so this story added no DDL. §7 is why that was never a close
call.

---

## 1. What the gate refuses, and where it sits

`db.WithinRequestTenantTxOpts` (`internal/platform/db/tenant.go`) is the whole gate.
`WithinRequestTenantTx` is a one-line delegation to it, so both entry points are gated and
neither can be bypassed by choosing the simpler-looking one.

The seam already sent a `set_config` round trip to establish `app.current_tenant`. The gate
rides along in the same `pgx.Batch`:

```
b.Queue("SELECT set_config('app.current_tenant', $1, true)", id.TenantID)
b.Queue("SELECT status FROM memberships WHERE user_id = $1", id.Subject)
```

Order is load-bearing. The membership `SELECT` is RLS-scoped by the GUC the first statement
sets, so queueing them the other way round would read across tenants —
`TestRLS_RequestSeamIssuesOneRoundTripForTheGate` and
`TestRLS_RequestSeamDoesNotSeeAnotherTenantsMembershipRow` pin both halves of that.

Two outcomes:

| The caller's `memberships` row in this tenant | The seam |
|---|---|
| exists, `status = 'active'` | runs the closure |
| exists, any other status, or does not exist at all | returns `db.ErrNotActiveMember` before the closure — §5 is why the second case was ever a question |

### 1.1 One shape skips the lookup entirely

`memberships.user_id` is `uuid`. A subject that is not a well-formed uuid can match no row, and
a failed statement would poison the batch's transaction — so a non-uuid subject falls through
to the ungated core with the tenant set and no membership read
(`TestRLS_RequestSeamSkipsTheLookupForANonUUIDSubject`, `…ForAnEmptySubject`). A request with
no identity, or with a malformed tenant id, is refused with `db.ErrNoTenant` before any
statement is issued at all (`TestRLS_RequestSeamIssuesNoStatementForAMalformedRequest`).

**Why this is not a hole.** Over HTTP the arm above can never fire. `Verifier.validate`
(`internal/platform/auth/verify.go:147`) rejects any token whose `sub` claim is not a
well-formed uuid with `ErrUnauthorized` — a 401 at token verification, before an `Identity` is
ever built, let alone reaches this seam. The only way to reach this arm at all is to construct
an `auth.Identity` by hand, in-process — exactly what §8.1's operator CLIs and boot-time seeders
do, on purpose, for callers the network never produced. So the arm is a worker/CLI boundary, not
read-path coverage this gate is failing to provide, and AUDIT-12 leaves it exactly as AUDIT-10
wrote it.

## 2. The three-way refusal, and why 403 is not 401

| Status | Sentinel / message | Meaning |
|---|---|---|
| `401` | `db.ErrNoTenant` → `unauthorized` | no verified identity, or no tenant claim |
| **`403`** | `db.ErrNotActiveMember` → `db.NotActiveMemberMessage` = `your membership in this workspace is not active` | authenticated, in the right tenant, membership not active or absent |
| `404` | handler's own not-found | authenticated and active, resource belongs to another tenant or does not exist |

**Why 403 and not 401 is load-bearing, not taste.** `isUnauthorized`
(`frontend/app/src/lib/authedFetch.ts:16`) is true for `status === 401` and nothing else, and
`createAuthedFetch` calls `onUnauthorized()` on exactly that predicate — which signs the user
out and returns them to the front door. A 401 here would be indistinguishable from "your token
expired": the suspended user would be bounced to sign-in with no explanation, and the message
this story exists to deliver would never render. 403 keeps the session and lets the SPA say
why. `TestRLS_RequestSeamRefusalIsNotTheUnauthenticatedSentinel` pins that the two sentinels
never collapse.

## 3. `invited` is refused exactly like `suspended`

One sentinel, one message, one predicate: `status != 'active'` — or no row at all, since
AUDIT-12 (§5). There is no separate "not yet accepted" path and no second wire message
(`TestRLS_RequestSeamRefusesAnInvitedCaller`, `…RefusesASuspendedCaller`).

Reads and writes now agree. Every write-path predicate in the tree already spelled
`AND status = 'active'` — `requireActiveAdmin` and the approval decision's AXIS 1 among them —
so before this story a caller could read every screen and change nothing, which read as a bug
in the UI rather than as a suspension.

**The seed carries zero `invited` rows**, so a test that needs one must mint it. Reactivation
is immediate and needs no new session: the gate reads status per request
(`TestRLS_RequestSeamAdmitsAReactivatedCaller`, `…RefusesAfterALiveSuspension`).

## 4. `GET /v1/me` is the one deliberate exemption

`tenancy.Store.Me` calls `db.WithinTenantTx` directly, skipping the gate. Scan 2 (§9) pins that
exemption to that one **func**, not to its file.

It is exempt because it is the SPA's only boot round trip, and `signIn` throws when it fails.
Gating it turns every suspended session into an unexplained sign-in failure — with nothing left
able to say why, since the screen that would carry the 403 never mounts. Exempting `/v1/me`
is what makes the 403 reachable at all.

The exemption is narrow by construction: `Me` returns only the caller's **own** tenant and
**own** role. It reads no other member, no invoice, and nothing belonging to anyone else. Its
two file-mates, `ListMemberships` and `SetMembershipStatus`, are gated — which is why the
exemption is func-scoped: a method added beside `Me` inherits the gate, and the guard fails if
anyone widens it.

### 4.1 What the exemption discloses, named plainly

`Store.Me` runs its own membership lookup (`SELECT role FROM memberships WHERE user_id = $1`)
and never reads `status`. A caller holding a valid token for a tenant id they already know can
therefore learn which of three cases they are in:

| Tenant id names | `/v1/me` answers | What that reveals |
|---|---|---|
| no `tenants` row | `404 {"error":"tenant not found"}` | the id names no workspace |
| a real tenant, caller has no `memberships` row | `403 {"error":"no membership"}` | the id names a workspace, the caller has never been a member |
| a real tenant, caller's row exists, any status | `200` + their own tenant and role | the id names a workspace, the caller has a row — active, suspended, or invited alike |

**The residue, stated honestly.** Everywhere else in this doc, a caller with no row and a
suspended caller now answer identically — the same `403`, the same `db.NotActiveMemberMessage`
(§5). `/v1/me` is the one place that still tells them apart: no row is `403 {"error":"no
membership"}`, any row at all is `200`. A caller refused elsewhere can still learn which of the
two cases they were in by calling this one exempt route. That is Core AC 2's purpose
(indistinguishability) not fully met by its letter (the shared 403 sentinel elsewhere) — the
exemption cannot be closed without breaking the sign-in flow described above, so the gap is
accepted, not hidden.

**Owned, with a reopen trigger.** Accepted at AUDIT-12's fork gate rather than fixed. Under
today's mock login, every token holder was already provisioned by an operator, so this
disclosure is one already-known user learning something about a workspace, not a stranger
learning about one. **Reopen trigger: real authentication or self-signup exists** — the moment
an unvetted party can obtain a token, the disclosure becomes reachable by strangers. No owner
today.

## 5. The strict rule: a caller with no membership row is refused, same as a suspended one

**After this story, both of these are true:** suspension bites on reads, and a caller holding a
valid token for a tenant they have no `memberships` row in is refused exactly like a suspended
one — same sentinel (`db.ErrNotActiveMember`), same wire message, same status.

`TestRLS_RequestSeamRefusesACallerWithNoMembershipRow` pins the refusal where AUDIT-10's
`TestRLS_RequestSeamAllowsACallerWithNoMembershipRow` used to pin the admission it replaces — the
rename is the visible flip. `TestRLS_RequestSeamRefusesACallerWhoseRowWasDeleted` pins the same
rule at its sharpest edge: deleting a membership now refuses exactly as suspending it does.
`TestRLS_RequestSeamRefusalIsIdenticalForSuspendedAndForNoRow` pins the two cases to one
sentinel and one message, so nothing downstream of the gate — §4's exemption aside — can tell
them apart.

**The cost was measured, not estimated: 723 failing tests across 9 packages**, against a
baseline of **0** failing tests and **0** failing packages on the code as it stood before the
fixture sweep:

```
$ DEV_DB_PORT=5433 bash scripts/dev/blast-radius.sh baseline
failing tests:    0
failing packages: 0
```

measured against a copy of `tenant.go` with one line widened —
`scanErr == nil && status != "active"` becomes `scanErr != nil || status != "active"` — and
nothing else:

```
failing tests:    723
failing packages: 9
  internal/approval  internal/dashboard  internal/document  internal/importer
  internal/invoice   internal/platform/db  internal/portfolio  internal/submission
  internal/tenancy
```

AUDIT-12's fixture sweep (subtasks 01–07) rebuilt every one of those packages' test callers off a
real seeded membership row instead of a bare, rowless uuid. The same `baseline` command against
the shipped `tenant.go` — see "Verify" in this story's PR — now reports **0 failing tests, 0
failing packages**: the strict rule ships with no assertion left behind.

**AUDIT-10's published estimate — 797 failing tests across 11 packages — was measuring a
different predicate, not this one.** AUDIT-10's prototype had no non-uuid guard (§1.1), so
widening its `status` predicate to "no row ⇒ refuse" refused a non-uuid subject too, a variant
this repo never shipped. Re-measured against today's code, that variant costs **800 failing
tests across 12 packages** — the same 9 above, plus `internal/archive`, `internal/audit` and
`internal/validation`, whose fixtures build callers with `Subject: "system"`. 800/12 sits within
3 tests and 1 package of AUDIT-10's 797/11; the small remaining gap is later commits, not a
different rule. **723 across 9 is the cost of the rule this story actually ships**, and it is
why `tenant.go:59`'s non-uuid arm (§1.1) stays exactly as AUDIT-10 wrote it: refusing that arm
too buys nothing reachable over HTTP for +77 tests and +3 packages.

## 6. The gate is not free: +12 to +42 µs/op

Three shapes were benchmarked against the seeded dev database:

| Shape | µs/op (three runs) | Delta vs today |
|---|---|---|
| A — today, no gate | 689 / 667 / 684 | baseline |
| B — gate as its own round trip | 833 / 880 / 885 | **+144 … +213** |
| C — gate batched into the existing `set_config` (**shipped**) | 702 / 709 / 712 | **+12 … +42** |

C is what shipped: one extra statement, not one extra round trip.

**An earlier draft of this page claimed C was *faster* than today (−14 … −58 µs/op). That claim
is withdrawn.** It came from three runs against the 13-row seeded `memberships` table and sat
inside the harness's own noise band; a negative cost for added work was the tell. The honest
statement is the one in the table: the gate costs something, it is small, and it is bounded by
one statement on a connection the seam had already opened.

Every figure above is Docker Desktop loopback. Treat them as an **upper bound on this link**,
not as a portable constant: a co-located production database will show a smaller absolute
number, and a distant one a larger. The shape of the claim — B is a round trip, C is a
statement — is what carries over. AUDIT-12's predicate widening (§5) is one comparison inside
the same `SELECT`, not a second statement, so this table is unchanged by it.

## 7. No test may assert the query plan for this predicate

**This is a flat prohibition, not a preference.** No test in this repo asserts the plan of
`SELECT status FROM memberships WHERE user_id = $1`.

The Seq-Scan-to-Index-Scan crossover tracks **`relpages`**, not row count:

| Rows | `relpages` | Plan |
|---|---|---|
| 13 | 1 | Seq Scan |
| 113 | 2 | Seq Scan |
| 163 | 3 | Seq Scan |
| 213 | 4 | Index Scan |
| 1,000 | 10 | Index Scan |

Two different crossovers were published in two review rounds, and **both were wrong**, because
`relpages` does not fall when rows are deleted. The same row count flips plans as the table
bloats and again after a `VACUUM FULL`. A test asserting the plan would have to own the table's
physical state, which no test in this suite does or should.

**"No migration" never depended on the plan.** `memberships_tenant_user_uq (tenant_id, user_id)`
already is the index this predicate wants — there is no index left to add. And at the one scale
where the planner declines it, the alternative is a single-page sequential scan costing
0.035 ms. The decision was cheap either way; the plan assertion would have been the only
fragile part of it.

## 8. The endpoint table — every route, covered or exempt

**`covered` is structural, not per-route inspection.** Scans 1 and 2 (§9) make the gated seam a
monopoly: outside eight named files and one named func, nothing in `internal/` **or `cmd/`** can
obtain a database handle at all, and the only callers allowed to reach the identity-free core
are workers, boot-time seeders, two operator CLIs and `GET /v1/me`. Every route that touches
tenant data is therefore gated by construction, and `covered` records that. `exempt` rows each
state their own reason.

The gate applies to the whole request, so a `POST`/`PATCH`/`DELETE` route is covered too — a
refused caller is now refused at the seam, before the write-path `status = 'active'`
predicates it would previously have hit inside the transaction.

| Route | Service | Verdict | Reason |
|---|---|---|---|
| `GET /v1/ping` | all seven (×7) | exempt | a liveness echo; it opens no transaction and reads no table |
| `GET /healthz` | every service (`internal/platform/server.go`) | exempt | must answer while the database is down; a membership lookup would invert its meaning |
| `GET /readyz` | every service (`internal/platform/server.go`) | exempt | same — readiness reports on the database, so it cannot depend on reaching it |
| `GET /healthz/fleet` | gateway | exempt | fleet roll-up across services, deliberately outside the verifier |
| `POST /auth/login` | gateway | exempt | unauthenticated by definition; there is no caller yet to hold a membership |
| `OPTIONS /auth/login` | gateway | exempt | the CORS preflight for the line above, same absence of a caller |
| `GET /.well-known/jwks.json` | gateway | exempt | serves the public verification keys; unauthenticated by design |
| `/api/` | gateway | exempt | the proxy mount, not an endpoint — it forwards to the seven services whose own routes are listed here |
| `GET /v1/me` | tenancy | exempt | §4 — the SPA's boot round trip; gating it would make the 403 unreachable |
| `POST /v1/validate/batch` | validation | exempt | `S2SMiddleware` peer call with no caller identity by construction, and the gateway strips any client-supplied `X-S2S-Token` (`internal/gateway/gateway.go`, `injectIdentity`) |
| `GET /v1/memberships` | tenancy | covered | |
| `PATCH /v1/memberships/{user_id}` | tenancy | covered | |
| `GET /v1/entities` | portfolio | covered | |
| `POST /v1/entities` | portfolio | covered | |
| `GET /v1/entities/{id}` | portfolio | covered | |
| `PATCH /v1/entities/{id}` | portfolio | covered | |
| `POST /v1/entities/{id}/offboard` | portfolio | covered | |
| `POST /v1/entities/{id}/onboard` | portfolio | covered | |
| `GET /v1/rollup` | dashboard | covered | |
| `PATCH /v1/rules/{key}` | validation | covered | |
| `GET /v1/invoices` | invoice | covered | |
| `POST /v1/invoices` | invoice | covered | |
| `GET /v1/invoices/{id}` | invoice | covered | |
| `PATCH /v1/invoices/{id}` | invoice | covered | |
| `GET /v1/invoices/{id}/history` | invoice | covered | |
| `GET /v1/invoices/{id}/source-document` | invoice | covered | |
| `GET /v1/invoices/{id}/ubl` | invoice | covered | |
| `POST /v1/invoices/{id}/transitions` | invoice | covered | |
| `POST /v1/invoices/{id}/validate` | invoice | covered | |
| `POST /v1/invoices/{id}/keep-as-is` | invoice | covered | |
| `DELETE /v1/invoices/{id}/keep-as-is` | invoice | covered | |
| `POST /v1/invoices/{id}/resolved-outside` | invoice | covered | |
| `DELETE /v1/invoices/{id}/resolved-outside` | invoice | covered | |
| `GET /v1/invoices/violation-summary` | invoice | covered | |
| `POST /v1/invoices/submissions` | invoice | covered | |
| `GET /v1/invoices/{id}/approval` | invoice | covered | |
| `POST /v1/invoices/{id}/approvals` | invoice | covered | |
| `GET /v1/audit-log` | invoice | covered | |
| `GET /v1/evidence-bundle` | invoice | covered | |
| `GET /v1/evidence-bundle/preview` | invoice | covered | |
| `POST /v1/imports` | invoice | covered | |
| `POST /v1/imports/preview` | invoice | covered | |
| `POST /v1/imports/document` | invoice | covered | |
| `GET /v1/imports/{id}` | invoice | covered | |
| `GET /v1/documents/{id}` | invoice | covered | |
| `GET /v1/documents/{id}/sheet` | invoice | covered | |
| `GET /v1/workflow-roles` | invoice | covered | |
| `POST /v1/workflow-roles` | invoice | covered | |
| `PATCH /v1/workflow-roles/{key}` | invoice | covered | |
| `DELETE /v1/workflow-roles/{key}` | invoice | covered | |
| `PUT /v1/workflow-roles/{key}/members` | invoice | covered | |
| `GET /v1/approval-policies` | invoice | covered | |
| `POST /v1/approval-policies` | invoice | covered | |
| `GET /v1/approval-policies/{id}` | invoice | covered | |
| `PUT /v1/approval-policies/{id}/draft` | invoice | covered | |
| `POST /v1/approval-policies/{id}/publish` | invoice | covered | |
| `DELETE /v1/approval-policies/{id}` | invoice | covered | |
| `GET /v1/extractions` | submission | covered | |
| `GET /v1/extractions/{id}` | submission | covered | |
| `GET /v1/extractions/{id}/pages/{n}` | submission | covered | |
| `POST /v1/documents` | submission | covered | |
| `POST /v1/extractions/{id}/fields/{name}/corrections` | submission | covered | |
| `POST /v1/extractions/{id}/line-items` | submission | covered | |

63 distinct routes, 69 registrations (`GET /v1/ping` is registered once per service).

### 8.1 The non-HTTP callers, so nobody looks for them above

These reach tenant data outside the request path. They are not endpoints and have no row in the
table. Most carry no request identity at all, so there is nothing to gate on; `internal/demodocs`
is the one exception and its row says so.

| Caller | Why there is no identity |
|---|---|
| `tools/backfill-source-rows` (`internal/importer/backfill.go`) | operator CLI; it carries a job tenant |
| `tools/revalidate-invoices` (`internal/invoice/revalidate.go`) | operator CLI; same |
| the submission and poll workers (`internal/submission`) | River jobs; the job row carries the tenant |
| the extraction worker (`internal/extraction`) | River job; the job's own args carry the tenant, and its `document.read` audit rows are attributed to the non-uuid subject `extraction-worker`, which can hold no membership. Live in production since EXTR-09 wired `POST /v1/documents` to `EnqueueExtraction` |
| the reconciliation sweep (`internal/reconciliation`) | scheduled worker; it also enumerates tenants as `invoice_tenant_reader` with no GUC set |
| the demo approval-policy seeder (`internal/demopolicy`) | boot-time, before the first request is served |
| the demo source-document seeder (`internal/demodocs`) | boot-time; its admin lookup carries none. **It is the exception**: `seedTenant` synthesizes an identity for the admin it found and writes through the gated seam, so the gate does apply to it. `tenantAdmin` therefore selects `AND status = 'active'` — without it a suspended admin sorting first would cost that tenant every source document, silently, since `cmd/invoice/main.go` logs a seeder failure and keeps serving (D-11) |
| boot provisioning (`internal/platform/db`) | `bootstrap`, `provision`, `reset` and the demo purge run on a superuser connection before any tenant context exists |
| migrations (`internal/platform/db/migrate.go`) | goose needs a `database/sql` handle, which no pgx pool can supply; it runs at boot on the migrator role |

## 9. The four guards that keep §8 true

Three of the four live in `internal/platform/db/seam_coverage_test.go`, run under `ci.yml`'s
existing `-run TestRLS` filter on this package, and touch no database. Each asserts an
**absence**, so each carries planted control needles and a population floor: a scan that has
stopped matching and a clean repo produce the same report, and the floor is what tells them
apart. Every allowlist is matched **exactly** — an entry matching nothing fails with "delete
it", so a stale exemption cannot outlive its reason.

| Guard | What it asserts | Needles | Floor (measured at AUDIT-10-04) |
|---|---|---|---|
| `TestRLS_NoDirectPoolUseOutsideTheSeam` | **no database handle is acquired outside eight named files and one named func**: no pool method on a `*pgxpool.Pool`-typed name, and no `pgx.Connect`, `pgxpool.New`, `pgconn.Connect` or `sql.Open` off a DSN | a fixture holding both `r.ReaderPool.Query(...)` and `r.URL.Query()` must find **exactly 1**; a bare pool parameter; a non-database method; an aliased local; all three DSN entry points; a renamed import; an acquisition inside a func literal, attributed to the literal | ≥130 files walked (139); ≥4 pool-typed names (4); ≥9 sites across ≥8 files (10 across 9) |
| `TestRLS_UngatedCoreIsWorkerAndExemptionOnly` | every call of the identity-free `db.WithinTenantTx`/`Opts` is a worker, a boot-time seeder, an operator CLI, or `tenancy` func `Me` | a call in a named func; a doc comment naming the seam (0 sites); a call inside a func literal, attributed to the literal | ≥130 files walked (139); ≥12 sites across ≥6 packages (14 across 7) |
| `TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute` | every `app.Mux` route in `cmd/*/main.go` and `internal/platform/server.go` has a row in §8 with a verdict, and no row classifies a route nobody registers | a const-indirected route resolves; an unresolvable argument fails loudly; a verdict cell must read exactly `covered` or `exempt`; a longer path cannot answer for a shorter one | ≥8 roots yielding routes (9); ≥55 registrations (63) |
| `TestRLS_ReadPathSuspensionDocHasNoStaleNarrowRuleClaim` | this page carries no sentence still asserting AUDIT-10's narrow rule (§5) | a fixture planting both stale phrases must be flagged; a fixture holding only the legitimate active-row line must not | this file parses to ≥10 top-level (`## `) section headings |

A fifth, `TestRLS_EveryAllowlistEntryNamesItsReason`, reads the guard file's own source and
fails any exemption whose line carries no trailing reason. Core AC 6 asks for a reason per
exemption, not a category.

**What the doc-accuracy guard cannot see.** It matches a named phrase list, so a stale claim
written in different words would still pass. The rest of §1, §1.1, §4 and §5 reading true is a
reviewer's read of this diff, not a mechanical property — there is no general "this prose is
accurate" oracle, and none is attempted here.

### 9.1 Why the scans invert the question

The obvious oracle — "prove each route reaches the gated seam" — is a call-graph question this
repo cannot answer cheaply. Routes are registered as higher-order constructions
(`invoice.ListHandler(store.List, store.RowFacts, app.Logger)`), so the func named at the route
is not the func that touches the database: the work is in a func **value** passed as an
argument, several hops and one package away, behind func types. Deciding it needs real type
information — `golang.org/x/tools`, which is not in `go.mod` — for one test whose
false-negative mode is silent.

So the scans ask the complementary question, which is locally decidable: **can HTTP-serving
code reach the database without the gate?** If it cannot, every route is gated by
construction and no call graph is needed. Coverage becomes a property of the seam's monopoly
rather than a property of each route.

### 9.2 Known limits

- Scan 1 resolves pool-typed identifiers by **type spelling**, so it cannot follow a pool
  passed as `any` or reached through an interface. It knows four handle packages — `pgx`,
  `pgxpool`, `pgconn` and `database/sql` — and a fifth driver would have to be added by hand.
  It counts **construction** (`pgxpool.New`) as an acquisition on purpose: a pool built into a
  short-var local carries no declared type, so the pool method later called on it is invisible
  to the type-spelling pass, and catching the constructor is what closes that.
- Scan 2 sees a direct `db.WithinTenantTx` selector call, not one reached through a func value.
- Scans 1 and 2 attribute each site to its **innermost enclosing func**, which is what lets an
  exemption name a func rather than a whole file. Both cross-check the attributed count against
  the file's raw count, so a site outside every func fails rather than vanishing. A func-scoped
  exemption matches by bare func name within its file, so two same-named methods on different
  receivers in one file would share one exemption.
- Scan 3 sees only routes registered on `app.Mux` in the walked roots, and resolves a
  non-literal route argument only when it is a string const declared in the same directory. Any
  other shape fails loudly; it is never skipped.
- No scan walks outside `internal/` and `cmd/`: scans 1 and 2 walk both trees whole, scan 3
  walks `cmd/` plus the one `internal/` file that registers a route for every service.
  `frontend/**` and `e2e/**` are out of scope for the same reason they are in
  `handler_mapping_test.go`: no `ci.yml` path filter routes a frontend-only commit to the Go
  job, so a Go assertion over them would be unreachable on the very commit it guards.
- **The mapping-arm guard cannot see a handler that maps neither sentinel.**
  `TestHandlerMappingEveryRefusalSiteNamesNotActiveMember` (`handler_mapping_test.go`) draws its
  population from funcs that already map `db.ErrNoTenant` — named `statusForErr`/`*StatusForErr`,
  or calling `errors.Is(_, db.ErrNoTenant)`. A future handler mapping neither sentinel is outside
  both populations, so it would fall to its `default` arm and answer a refused caller `500`
  instead of the `403` §2 specifies. The seam still refuses the caller; only the status is wrong.
- **The doc-accuracy guard (§9's fourth row) matches literal phrases**, not meaning: a future
  edit that reintroduces the narrow rule in different words would pass it silently. It closes the
  mechanical half of "this page describes the shipped behaviour"; the rest is reviewer read.

## 10. The hand-maintained mirror

The wire message is written down **once** in Go — `db.NotActiveMemberMessage`,
`internal/platform/db/tenant.go` — and `TestHandlerMappingMessageIsNeverRetyped` fails if any
other Go file retypes the literal.

That Go walk covers `internal/`, `cmd/` and `tools/` only. Two TypeScript copies live outside
it and are pinned by `frontend/app/src/lib/wireMirrors.test.ts` (AUDIT-10-07), which extracts
the Go literal from source and compares it to both:

| Copy | Why it exists |
|---|---|
| `frontend/app/src/lib/authedFetch.ts` — `NOT_ACTIVE_MEMBER_MESSAGE` | `isSuspended` matches on the message, not the bare 403 |
| `e2e/api/suspension.spec.ts` — `NOT_ACTIVE_MESSAGE` | the deployed-wire assertion |

The extractor refuses a literal containing any backslash escape, and a zero-length extraction
fails the run rather than comparing `'' === ''`. Reword all three in one commit.

`GET /v1/me`'s wire shape does not change, so the three existing `Me` mirrors stay as they are.

The SPA copy that describes suspension to a human is a separate matter and is NOT pinned to
this literal: `lib/members.ts`'s `SUSPEND_EXPLANATION` and `App.tsx`'s `SUSPENDED_NOTICE`
are product sentences, guarded by their own byte-pins.

## 11. Still unsettled, and who owns it

**11.1 A caller with no membership row still reads. CLOSED.** §5. **AUDIT-12 Membership
Presence on Reads** shipped the strict rule: such a caller is now refused, identically to a
suspended one.

**11.2 The TypeScript mirror. SETTLED — it ships.** §10. AUDIT-10-07 added it, rewrote the SPA
copy that read "Sign-in is not blocked yet" and the `e2e/topology` fixture that mirrors it byte
for byte, and made `tenant.go`'s comment true.

Of the two residues it left, one is now closed:

- **The vault's MEMB-01 §8 still carries the old sentence.** `lib/members.ts` says so at its
  docblock. The repo is now the more recent of the two.
- **A suspended member can leave the notice. CLOSED — lead decision, 2026-08-26.** As first
  written the card rendered no control at all, per the subtask design ("no retry loop, no
  partial workspace"), so the only exit was clearing site data. The card now carries a sign-out
  button: neither a retry nor a partial workspace, and a page a user cannot leave is a defect
  whatever the plan said. It calls `App.tsx`'s own `signOut` — the callback the 401 seam fires
  and the one `Sidebar.tsx` calls — so there is one sign-out, not two, and on a deployed build
  it lands the user on the landing page, the product's single front door.
  `App.suspended.test.tsx` pins the control's presence, its label and its end state.

**11.3 A pre-existing full-suite flake.** `TestAudit_ComposedPageIsIndexServedAndUnsorted`
(`internal/audit`) fails in a full-suite run and passes in isolation. It predates this story
and is already recorded in `audit-log-read-contract.md` §10.7. Not AUDIT-10's, and not fixed
here.

**11.4 Whether an `e2e/api` spec can mint a token for a suspended subject in a PR
environment. SETTLED — it can.** `platform.Posture()` maps a `pr-<N>` environment name to
`PosturePreview`, and `MockLoginHandler` consults its persona allowlist only under
`PostureHosted`, so the triple is never matched. That is not only a code reading:
`contract-tenancy.spec.ts` already mints a random-UUID tenant and a tenant-A token for persona
B's subject — neither triple is in `loginPersonas` — and both are green in this job.
`e2e/api/suspension.spec.ts` is the deployed proof of §2 that rides on it, and it does not
rely on minting alone: it also suspends and reactivates a live membership mid-spec, which
holds under either posture.

**11.5 The demo seed un-suspends on every boot.** `docs/demo-reset.md` §103 records that the
`memberships` seed converges identity and status at each boot. That was harmless when
suspension only blocked writes. It now means a suspended demo membership silently regains
**read** access at the next deploy. Flagged, not owned.

## 12. The layout oracle this story deleted, and why nothing replaces it

AUDIT-12-06 deleted `e2e/topology/invoice-surfaces.spec.ts`'s **geometry B** and the
`display_name`/cross-tenant-leak assertions that rode alongside it. This section is that
deletion's finding, written where the next reader of this contract meets it.

**The claim that is now unproven, precisely:** at every swept width down to the rail's 1180px
floor, each `strip-actor` caption's `scrollWidth` stayed within 1px of its `clientWidth` — the
caption rendered whole — while the strip itself was what overflowed. That claim depended on a
caption long enough to put the node under width pressure, and the only such caption in this
repo came from a caller resolved to an actor with no name: `resolve.go`'s fallback prints
`user_id` truncated to 8 hex characters, which reliably overflows a fixed-width node in a way a
real person's name does not.

**Why no reachable actor can restore it.** Under the strict rule (§5), a caller with no
membership row is refused at the seam before it can write anything — including the audit rows
`invoice-surfaces.spec.ts` inspected to find that actor. The only way back to a name-shaped
gap in the fixture is a `memberships` row with a NULL `display_name`, and the demo seed fills
that column for every one of its 13 rows (`db/seed.dev.sql`). Producing the gap deliberately
would mean seeding it — two nameless members in the demo tenant, purely to keep this one caption
long. **The user declined that trade at AUDIT-12's fork gate (D-9): drop the fixtures, seed
nothing.** The architect had recommended seeding; the user overrode it. Both sides of that
reasoning are kept in AUDIT-12's decision log on purpose.

**What still partially covers the strip.** The suite's other three geometries survive
re-pointed and still measure the strip on a real browser: it stays above the fold at four
swept widths, its nodes keep their width while the connectors between them absorb the slack,
and its 96px band never overlaps the detail grid below it. So the strip's own layout is still
proven; it is specifically **whether a long caption stays contained inside its node** that is
not. `StatusStrip.test.tsx`'s jsdom suite still asserts the component *requests*
`flex: none` / `max-content` / `nowrap` on that node — its own comment already states the
limit: a browser is what turns a request into a rendered fact, and jsdom cannot prove that.

**No substitute is offered, and none should be written.** A numeric width assertion is not a
safe stand-in here — this repo has already shipped one that passed on the exact bug it was
meant to catch (`[[layout-needs-rendered-verification]]`: a fixed-width check passed while 32%
of a real screen sat dead). If this claim is ever reasserted, it must again compare rendered
relationships — `scrollWidth` against `clientWidth`, on a real page at a real width — never a
hardcoded pixel count.

**Reopen triggers, any one of:**

1. A `memberships` row with a NULL `display_name` and a non-null `email` becomes reachable.
   `actor.Name` renders an email whole (no spaces to first-word), so a long enough address would
   restore the pressure without a nameless row.
2. Real authentication or self-signup lets a caller act before a membership names them —
   producing exactly the un-membered, unnamed actor this claim used to depend on, without seeding
   anything.
3. Someone measures on a PR environment that the strip still overflows at 1180px with named
   captions. If so, geometry B is restored verbatim from AUDIT-12-06's diff.

No owner today.
