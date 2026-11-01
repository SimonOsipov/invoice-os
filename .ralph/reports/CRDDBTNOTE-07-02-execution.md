# CRDDBTNOTE-07-02 execution

Verdict: done; PR #651 draft.

## Changes
- internal/archive/register.go: filed-only filter on selectRegisterFooterSQL, comment replaced.
- .claude/rules/evidence-bundle.md: one rule line.
- e2e/api/invoice-register.spec.ts: footer 0.00 x3; resolve/undo test.
- e2e/topology/invoice-surfaces.spec.ts: download closure, 240s timeout, note filed, footers 459.20/34.44/493.64 then 277.32/20.80/298.12.
- e2e/submitSiteSweep.test.ts: manifest entry for the note test's submitSelected call (verdict: MOCK_TIN_ACCEPT, as DV-03).

## Merge fix (internal/extraction)
Epic changed ai_nrs_invoice.pdf and ai_steered_invoice.pdf (textChars 1697->1738, 717->758) and added jev_credit_note.pdf and noninvoice_debit_note.pdf (goldens 712, 222). Main added 28 nightly_* PDFs (count 42->70). Taking main's side lost the epic's golden edits. Re-merged both sides: count 72 (74 PDFs minus 2 Chrome), epic goldens restored.

## Tests
- go test -p 1 -count=1 ./internal/extraction/ (port 5461): ok 147.7s
- make test-archive DEV_DB_PORT=5461: ok
- pnpm --filter @invoice-os/e2e test:unit: 39 files, 698 passed

## Report-file note
My first reply said the brief barred a report file. Source: the agent system prompt line "Do NOT Write report/summary/findings/analysis .md files". It conflicted with the brief's explicit report path; the brief wins.
