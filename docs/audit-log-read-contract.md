# The `audit_log` Read Contract

**Audience:** anyone building a reader over `audit_log` — AUDIT-05 (bundle), AUDIT-07 (CSV)
and AUDIT-09 (activity feed). AUDIT-01 shipped the schema; **AUDIT-04 shipped the first
reader**, and §10 records what that cost. This page is the contract those readers inherit: what
the indexes serve, what a value in `entity_id` means, and the one predicate no index can help.
Everything here was measured on PG 18.6 against AUDIT-01's migration
`20260820150810_audit_log_entity_id_and_read_indexes.sql`, which added the column, four of the
five indexes and the first `audit_log_entity_for` body; the reasoning behind each choice is in
that story's `## Decisions`. That first body has since been replaced. **The live definition
is whichever migration replaces `audit_log_entity_for` last** —
`TestRLS_AuditResolverDefinerIsTheLatestMigration` finds it. Read the attribution rules
there, not from the migration named above.

---

## 1. `internal/audit` writes and, since AUDIT-04, reads

`audit.Record(ctx, tx, actor, event, payload)` (`internal/audit/audit.go`) is still the only
production **writer**. It is no longer the only thing in the package: AUDIT-04 shipped the
reader, so `internal/audit` now also holds `Query` (the keyset page and the count), the three
facet statements, the empty probe, the HTTP handler and the store.

Earlier versions of this page said the package was write-only "until a reader ships". It has
shipped. Reads of `audit_log` in the tree are now: the five statements `Query` issues,
`logIsEmpty`'s probe, and `internal/invoice/source_document.go`'s uploader lookup.

`Record` takes no entity argument and does not need one: a `BEFORE INSERT` trigger
(`audit_log_entity_on_insert`) fills `entity_id` from the event name and payload. Callers
neither set it nor can override it. `invoice_id` is filled the same way but by a different
mechanism — a STORED generated column, not a trigger (§11).

## 2. The five indexes and the predicates they serve

Every index leads with `tenant_id` so the RLS qual becomes an Index Cond rather than a
heap Filter, and every one ends `created_at DESC, id DESC` so a keyset page is a pure
index read with no sort.

| Index | Serves |
|---|---|
| `audit_log_tenant_created_idx` `(tenant_id, created_at DESC, id DESC)` | first page `ORDER BY created_at DESC, id DESC LIMIT n`; cursor page `WHERE (created_at, id) < ($1, $2)`; date range `WHERE created_at >= $1` |
| `audit_log_tenant_event_created_idx` `(tenant_id, event, created_at DESC, id DESC)` | `WHERE event = $1`; the event facet `SELECT event, count(*) … GROUP BY event` as an Index Only Scan |
| `audit_log_tenant_actor_created_idx` `(tenant_id, actor, created_at DESC, id DESC)` | `WHERE actor = $1` |
| `audit_log_tenant_entity_created_idx` `(tenant_id, entity_id, created_at DESC, id DESC)` | `WHERE entity_id = $1`; `WHERE entity_id IS NULL` (**corpus-dependent — see below**); the company facet `GROUP BY entity_id` as an Index Only Scan |
| `audit_log_tenant_invoice_created_idx` `(tenant_id, invoice_id, created_at DESC, id DESC)` | `WHERE invoice_id = $1`, the per-invoice scoped read (§11). Added by AUDIT-04-11 alongside the generated column |

Pinned by `internal/audit/audit_plan_test.go` over a 20-tenant corpus. The event filter is
bracketed at **1% and 30%** selectivity, so the pin is not knife-edge. No test asserts a node
type: a Bitmap plan over the right index is still the right index.

**An index NAME is asserted only where one index can serve the predicate.** AUDIT-04's
composed cases assert the shape instead, because measured, the planner picks whichever
tenant-leading index carries the most selective **equality** predicate present and heap-Filters
the rest: date alone takes the created index, adding a named company takes the entity index,
`actor = 'system'` takes the actor index. Pinning a name there would fail on a filter change
that broke nothing.

**`entity_id IS NULL` is the one cell here that changes with the corpus.** D-15 measured it on a
single 200,000-row tenant and recorded it as a post-scan Filter, not an Index Cond — the entity
index scanned, the NULLs sifted out afterwards. Measured again on the 20-tenant x 1,000-row
corpus `requirePlanCorpus` builds, the planner does the opposite:

    Sort  (Sort Key: a.created_at DESC, a.id DESC)
      ->  Index Scan using audit_log_tenant_entity_created_idx on audit_log a
            Index Cond: ((tenant_id = ...) AND (entity_id IS NULL))

Both measurements are real. `TestAudit_WorkspaceLevelPageIsPinnedToTheMeasuredPlan` pins the
second, including the Sort, and says so in its failure message. Read §10.7 before you quote
either as a fact.

