# CRDDBTNOTE-03-04 Execution
Verdict: green. Files: internal/invoice/support.go (requeueNoteChecksTx called in RequeueFailedTx after the refusals; reuses readOriginalFactsTx, locksOriginal, noteViolations, hasBlockingViolation), internal/staffjobs/handlers.go (409 arm).
Tests: go test -p 1 ./internal/invoice/ ./internal/staffjobs/ -> ok both. hm services: operational. No breaklist searches run.
