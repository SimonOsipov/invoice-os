# The Evidence Bundle Contract

**Audience:** anyone building against the evidence bundle — AUDIT-08 (the download drawer)
first, and any future story that touches `internal/archive`. AUDIT-05 shipped the package
(subtasks 01–10, story `M5-07`, sysmap group `audit-archive`) and the full reasoning is in
that story's `## Decisions`; this page is the contract those readers inherit — the two routes,
the ZIP a download produces, and the query shapes it depends on — so **this is the file
AUDIT-08 reads instead of the story.** Everything below was measured in this worktree against
PostgreSQL 18.6 on port 5434, or read directly from `internal/archive`'s committed Go source.

---

## 1. Overview

`internal/archive` is a leaf package, exposing one streaming assembler and two HTTP handlers,
mounted on the existing **invoice** service. No new service, no new binary dependency, no
migration — every source table, column and index it reads shipped months before this story.

```
HTTP GET /api/invoice/v1/evidence-bundle?entity_id=&from=&to=
   │
   gateway (JWT → tenant, authorize(): tenant required, no role gate)
   │
cmd/invoice  app.Mux  "GET /v1/evidence-bundle"
   │
internal/archive.DownloadHandler
   │  parse+validate (400 before a byte is written)
   │  db.WithinRequestTenantTxOpts, REPEATABLE READ READ ONLY — one snapshot (§6)
   │     ├─ selectEntity(entity_id)               → name/TIN, or ErrEntityNotFound → 404
   │     ├─ selectInvoices(entity, from, to)      → invoices.csv         (+ id list, in memory)
   │     ├─ selectHistory(ids, chunked)           → status_history.csv   (+ internal/actor.Resolve)
   │     ├─ selectSubmissions(ids, chunked)       → submissions.csv      (poll_ref lives here, once)
   │     ├─ selectExchange(ids, chunked)          → exchange.csv         (+ submission.ScrubHeaders)
   │     ├─ per exchange row with a body          → bodies/<id>.request|.response (verbatim)
   │     └─ manifest.json                          (entry list + SHA-256 + counts)
   │
   archive/zip.Writer → http.ResponseWriter (non-seekable; measured OK, ZIP64 auto)
```

Both routes are mounted beside `GET /v1/audit-log` in `cmd/invoice/main.go` and share one
`archive.Store`. The gateway routes on the first URL segment under `/api/`, so reaching this
service needed no gateway route change — only the CORS expose grant in §2.4.

## 2. The two routes

Both `GET`. Identity is checked **first**, before any query parameter is read, so an
unauthenticated caller cannot learn which parameters exist by watching 400s. Parameter
parsing then runs fully before either route touches the database.

### 2.1 `GET /v1/evidence-bundle` — the bundle

| | |
|---|---|
| Gateway path | `GET /api/invoice/v1/evidence-bundle` |
| Auth | bearer JWT; `authorize()` requires a tenant and nothing else — **no role gate**. Readable by every workspace member. |
| `entity_id` | **required**, well-formed uuid. One company — not "the active entity". |
| `from` | **required**, RFC3339. Inclusive. |
| `to` | **required**, RFC3339. Inclusive. |
| 200 | `Content-Type: application/zip`, `X-Content-Type-Options: nosniff`, `Content-Disposition: attachment; filename=…` (see §2.3 for the exact rendering), **no `Content-Length`** — see the framing note below. |
| 400 | `{"error":"<message>"}` — malformed/absent param, `from` after `to`, or the invoice count over the cap (§5). Every 400 is raised **before the first ZIP byte**. |
| 401 | `{"error":"unauthorized"}` — no identity, or the store's `db.ErrNoTenant`. |
| **404** | `{"error":"not found"}` — no **visible** `business_entities` row matches `entity_id`. Under FORCE RLS, "does not exist" and "belongs to another tenant" are the same answer, deliberately. |
| 500 | `{"error":"internal server error"}` — only reachable before streaming starts. |
| mid-stream failure | the response is **abandoned without a ZIP central directory** — see §7. |

**The `Content-Length` framing.** The handler declares no length. Measured on this worktree's
toolchain against a real `httptest.NewServer` (so Go's own transport decides the framing, not
a recorder): net/http back-fills `Content-Length` itself for a body of 2048 bytes or fewer, and
switches to chunked transfer at 2049 bytes and above; an explicit `Flush()` forces chunked at
any size. So the honest contract is **"the handler declares no length; net/http frames the
body, and for any bundle of real size that means `Transfer-Encoding: chunked`"** — not "the
wire never carries a length".

