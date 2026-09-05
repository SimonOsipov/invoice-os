# `POST /v1/documents` — the extraction upload route

**Audience:** anyone adding a file type to the picker, or debugging why a document was
refused, stored twice, or never extracted. The route lives on the **submission** service
(`internal/extraction/handlers_upload.go`) and is reached through the gateway as
`POST /api/submission/v1/documents`. Spreadsheets are refused here — `POST /v1/imports/preview`
owns those.

Multipart, one part named `file`. Body cap 15 MiB (`maxUploadBytes`), the same bound as
`documents.size_bytes`' CHECK (`migrations/20260802163544_documents.sql`). 201 returns
`document_id`, `filename`, `size_bytes`, `content_type`, `reused`.

## Accepted types

The extension decides first; an unrecognised one falls back to the declared `Content-Type`,
matched against the **canonical spelling only**. The recorded `declared_content_type` is
always the canonical value below, never the header the client sent.

| Extension | Recorded content type |
|---|---|
| `.pdf` | `application/pdf` |
| `.docx` | `application/vnd.openxmlformats-officedocument.wordprocessingml.document` |

Anything else is a 400 reading `this file type cannot be read here`. The table is a
package-level literal because CLASSIFY-5 compares it to the picker's TypeScript copy
(`ACCEPTED_PICKED_TYPES` in `frontend/app/src/lib/importFlow.ts`); an inline switch would let
the two drift.

## Refuse before store

The ordering is the contract, pinned by `TestUploadHandler_ClassifyPrecedesStore`:

    identity 401 -> MaxBytesReader 413 -> ParseMultipartForm -> FormFile
      -> classify -> refuse 400 -> store -> enqueue -> 201

Classification sits **above** the store, inverting `POST /v1/imports/preview`, which stores
bytes it then refuses to read. A refused file therefore leaves no `documents` row and no
object in the bucket.

## The idempotency key

`EnqueueExtraction` records `extract:<document_id>` before inserting the River job, on the
caller's transaction. The key is per **document**, so:

- Re-uploading identical bytes reuses the `documents` row through
  `documents_tenant_content_hash_uq` (`reused: true`) and the enqueue is skipped — one
  extraction per document, not per upload.
- The dedupe is **permanent, not in-flight**. A document whose extraction dead-letters is
  never re-enqueued through this seam
  (`TestRLS_EnqueueExtractionRefusesEvenAfterTheJobDeadLetters`). Re-extraction is EXTR-17's.
- A dead-letter is therefore terminal for the user too, and since EXTR-15-04 it says so by name:
  the job's `failure_kind` reaches the client, which renders one of six sentences off it
  (`deadLetterRefusal`, `frontend/app/src/lib/documentRun.ts`) and offers manual entry as the way
  out. Nothing here retries.

DOCX is accepted by this route and skips page rendering: it extracts from the text layer
alone, with no page images and no field regions. With the permanent key above, it gets
exactly one attempt.

PNG, JPEG and WebP were accepted until EXTR-15-03 and are now 400s. The narrowing is by
extension and by canonical content type; an image renamed `.bin` and DECLARED
`application/pdf` still stores, exactly as a `.csv` declared `application/pdf` does
(`TestUploadHandler_ExtensionWinsOverAContradictingContentType` pins both). The previewer was
deliberately not narrowed — `frontend/app/src/lib/sourceDocument.ts` keeps its image rows so a
document stored before the change still renders.

Rendering is gated on `documents.declared_content_type` (`RendersPageImages`,
`internal/extraction/classify.go`), which makes that column load-bearing. `POST
/v1/imports/preview` stores the client's raw part header before it judges the format
(`internal/importer/handlers.go:401`), and `Upsert` is first-writer-wins on the content hash,
so a PDF previewed there under a non-canonical type extracts from text alone ever after. Not
reachable from the app -- the wizard refuses a document-kind file before the preview call
(`frontend/app/src/App.tsx:681`) -- but a direct API caller can do it to their own bytes.
