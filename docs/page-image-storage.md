# Page-image storage

Every **PDF** that goes through extraction is rendered to one PNG per page, and those PNGs are
what a review canvas will draw a highlighted region on. Since EXTR-09 the upload route also
accepts PNG, JPEG, WebP and DOCX; `PageStore.Ingest` reads PDFs only, so those four render zero
pages and their job dead-letters at `FailurePagesNotRendered` before the extractor ever runs
([docs/document-upload.md](document-upload.md)). This page records the four things about
them that are not obvious from the code: the render profile, the object-key scheme, where the
800-page cap comes from, and how long the objects live.

Two stores are involved and they hold different things. The **objects** — the pixels — live in
the same bucket as the source documents (`DOCUMENT_BUCKET`, Tigris on Railway), written by
`newPageSink` in `cmd/submission/main.go` over `document.ObjectStore`. The **inventory** — one
row per page, carrying the page number, the rendered pixel dimensions and the object key — lives
in `extraction_page_images`, under FORCE row-level security like every other tenant-owned table.
There is no manifest object: the row set is the manifest.

The order is deliberate. `PageStore.Ingest` renders and PUTs every page first, and only then does
`ExtractWorker.Work` open a tenant-scoped transaction and replace the document's whole row set.
So a committed row always names an object that was successfully written; the converse does not
hold, and that asymmetry is the design. A PUT that fails half way through leaves orphan objects
and no rows at all, which reads downstream as "this document has no page images" and retries
cleanly on River's next attempt. The reverse order would leave a row naming a 404, which nothing
downstream could tell from a real page.

## Render profile

**DPI 150, grayscale, PNG.** US-Letter lands on a 1275x1651 pixel grid, which is legible at full
width on any review screen. The dimensions stored on each row are the renderer's own — never
`pageWidthPt * dpi / 72`, because go-pdfium ceils that product, so 792 pt at DPI 150 is 1651 rows
and not 1650. A canvas scales a normalised bounding box by the stored numbers, so a recomputed
value drifts down the page.

Measured on a dense synthetic invoice (72 text rows plus table rules):

| DPI | format | PNG | JPEG q85 |
|---|---|---|---|
| 150 | grayscale | **113 KiB** | 952 KiB |
| 150 | RGBA | 1140 KiB | 932 KiB |
| 200 | grayscale | 484 KiB | 1431 KiB |

PNG beats JPEG by roughly 8x on grayscale line art, and DPI 180 is a compression cliff. The cost
of the choice is colour, and the reason is bandwidth rather than storage: 113 KiB against 1.1 MiB
per page matters on a Nigerian mobile connection, and at Railway's $0.015/GB-month both options
are pennies either way.

The committed test corpus renders far smaller than that, because the fixtures are sparse
three-line PDFs rather than real invoices:

| fixture | pages | render time | bytes per page |
|---|---|---|---|
| `native_invoice.pdf` | 1 | 5 ms | 11,328 |
| `native_3page.pdf` | 3 | 15 ms | 11,233 avg |
| `scanned_invoice.pdf` | 1 | 16 ms | 5,508 |
| `hybrid_invoice.pdf` | 2 | 21 ms | 8,600 avg |

Do not size anything from that table. **113 KiB per page is the figure the cap and the cost model
below are derived from**, and it is the one a real invoice page produces at this profile.

## The key scheme

    tenants/<tenant_id>/pages/<content_hash>/v1/p<NNNN>.png

Both variable segments are **server-derived**: `tenant_id` comes from the River job's own args,
never from a request, and the content hash is `sha256(document bytes)`, computed inside
`PageStore.Ingest` from the bytes it is about to render. No caller-supplied text reaches a key, which is the same property
`document.StorageKey` already has for source documents, and the two share the
`tenants/<tenant_id>/` prefix.

That prefix is load-bearing twice. It is the object-storage access-control boundary, and it is
also enforced in SQL: `extraction_page_images_key_tenant_scoped` is a CHECK that no row may store
a key outside its own tenant's prefix. Row-level security protects the row; the CHECK protects
what the row points at. The comparison is `starts_with(storage_key, 'tenants/' || tenant_id::text
|| '/')`, and `uuid::text` renders **lowercase**, so `PageKey` must never fold the case of the
tenant segment in either direction.

The other two segments carry their own weight. `v1` is the render profile: changing DPI, colour
model or format means minting `v2` keys rather than overwriting v1 objects, and it forces whoever
does that to decide explicitly what happens to the rows already pointing at v1. The page number
is zero-padded to four digits, so keys sort in page order and `p0800` — the cap — still fits.

Because the hash is of the document's bytes, the keys are stable: a River retry re-renders
identical pixels to identical keys and overwrites them. A *second* extraction job over the same
document would do the same, but no seam can mint one — see Retention below. Nothing accumulates
per attempt, and the row set is replaced whole (DELETE then INSERT in one transaction) rather
than appended to.

