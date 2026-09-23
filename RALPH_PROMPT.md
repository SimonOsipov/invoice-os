# Ralph Workflow — ASComply Africa (invoice-os)

## Overview

`/ralph <STORY>` builds one story: plan once (Phase 0.6), then per subtask Test-Spec* → Execution → QA Verify (*Test-Spec only for `Test-first: yes`), then a story-level deploy gate (Phase 3.5). The deploy gate is the `dev-env.yml` run on the PR: it deploys the whole fleet to the PR's own ephemeral Railway environment (forked from `development`; its database is bootstrapped, migrated, demo-purged and seeded at gateway boot), checks fleet health, and runs smoke + topology E2E. Work runs in an isolated git worktree.

**One story = one branch = one PR.** RALPH splits it into subtasks (`<STORY>-01`, …) internally.

A story arrives **basic** (Objective, Core ACs, Out of Scope; from `/pm-story` or `/pm-epic`; no Backlog subtasks) or **pre-planned** (Backlog subtasks exist, or the story already has an `## Implementation Subtasks` section). Phase 0.6 plans basic stories; pre-planned stories skip it.

Each PR has its own environment, so several `/ralph` runs MAY run concurrently.

## Agents and models

Every agent runs on Opus. The model is set in each agent's own definition; do not pass `model:` on a spawn.

| Stage | Subagent |
|-------|----------|
| Plan (Phase 0.6a), plan-review fixes | `product-architecture-spec` |
| Plan review (Phase 0.6b), Test-Spec (Mode A), QA Verify (Mode B), deploy-gate verification | `product-qa-spec` |
| Execution | `product-executor` |

## CRITICAL RULES

### 1. Separation of duties
The lead orchestrates. It may read, search and run commands directly to orient itself and to check evidence. It never edits product code or tests: `product-executor` writes code, `product-qa-spec` writes tests and verifies. Keep suite output in log files, not in this context (Stage 3).

### 2. No assumptions, no feature cuts
Never guess or reduce functionality. When something is unclear:
- **Interactive run** (the user is present and you are not inside a `/ralph` phase) — ask.
- **Unattended run** (every `/ralph` phase) — take the conservative default: the option closest to the story's text, smaller scope. Record it in `## Decisions`. Never block on the user, except at the critical-fork gate (Phase 0.6d).

Neither path licenses a silent feature cut.

| WRONG | RIGHT |
|-------|-------|
| "The rules table is empty, so skip validation" | "The rules table is empty, so seed the rule-set" |
| "RLS makes this hard, query as superuser" | "Run inside `WithinTenantTx` as `invoice_app`" |
| "The CEL escape hatch is complex, drop it" | "Implement the CEL rule type with a golden test" |

### 3. CI gate + deploy gate (blocking)
`ALL_TASKS_COMPLETE` requires BOTH: (a) the aggregate **`CI`** check green on the PR head, AND (b) the **`dev-env.yml`** `pull_request` run on the PR head green (Phase 3.5).

```bash
gh pr checks [PR_NUMBER]
# CI (ci.yml) rolls up: go, frontend, clean-clone, migrations, docker-canary, rls, queue, audit.
# dev-env.yml (on a ready PR): await-ci + prepare-env → deploy-gateway (migrator) → health-gate →
#   deploy-context ×7 + deploy-spas ×3 → fleet-gate → e2e (smoke + api + topology + demo).
```

Not part of the gate: `dev-env-teardown.yml` (deletes the PR environment on close), `dev-env-sweeper.yml` (daily reaper of orphaned PR environments), `railway-invariants.yml` (asserts Railway PR Environments stay OFF). CI tears environments down; never use destructive Railway MCP calls.

### 4. One branch, one PR per story
All subtasks share one feature branch, one draft PR and one worktree.

