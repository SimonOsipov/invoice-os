# APPR-17-02 execution
Verdict: done. approveGate + approve_blocked_by_creator on both wires; creator read in a second tenant tx (ApprovalFacts, RowFacts), error to CreatorErr + log.
Breaklist: `go run ./internal/tools/breaklist 'approvalGate|approveGate|ApprovalFacts|RowFacts|CreatorErr|CreatorStanding' cmd internal` -> TOTAL 509 hits in 32 files. `... 'approve_blocked_reason' frontend e2e` -> TOTAL 155 hits in 25 files.
Tests: make test-invoice ok; make test-approvals ok; frontend tsc clean, e2e tsc clean; vitest 11 files / 1135 passed.
Notes: added approval.GateFacts.PendingRoleKey. New key is last on both wires (after verdict_stale); wire-order tests updated. TestStoreApprovalFacts_OneTransaction now expects 2 tx for an open-run invoice. Second tx skipped when no open run/pending step.