### 2.2 `GET /v1/evidence-bundle/preview` — what the drawer states before downloading

Same params, same auth, same 400/401/404 rules as §2.1, run through the identical
`parseRequest`. Returns:

```json
{
  "entity": { "id": "…", "name": "Honeywell Group", "tin": "12345678-0001" },
  "period": { "from": "2026-01-01T00:00:00Z", "to": "2026-03-31T23:59:59Z",
              "bounds": "inclusive", "basis": "invoices.created_at" },
  "filename": "ASComply_evidence_Honeywell-Group_20260101_20260331.zip",
  "counts": { "invoices": 507, "status_transitions": 2028, "submissions": 507,
              "exchange_attempts": 1521, "body_files": 3042 },
  "over_limit": false
}
```

`entity.tin` is `null` when the column is NULL — never `""` (§3.2's rule extends to this
field). **No byte estimate.** `sum(octet_length(...))` over the wire bodies would detoast every
one of them — 823 MB of reads at the measured shape — purely to render a number in a drawer;
AUDIT-08's "estimated size" must be derived from the attempt count with an honest "up to"
framing, or dropped. The actual size is known once the Ready phase has the downloaded response.

`preview` reuses `manifest.json`'s own Go types (`manifestEntity`, `manifestPeriod`,
`manifestCounts`), so the two descriptions of one bundle cannot drift apart independently of
each other.

### 2.3 The filename algorithm

Both routes compute the same string, so AUDIT-08's client-side copy can be **derived**, not
guessed:

```
ASComply_evidence_<slug>_<fromYYYYMMDD>_<toYYYYMMDD>.zip

slug  = business_entities.name, every run of [^A-Za-z0-9] replaced by "-",
        leading/trailing "-" trimmed, truncated to 48 bytes, case preserved;
        an empty result falls back to the entity uuid.
dates = UTC.

e.g.  ASComply_evidence_Honeywell-Group_20260101_20260331.zip
```

Source: `bundleFilename` in `internal/archive/request.go`.

**`Content-Disposition` is rendered with `mime.FormatMediaType`, which leaves this filename
alphabet UNQUOTED.** `bundleFilename`'s output characters are all RFC 2045 token characters
(`[A-Za-z0-9_.-]`), so the header reads `attachment; filename=ASComply_evidence_…zip` — no
quotes around the name. **AUDIT-08's client-side mirror must not parse the header with a
`filename="([^"]+)"` regex** — it will not match the normal case. `FormatMediaType` only quotes
when a name contains a tspecial (`:`, a space, …); the one measured way to reach that is an
`entity_id` supplied in `urn:uuid:…` form against an entity whose name has no alphanumeric
character, which falls back to the raw uuid string and picks up the `:`. This is handled by
construction, not by a special case: `FormatMediaType` is safe for both shapes.

### 2.4 The CORS expose grant

`Access-Control-Expose-Headers: Content-Disposition` is set in
`internal/gateway/cors.go`, inside the existing `if originAllowed` block. Without it a browser
`fetch` from the app SPA cannot read `Content-Disposition` on a cross-origin response, so
AUDIT-08's toast could not state the filename. **Blast radius:** this grant applies to every
gateway response to an allowed origin — every routed service under `/api/`, not only this
route — because the gateway builds one `withCORS` wrapper and applies it once. It grants
readability of one already-public header and nothing else.

## 3. The ZIP layout

`archive/zip` writes to a non-seekable writer and emits ZIP64 automatically; 70,000 entries
round-trip correctly (measured). **Entries are written in this order** — corrected from an
earlier draft of this contract, because `archive/zip` allows exactly one open entry at a time
(`Writer.prepare` force-closes the previous entry on every `Create` call) and bodies stream
live during the exchange loop while `exchange.csv`'s own entry is deferred to that loop's end:

```
invoices.csv
status_history.csv
submissions.csv
bodies/<exchange_id>.request        (only when request_body IS NOT NULL, interleaved in row order)
bodies/<exchange_id>.response       (only when response_body IS NOT NULL, interleaved in row order)
exchange.csv                        ← bodies precede this entry, not the reverse
manifest.json                       ← always LAST: it carries the SHA-256 of every entry above
```