### 5. Keep going
Every `/ralph` phase is unattended. Put each status note in the same message as the next action.
End a turn only in these cases:
- Phase 0.6d halts at `AWAITING_ANSWERS`.
- A spawn fails a third time (HALT, Phase 1).
- Phase 3.5 escalates to the user.
- The run outputs `ALL_TASKS_COMPLETE`.
- A background command or Monitor you started is still running. Its completion wakes you.

In every other case, take the next step. Do not end a turn on "Starting X now", "Next: subtask 4", or an offer to continue.

---

## MCP servers
| Server | Use |
|--------|-----|
| Backlog | `mcp__backlog__*` — subtasks |
| Context7 | `mcp__context7__*` — check before writing library code (pgx, River, CEL, goose, Vite, React) |
| Playwright | `mcp__playwright__*` — UI verification against the deployed PR environment |
| Sentry | `mcp__sentry__*` — deployed errors |
| Railway | `mcp__railway-mcp-server__*` — read-only; deploys happen in `dev-env.yml` |
| Obsidian | `mcp__obsidian-mcp-tools__*` — story files |
| sysmap | `mcp__sysmap__*` — feature records |

## Docs
Read the docs related to your change. `ls docs/` is the list, and every file in it is a Stage 4 sweep target. Key ones: `migrations.md`, `deploy-model.md`, `topology-e2e.md`, `add-a-service.md`, `e2e-convention.md`, `mock-app-adapter.md`.

Design references for UI stories: Claude Design **prototype** project `6269a212-5677-4abd-b8a9-08aad10b1c65` (`InvoiceOS Africa.dc.html` = landing, `Platform.dc.html` = `frontend/app`, `Ops Console.dc.html` = `frontend/ops-console`) and **design system** project `999b7034-9f23-43d4-9229-51af7dde9f62`.

---

## Workflow

### Phase 0: Story resolution

1. **Validate the argument.** Exactly one story reference:
   - a **sysmap feature** — `F-192` or its slug. The current form.
   - an **Obsidian story id** — `AIR-08`, `M3-04`, `BUG-19`.
   If it resolves as both, prefer the feature and say so. Error and exit if missing.
2. **Read the story.**
   - *Feature:* `sysmap_feature_show`. Name + description = Objective; acceptance criteria = Core ACs; `screen` = surface; `depends_on` = prerequisites. Set `PLANNING_REQUIRED=true`, `STORY_SOURCE=sysmap`. Refuse a feature whose status is not `planned` or `building`, and name the status. Set it to `building` before Phase 1.
   - *Obsidian:* `get_vault_file` on `Simon Vault/Projects/ASComply Africa/User Stories/<EPIC>/<STORY>*.md`; also check `User Stories/Archive/<EPIC>/`. Set `STORY_SOURCE=obsidian`. If no file exists, error: "run /pm-story first".
3. **Branch slug.** Use the story's `## Branch Strategy` if present. Otherwise `feature/<lowercase-id>-<kebab-title>` (`F-192 Notice a submission failure` → `feature/f-192-notice-a-submission-failure`).
4. **Backlog subtasks:** `mcp__backlog__task_list({ labels: ["story:<lowercase-id>"], status: "To Do" })`.
5. **Refuse a story with unanswered questions.** If `## Blocking Questions` has any entry, stop and ask the first open one (Phase 0.6d rules). Never default them or delete the section to proceed. (`## Open Questions` is a different, non-blocking section.)
6. **Classify the story state:**
   - `STORY_SOURCE=sysmap` → **BASIC**.
   - Zero subtasks + Objective/Core ACs → **BASIC** → `PLANNING_REQUIRED=true`.
   - Subtasks returned, or an `## Implementation Subtasks` section → **PRE-PLANNED** → topo-sort by dependencies, log the plan, skip Phase 0.6.
   - Neither → error: "story <ID> is neither basic nor pre-planned — run /pm-story, or /pm-epic for a multi-story topic."

### Phase 0.5: Worktree bootstrap

