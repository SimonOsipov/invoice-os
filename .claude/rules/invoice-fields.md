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
  - "internal/importer/rulebreaks.go"
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
- Add the column to `lineItemColumns`, `scanLineItem` and both `INSERT INTO line_items` statements in `internal/invoice/store.go`.
- Add the column to the `INSERT INTO invoices` statement in `Store.Create` in `internal/invoice/store.go`.
- Add the field to `headerFieldsPresent` and to the inline guard of `Store.Update` in `internal/invoice/store.go`.
- Add the field to the request structs in `internal/invoice/handlers.go`.
- Copy the field from the request into `CreateInput`, `EditInput` and `LineItemInput` in `internal/invoice/handlers.go`.
- Add the field to `MBSPayload` in `internal/invoice/payload.go`. Add a line field to `mbsLine` there.
- Add the field to `contentFingerprint` in `internal/invoice/payload.go`. An unhashed field does not demote a validated invoice.
- Add the field to `invoicesCSVHeader` in `internal/archive/invoices.go`.
- Add the field to the per-field `CreateInput` assignment in `internal/importer/service.go`.
- Add the field to `documentCreateInput` in `internal/importer/document.go`.
- Add a header field to the path map in `internal/importer/rulebreaks.go` when a rule targets it and the field is not locked.
- Add the field to `invoiceEditFor` in `cmd/submission/main.go`.
- Add a spec for an extracted header field to `tier1Specs` in `internal/extraction/tier1.go`. Give it a label id from `anchorLexicon` in `internal/extraction/anchor.go`.
- Without a spec, the Tier-1 rules and the learning path skip the field.
- Add the field to `lockedFields` in `internal/extraction/handlers_correction.go` only when a correction needs the extractor's doubt flag.
- Add the field to `doubtfulFields` in `internal/extraction/reconcile.go` only when the reconcile pass must present its adjacent generic reads as doubtful.
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
- Change `mappingCheckInstructions` in `internal/importer/jevcheck.go` and `MappingCheckInstructions` in `internal/jevmeasure/wording.go` with it. The two stay byte-identical.
- The 11 measured definition lines of `mappingSystem` stay byte-identical (`TestMappingSystem_KeepsTheElevenMeasuredDefinitions`). Only add lines.
- Mapping pins: `TestMappingPrompt_MatchesTheMeasuredHarness` (`SYSTEM`), `TestMappingFields_MatchTheMeasuredHarness` (csvrun `FIELDS`), `TestMappingCheck_TheQuestionIsTheMeasuredQuestion` (measured text, run-2 prefix).
- Score csvrun answers with `tools/aimodeltest/csvscore.py`. `tools/aimodeltest/csvscore_test.go` pins it.
- Turning on `Extract` for a header field changes `aiFieldSchema`. Change `aiSystem` in `internal/extraction/aireading.go` in the same story.
- Change `SYSTEM` and `FIELDS` in `tools/aimodeltest/run.py` with it.
- Turning on `Extract` for a line field changes the line schema and `LineRoles`. Change `aiLinesSystem` in `internal/extraction/ailines.go` in the same story.
- Change `LINE_SYSTEM` and `LINE_ROLES` in `tools/aimodeltest/run.py` with it. `TestAliRunPy_LineRolesIsExactlyExtractionLineRoles` asserts they match.
- Update the characterization pins of each path you change.
- Import pins: `TestImportKeys_AreTheThirtySixImportFieldsInOrder` and `TestCanonicalFields_AreTheImportKeys`. The vitest pin is `CANON is the import fields, invoice_number alone required` in `frontend/app/src/lib/invoiceFields.test.ts`.
- The import set is 36 keys. `ImportKeys()` lists the `Lead` fields (the original 11) first, then the rest in list order; the generated `IMPORT_KEYS` carries that order. The 10 `supplier_*` fields stay off the import path: the importer reads the supplier from the entity.
- The AI schema requires every import key. Mirror a new key in `IMPORT_KEYS` in `e2e/importFixtures.ts`. The three steered answers spread `nullImportKeys()`.
- Header extract pins: `TestExtractHeaderKeys_AreTheTenInOrder` and `TestHeaderFields_AreTheTenInOrder`. The vitest pin is `HEADER_FIELDS is the ten extraction header fields in order`.
- Line extract pins: `TestExtractLineKeys_AreTheFiveInOrder` and `TestLineRoles_AreTheFiveRoleConstantsInEmitOrder`.
- Edit pin: `EDIT_FIELD_KEYS is the nine editable header fields in order` in `frontend/app/src/lib/invoiceFields.test.ts`.
- Form pin: `Draft is the five form keys plus items` in `frontend/app/src/lib/invoiceFields.test.ts`.
- The Go pins live in `internal/invoicefields/fields_test.go`, `internal/importer/fields_test.go` and `internal/extraction/vocabulary_list_test.go`.

## Limits

- Add no file under `docs/`.
- Cite no `file.ext:NN` line number. `citegate` rejects it.