No acceptance criterion depends on the relative order of the first six entries — only
"`manifest.json` is last" is asserted. `TestBundleWriter_…` (real `bundleWriter`, not fakes)
pins this order with at least two exchange rows each carrying both a request and a response
body.

Every CSV is always present, header row included, even when it holds no data rows — the
ordinary state of `submissions.csv` and `exchange.csv` for a firm whose invoices are all still
`draft` or `validated`.

### 3.1 Declared CSV headers

These header rows **are** the contract. A test parses each CSV back and asserts its first
record equals the constant the writer used. Copied verbatim from the Go source below — do not
retype these by hand if the source ever changes them.

**`invoices.csv`** — `invoicesCSVHeader`, `internal/archive/invoices.go`:
```
invoice_id,invoice_number,status,issue_date,currency,subtotal,vat,total,
supplier_tin,supplier_name,buyer_tin,buyer_name,irn,csid,qr_payload,
rejection_reasons,created_at
```
Ordered by `created_at, id`. 17 columns.

**`status_history.csv`** — `historyCSVHeader`, `internal/archive/history.go`:
```
invoice_id,invoice_number,seq,from_status,to_status,actor_name,actor_kind,changed_at
```
Ordered by `invoice_id, changed_at, id`; `seq` is 1-based within the invoice. **There is no
raw-subject column** — `actor_name` carries the resolved name, or the raw subject verbatim with
`actor_kind: raw` when the ladder cannot name someone (`internal/actor`, see
`docs/audit-log-read-contract.md` §9).

**`submissions.csv`** — `submissionsCSVHeader`, `internal/archive/submissions.go`:
```
invoice_id,invoice_number,submission_job_id,idempotency_key,state,attempts,
adapter,adapter_version,poll_ref,last_error,created_at,updated_at
```
Ordered by `invoice_id, created_at, id`. One row per `submission_jobs` row.
**`poll_ref` appears here and nowhere else in any CSV.**

**`exchange.csv`** — `exchangeCSVHeader`, `internal/archive/exchange.go`:
```
invoice_id,invoice_number,submission_job_id,exchange_id,operation,outcome,attempt,
http_status,latency_ms,truncated,encoding_coerced,request_headers,response_headers,
request_body_file,response_body_file,adapter,adapter_version,occurred_at
```
Ordered by `invoice_id, occurred_at, id`. `operation` ∈ `submit|poll`. `outcome` ∈
`sent|blocked_rate_limit|skipped_already_cleared|transform_failed|connection_failed`
(**five** values). `request_headers`/`response_headers` are compact JSON objects, re-scrubbed
through `submission.ScrubHeaders` on the way **out**, regardless of what write time already
stored — so no header outside its twelve-name allowlist can appear here even if a row arrived
by another route (a superuser insert, a fixture, a future adapter writing directly).
`*_body_file` names the `bodies/…` entry, or is empty when the source column is NULL.

### 3.2 NULL versus empty — the empty-cell rule

CSV cannot distinguish NULL from `""`. It does not have to. `irn`, `csid` and `qr_payload` each
carry an `IS NULL OR char_length(x) > 0` CHECK constraint — verified live in this worktree, and
present in the migration that added the columns:

```sql
-- migrations/20260722083015_invoices_fiscal_outcome.sql:58-60
ADD COLUMN irn               text  CHECK (irn        IS NULL OR char_length(irn)        > 0),
ADD COLUMN csid              text  CHECK (csid       IS NULL OR char_length(csid)       > 0),
ADD COLUMN qr_payload        text  CHECK (qr_payload IS NULL OR char_length(qr_payload) > 0),
```

A stored value in those three columns is therefore **never** the empty string, so an empty cell
in `irn`, `csid` or `qr_payload` can only mean "absent" — never "present but blank". The rule is
uniform across every CSV in the bundle: **an empty cell means the source column was NULL**, and
`manifest.json` states this in its `notes`. The same rule governs the preview's `entity.tin`,
which marshals as JSON `null` rather than `""`.

Two things make this durable rather than a comment: the writer (`emptyIfNil` /
`emptyIntIfNil`, `internal/archive/invoices.go`) never emits a quoted `""` where it means
absent, and a DB-backed test asserts the three CHECKs still exist — so a dropped CHECK is
caught by that test, not by a reviewer reading SQL.

### 3.3 `manifest.json`