1. **Paths:** `MAIN_CHECKOUT=/Users/samosipov/Downloads/invoice-os`, `WORKTREE_PATH="$MAIN_CHECKOUT/.claude/worktrees/<lowercase-id>"`, `BRANCH=<slug>`.
2. **Pre-flight:** if `$WORKTREE_PATH` exists and `git worktree list` shows it, another run owns this story — error and exit. If it exists but is not listed, it is stale — tell the user to run `/post-merge-cleanup`, and exit.
3. **Create:**
   ```bash
   git -C "$MAIN_CHECKOUT" fetch origin main
   git -C "$MAIN_CHECKOUT" worktree add -b "$BRANCH" "$WORKTREE_PATH" origin/main
   ```
4. **Symlink `CLAUDE.md`** (gitignored, so the worktree lacks it; subagents need it). A symlink, never a copy:
   ```bash
   [ -f "$MAIN_CHECKOUT/CLAUDE.md" ] && ln -sfn "$MAIN_CHECKOUT/CLAUDE.md" "$WORKTREE_PATH/CLAUDE.md"
   ```
5. **Dependencies:**
   ```bash
   (cd "$WORKTREE_PATH" && pnpm install --frozen-lockfile) &
   # Own dev Postgres per worktree, on an unused host port. Check `docker compose ps` /
   # `lsof -iTCP -sTCP:LISTEN` first. Never reuse or tear down another worktree's stack;
   # /post-merge-cleanup tears down this one. Skip if the story touches no Go DB code or migrations.
   (cd "$WORKTREE_PATH" && DEV_DB_PORT=<unused-port> make dev-db) &
   wait
   ```
   Record `DEV_DB_PORT`. Every DB-backed suite passes it; without it a suite hits port 5432, which may be another worktree's database.
6. All later commands run inside `$WORKTREE_PATH`. Pass it as CWD to every subagent.
7. Pre-planned stories: move all subtasks to "In Progress".

### Phase 0.6: Planning (basic stories only)

Runs only when `PLANNING_REQUIRED=true`, inside the worktree.

#### a. Architecture — finalize the story
Spawn `product-architecture-spec` with the full basic story and its Obsidian path, per its "Expanding a basic story" section. It rewrites the story in place: design, `## Implementation Subtasks`, and `## Decisions`. Pass these rules:

- **Diff every new control against its siblings.** A control added to an existing bar or panel matches the siblings' visibility and disabled treatment. A sibling's shipped decision is the spec.
- **A layout constant ships a layout assertion.** When a subtask adds or changes a width, grid track, clearance, overflow or alignment, name a topology layout assertion as its deliverable. `e2e/topology/layout.ts` sweeps `WIDE_WIDTHS` (2560/1920/1440/1280); `e2e/topology/invoice-surfaces.spec.ts` is the worked example. Planning it here puts it in the first deploy-gate run.
- **Re-measure every fact the story asserts** — a root cause, a mechanism, a count, another PR's state, whether the prescribed fix can work. Measure it in the worktree. Paste the command and its output into `## Decisions`, one entry per fact, tagged `premise — verified` or `premise — CORRECTED: story said X, actually Y`.
- **Measure a fact about the deployed app on the deployed app** (browser output, sidecar responses, production data): Playwright MCP or Railway logs. Paste the output into the same `premise —` entry. Never escalate an unmeasured premise.
- **An AC that already holds at head is not work.** Record the proving test in `## Decisions`; write no subtask.
- **Traceability:** every derived AC and subtask traces to the Objective or a Core AC. Nothing in Out of Scope appears in a subtask.
- **Checkpoint:** `STORY_FINALIZED`

#### b. Plan review — unattended
Run `/qa-verify` in **unattended** disposition. Judgement and unresolved findings take the conservative default; never block here — Phase 0.6d re-tests them. A finding that falsifies a premise is recorded as `premise — CORRECTED`, not repaired as wording. The log goes to the story's `… QA Debate Log.md`.
- **Checkpoint:** `PLAN_VERIFIED`

