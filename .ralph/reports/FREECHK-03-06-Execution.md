# FREECHK-03-06 Execution: DONE
Files: internal/readinesscheck/handlers.go (real handlers), cmd/worker/public_check_routes.go, cmd/worker/main.go (Checker wiring, routes, RequireGateway before Run, package comment), internal/platform/db/seam_coverage_test.go (3 exempt verdicts), .claude/rules/security-threat-model.md, .claude/rules/e2e.md.
Tests: go test ./internal/readinesscheck ./cmd/worker ok (boot tests ran on 5462); make test-rls ok (TestRLS_EveryRegisteredRouteHasAVerdict green).
breaklist 'RequireGateway\(' cmd internal: TOTAL 26 hits in 21 files (only cmd/worker/main.go changed; others are tests). breaklist 'public-checks|checks_left|readiness-check' e2e frontend/app/src/lib: TOTAL 3 hits in 1 file (readiness-check-contact.spec.ts, unrelated). No client.ts mirror exists; subtask 09 must add the gateway-side client and use captured-mail on the worker.
Assumptions/concerns:
- Seam scan finds routes by selector base name "Mux", so registerPublicCheckRoutes names its parameter `Mux` (comment says why).
- The mapping route shares the 2-slot limit with run (contract lists 503 for mapping).
- Budget (240 s) covers the body read and Count as well as Check; mail send and insert use a separate 15 s context.
- Engine-error text for 413/400/500 words: "too large", "unsupported format", "internal error".
hm services: all operational.
Memory notes applied: none