```json
{
  "format": "ascomply-evidence-bundle/1",
  "generated_at": "2026-08-22T09:14:02Z",
  "generated_by": { "name": "Adaeze Okafor", "kind": "person" },
  "tenant_id": "…",
  "entity": { "id": "…", "name": "…", "tin": "…" },
  "period": { "from": "…", "to": "…", "bounds": "inclusive", "basis": "invoices.created_at" },
  "counts": { "invoices": 507, "status_transitions": 2028, "submissions": 507,
              "exchange_attempts": 1521, "body_files": 3042 },
  "entries": [ { "name": "invoices.csv", "bytes": 91234, "sha256": "…", "rows": 507 } ],
  "notes": [
    "An empty CSV cell means the source column was NULL.",
    "irn, csid and qr_payload cannot hold an empty string (database CHECK), so an empty cell there means absent.",
    "Body files are the bytes recorded at transmission time, verbatim. A row whose truncated or encoding_coerced flag is true carries a body that is not the complete wire bytes.",
    "Request and response headers are limited to a twelve-name allowlist applied when the evidence was written; credential headers were never stored.",
    "This manifest lists a SHA-256 checksum for each entry, for self-verification. It is not a cryptographic signature."
  ]
}
```

`entries[].rows` is a non-nil pointer, even to `0`, for every CSV; it is absent (omitted) for a
body file, which has no row count. `generated_by` is the acting user, resolved through the same
`internal/actor.Resolve` function `status_history.csv` uses — its own call, scoped to just the
acting subject — inside the same transaction.

**It is a checksum manifest, not a cryptographic signature — say so plainly in every surface
that describes it.** This repo has no signing key, no key distribution and no rotation story; a
`grep -rniE "sign|hmac|kms|private.?key|x509|rsa\.|ecdsa\."` across `internal/` and `cmd/` finds
only JWT *verification*, nothing that signs. What ships is a per-entry SHA-256 list,
self-verifiable — that is what "SHA-256 manifest" actually buys, and no field name, value or
comment in this manifest may imply more than that. **Handoff obligation for AUDIT-08:** its
`MANIFEST SIGNED` copy must become `MANIFEST · SHA-256` or equivalent — this was a design-spec
claim the assembler deliberately does not deliver on, decided at the user checkpoint of
2026-08-22 (read: an agreement to ship the unsigned digest and label it honestly, not a
license to call a digest "signed"). Real signing is a separate story with its own
key-management decisions.

## 4. Query design — measured under bound parameters

### 4.1 Entity and invoice selection

```sql
-- selectEntity: the filename's source, and the 404.
SELECT id, name, tin FROM business_entities WHERE id = $1

-- selectInvoices
SELECT id, invoice_number, status, issue_date, currency, subtotal, vat, total,
       supplier_tin, supplier_name, buyer_tin, buyer_name,
       irn, csid, qr_payload, rejection_reasons, created_at
  FROM invoices
 WHERE entity_id = $1 AND created_at >= $2 AND created_at <= $3
 ORDER BY created_at, id
```

No `WHERE tenant_id` anywhere in this package — FORCE RLS plus the `app.current_tenant` GUC is
the isolation. A caller cannot reach another tenant's entity because the row is invisible, not
because a predicate was remembered.

Measured under bound parameters at 6,000 invoices / 3,000 per entity: `Bitmap Index Scan on
invoices_entity_status_idx` for `entity_id`, with the period **and** the RLS predicate as a
post-`Filter`. No new index needed.

### 4.2 The children — `= ANY($1::uuid[])`, never a JOIN

**`= ANY` is index-served in both plan-cache states; a JOIN over `invoices` is only
index-served in one of them.** `db.NewPool` sets no `QueryExecMode`, so pgx v5.10.0 uses its
default `QueryExecModeCacheStatement` — server-side prepared statements, planned with a
**custom** plan for the first five executions on a connection and a **generic** plan
thereafter. Both states are measured below. Same corpus: 507 target invoices out of 6,000,
18,000 `app_exchange` rows, statistics current.