#### c. Subtask generation
Run `/subtask-generator` on the finalized story: one Backlog task per subtask, labelled `story:<slug>`, dependencies wired. Move them to "In Progress". Topo-sort into execution order. Log the plan: title, branch, ordered subtasks, count of Decisions and conservative defaults.
- Do not emit `SUBTASKS_READY` while a fact the story asserts lacks a `premise —` entry with pasted output.
- **Checkpoint:** `SUBTASKS_READY`

#### d. Critical-fork gate — the one place the run asks a question
Test every `## Decisions` entry (conservative defaults and `premise —` entries included) against five questions:

- Does it decide **who is allowed** to do something?
- Does it decide **what the system claims** to an outside party: the authority, the customer, the audit record?
- Does it let the system **silently override a human's action**?
- Does a **corrected premise** remove something the scope needs: a shipped screen, an endpoint, a merged PR, a seeded row?
- Does it change the **meaning of a Core AC** (or a subtask AC taken from one)? A rewording that keeps the meaning is not a fork.

Any "yes" makes the fork **critical**. Expect zero to two per story.

**First check whether the user already answered it.** Look in the story's epic folder for a file with frontmatter `type: decision-log` (fallback: filename `*Decision Log*.md`). For each critical fork:
- The log answers the same question → not blocking. Record `user — <choice> (<file>, <decided: date>)`.
- The log is silent → blocking.
- Your default contradicts the log → blocking; say so in the question.
Match on the question, not on shared nouns.

With critical forks left:
1. Write each under `## Blocking Questions` (exact heading): the question in one line, the default, the alternative.
2. Ask the first question alone, in plain words, with 2–3 options and the default marked. **Halt.** **Checkpoint:** `AWAITING_ANSWERS`.
3. After each answer, ask the next. When the user pushes back or says "your call", take the default for every question left.

When every question is answered or defaulted, record each as `user — <choice>` in `## Decisions`, delete `## Blocking Questions`, and continue without re-planning.

**Boundary:** pre-planned stories skip Phase 0.6 and so skip this gate.

#### e. Decisions surfacing (non-blocking)
Tell the FIRST subtask's executor to put the story's `## Decisions` section and a pointer to the QA Debate Log in the draft PR description.

### Phase 1: Sequential subtask execution

For each subtask in dependency order, run the stages below. Stage numbers start at 2.5 because code comments cite them. Run one stage agent at a time, and run no suite while a stage agent runs: agents in one worktree share its files and its dev Postgres.

Before every spawn, run `git -C "$WORKTREE_PATH" status --short` and `git -C "$WORKTREE_PATH" log --oneline -3`. A report that says "committed" is not evidence; the log is. Commit orphaned work under its own subtask's message with explicit paths, never `git add -A`.

After a context compaction, also run `mcp__backlog__task_list` for `story:<slug>` and `gh pr checks` before the next spawn. The summary says where the run was; git, Backlog and CI say where it is.

If a spawn fails, retry twice. On a third failure, HALT: leave the subtask "In Progress" and report the stage and error. Never perform a stage yourself — a same-context QA pass of your own work is worthless evidence.

**Test-first is the default for logic-bearing work** (rules engine, tax maths, state machines, RLS, validation). `Test-first: no` is for UI, copy and config whose oracle is the deploy gate.

**"No honest oracle exists" is a finding, not a waiver.** When `Test-first: no` is chosen because no test can see the failure, record it in `## Decisions` and name in the PR body which Phase 3.5 artifact stands in.

#### Stage 2.5: Test-Spec (`Test-first: yes` only)
- Spawn `product-qa-spec` (Mode A) with the subtask's Test Specs.
- It writes runnable tests and confirms each fails on its target assertion, not on a compile or setup error. DB-backed suites use `make test-rls|test-queue|test-audit DEV_DB_PORT=…` or `go test` with the env-gated DSN.
- It commits the red tests.
- **Checkpoint:** `TESTS_RED`

#### Stage 3: Execution
Spawn `product-executor` with the complete Backlog subtask. Pass these orientation rules; the executor does them before it edits:

- **Search with `breaklist`:** `go run ./internal/tools/breaklist '<Go regexp>' [path ...]` from the worktree root. Report the command and its `TOTAL` line. Do not substitute `grep`/`git grep` or pipe through `head` — they drop NUL-byte files, ignore `\b` and truncate silently. A search finds only code that names the thing, not a test that depends on the behaviour.
- **Go signature/API change:** enumerate every caller and test across `cmd/` and `internal/` as a deliverable.
- **JSON wire-shape change:** also search `e2e/` and the SPA wire mirrors (`frontend/*/src/lib/*.ts`). No compiler links a Go struct to its TypeScript copies. `e2e/api/client.ts` is the one that gets forgotten.
- **UI-touching change:** search `e2e/` (smoke + topology) for changed routes, testids and labels; each match is a deliverable.
- **A backend change to a value the frontend branches on is UI-touching** (a reason code, a doubt scope, an enum value, a URL parameter). Find the frontend code that reads it, then search `e2e/` for the testids it renders.
- **Migrations:** goose is timestamp-ordered. Scaffold with `make migrate-create name=<slug>` inside the worktree. Every tenant-owned table is born with `tenant_id` + the FORCE-RLS policy template and a working `-- +goose Down`. Verify `make migrate-up` and the down/up round-trip locally; a bad migration crash-loops the PR environment.
- **A spec copies a backend value from its Go constant**, and names the constant beside it.
- For `Test-first: yes`, drive the red tests green without weakening, skipping or deleting any.

The executor commits and handles push and PR state per its `Order` field (Phase 2).

Then run the suites yourself, with output to logs:
```bash
L="$WORKTREE_PATH/.ralph"; mkdir -p "$L"
(cd "$WORKTREE_PATH" && scripts/dev/wait-go-test-idle.sh .)                   > "$L/wait.log" 2>&1; echo "wait=$?"  # wait=1: stop, do not run the suites
(cd "$WORKTREE_PATH" && make fmt-check)                                        > "$L/fmt.log"  2>&1; echo "fmt=$?"
(cd "$WORKTREE_PATH" && go build ./... && go vet ./... && go test ./...)      > "$L/go.log"   2>&1; echo "go=$?"
(cd "$WORKTREE_PATH" && make test-rls test-queue test-audit DEV_DB_PORT="$DEV_DB_PORT") > "$L/db.log" 2>&1; echo "db=$?"
(cd "$WORKTREE_PATH" && pnpm -r typecheck && pnpm -r build)                   > "$L/spa.log"  2>&1; echo "spa=$?"
# `pnpm -r test` alone would launch Playwright (e2e's `test` script is the browser suite).
(cd "$WORKTREE_PATH" && pnpm -r --filter '!@invoice-os/e2e' test && pnpm --filter @invoice-os/e2e test:unit) > "$L/unit.log" 2>&1; echo "unit=$?"
grep -hE '^(ok|FAIL|--- FAIL|Test Files|Tests)' "$L"/*.log | tail -40
```
- Never kill a running suite: a killed DB suite skips `t.Cleanup` and leaves an orphan tenant and a stuck `river_job`. Run the block in the background. Its exit wakes you.
- A zero exit and its summary line are the evidence. Read a full log only when its suite failed.
- A subagent's report of a suite is not the suite's result.
- **Checkpoint:** `EXECUTION_DONE`

#### Stage 4: QA Verify
Spawn `product-qa-spec` (Mode B) with the acceptance criteria, the plan, the changed files and the Definition of Done. Pass these rules:

- For `Test-first: yes`, confirm the Stage 2.5 tests are green and still meaningful, then add adversarial, edge and negative coverage, including a cross-tenant RLS refusal test for any new tenant-owned table.
- **Prove every AC test can fail**, one row per item the AC lists (a value, a role, a state, an exit, an input class). For each item, break the production code the AC names and name the test that must go red. Never break a test helper. For a value AC, change the value; do not remove it. Append each row to `$WORKTREE_PATH/.ralph/mutations-<SUBTASK-ID>.jsonl`:
  ```json
  {"ac": "AC-2 / exit via back button", "file": "frontend/app/src/lib/route.ts", "find": "<exact text, once in the file>", "replace": "<broken text>", "test_file": "frontend/app/src/lib/route.test.ts", "test": "<full test title>"}
  ```
  An AC item with no row is a QA failure. A Playwright spec cannot replay locally: cite its assertion for Phase 3.5 and write no row.
- **Assert a collection is non-empty** before asserting over its items.
- **Every source scan** (a grep, a source walk, a forbidden-string guard, a site count):
  1. strips comments before it matches (TypeScript: `stripComments` from `@invoice-os/api-client/strip-comments`; Go: `go/ast` or strip first);
  2. reads only the function or block it guards, not the whole file;
  3. matches every letter case, unless case is the point — then a comment says so.
  Prove it with one break: delete the guarded code, keep its comment, run the scan, and it must go red. Record that as a mutation row.
- **An absence scan** also needs a control needle that must be found and a floor on the population scanned. Offer no "zero hits" as evidence until the same command has found a planted hit.
- **Re-read every comment and doc your change made false**, and fix them in the same commit. Sweep in cost order: (1) comments your diff did not edit in files it did (`git diff main...HEAD -U15`); (2) comments in files you never opened — name the fact your change altered and search the whole tree for it; (3) every file in `docs/`. Treat your own earlier future-tense notes as suspects.
- **State a shared fact in one place** and cite that place.
- **A fix to a false comment deletes the false clause and adds no new clause.** A needed new claim names the test or command that proves it.
- Frontend: Playwright MCP verification against the deployed PR environment once it exists.

When QA returns, **replay the mutation rows yourself**, with no other agent running: `go run ./internal/tools/mutationreplay .ralph/mutations-<SUBTASK-ID>.jsonl`. It edits source in place and restores the exact bytes. Any `NOT-PROVEN` or `INVALID` row fails QA; send it back. A row is a claim; the replay is the evidence.

If issues are found, spawn `product-executor` to fix, then re-verify. Update the Backlog task's implementation_notes with QA findings.
- **Checkpoint:** `QA_VERIFIED`

After each subtask, wait for `CI` on the pushed commit (CI Monitoring Protocol):
```bash
SHA="$(git -C "$WORKTREE_PATH" rev-parse HEAD)"
RUN_ID="$(gh run list --workflow ci.yml --commit "$SHA" --limit 1 --json databaseId -q '.[0].databaseId')"
gh run watch "$RUN_ID" --exit-status
```
A red run stops the next subtask until it is green. Then take the next subtask.

### Phase 2: PR lifecycle

`product-executor` manages PR state from the subtask `Order` field:
- `1 of N (FIRST)` → push and create the **draft** PR (drafts skip `dev-env.yml`).
- `K of N` → push only.
- `N of N (FINAL)` → `git fetch origin`, merge `origin/main` if behind, push, `gh pr ready`.

The orchestrator never runs `git checkout -b`, `gh pr create` or `gh pr ready`.

### Phase 3: CI

After the FINAL subtask's QA, wait for the aggregate `CI` per the CI Monitoring Protocol. No automated code review runs on this repo; do not report one.

### Phase 3.5: Story-level deploy gate

Runs once per story, after `CI` is green. It verifies the assembled feature against the original objective.