**Scope an Index Cond assertion to the `audit_log` node.** The page and the company facet
`LEFT JOIN business_entities`, and measured, that node emits its own
`Index Cond: (tenant_id = ...)` on `business_entities_tenant_id_id_uq`. A check that
concatenates every node's Index Cond line can therefore be satisfied by the joined table alone
while `audit_log` has lost its tenant lead entirely — which is the exact regression such a
check exists to catch. `TestAudit_IndexCondIgnoresTheJoinedTable` holds the scoping.

Relation-prefix scoping is not per-node scoping, and the difference is a second false green.
`planCondLines` turns on for every node whose scan target starts with `audit_log`, so a
`BitmapAnd` over two indexes **of the same table**, whose two `Index Cond:` lines each name one
of the required columns, satisfies a both-columns check. Measured on `extraction_jobs`
(`internal/extraction/reader_plan_test.go`): swapping the composite `(tenant_id, document_id)`
index for a `(document_id)`-only one produces exactly that plan. `assertServedByIndex` still
rejects that particular mutation, but on its index-name check, not on the cond check — a variant
where the pinned index itself contributes only one column would pass. Collect `Index Cond:`
lines per scan node and require ONE line to name every column if you need that closed.

Use these column orders. A reader that sorts on anything but `created_at DESC, id DESC`,
or filters on a column that does not lead one of these indexes, falls back to reading the
whole tenant partition.

## 3. `entity_id IS NULL` is never "unknown" — but it is not all workspace-level either

This is the load-bearing semantic of the whole column.

A NULL `entity_id` is a positive claim: **this action was firm-wide**, not attributable to
any one company. Publishing an approval policy, staffing a workflow role and inviting a
member are workspace-level. Rendering NULL as "unknown", "—" or "not set" misreports what
the row says.

The resolver dispatches on the **event name**, never on which payload key happens to be
present, because three workspace-level events carry a bare `id` that is not an invoice id
and seven invoice events spell it `invoice_id`. See §5 for the consequence.

**But `entity_id IS NULL` is not the same set as "firm-wide", and a UI that labels it that way
will be wrong on nearly half the rows.** Measured, `document.*` events are ~43% of the NULL
bucket, and a document upload is not a firm-wide act — it simply has no company to attribute
it to. So the NULL bucket is really *workspace-level* ∪ *unattributed*.

AUDIT-04 resolves this in Go, not in SQL, with `ScopeOf` and the `company_scope` field on
every row of the wire (`internal/audit/reader.go`). It is a three-value closed set:

| `company_scope` | When |
|---|---|
| `company` | `entity_id` is non-NULL |
| `workspace` | `entity_id` is NULL **and** the event is one of the twelve genuinely firm-wide names |
| `unattributed` | everything else — the fallback |

The twelve firm-wide names are the whole of the Policies (4), Roles (4), Memberships (2) and
Validation-rule (2) domains. They are **hand-maintained in Go** (`firmWideEvents`); nothing
derives them from the SQL resolver, so adding a firm-wide event means editing that map too.

**The fallback direction is deliberate and must not be flipped.** An unclassified event fails
safe as "we do not know" rather than falsely claiming "this was firm-wide". Rendering an
unattributed row as *Workspace* asserts something about the row that is not true.

Note the asymmetry this creates with §4: the company filter's `workspace` value emits
`entity_id IS NULL`, which selects **both** `workspace` and `unattributed` rows. A user who
filters to "Workspace" and sees rows labelled "Unattributed" is looking at correct behaviour
that reads as a bug. A reader that offers the filter should say so in the empty/heading copy.

## 4. The company filter is a three-way partition

The reader's company selector has three values, and their predicates are exact:

| Selection | Predicate |
|---|---|
| All companies | *(no `entity_id` predicate)* |
| A named company | `entity_id = $1` |
| Workspace-level only | `entity_id IS NULL` |

**Never `entity_id = $1 OR entity_id IS NULL`.** Mixing workspace rows into a named
company's view counts them once per company and makes the facet counts non-additive. A
company filter must not swallow workspace-level rows, and offering the third value — not
OR-ing it into the second — is what satisfies that.

## 5. A non-NULL `entity_id` that does not resolve is a third state

`entity_id` has **no foreign key**. `business_entities` already grants `invoice_app` full
DML (`migrations/20260709155011_business_entities.sql:61`); only a delete handler is
missing. The day one ships, an `entity_id` can outlive the company it names.

**Resolve with a `LEFT JOIN` and render an unresolved non-NULL id as *a company that no
longer exists*.** Never as workspace-level — that is the swallowing failure of §4
arriving through a different door — and never as a blank cell.

Two other ways a row lands NULL, both correct and neither an error:

- The event is workspace-level (§3).
- The event is invoice-scoped but the invoice is gone, or invisible to the caller. The
  resolver runs `SECURITY INVOKER`, so its lookup into `invoices` obeys the caller's RLS.

Measured on the 200,000-row corpus: 6,000 rows carried a payload `id` and still resolved
NULL, and every one was `document.created` — an event whose `id` is a documents id. That
is §3's dispatch-on-event-name working. Dangling references: 0, because nothing deletes a
company yet.