| shape | plan-cache state | plan | buffers | time |
|---|---|---|---|---|
| `invoice_id = ANY($1::uuid[])` | **custom** (exec 1–5) | Bitmap Index Scan on `app_exchange_tenant_invoice_idx` | 550 | 1.30 ms |
| `invoice_id = ANY($1::uuid[])` | **generic** (exec 6–7) | Index Scan on the same index | 544 | 0.66 ms |
| `JOIN invoices … WHERE entity_id/period` | **custom** (exec 1–5) | **Hash Join + Seq Scan** on `app_exchange` (18,000 rows) | 1532 | 9.43 ms |
| `JOIN invoices … WHERE entity_id/period` | **generic** (exec 6–7) | Nested Loop + Index Scan (507 index searches) | 1734 | 1.65 ms |
| `invoice_id = $1` (one invoice) | either | Index Scan on the same index | 5 | 0.018 ms |

The JOIN is not merely slower — it is *plan-cache-state dependent*, so which plan a pooled
connection runs depends on how many times that connection has already executed the statement.
`= ANY` is stable and cheapest in every state, which is why the assembler chunks ids through
`= ANY` rather than joining against `invoices` for every child table (`invoice_status_history`,
`submission_jobs`, `app_exchange`).

**Corpus-validity correction — the table above does not describe the shipped code's own
tests, and the `= ANY` row is wrong at the corpus it names.** The table was measured with
SQL-level `PREPARE … EXECUTE name(:'targets')` — a literal-substituted array. The shipped Go
code, and its own plan-shape tests, send a genuine wire-protocol bound parameter
(`tx.Query(ctx, "EXPLAIN (COSTS OFF) "+selectExchangeSQL, ids)`), and Postgres's planner does
not treat the two identically. Reproduced independently in this worktree: rebuilding the exact
6,000-invoice / 18,000-row / 507-target corpus above, with `ANALYZE` current, and EXPLAIN-ing
through a real bound parameter — matching the shipped test's own mechanism — yields **Seq Scan
on `app_exchange` in both `force_custom_plan` and `force_generic_plan`**, not the Bitmap Index
Scan / Index Scan the table claims.

What actually governs the plan is **selectivity** (targets ÷ total tenant rows), not scale or
body size: at 6,000 invoices / 507 targets, selectivity is 8.45% — too dense for an index scan
to earn back its own overhead, so Seq Scan is the *correct* planner choice, not a bug. The
shipped, PINNED corpus is **30,000 invoices / 507 targets (1.69% selectivity)** —
`TestSelectExchange_IsIndexServedInBothPlanModes` (`internal/archive/exchange_db_test.go`)
confirms an Index Scan with an `Index Cond` on `invoice_id` at that scale, in both plan-cache
modes, and is the current source of truth. The qualitative conclusion above (`= ANY` reachable
in both plan-cache states; a JOIN is not) still holds, but only at realistic selectivity — treat
the table's specific buffer/time numbers as illustrative of the mechanism, not as a claim about
any particular corpus size. The JOIN row's own numbers were never re-verified with a genuine
bound parameter at any corpus size and should be read the same way.

**The statistics caveat that makes this table true.** Every plan above was measured with
`ANALYZE` current on `app_exchange`. **Without current statistics the `invoice_id` predicate is
demoted to a post-scan `Filter` in every form above** — a statistics problem, not a query-shape
problem. The EXPLAIN below is from the same superseded 6,000-row corpus as the table (its
`Rows Removed by Filter: 16479` is `18000 − 1521`); the underlying mechanism — stale statistics
demote `invoice_id` off the index regardless of corpus size — is independently confirmed at the
current, pinned 30,000-invoice scale by `TestSelectExchange_IndexAssertionIsNotVacuous`:

```
Bitmap Heap Scan on app_exchange  (cost=10.26..298.21 rows=83)
  Recheck Cond: (tenant_id = …)                       ← only the RLS qual reaches the index
  Filter: (invoice_id = ANY ('{…}'))                  ← demoted
  Rows Removed by Filter: 16479                       ← 18000 − 1521
  ->  Bitmap Index Scan on app_exchange_tenant_job_idx  ← the WRONG index
```

Nothing in the query design can prevent that; only current statistics can. **This is a live,
bounded, ownerless production residual.** Every environment is reset and re-seeded at boot
(bootstrap → migrate → reset → purge → seed), leaving `app_exchange` at whatever statistics it
last had, and nothing in that boot sequence runs `ANALYZE`. Consequence is bounded — results
stay correct, only the plan is bad, and autovacuum closes the gap on its own within roughly a
minute of deploy plus however long a low-write table takes to cross its analyze threshold
(`threshold=50`, `scale_factor=0.1`, `naptime=60`). Not fixed by this story: adding an
`ANALYZE` to the boot/seed path is out of this story's scope and belongs to whoever owns
`reset.go` / `demopurge.go` / `bootstrap.go`. **No owner is assigned to this residual.**

