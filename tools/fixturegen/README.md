# fixturegen

A deterministic generator that emits the committed synthetic invoice fixture
set under [`testdata/invoices/`](../../testdata/invoices/). The committed
CSVs are its checked-in output — **never hand-edit them**. Any drift between
a committed file and what `manifest(defaultSeed, defaultInvoices)` produces
fails `TestCommittedFixtures_MatchRegeneration` in
[`committed_test.go`](committed_test.go).

## Regenerating

```
go run ./tools/fixturegen --seed 42 --invoices 500 --out testdata/invoices
```

That is also the plain no-flags default:

| Flag | Default | Meaning |
|---|---|---|
| `--seed` | `42` | Base seed. Every committed file derives its own seed as a fixed offset from this (see `manifest` in [`main.go`](main.go)), so changing `--seed` changes every file's content, not just one. |
| `--invoices` | `500` | Base invoice count for the green files. Edge files ignore this and always use a small fixed count (`edgeInvoices = 20`, or 5 for `edge_in_file_dupes.csv` — its own builder pins that count directly) — their purpose is one specific defect shape, not dataset scale. |
| `--out` | `testdata/invoices` | Output directory. |

Determinism: identical `(seed, invoices)` always produces byte-identical
output (`math/rand`, no wall-clock, no map-iteration order in generated
bytes). Regenerating with the defaults above and diffing against
`testdata/invoices/` should show zero changes.

**Do not regenerate with different flags and commit the result.**
`TestCommittedFixtures_MatchRegeneration` in
[`committed_test.go`](committed_test.go) diffs the committed files against
`manifest(defaultSeed, defaultInvoices)` — i.e. seed 42 / invoices 500,
hardcoded in [`main.go`](main.go). Committing output from any other
`(seed, invoices)` pair fails that guard exactly like a hand-edit would; if
the fixture set genuinely needs to change shape, update `defaultSeed`/
`defaultInvoices` (or the manifest itself) in the same change.

## The fixture set

Outcomes below are what
[`internal/importer/fixtures_green_test.go`](../../internal/importer/fixtures_green_test.go)
and
[`fixtures_edge_test.go`](../../internal/importer/fixtures_edge_test.go)
assert against the real import/validate pipeline — see "Running the
verification suites" below to run them yourself against a dev DB.

| File | Shape | Intended path outcome |
|---|---|---|
| `green_500.csv` | 500 invoices x 3 line rows = 1,500 data rows | Imports and validates fully clean end to end (zero quarantine, zero rule violations). The Day-60/90 demo dataset. |
| `green_second.csv` | 250 invoices, distinct seed (`baseSeed+1`) | A second, genuinely different clean dataset (not a truncation of `green_500.csv`), for demo variety / a second entity. |
| `edge_missing_columns.csv` | Green base with the `Total` column dropped entirely | Rejected **before any write** (`ErrValidation`) — `stdMapping` maps `total` to header `"Total"`, which the file no longer has. |
| `edge_in_file_dupes.csv` | 5 invoices; one has a 4th row repeating every field except `Issue Date` | Import completes; exactly 1 invoice quarantined with a `RowError` containing `"rows disagree on issue_date"`; the other 4 commit. |
| `edge_bad_encoding.csv` | Green base (first buyer name forced non-ASCII) re-encoded as UTF-16LE **without a BOM** | Rejected **before any write** (`ErrValidation`) — the tolerant windows-1252 fallback decode mangles the header row past recognition. |
| `edge_bad_tin.csv` | One invoice's Buyer TIN mutated to 7 digits before the dash (fails `^[0-9]{8}-[0-9]{4}$`) | Imports clean (zero quarantine); validation flags exactly that one invoice with `buyer-tin-format`. |
| `edge_vat_math_wrong.csv` | One invoice's VAT forced to 0.00 (Total recomputed to `Subtotal`; lines still reconcile) | Imports clean (zero quarantine); validation flags exactly that one invoice with `vat-standard-rate` and **only** that rule across the whole batch. |
| *(oversized)* | Not committed — `buildOversized` in [`gen.go`](gen.go) synthesizes >11 MiB of green content, but only as an in-memory byte-length sanity check in the generator's own tests (`gen_test.go`'s `TestGen_OversizedInflator_ExceedsMaxUploadBytes`, no DB/HTTP). The pipeline-level test below inflates the *committed* `green_500.csv` bytes directly instead — it never calls `buildOversized`. | `fixtures_edge_test.go`'s `TestFixtures_OversizedRejected413` repeats `green_500.csv` past the importer's 10 MiB upload cap → `CreateHandler` returns 413, before `Service.Import` is ever reached (no DB required — the cap fires in `ParseMultipartForm`). |

`edge_bad_tin.csv`, `edge_vat_math_wrong.csv`, and `edge_in_file_dupes.csv`
each mutate exactly one invoice — every other invoice in that file stays
clean, so their quarantine/violation counts pin the mutation, not collateral
damage. `edge_missing_columns.csv` and `edge_bad_encoding.csv` are whole-file
structural defects (a dropped column, a broken encoding) rejected before any
single invoice is evaluated, so no per-invoice count applies to them.

## Canonical header and field mapping

```
Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price
```

Every committed file uses this exact 11-column header
(`canonicalHeader` in [`gen.go`](gen.go)). Consumers must submit this
canonical-field → header-column mapping (`stdMapping`,
[`internal/importer/service_test.go`](../../internal/importer/service_test.go)):

| Canonical field | Header column |
|---|---|
| `invoice_number` | `Invoice No` |
| `issue_date` | `Issue Date` |
| `buyer_tin` | `Buyer TIN` |
| `buyer_name` | `Buyer` |
| `currency` | `Currency` |
| `subtotal` | `Subtotal` |
| `vat` | `VAT` |
| `total` | `Total` |
| `line_description` | `Item` |
| `line_quantity` | `Qty` |
| `line_unit_price` | `Unit Price` |

## Running the verification suites

**Generator tests (no DB required)** — assert the generator's byte output
directly (determinism, shapes, canonical header, the committed set matching
regeneration):

```
go test ./tools/fixturegen/
```

**DB-backed fixture verification** — runs each committed CSV through the
real import/validate pipeline
([`internal/importer/fixtures_green_test.go`](../../internal/importer/fixtures_green_test.go)
and
[`fixtures_edge_test.go`](../../internal/importer/fixtures_edge_test.go)).
Requires a local dev DB:

```
make dev-db
# or, for a second worktree stack on a distinct port:
DEV_DB_PORT=5433 make dev-db
```

then, with the app and superuser DSNs set:

```
DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
go test ./internal/importer/ -run 'TestFixtures'
```

(Adjust the port to match `DEV_DB_PORT` if not the default 5432.) These
tests call `t.Skip` — not fail — when `DATABASE_URL` or
`DATABASE_SUPERUSER_URL` is unset, so plain `go test ./...` runs everywhere
without a DB present.

## For consumers

Fixtures live under [`testdata/invoices/`](../../testdata/invoices/).
Consumers (M4-13/M4-15/M4-16, Playwright Day-60/90 demos) should read the
committed files directly and submit them against the canonical mapping
above — they consume these fixtures rather than authoring their own.