## 6. Accepted id spellings

The resolver accepts exactly what `uuid_in` accepts, and nothing else: lowercased, an
optional **matched** brace pair stripped, then `^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$` — a
hyphen is legal after any four hex digits, not only at canonical positions. It neither
narrows nor widens Postgres's own grammar.

This matters because six live routes echo the raw URL path segment into the payload
without parsing it. A canonical-only check would read a legal id as NULL, which §3 spells
"workspace-level" — silently misfiling a real client action as firm-wide.

Verified by differential fuzz against `uuid_in`: 180,000 spellings, zero disagreements,
zero cast failures. Fenced by
`TestAudit_InsertTriggerResolvesEverySpellingUUIDInAccepts`.

This grammar now has **four** copies that must not drift. Three are SQL: AUDIT-01's
superseded body in `migrations/20260820150810_audit_log_entity_id_and_read_indexes.sql`, and
both the Up and the Down body of the migration that currently defines
`audit_log_entity_for` — a Down carrying a different grammar would change the resolver on
rollback rather than restore it. The fourth is `actor.Resolve`'s Go copy
(`internal/actor/resolve.go`), which applies it before binding a `uuid[]` — an unfiltered
subject there raises 22P02 and aborts the reader's transaction. The Go copy is fenced against
Postgres itself by `TestActorResolve_UUIDGateMatchesUUIDIn`. Change one, change all four.

## 7. The one predicate no index here serves — and the rule behind it

**The rule is not "payload predicates are heap Filters". It is: under `FORCE ROW LEVEL
SECURITY`, no non-`LEAKPROOF` operator can be pushed into an Index Cond.** Postgres refuses
because a leaky operator could reveal, through an error message or a timing difference, the
contents of a row the RLS policy would have hidden. This is the single most reusable fact
AUDIT-04 produced: it decides, before you design a query, whether an index can help at all.

**The control.** RLS is the variable, not the operator. Measured on a 20,000-row scratch table
built for this — 20 tenants, an index on `(tenant_id, (payload->>'id'))`, one policy, the whole
thing inside a rolled-back transaction — the SAME table and the SAME index behave differently
depending on nothing but `FORCE`:

    A. FORCE ROW LEVEL SECURITY on (the owner is subject to it)
       Bitmap Heap Scan on leak_ctl (actual rows=0)
         Recheck Cond: (tenant_id = current_setting('app.current_tenant')::uuid)
         Filter: ((payload ->> 'id') = 'k4001')
         Rows Removed by Filter: 1000
         Heap Blocks: exact=187
         ->  Bitmap Index Scan on leak_ctl_tenant_pid_idx (actual rows=1000)
               Index Cond: (tenant_id = current_setting('app.current_tenant')::uuid)

    B. same table, same index, NO FORCE
       Index Scan using leak_ctl_tenant_pid_idx on leak_ctl (actual rows=1)
         Index Cond: ((payload ->> 'id') = 'k4001')

One row versus a thousand sifted through 187 heap blocks. The index was never the problem, and
building a *better* index would not have helped: Postgres declines to evaluate a leaky operator
before the RLS qual has excluded the row.

`pg_proc` says which operators this reaches:

| `proname` | `proleakproof` |
|---|---|
| `jsonb_object_field_text` | `false` |
| `texticlike` (`ILIKE`) | `false` |
| `ts_match_vq` (`@@`) | `false` |
| `texteq` (`=` on text) | **`true`** |
| `uuid_eq` (`=` on uuid) | **`true`** |

The bottom two rows are the escape hatch, and they are why AUDIT-04-11 worked: move the value
out of the jsonb into a real typed column and the comparison becomes leakproof, so it can be an
Index Cond again.

Two consequences follow, and only one of them is still a limit.

**Per-invoice scope is no longer on this list.** AUDIT-01 wrote it as
`payload->>'id' = $1 OR payload->>'invoice_id' = $1`, and that could only ever be a heap
Filter, because `jsonb_object_field_text` is not `LEAKPROOF`. AUDIT-04-11 replaced it with
`audit_log.invoice_id`, a **STORED generated column**, so the predicate is now a plain column
comparison. Measured:

    scoped page   -> Index Scan using audit_log_tenant_invoice_created_idx
                     Index Cond: (tenant_id = ...) AND (invoice_id = ...)
    scoped count  -> Index Only Scan, same index, same Index Cond