Separately, and specific to this repository's operational role: `ANALYZE` run **as
`invoice_app`** is refused with a WARNING and silently skipped, because `invoice_app` is a
`NOBYPASSRLS` non-owner role and only a table owner or superuser may `ANALYZE` a table. This
silently invalidated a query-plan measurement earlier in this story's work — a re-run "to
refresh statistics" that used the app role's connection string did nothing, and the
stale-statistics plan persisted until the `ANALYZE` was re-run against the superuser
connection. Anyone re-measuring a plan in this package must run `ANALYZE` as the migrator or
superuser role, never as `invoice_app`, and should not trust a silent no-op WARNING to mean
success.

Chunking is also load-bearing for a second reason: at all 6,000 ids in one array the planner
reverts to a Seq Scan (18,000 rows, 1637 buffers) — correctly, since selectivity is 100%. The
`chunk(ids, 500)` helper keeps each query selective.

`invoice_status_history` has no `(tenant_id, invoice_id)` index — only `(invoice_id)` and
`(tenant_id)`. `= ANY` reaches `invoice_status_history_invoice_id_idx` with the RLS predicate as
a recheck `Filter` (141 buffers, 1.74 ms for 2,028 rows). Acceptable; no new index.

### 4.3 Actor resolution

`internal/actor` (shipped with AUDIT-02) is reused, not reimplemented. Per chunk, the assembler
collects the distinct `actor` strings from that chunk's `invoice_status_history` rows and calls
`actor.Resolve(ctx, tx, subjects)` **once** — Resolve is a single `user_id = ANY($1::uuid[])`
over `memberships`, scoped by RLS alone, so it must run on the assembler's own transaction. See
`docs/audit-log-read-contract.md` §9 for the ladder's full contract (three shapes, four
`Kind`s, empty-string-is-absent, whitespace-is-not-absent) — nothing in this package changes
that contract; it is a pure consumer.

## 5. Streaming, memory and the cap

`SET COMPRESSION lz4` on the two body columns is TOAST-level and transparent to `SELECT` —
there is no decompression step to write. What is real is the size: 6,000 invoices → 18,000
evidence rows → 823 MB of raw body text, in a table that is itself only 15 MB. An assembler
that buffered would need that 823 MB resident. This design never holds more than one row's
bodies: one transaction spans the whole stream so every CSV sees one snapshot (§6); `pgx.Rows`
streams and nothing calls `CollectRows`; each body is copied
`row → io.MultiWriter(zipEntry, sha256)` and released; only the invoice-id slice and the
running manifest entry list persist for the request.

**Invoice cap: 10,000**, a field on `Store` (`maxInvoices`), counted with `countInvoices` before
the first byte of the ZIP is written. At the measured shape that is roughly 30,000 evidence
rows and ~1.4 GB of raw bodies through deflate, inside one transaction.

**The over-limit behaviour is asymmetric by design, and both sides are deliberate:**

- **The download refuses.** Over the cap, `Store.Assemble` returns `*TooManyInvoicesError`
  before `newBundleWriter` is ever constructed, and `DownloadHandler` renders it as
  `400 {"error":"<count> invoices exceeds the bundle limit of <limit>"}` — the count and the
  limit are read from the error's typed fields, never from `Error()`'s string, so the
  package-internal `archive: ` prefix never reaches the wire.
- **The preview does not refuse.** `Store.Preview` always computes the real counts and returns
  `200` with `"over_limit": true` when `len(ids) > maxInvoices`. This is how a caller learns a
  bundle would be refused *before* clicking download, without the preview needing its own
  error path or short-circuit — the child counts are computed unconditionally, cap or no cap.

## 6. The transaction guarantee — REPEATABLE READ, READ ONLY

**`db.WithinRequestTenantTx` alone does NOT give one consistent snapshot.** The helper calls a
bare `pool.Begin(ctx)` with no `pgx.TxOptions`, and this server's
`default_transaction_isolation` is `read committed`. Under READ COMMITTED, **each statement
inside one transaction takes its own snapshot** — one transaction buys atomicity between
statements, not a consistent read across them.