1. **Read the original acceptance criteria**, not the possibly-edited subtask ACs. `STORY_SOURCE=sysmap`: the feature's acceptance criteria (standing invariants). `STORY_SOURCE=obsidian`: the story's Objective and Core ACs.
2. **Find the deploy-gate run on HEAD** — a `pull_request` run on the head commit that was not skipped:
   ```bash
   BRANCH="$(git -C "$WORKTREE_PATH" rev-parse --abbrev-ref HEAD)"
   SHA="$(git -C "$WORKTREE_PATH" rev-parse HEAD)"
   RUN_ID="$(gh run list --workflow dev-env.yml --commit "$SHA" --limit 10 --json databaseId,event,conclusion \
     -q '[.[] | select(.event == "pull_request" and .conclusion != "skipped")][0].databaseId')"
   ```
   - `gh pr ready` does not reliably start a run. If `RUN_ID` is empty on the first lookup, push an empty commit: `git -C "$WORKTREE_PATH" commit --allow-empty -m "ci: run the deploy gate" && git -C "$WORKTREE_PATH" push`. GitHub applies the path filter to the whole PR diff, so this starts the gate.
   - A `skipped` run is a draft run: neither pass nor fail.
   - `gh workflow run dev-env.yml --ref "$BRANCH"` is for diagnosis only. It targets `development`, not the PR environment, so it proves nothing about this PR.
   - `dev-env.yml` is paths-filtered (`frontend/ packages/ e2e/ cmd/ internal/ migrations/ db/ tools/prenv/ scripts/ci/`, go.mod/sum, Dockerfile, Caddyfile, package.json, pnpm-*, the workflow file). A docs-only PR never fires it; escalate to the user rather than faking it green.
   - **Freshness:** `git -C "$WORKTREE_PATH" fetch origin`. If `origin/main` has commits the branch lacks, merge, push, and let `CI` and the gate re-run. A base missing main's migrations crash-loops the gateway.
3. **Watch the run** to conclusion per the CI Monitoring Protocol.
   - Re-run a red gate whole: `gh run rerun "$RUN_ID"`, never `--failed`. The database resets only when the gateway deploys.
   - A spec this PR changed that passed only on retry fails `e2e`. Fix the spec or the race; do not re-run for luck.
   Green means: fleet deployed, gateway migrated, DB bootstrapped + demo-purged + seeded, all 8 backends up, smoke + topology E2E passed, including cross-tenant isolation.
4. **Spawn `product-qa-spec`** to verify **each** original AC against the green run:
   - Quote each AC beside its evidence. Evidence of different behaviour than the quoted text fails that AC.
   - Backend / data / RLS ACs → cite the passing CI job or E2E assertion.
   - **UI ACs** → drive the deployed SPA read-only with Playwright MCP as the seeded user. Capture each touched surface and state to `$WORKTREE_PATH/.ralph/fidelity/<surface>-<state>.png`. Diff live `getComputedStyle` and layout against the prototype (`.dc.html`; confirm the file→surface mapping first) and the design system. A delta citing a design-system rule or a prototype CSS rule is a fail; uncited taste is advisory → escalate, never bounce.
   - **Assert the relationship, not the dimension.** A layout AC is satisfied by what the number encodes — gutter symmetry, containment, alignment to a sibling. A width assertion passes on the very bug it should catch. This applies whenever the diff adds or changes a layout constant, not only when an AC names layout. **Measure widest first:** `e2e/topology/layout.ts` sweeps 2560/1920/1440/1280; every other sweep in `e2e/` stops at 1280.
   - **A pixel figure derived from source is a guess.** Measure it on the gate run with `e2e/topology/layout.ts` and cite the run id before a CSS edit, a bounce or an escalation.
   - No holistic "looks done": every AC needs its own evidence.
5. **Fix loop (cap 2 cycles):** batch all fails into one report → `product-executor` fixes → push (re-fires `dev-env.yml`) → wait → re-verify only the failed items. Every bounce cites an AC id, a design-system rule or a prototype CSS rule. After 2 cycles, escalate the rest to the user; each gate run rebuilds an 11-service environment.
6. **Log** under `## Post-Deploy QA — <date>` in the QA Debate Log: per-AC verdict + evidence, fidelity deltas, fix cycles, run ids, advisory notes.
7. **On PASS** (all original ACs pass on a green run, no unresolved bounces, fidelity evidence for UI stories): move all subtasks to "Done" and output `<promise>ALL_TASKS_COMPLETE</promise>`.
   **Otherwise:** leave subtasks "In Progress", do not emit completion, escalate to the user.

