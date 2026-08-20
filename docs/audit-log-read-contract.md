# The `audit_log` Read Contract

**Audience:** anyone building a reader over `audit_log` — **AUDIT-04** first, then
AUDIT-05 (bundle), AUDIT-07 (CSV) and AUDIT-09 (activity feed). AUDIT-01 shipped the
schema and stopped there. This page is the contract those readers inherit: what the
indexes serve, what a value in `entity_id` means, and the two predicates no index can
help. Everything here was measured on PG 18.6 in migration
`20260820150810_audit_log_entity_id_and_read_indexes.sql`; the reasoning behind each
choice is in that story's `## Decisions`.

---

## 1. `internal/audit` is write-only, and stays that way until a reader ships

`audit.Record(ctx, tx, actor, event, payload)` (`internal/audit/audit.go:39`) is the only
production writer. There is exactly one production **read** of `audit_log` in the tree —
`internal/invoice/source_document.go:78`, the uploader lookup. AUDIT-04 owns the second.
AUDIT-01 added no Go code at all.

`Record` takes no entity argument and does not need one: a `BEFORE INSERT` trigger
(`audit_log_entity_on_insert`) fills `entity_id` from the event name and payload. Callers
neither set it nor can override it.

## 2. The four indexes and the predicates they serve

Every index leads with `tenant_id` so the RLS qual becomes an Index Cond rather than a
heap Filter, and every one ends `created_at DESC, id DESC` so a keyset page is a pure
index read with no sort.

| Index | Serves |
|---|---|
| `audit_log_tenant_created_idx` `(tenant_id, created_at DESC, id DESC)` | first page `ORDER BY created_at DESC, id DESC LIMIT n`; cursor page `WHERE (created_at, id) < ($1, $2)`; date range `WHERE created_at >= $1` |
| `audit_log_tenant_event_created_idx` `(tenant_id, event, created_at DESC, id DESC)` | `WHERE event = $1`; the event facet `SELECT event, count(*) … GROUP BY event` as an Index Only Scan |
| `audit_log_tenant_actor_created_idx` `(tenant_id, actor, created_at DESC, id DESC)` | `WHERE actor = $1` |
| `audit_log_tenant_entity_created_idx` `(tenant_id, entity_id, created_at DESC, id DESC)` | `WHERE entity_id = $1`; `WHERE entity_id IS NULL`; the company facet `GROUP BY entity_id` as an Index Only Scan |

Pinned by `internal/audit/audit_plan_test.go` — seven tests over a 20-tenant corpus. The
event filter is bracketed at **1% and 30%** selectivity, so the pin is not knife-edge.
Each test asserts the index **name** and that `tenant_id` is in the Index Cond, never the
node type: a Bitmap plan over the right index is still the right index.

Use these column orders. A reader that sorts on anything but `created_at DESC, id DESC`,
or filters on a column that does not lead one of these indexes, falls back to reading the
whole tenant partition.

## 3. `entity_id IS NULL` means workspace-level — never "unknown"

This is the load-bearing semantic of the whole column.

A NULL `entity_id` is a positive claim: **this action was firm-wide**, not attributable to
any one company. Publishing an approval policy, staffing a workflow role and inviting a
member are workspace-level. Rendering NULL as "unknown", "—" or "not set" misreports what
the row says.

The resolver dispatches on the **event name**, never on which payload key happens to be
present, because three workspace-level events carry a bare `id` that is not an invoice id
and six invoice events spell it `invoice_id`. See §5 for the consequence.

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

## 7. Two predicates no index here serves

**Do not assert "no Seq Scan" and call a plan proven.** Both queries below read the whole
tenant partition as an Index Scan with a heap Filter, so a Seq-Scan assertion passes green
on them.

1. **Per-invoice scope**, `payload->>'id' = $1 OR payload->>'invoice_id' = $1`.
   `jsonb_object_field_text` is not `LEAKPROOF`, so under RLS the payload match can only
   ever be a heap Filter, never an Index Cond — the rule the 2026-08-03 partial-index
   migration already states. AUDIT-01 measured 20.2 ms against a 50,000-row tenant
   partition (P8).
2. **Free-text search**, `event ILIKE '%…%'`. Same shape, 16.6 ms on the same corpus.

A reader that needs either at scale needs a new index — an expression index on the payload
keys, or a trigram index on `event` — and that is nobody's story yet. AUDIT-09's
per-*company* feed can use `entity_id` (§4); its per-*invoice* feed cannot.

## 8. What AUDIT-01 did not settle

- **The production `audit_log` row count has never been probed.** Every number in this
  page comes from a synthetic local corpus.
- **No retention or partitioning policy.** `db.PurgeDemoTenants` bounds demo-tenant rows
  only (`internal/platform/db/demopurge.go:161-163`); it is not a reason to assume the
  table is small.
- **Company deletion.** If a delete handler is ever specced, that story decides whether to
  null the pointers, keep them dangling, or block the delete. Until then, §5 is the rule.