## The page cap

`maxPages` in `internal/extraction/pdfium.go` is **800**, and a document over it fails its job
with a stated reason rather than being silently truncated. The number is derived, not chosen:

- `ExtractWorker.Timeout` returns 10 minutes, so extraction runs under a 600-second River job
  timeout. That is a per-kind override of River's 60-second client default, so it applies to
  this kind alone.
- Reserve 120 s of it for the wazero pool's cold start (~1.1 s), the up-to-15 MiB document
  download, three database transactions and River's own bookkeeping. That leaves 480 seconds of
  render budget.
- Budget 300 ms per page on Railway: 110 ms of compute (render 10.1 + PNG encode 17.3 + text
  extraction 8.5 = 35.9 ms measured on darwin/arm64, derated 3x for a slower shared CPU) plus
  190 ms for one sequential ~113 KiB Tigris PUT inside the render loop.
- 480 s / 300 ms gives a 1,600-page ceiling with zero margin, and the cap is that ceiling
  divided by a safety factor of 2.

The 190 ms PUT term was **never measured** — there were no bucket credentials and no network in
the environment the rest of these numbers came from. That single unmeasured term is the entire
reason for the safety factor of 2, and it is why 1,600 is not the cap even though the arithmetic
allows it. If the per-page cost on Railway is ever measured and comes in above 300 ms, re-derive
the cap; do not raise it on the strength of the table below alone.

| cap | worst case | share of the 480 s budget | page images at the cap |
|---|---|---|---|
| 800 (shipped) | 240 s | 50% | 88.3 MiB |
| 1,600 | 480 s | 100%, zero margin | 177 MiB |
| 5,400 | 1,620 s | 338%, times out | 596 MiB |

The arithmetic assumes **one render per job**, and today that holds only because of what is
wired. `PDFiumExtractor.Extract` reads through the same `PageReader` with a no-op page callback,
and `PDFiumReader.Read` renders and PNG-encodes every page regardless of what that callback does
— measured: `Extract` over the three-page fixture releases three render bitmaps. So a fleet that
wires `PDFiumExtractor` beside `PageStore` renders every document twice and pays the 110 ms
compute term again, which takes an 800-page worst case from 240 s to 328 s of the 480 s budget.
Nothing wires it today: `cmd/submission/main.go` builds `MockExtractor`. Re-derive the cap, or
give the extractor a text-only read path, before that changes.

Refusing beats truncating because a timeout is not a graceful failure. River retries three times,
each attempt re-renders from scratch and causes a fresh `document.read` audit row, and the job
dead-letters after roughly 24 minutes carrying "context deadline exceeded" instead of a reason
anyone can act on. Turning that into a stated reason is the cap's whole job, so a cap with no
margin does not do it.

## Retention

Page images live as long as the source document they were rendered from. There is no TTL and no
bucket lifecycle rule. They are a derived, regenerable copy of a document that is itself kept, so
deleting them serves no data-minimisation purpose while breaking the review canvas for any older
invoice. At 113 KiB a page and ~226 KiB for a typical two-page invoice, 100,000 documents is
about $0.34/month — page images are not a cost driver at any volume this product will see for
years.

The stored objects are **never deleted by the app**: nothing in the fleet issues a delete against
the bucket, a re-render overwrites its own keys, and an abandoned key is simply orphaned. The one
carve-out is the demo tenant set, and even there only the rows go — `db.PurgeDemoTenants` empties
`extraction_page_images` on every gated boot and leaves the PNGs standing, so a purged demo
tenant's pixels remain in the bucket with no row left to name them. That is the same posture the
purge already takes for `documents` itself, and it is enumerated in
[docs/demo-reset.md](demo-reset.md).

Orphans are therefore expected in two ways, both cheap and both inherited rather than new: a
failed PUT part-way through a render, and a purged demo tenant. Neither is reachable from the
application, because reaching an object requires a row.

They are **not** recoverable by re-running extraction today. EXTR-09's `EnqueueExtraction` records
a permanent per-document key `extract:<document_id>`, so the one sanctioned seam can never mint a
second job for a document — pinned by `TestRLS_EnqueueExtractionRefusesEvenAfterTheJobDeadLetters`.
Within a job River's own retries still re-render to the same content-derived keys. Re-extraction is
EXTR-17's.

## What this does not cover

No read path exists yet. `extraction_page_images` is written and never read: nothing serves a page
image, no store method selects one, and no screen displays one. The inventory is here so that the
story which builds the review canvas has something to enumerate; until then, the strongest true
statement about the tenant isolation on these rows is that the schema enforces it, not that a
request path exercises it.