Measured with the real pgx driver against the live dev DB, not reasoned: inside one
`WithinRequestTenantTx`, a first statement counted 0 rows, a second connection committed an
insert, and a second statement in the **same** transaction then counted 1. Applied to this
package, that gap could produce a bundle where `exchange.csv` shows an `app_exchange` attempt
for an invoice whose `invoices.csv` row still reads `validated` — an internally inconsistent
artifact handed to a regulator. The same experiment, repeated against the preview's five counts
with a concurrent commit landing between the id read and the child counts, moved
`exchange_attempts` from 1 to 2 under READ COMMITTED — the preview is exactly as exposed as the
download would have been.

**Fix: both routes run under `pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode:
pgx.ReadOnly}`** (`bundleTxOptions`, `internal/archive/assemble.go`), via a new
`WithinTenantTxOpts` / `WithinRequestTenantTxOpts` pair in `internal/platform/db` that takes
`pgx.TxOptions` and calls `pool.BeginTx`; the two pre-existing helpers become one-line
delegations passing the zero value, so every other caller in the repo is unaffected —
`pool.Begin` and `pool.BeginTx` with a zero-value `TxOptions` render the identical `begin` on
the wire.

Measured for that exact option pair: `transaction_isolation` reads back `repeatable read` and
`transaction_read_only` reads back `on`; the tenant-scoping helper's own `set_config` call is
**accepted** under READ ONLY (a local GUC is not a data write); the drift reproduced above is
gone (0 rows before a concurrent commit, 0 rows after, in the same transaction); and a write
attempt inside the transaction is refused with `SQLSTATE 25006`
(`cannot execute UPDATE in a read-only transaction`). READ ONLY is not decoration — the
assembler and the preview must never write, and this makes the database enforce that rather
than a comment.

**Why REPEATABLE READ and not SERIALIZABLE:** a serialization failure (`40001`) is raised only
on a write conflict, and a read-only transaction has none, so `40001` is unreachable here.
SERIALIZABLE would additionally raise on read anomalies with no corresponding benefit for a
pure reader, which is why it was not chosen.

**Named, accepted residual, no owner.** A REPEATABLE READ transaction holds its snapshot — and
therefore the `xmin` horizon — for the whole request, where READ COMMITTED releases it between
statements. A bundle at the 10,000-invoice cap keeps autovacuum from reclaiming dead tuples
database-wide for the duration of that request. The cap bounds the window; the exposure is the
same shape as any `pg_dump`. This repo sets no `statement_timeout`, so nothing else bounds it.

## 7. Error handling

Three regimes, split by whether a response byte has been written yet.

**Before the first byte** — parameter validation, identity, `selectEntity`, and the invoice
count against the cap. Every failure here is an honest 400/401/404/500 JSON body.

**After the first byte** — the status line is already 200 and cannot be withdrawn. On any error
after this point, the assembler returns without calling `bw.Close()`, so the response ends with
no ZIP central directory. Every ZIP reader rejects such a stream. A truncated-but-valid archive
that silently omits half a regulator's evidence is the failure this design prevents; a file
that refuses to open is loud instead. `bundleSink`'s `wrote` flag (`internal/archive/handlers.go`)
is the exact oracle the handler uses to decide between "still an honest JSON error" and "log
only, append nothing" — the same oracle applies identically to a mid-stream failure on the
download route (the preview route never streams, so this regime does not apply to it).

The transaction rolls back on the same path in both cases. Nothing was written by either route,
so rollback costs nothing.

## 8. Handoff obligations for AUDIT-08

These are places where this contract requires a specific client-side behaviour, collected here
so a client implementer does not have to re-derive them from the sections above:

1. **Filename parsing.** Derive the filename string yourself from §2.3's algorithm, or read it
   off `preview.filename` / the download's `Content-Disposition`. Do **not** parse
   `Content-Disposition` with a `filename="([^"]+)"` regex — the normal case is unquoted (§2.3).
2. **Manifest copy.** Render `MANIFEST · SHA-256` or equivalent — never `MANIFEST SIGNED`. This
   manifest is a checksum list, not a cryptographic signature (§3.3).
3. **Estimated size.** There is no byte estimate in the preview response (§2.2). Either derive
   an "up to" figure from `counts.exchange_attempts`, or drop the estimate from the UI.
4. **`Access-Control-Expose-Headers`** already covers `Content-Disposition` for a cross-origin
   `fetch` from the app SPA (§2.4) — no additional gateway change is needed to read the
   filename client-side.