### Phase 4: Worktree cleanup

After the PR merges (manually or via `/gh-merge-pr`), run `/post-merge-cleanup <STORY>`. It removes the worktree and branch, marks subtasks Done, archives the story, and moves the feature off `building` in sysmap. A `/ralph-goal` loop advances only after this runs.

Teardown of the PR environment is repo-side: `dev-env-teardown.yml` on PR close (best-effort), `dev-env-sweeper.yml` daily as the authority. See `docs/deploy-model.md`.

---

## CI Monitoring Protocol

Wait with a background watcher. Run `gh run watch "$RUN_ID" --exit-status` through Bash with `run_in_background: true`. Its exit wakes you.

If `RUN_ID` is empty, GitHub has not registered the run yet. Start a background until-loop that repeats the `gh run list` query every 30 s and exits on the first id. Phase 3.5 step 2 owns a deploy-gate run that never starts.

Foreground `sleep` is blocked. Never end a turn on a wait you did not start.

1. **A check failed?** `gh run view [RUN_ID] --log 2>&1 | tail -60`, fix in the worktree, commit, push. CI (and `dev-env.yml` on a ready PR) restart on push.
2. **`CI` green?** → Phase 3.5.
3. **`dev-env.yml` green?** → Phase 3.5 step 4.

Select runs by head commit, never by latest-on-branch:
```bash
gh run list --workflow ci.yml --commit "$(git -C "$WORKTREE_PATH" rev-parse HEAD)" --limit 1 --json databaseId,status,conclusion -q '.[0]'
```

---

## Editing This File

An agent parses these instructions with no one to ask. Write for that reader.

- **One word, one meaning.** Reuse one verb per action. Reserve `check` for a CI check and `assert` for a test assertion.
- **One instruction per sentence**, twenty words or fewer for a procedure.
- **State each rule once**, in the phase that owns it.
- **No incident narratives.** A rule states what to do; the test or command that enforces it is the only citation.

## Completion Rules

1. Both gates green on the PR head (Core Rule 3). Local tests green ≠ done.
2. Subtask status: To Do → In Progress (Phase 0.5 / 0.6c) → Done (only after Phase 3.5 passes).
3. Output `<promise>ALL_TASKS_COMPLETE</promise>` only after Phase 3.5 passes and subtasks are Done.

## Anti-Patterns

| Anti-Pattern | Correct Approach |
|-------------|------------------|
| Lead editing product code or tests | Delegate to `product-executor` / `product-qa-spec` |
| Skipping QA Verify or a required Test-Spec stage | Every stage runs; Test-Spec is skipped only for `Test-first: no` |
| Weakening, skipping or deleting a red test | Fix the implementation; flag a wrong test |
| A tenant-owned table without RLS + a cross-tenant refusal test | `tenant_id` + FORCE-RLS policy at birth; QA adds the refusal test |
| Hand-setting a goose migration order or an untested `Down` | `make migrate-create` in the worktree; verify up + round-trip locally |
| Querying as superuser to get past RLS | `WithinTenantTx` as `invoice_app` |
| Working in the main checkout | Always `$WORKTREE_PATH`; main is the user's space |
| Finding subtasks by title | Use the `story:<slug>` label |
| Blocking on the user in an unattended phase | Conservative default + `## Decisions`; Phase 0.6d is the only question |
| Architect inventing scope | Every derived AC traces to the Objective / a Core AC |
| Bouncing the executor on uncited taste | Cite a design-system or prototype rule; taste is advisory |
| Renaming a variable and checking only the rename | Search every other variable's rendered value for the old name before merge |
| Running the deploy gate only before a variable deletion | Re-run it after the deletion is applied |
| Leaving the worktree after merge | `/post-merge-cleanup <STORY>` |
