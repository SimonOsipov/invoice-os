---
paths:
  - "internal/invoicefields/**"
  - "frontend/app/src/lib/invoiceFields*.ts"
  - "internal/invoice/store.go"
  - "internal/invoice/invoice.go"
  - "internal/invoice/handlers.go"
  - "internal/invoice/payload.go"
  - "internal/importer/suggest.go"
  - "internal/importer/service.go"
  - "internal/importer/document.go"
  - "internal/extraction/aireading.go"
  - "internal/extraction/ailines.go"
  - "internal/extraction/tier1.go"
  - "internal/extraction/reconcile.go"
  - "internal/extraction/mock.go"
  - "internal/extraction/handlers_correction.go"
  - "internal/archive/invoices.go"
  - "cmd/submission/main.go"
  - "tools/aimodeltest/*.py"
  - "frontend/app/src/lib/invoices.ts"
  - "frontend/app/src/lib/invoiceDraft.ts"
  - "frontend/app/src/lib/mapping.ts"
  - "e2e/topology/importWizardShared.ts"
---
# Invoice fields

- Add a field to `All` in `internal/invoicefields/fields.go`.
- Regenerate the SPA copy with `go test ./internal/invoicefields -run TestInvoiceFields_TheSPAFileIsGeneratedFromTheList -update`.
- Commit `frontend/app/src/lib/invoiceFields.gen.ts` with the change. Never edit it by hand.
- Set `Import`, `Edit`, `Extract` and `FormKey` off for a new field, unless the story owns that path.
- Add the migration for the column. Follow `.claude/rules/db-migrations.md`.
- Add the column to `invoiceColumns`, `scanInvoice` and the update set clauses in `internal/invoice/store.go`.
- Add the field to the `Invoice`, `LineItem`, `CreateInput`, `LineItemInput` and `UpdateInput` structs in `internal/invoice/invoice.go`.
- Add the field to the request structs in `internal/invoice/handlers.go`.
- Add the field to `MBSPayload` in `internal/invoice/payload.go`.
- Add the field to `invoicesCSVHeader` in `internal/archive/invoices.go`.
- Add the field to the per-field `CreateInput` assignment in `internal/importer/service.go`.
- Add the field to `documentCreateInput` in `internal/importer/document.go`.
- Add the field to `invoiceEditFor` in `cmd/submission/main.go`.
- Add a spec for an extracted field to `tier1Specs` in `internal/extraction/tier1.go`. Extraction drops a field without a spec.
- Add the field to `lockedFields` in `internal/extraction/handlers_correction.go`.
- Add the field to `doubtfulFields` in `internal/extraction/reconcile.go`.
- Add the field to the mock readings in `internal/extraction/mock.go`.
- Add the field to the wire interfaces in `frontend/app/src/lib/invoices.ts`. Add its rows to `frontend/app/src/lib/wireMirrors.test.ts`.
- Add the field to `MBS_PATH_TO_EDIT_FIELD` in `frontend/app/src/lib/invoices.ts`.
- Add the field to `draftToCreateRequest` in `frontend/app/src/lib/invoiceDraft.ts`.
- Add the form and editor inputs for the field.
- Add an `ALIAS` entry in `frontend/app/src/lib/mapping.ts` when the field needs header synonyms.
- Add the field to `VOCABULARY` in `e2e/topology/importWizardShared.ts` when you add an extract header field.

## Prompts

- Turning on `Import` changes `mappingSchema`. Change `mappingSystem` in `internal/importer/suggest.go` in the same story.
- Change `FIELDS` in `tools/aimodeltest/csvrun.py` with it. Change `FIELDS` in `tools/aimodeltest/csvgen.py` too.
- Turning on `Extract` for a header field changes `aiFieldSchema`. Change `aiSystem` in `internal/extraction/aireading.go` in the same story.
- Change `SYSTEM` and `FIELDS` in `tools/aimodeltest/run.py` with it.
- Turning on `Extract` for a line field changes the line schema and `LineRoles`. Change `aiLinesSystem` in `internal/extraction/ailines.go` in the same story.
- Change `LINE_SYSTEM` and `LINE_ROLES` in `tools/aimodeltest/run.py` with it. `TestAliRunPy_LineRolesIsExactlyExtractionLineRoles` asserts they match.
- Move the characterization pins of each path you change.

## Limits

- Add no file under `docs/`.
- Cite no `file.ext:NN` line number. `citegate` rejects it.