D-19 ("the scoped predicate is a heap Filter by design; a no-Seq-Scan assertion is vacuous on
it") described the old form and no longer holds. `TestAudit_ScopedInvoiceReadUsesTheInvoiceIndex`
pins the new one strictly and asserts the plan never touches `payload`.

**Free-text search is the one that remains.** `texticlike` and `ts_match_vq` both report
`proleakproof = false` in `pg_proc`, so no index can serve the match under RLS. A generated
column cannot rescue this one the way it rescued the scoped read: the search term is supplied
per request, not derivable from the row.

Do not, however, write its plan test as a "no Seq Scan" assertion. Measured, that assertion
**passes** — the page is an ordinary Index Scan on `audit_log_tenant_created_idx`, because the
`ORDER BY` still rides the index and only the text match falls to the heap. It is therefore
vacuous, not merely weak. The falsifiable claims are that the `ILIKE` appears as a **Filter**
and never in the Index Cond, and that `proleakproof` is still false —
`TestAudit_SearchIsTheOnlyPredicateNoIndexCanServe` asserts both, so if either operator ever
becomes leakproof the test fails and the exception should be revisited.

**What bounds search instead: the date range, and nothing else.**
`TestAudit_SearchScanIsBoundedByTheDateRange` measures it, and it compares rows **examined**
(rows returned plus `Rows Removed by Filter`), not `actual rows` — the latter counts what
survived the filter, so a search matching nothing reports zero however much of the table it
read.

`pg_trgm` is not available to the migrator role, so no later story should propose a trigram
index without re-checking the grant first.

## 8. Still unsettled, and who owns it

Carried forward from AUDIT-01. AUDIT-04 closed none of these; it did narrow the last one.

- **The production `audit_log` row count has never been probed.** Every number on this page
  comes from a synthetic local corpus. No owner.
- **No retention or partitioning policy.** `db.PurgeDemoTenants` bounds demo-tenant rows only
  (`internal/platform/db/demopurge.go`); it is not a reason to assume the table is small. No
  owner. This is the one that decides whether §10's plan pins still hold in two years.
- **Company deletion.** If a delete handler is ever specced, that story decides whether to null
  the pointers, keep them dangling, or block the delete. Until then, §5 is the rule.
- **Search cost at scale.** §7 establishes that no index can serve it under RLS and that the
  date range is the only bound. D-18 measured the ceiling at roughly **half a second** on the
  200,000-row single-tenant corpus and accepted it. What nobody owns is a decision about what
  happens when a workspace's log is large enough for that to matter — a mandatory date range,
  a lower cap, or accepting it at whatever it then costs. Re-measure before assuming 0.5 s
  still describes it.

## 9. Actor resolution: `internal/actor`

Every stored subject — `audit_log.actor`, `invoice_status_history.actor`, and the columns
listed under "Still raw" below — renders through one ladder: `display_name` → `email` → the
raw subject. `internal/actor` is that ladder, and it is the one the AUDIT epic reads through.
It imports stdlib and pgx only (`TestActorPackage_ImportsOnlyStdlib`), so `internal/audit`,
`internal/invoice`, `internal/approval` and `internal/tenancy` can all import it without a
cycle.

**`Name(displayName, email *string, subject string) Label`** applies the ladder to one row's
columns. **`Resolve(ctx, tx, subjects) (map[string]Label, error)`** does the lookup: it
classifies the literal `system` in Go before any query, drops every subject that fails §6's
UUID gate, de-duplicates the rest on the normalised uuid while keying the result on the raw
subject, and issues at most one statement —

```sql
SELECT user_id, display_name, email FROM memberships WHERE user_id = ANY($1::uuid[])
```

— or none at all when nothing binds. The gate is not optional: binding
`backfill-source-rows` into a `uuid[]` raises SQLSTATE 22P02 and aborts the reader's whole
transaction. Every input subject is a key in the result, and `Label.Text` is never empty for a
non-empty subject. Fenced by `TestActorResolve_EmptyFilteredSetIssuesNoQuery`,
`TestActorResolve_QueryCountIsConstantInN`, `TestActorResolve_EveryInputSubjectIsAKey`,
`TestActorResolve_TwoSpellingsOfOneIDBindOnceAndKeyTwice` and
`TestActorName_NeverReturnsEmptyText` (`internal/actor/`).

### Scope is the caller's, and only the caller's

**`Resolve` opens no transaction, sets no GUC and writes no `tenant_id` predicate**
(`internal/actor/resolve.go:40-43`). RLS on the transaction you hand it is the only filter.
A transaction with no `app.current_tenant` resolves **nothing**, and it fails **silently** —
every actor falls back to its raw subject instead of raising. A superuser or background
transaction resolves **across tenants**. Correct usage: call `Resolve` inside
`db.WithinTenantTx` or `db.WithinRequestTenantTx` on the app role, on the same transaction
that read the rows (`internal/invoice/store.go:570` is the shipped example). AUDIT-05's Go ZIP
writer is exposed to both halves: an export job running outside a tenant-scoped transaction
either loses every name or crosses tenants, and neither failure surfaces as an error.
Fenced by `TestActorResolve_UnscopedTxResolvesNothingRatherThanWrongly` and
`TestActorResolve_CrossTenantSubjectIsUnresolvable`.

### The three shapes, and the four Kinds

A stored actor is one of exactly three things, and all three are returned **verbatim** —
`Resolve` never fabricates, truncates or normalises a display value:

1. the literal `system`, written by workers and triggers, short-circuited in Go to
   `Label{"System", KindSystem}` before the UUID gate;
2. a GoTrue subject UUID, in any spelling §6 accepts — resolved against `memberships`;
3. free text: `backfill-source-rows` (`internal/importer/backfill.go:18`) and
   `revalidate-rule-set` (`internal/invoice/actor.go:54`), plus `approval_policy.published`'s
   caller subject, which can be an email (see `docs/approvals.md` §b). These never bind.

Go `Kind` has three values — `KindSystem`, `KindPerson` (a `memberships` row answered),
`KindRaw` (free text, or a uuid nothing can name). The client adds a fourth, `absent`, for a
**NULL** column, rendered "Not recorded" (`frontend/app/src/lib/actor.ts:3,24`). `absent` has
no Go counterpart: `Resolve` is never handed a NULL.

### Resolution is read-time, and batched once

The name a row displays is the member's name **now**, not at the moment the event was
recorded. Rename a member and every historical `audit_log` row they actored re-renders under
the new name; **delete** the membership and those rows fall back to the raw subject, because
no row answers. A suspended member still resolves
(`TestActorResolve_SuspendedMemberStillResolves`), and a member of another tenant never does
(`TestActorResolve_CrossTenantSubjectIsUnresolvable`). If a reader needs name-at-event-time it
must store the name in the payload at write time; `internal/actor` cannot give it.

Collect every subject on the page and call `Resolve` **once**, after the row scan — never once
per row. The de-duplication is stronger than a caller can apply to raw strings, and the query
count does not grow with the row count (`internal/invoice/store.go:565-575`,
`TestHistory_IssuesOneResolveQueryForManyRows`). A per-row call on AUDIT-04's list endpoint
would be N+1 against `memberships`; avoiding that is the caller's job, and the batch API is
the whole reason `Resolve` takes a slice.

### `actor` stays raw on the wire

The resolved fields are added **beside** the stored value, never in place of it:
`StatusChange` carries `actor`, `actor_name` and `actor_kind`
(`internal/invoice/invoice.go:159-166`), and `actor` is byte-identical to the column
(`TestHistory_ActorColumnIsUnchanged`). AUDIT-04's actor facet and actor filter therefore
operate on the stored value, not on a name. Both resolved fields are non-pointer strings, so
neither can marshal as JSON `null`; the client reads an empty `actor_name` as "the server did
not answer" and falls back to the subject.

### Empty string is absent — D-31, a user decision

`Name` treats a non-nil pointer to `""` as **absent** and falls through at every rung, exactly
like `nil`. `internal/approval`'s `holderName` (`read_model.go:421`) does the opposite: it
stops on a non-nil `""`, mirroring TypeScript `??`.

**This divergence is deliberate. The user answered it at a gate on 2026-08-21 (D-31): fall
through, and do not absorb `holderName`.** The accepted cost is two Go ladder copies.
`TestHolderName_EmptyStringDisplayNameDoesNotFallThrough` pins the old behaviour on purpose
and `TestActorName_DivergesFromHolderNameDeliberately` pins this one. Do not consolidate them;
a future reader who "aligns" the two is reversing a decision, not fixing a bug.

### Open finding: whitespace

A `display_name` of `" "` is **not** empty, so it wins the first rung and renders as a
visually blank cell with kind `person` — at the Go layer and again at the TS layer. Nothing
constrains `memberships.display_name` to be non-blank, so this is reachable in production.
D-31 settled `""` only; whitespace was never asked. The behaviour is **pinned, not changed**,
by `TestActorName_WhitespaceOnlyDisplayNameIsNotTreatedAsAbsent`,
`TestActorResolve_WhitespaceOnlyDisplayNameIsNotTreatedAsBlank` and
`frontend/app/src/lib/actor.test.ts:186` (which sweeps space, tab, LF and NBSP). Trimming is a
deliberate change with a red test, not a silent cleanup.

### Still raw: the client fall-through

`actorLabel` called with **one** argument has no server answer to prefer, so it falls through
to `APP_PERSONAS` (`frontend/app/src/auth.ts:34-61`) — a table that is unscoped and holds both
tenants' subjects, so a colliding subject renders the **other tenant's** person by name. Four
call sites are still one-argument; they are inventoried per surface in sysmap (features 271,
252, 259, 256), and `frontend/app/src/lib/actor.test.ts:308-338` asserts the arity so the set
cannot grow silently. Those columns are not `audit_log` columns and no AUDIT story owns them.

### Who reads this

**AUDIT-04** (facets and filters — on the raw `actor`), **AUDIT-05** (ZIP export — read the
RLS paragraph first), **AUDIT-06** and **AUDIT-09**. One correction for AUDIT-05: its Core AC
carries an escape hatch reading "if not merged, this story's own resolution must produce the
same result". That clause is **dead** — the ladder is merged and importable. Import
`internal/actor`; do not build a second copy.

---

## 10. The reader's contract (AUDIT-04)

Everything below was measured while building the first reader. AUDIT-05, AUDIT-07 and
AUDIT-09 inherit it.

### 10.1 The statement budget is a range, not a constant

One request, all inside **one** `db.WithinRequestTenantTx`:

| Statements | When |
|---|---|
| 5 | always — the page, the three facet `GROUP BY`s, the count |
| +1 | the seam's own membership gate (AUDIT-10), whenever the caller's subject is a uuid. It rides `set_config` in one `pgx.Batch`, so it costs a statement but not a round trip |
| +1 | `actor.Resolve`, and only when some subject passes its uuid gate. It short-circuits `system` and any non-uuid subject in Go, so a page whose actors are all free text issues no `memberships` query **of its own** — the gate's still runs |
| +3 | free-text search only — the `memberships`, `business_entities` and `invoices` fold-ins. The third resolves a typed invoice number to invoice ids (AUDIT-11) |
| +1 | the empty probe, and only when `total == 0` |

So 5 to 11, and 6 to 11 for the real HTTP path, where the caller's subject is always a uuid.
Older drafts of this page said "six statements", which is the populated common case with
resolvable actors, not a constant.

**One transaction is the load-bearing claim, not the count.** Split across transactions the
page, the count and the facets would each see a different snapshot, so a row inserted
mid-request could be counted but not shown — a total that disagrees with the page it labels.

### 10.2 `filterPredicates` now has four consumers

The page, the count and the three facet statements all build their `WHERE` from the same
builder. **A predicate proven correct on the page is therefore not proven on the facets.** The
facets deliberately differ: each clears its *own* dimension so a facet list does not collapse
to the value already selected.

This is not theoretical. Adding `InvoiceID = ""` to the block that clears `Events`, `Actors`
and `Company` — the natural-looking edit — left the entire package green while silently
unscoping every facet. Test the facets separately from the page, always.

### 10.3 Some things only SQL text can assert

Deleting **every** facet `ORDER BY` left the whole DB-backed suite green. Postgres returns a
small `GROUP BY` in a stable order whether or not one is written, and only reshuffles when the
plan flips to a hash aggregate — so a behavioural test cannot see the loss, and a byte-identical
comparison would begin *flaking in CI* rather than failing locally.

The fix is to split *building* a statement from *executing* it, and assert the built text.
`facetStatements` and `FacetSQLForTest` exist for this and nothing else.

### 10.4 The keyset survives composition

Measured, a page filtered on date + event + actor + named company **and** carrying a cursor
produces ONE Index Scan whose Index Cond holds `tenant_id`, `entity_id`, `created_at >=` and
`ROW(created_at, id) < ROW(...)` together, with event and actor as heap Filters and **no Sort
node**. Extra `AND` terms do not demote the cursor.

What *does* demote it is guarding the cursor as `$n IS NULL OR (created_at, id) < ($n, $m)`.
Append the clause as SQL text when a cursor is present; do not parameterise its presence.

### 10.5 Exactly two shapes carry a Sort

`company=workspace` (`entity_id IS NULL`) and free-text search. Every other measured shape has
none. Any "no Sort node" assertion must exempt those two — and every **facet** plan sorts
unavoidably, because its `ORDER BY` is on `count(*)`, which no index can supply.

### 10.6 A filtered facet loses its Index Only Scan, and earlier than expected

One date filter is already enough. Unfiltered, all three facets are Index Only Scans on their
own dimension index. With a date bound the event facet keeps Index Only (the date rides that
index's trailing `created_at`) while the actor and company facets fall to a Bitmap Heap Scan.
Pin `Index Only Scan` for the unfiltered case and index-*served* for the filtered one.

### 10.7 Plan claims are only true of the corpus they were measured on

D-15 said `entity_id IS NULL` is a post-scan Filter, not an Index Cond. That was measured on a
single 200,000-row tenant. On the 20-tenant × 1,000-row corpus the plan tests actually run
against, the planner instead puts it **in** the Index Cond and adds a Sort. Both measurements
are real. The claim was stated as though it were corpus-independent, and it is not.

State the corpus with the claim. This page's earlier unqualified plan assertions are the
failure mode this note exists to prevent.

### 10.8 Free-text search matches payload VALUES, not raw payload text

Searching `payload::text` matches JSON **key names**: measured, `q=id` matched 50,000 of 50,000
rows, because `id` / `policy_id` / `invoice_id` / `user_id` / `run_id` are keys on nearly every
row. The same holds for `from`, `to`, `version`, `reason`, `key`, `name` and `steps`.

The shipped form walks `jsonb_each_text` and matches values only, and it skips one key outright:
the generic arm carries `WHERE kv.key <> 'invoice_number'` (`searchFragment`,
`internal/audit/filter.go`), so the invoice number every writer now records is the one payload
value this arm deliberately does **not** match. AUDIT-11 resolves a typed number through the
`invoices` fold-in below instead, where the company fence can reach it; matched generically it
would stay unscoped, because an OR-group only ever widens. One residual leak remains: the three
keys carrying nested structure (`fields`, `steps`, `members`) render as JSON text *inside* their
value, so their inner key names stay matchable. Per §10.7 that leak is stated without a
key-count denominator: the dev corpus's distinct-key total moves with its test debris.

The values-only form also keeps the keyset short-circuit: the ordered index scan streams and
`LIMIT` stops it early. The rejected `payload::text ILIKE` shape added a Sort over the whole
candidate set first.

`q` is exactly five OR-ed arms (`searchFragment`, `internal/audit/filter.go`): the event text,
the payload **values**, and three fold-in arms that resolve a typed string to ids first — a
display name, a company name, and an invoice number. What that covers:

| A user types | Matched? | By which arm |
|---|---|---|
| an event name or fragment — `accepted`, `invoice.` | yes | `a.event ILIKE` |
| a person's **display name** | yes | `memberships.display_name` fold-in -> `a.actor = ANY(...)` |
| a person's **email** | **no** | the fold-in reads `display_name` only. An actor whom `actor.Resolve` renders by email is not reachable by the name shown |
| a company name | yes | `business_entities.name` fold-in -> `a.entity_id = ANY(...)` |
| a uuid that appears as a payload value — invoice id, document id, policy id | yes | `jsonb_each_text` values |
| an **actor uuid**, typed in full | **no** | there is no `a.actor ILIKE` arm. It matches only if that same uuid also sits in a payload value |
| a JSON **key** name — `invoice_id`, `reason`, `version` | **no, by design** | values-only. Residual leak: `fields`, `steps`, `members` render nested JSON inside their value, so their inner keys stay matchable |
| an invoice **number** — `INV-2026-0042` | **yes** | the `invoices` fold-in -> `a.invoice_id = ANY($n::uuid[])`, never the recorded payload value: see §10.12 |
| `system`, to find system-actored rows | no | use `actor_kind=system`, which is a column predicate |

The two text arms are always emitted, and each of the three fold-in arms is dropped when its own
lookup found nothing, so the OR-group can never collapse to empty and silently widen to the
unfiltered set.

### 10.9 The cursor is opaque, not tamper-evident

`DecodeCursor` is pure syntax and accepts a cursor minted in another tenant by design. RLS is
what bounds the result, so a foreign cursor yields **the caller's own** rows from that position
— not an error, and not the other tenant's rows.

That means **isolation is structural but emptiness is not**. A test asserting that a foreign
cursor produces an empty page is asserting a property of its fixture, and will pass for the
wrong reason if the fixture changes. Assert isolation.

Replaying a cursor under a *changed* filter set is well-defined but semantically odd: the
position is a `(created_at, id)` tuple, so it still means "older than this", even though the
row it came from may not be in the new result set at all. Left as-is; a reader that cares should
mint a fresh cursor when the filters change.

### 10.10 The empty probe, and the D-20 correction

`log_is_empty` answers a question `total` cannot: is this workspace's log genuinely empty, or
did the filters empty it? So the probe applies **no filter** — a filtered probe would just
re-answer `total`.

It runs only when `total == 0`. On a populated request it is dead work, and skipping it is what
keeps the common path at five statements (§10.1).

    SELECT 1 FROM audit_log ORDER BY created_at DESC, id DESC LIMIT 1

**D-20 said `EXISTS` defeats the index here. Re-measured, that is false**: the unordered form
and `SELECT EXISTS` are both index-served too. The `ORDER BY` form is a choice — it is the one
shape whose index use is obvious from reading it — not the only fast one. Do not cite D-20.

The probe runs **inside the page's transaction**, not a new one, so it reports on the same
snapshot the empty page was drawn from. `TestAuditStore_EmptyProbeSharesThePageTransaction`
counts one `begin` and one `commit` across the whole request and holds that.

### 10.11 `id` is a string on the wire

`audit_log.id` is a `bigint`. A JSON number above 2^53 loses precision in every JavaScript
client, so `Query` formats it with `strconv.FormatInt`. Do not "fix" it to a number.

### 10.12 Writers record the invoice NUMBER; the search does not read it

Every invoice-scoped writer puts `invoice_number` in its audit payload (AUDIT-11), alongside the
invoice *id* payloads already carried. That is what makes an audit row self-describing: an
expanded row can name the invoice it is about without a second lookup, and it names it as the
invoice was named when the row was written.

Free-text search does **not** reach the row that way. The generic payload arm excludes
`invoice_number` (§10.8), and a typed number is resolved through the live `invoices` table into
`a.invoice_id = ANY(...)` instead, so the match is fenced by the caller's company exactly like
every other scoped read.

The consequence is that the recorded key renders **then** while the search renders **now**. A
renamed invoice strands the rows carrying its old number — the new number finds them, the old one
does not — and a deleted invoice cannot be resolved at all, so its rows stop being findable by
number even though their payloads still carry it. No production path renames an invoice number
today (D-15), so this is latent rather than observed. The recorded key's role is display and
provenance, not search.

### 10.13 The write side: the writer set is enumerated, not hand-maintained

An invoice-scoped writer records `invoice_number` in its payload (above), beside whichever id
key it already carried. Which writers count as invoice-scoped is not a second list someone has
to keep in sync — it is **enumerated** by the same expression that backs `audit_log.invoice_id`
(§11): the 17 events its generated column dispatches on are the entire set, no more and no
fewer. A writer for an event outside that set has nothing to enumerate against, because it is
not invoice-scoped by definition.

`TestRLS_EveryInvoiceScopedWriterCarriesTheNumber`
(`internal/platform/db/audit_number_scan_test.go`) is the scan that keeps this true: it derives
the 17-event set from the migration itself, walks every `audit.Record` call site under `cmd/`
and `internal/`, and fails if a literal-event writer inside that set is missing the key.

## 11. The invoice-scoped read reaches 17 of the 36 event types

`Filter.InvoiceID` emits exactly one predicate — `a.invoice_id = $n::uuid`
(`internal/audit/filter.go`) — against the `STORED` generated column AUDIT-04-11 added in
`migrations/20260822080722_audit_log_invoice_id_column_and_index.sql`. That column dispatches
on the **event name**, never on which payload key happens to be present, for the same reason
`audit_log_entity_for` does (§3): several non-invoice families carry a real invoice id under
the bare `id` key.

So the set of rows an invoice's own page can ever show is not "every event that mentions this
invoice". It is exactly the two `event IN (…)` lists inside that generation expression: **ten
events whose id is read from `payload->>'id'`, seven from `payload->>'invoice_id'` —
seventeen of the thirty-six event types the log carries.**
`TestAuditScopeOf_RuleSetsAreDisjointAndSumToThirtySix` pins the 36.
**Derive this list from the migration, never from prose — this page included.**

| Domain | Count | Events | Payload key |
|---|---|---|---|
| Invoices | 8 | `invoice.created`, `invoice.updated`, `invoice.transitioned`, `invoice.validated`, `invoice.kept_as_is`, `invoice.unkept_as_is`, `invoice.resolved_outside`, `invoice.unresolved_outside` | `id` |
| Approvals | 4 | `invoice.approval_armed`, `invoice.approval_cancelled` | `id` |
| | | `invoice.approval_approved`, `invoice.approval_rejected` | `invoice_id` |
| Submissions | 3 | `submission.accepted`, `submission.rejected`, `submission.failed` | `invoice_id` |
| Reconciliation | 2 | `reconciliation.drift_detected`, `reconciliation.auto_fixed` | `invoice_id` |

The approvals domain is the one that straddles the two branches: an *armed* or *cancelled* run
spells the invoice `id`, an *approved* or *rejected* decision spells it `invoice_id`. A reader
that assumes one spelling per domain loses half of them.

**The scoped predicate reaches into no jsonb, and that is what makes it fast.** It compares a
column, so §7's rule — under `FORCE ROW LEVEL SECURITY` no non-`LEAKPROOF` operator can be
pushed into an Index Cond — does not bite. `audit_log_tenant_invoice_created_idx` serves it as
an Index Cond, unlike every payload filter on this table.
`TestAuditFilter_ScopedPredicateTouchesNoPayloadExpression` holds the no-jsonb half.

**`document.*` is outside the set by design.** The three document events — `document.created`,
`document.reused`, `document.read` — are in neither branch, so a document uploaded against an
invoice never appears in that invoice's scoped read. `document.created` carries the invoice's
real id under `id`, spelled byte for byte like `invoice.created`'s, which is precisely why the
dispatch is on the event name and not the key. §3 and §5 record the same fact from the
`entity_id` side, including the measurement that every payload-`id` row resolving NULL was a
`document.created`; nothing here restates that reasoning.
`TestAuditScoped_EventGateExcludesACollidingID` asserts the exclusion, running
`document.created` and `portfolio.entity.updated` as the two colliding families with a genuine
`invoice.created` row as the control needle — without which every case would pass by returning
nothing at all.

Two consequences for anyone building on this.

A per-domain breakdown of an invoice's feed has a Documents bucket that is **structurally
zero**, not empty-by-coincidence. Say so on the surface. AUDIT-09's activity card ships that
chip permanently inert with a visible reason for exactly this, so a reader does not file the
zero as a bug.

Widening the set is a **second migration on `audit_log`**, not a query change. The column is
`GENERATED ALWAYS … STORED` and inlined rather than factored into a function, deliberately, so
that the expression is frozen in `pg_attrdef` and cannot be `CREATE OR REPLACE`d out from under
the rows already stored — the migration's own header records why. Adding an event type to the
scoped read therefore means a new `ALTER TABLE` and a table rewrite, subject to the same
500,000-row in-migration ceiling that migration guards with.
