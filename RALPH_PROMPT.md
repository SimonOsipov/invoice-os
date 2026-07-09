# Ralph Workflow — FiscalBridge Africa (invoice-os)

## Overview

Automated execution of a single build-plan task through per-subtask quality gates (Architecture → Explore → Test-Spec* → Execution → QA Verify; *Test-Spec runs only for logic-bearing `Test-first: yes` subtasks), followed by a story-level deploy gate (Phase 3.5) that verifies the assembled feature against the original objective via the **`dev-env.yml`** run on the PR (deploy the whole fleet to the shared Railway dev env → reset+seed → fleet health → smoke + topology E2E). Runs in an isolated git worktree so the main checkout stays clean.

**Story unit:** one **build-plan task** = one story = one branch = one PR (e.g. `M3-04` "Validation v1"). RALPH decomposes it into sub-subtasks (`M3-04-01`, …) internally. This matches exactly how M1/M2 shipped (`task-20` → `task-20.1–.4`).

Stories arrive in one of two states: **basic** (intent-only — Objective, Core ACs, Out of Scope; produced by `/pm-review`; zero Backlog subtasks) or **pre-planned** (Backlog subtasks already exist, or the Obsidian story is already architect-level like the M1/M2 stories). Basic stories are planned **in-run** by Phase 0.6; pre-planned stories skip Phase 0.6.

**Invocation**: `/ralph <STORY-ID>` (e.g., `/ralph M3-04`). **One story per invocation, executed serially** — the Railway dev env is a single shared, single-agent environment (`dev-env.yml` concurrency `dev-preview-shared` queues rather than races), so RALPH verifies one story to completion before the next begins. Do not advertise or run multiple stories in parallel against dev.

## Model Selection

**Never use the Haiku model.** All agents and subagents must use Sonnet or Opus only.

## CRITICAL RULES

### 1. MANDATORY Agent Delegation
**All file operations happen through spawned agents. No direct reads/edits in lead context.**

| Action | WRONG (bloats context) | RIGHT (uses agents) |
|--------|------------------------|---------------------|
| Find files | `Glob("internal/validation/**")` | `Task(subagent_type=Explore, prompt="Find all validation-engine files...")` |
| Read code | `Read("/path/to/engine.go")` | Agent reads during its task |
| Edit code | `Edit(file_path=..., old_string=...)` | `Task(subagent_type=product-executor, prompt="Implement...")` |
| Research | Multiple Grep/Read calls | `Task(subagent_type=Explore, prompt="Research how the tenant-tx helper works...")` |

**Exception:** MCP tools (Backlog, git, gh, Railway read-only) can be called directly.

### 2. NO Assumptions, NO Feature Cuts
Never guess, assume, or reduce functionality. If unclear, ask the user.

| WRONG | RIGHT |
|-------|-------|
| "The rules table is empty, so skip validation" | "The rules table is empty, so seed the MBS v1 rule-set" |
| "RLS makes this hard, let's query as superuser" | "RLS is the point — run inside `WithinTenantTx` as `invoice_app`" |
| "The CEL escape hatch is complex, drop it" | "Implement the CEL rule type properly with a golden test" |

### 3. CI Gate + Deploy Gate (BLOCKING)
Cannot output `ALL_TASKS_COMPLETE` until BOTH (a) the aggregate **`CI`** check passes on the PR AND (b) the **`dev-env.yml`** run on the PR concludes **green** (see Phase 3.5). The deploy gate deploys the whole fleet to the shared dev env and runs smoke + topology E2E — it is required for completion.

```bash
gh pr checks [PR_NUMBER]
# Aggregate per-push check (ci.yml): CI  — rolls up: go, frontend, clean-clone,
#   migrations, docker-canary, rls, queue, audit (each gated on `changes`).
# Deploy gate (dev-env.yml, fires when the PR is marked ready): await-ci →
#   deploy-gateway (migrator) → health-gate → deploy-context ×7 + deploy-spas ×3
#   → reset-seed → fleet-gate → e2e (smoke + topology). Required for completion.
```

### 4. ONE Branch, ONE PR per Story
All subtasks of a single build-plan task share one feature branch and one draft PR, all in one worktree. Never mix subtasks from different stories on one branch.

---

## Available MCP Servers
| Server | Purpose | Usage |
|--------|---------|-------|
| **Backlog** | Task tracking (MCP) | `mcp__backlog__*` |
| **Context7** | Library docs | `mcp__context7__*` — ALWAYS check before writing library code (pgx, River, CEL, goose, Vite, React) |
| **Playwright** | Visual verification, E2E, bug research | `mcp__playwright__*` — Use for UI/topology verification against deployed dev |
| **Sentry** | Error tracking, issue investigation | `mcp__sentry__*` — Check for dev errors |
| **Railway** | Deployment status (read-only) | `mcp__railway-mcp-server__*` — NO destructive actions; deploys happen in `dev-env.yml` |
| **Obsidian** | User stories + build plan | `mcp__obsidian-mcp-tools__*` — read the story + `Build Plan — 0 to MVP.html` |

## Documentation (docs/)
Reference before making changes to related areas:
- `docs/migrations.md` — goose harness, roles, gateway-as-migrator, GUC helper contract
- `docs/deploy-model.md` — Railway dev deploy model, scale-to-zero
- `docs/topology-e2e.md` — the unified dev-env deploy+verify workflow, prerequisites, secrets
- `docs/add-a-service.md` — config-as-code recipe for a new Railway service

Design references (for UI stories): the Claude Design **prototype** project `6269a212-5677-4abd-b8a9-08aad10b1c65` (`InvoiceOS Africa.dc.html` = landing, `Platform.dc.html` = `frontend/app`, `Ops Console.dc.html` = `frontend/ops-console`; all deployed to Netlify) and the **design system** project `999b7034-9f23-43d4-9229-51af7dde9f62`.

---

## Workflow

### Phase 0: Story Resolution

1. **Validate the story arg.** `/ralph` requires exactly one build-plan task ID (e.g., `M3-04`). Error and exit if missing.
2. **Read the Obsidian user story file**:
   - `mcp__obsidian-mcp-tools__get_vault_file` matching `Simon Vault/Projects/FiscalBridge Africa/User Stories/<Mn>/<STORY-ID>*.md` (use `list_vault_files` against `.../User Stories/<Mn>/` to disambiguate; also check `.../User Stories/Archive/<Mn>/`).
   - If no Obsidian story exists yet, fall back to the build plan: read `Simon Vault/Projects/FiscalBridge Africa/Build Plan — 0 to MVP.html`, find the `<STORY-ID>` row (Task / Layer / Size / Depends / the milestone's "Ships when true"), and treat that row + the milestone goal as a **basic** story (set `PLANNING_REQUIRED=true`).
3. **Derive the branch slug.** If the story has a `## Branch Strategy` section, use it. Otherwise synthesize `BRANCH=feature/<lowercase-story-id>-<kebab-title>` (e.g. `feature/m3-04-validation-v1`).
4. **Query Backlog for subtasks**:
   ```
   mcp__backlog__task_list({ labels: ["story:<lowercase-story-id>"], status: "To Do" })
   ```
5. **Classify the story state**:
   - **Zero subtasks + Objective/Core ACs present (or build-plan fallback)** → **BASIC** → set `PLANNING_REQUIRED=true`; topo-sort + plan-logging happen at the end of Phase 0.6.
   - **Subtasks returned (or an architect-level Obsidian story with a Subtasks section)** → **PRE-PLANNED** → topo-sort by `dependencies` → linear execution order, log the plan, skip Phase 0.6.
   - **Neither** → error: "story <ID> is neither basic (no Objective/Core ACs, not in the build plan) nor pre-planned (no Backlog subtasks) — run /pm-review first."

### Phase 0.5: Worktree Bootstrap

1. **Resolve paths**:
   - `MAIN_CHECKOUT=/Users/samosipov/Downloads/invoice-os`
   - `WORKTREE_PATH="$MAIN_CHECKOUT/.claude/worktrees/<lowercase-story-id>"`
   - `BRANCH=<branch-slug>`

2. **Pre-flight check**:
   ```bash
   if [ -d "$WORKTREE_PATH" ]; then
     # If 'git -C "$MAIN_CHECKOUT" worktree list' shows it: another /ralph instance is running this story → error and exit.
     # Otherwise it's stale: tell the user to run /post-merge-cleanup before retrying.
     exit 1
   fi
   ```

3. **Sync main and create the worktree on a fresh feature branch**:
   ```bash
   git -C "$MAIN_CHECKOUT" fetch origin main
   git -C "$MAIN_CHECKOUT" worktree add -b "$BRANCH" "$WORKTREE_PATH" origin/main
   ```

4. **Bootstrap dependencies** — Go needs no per-worktree install (the module cache is shared); the SPAs and the DB-backed tests do:
   ```bash
   (cd "$WORKTREE_PATH" && pnpm install --frozen-lockfile) &
   # Local dev Postgres (compose) → bootstrap roles → migrate → seed. Required for
   # the RLS / queue / audit suites (make test-rls|test-queue|test-audit). One local DB
   # is fine across serial stories; skip if this story touches neither Go DB code nor migrations.
   (cd "$WORKTREE_PATH" && make dev-db) &
   wait
   ```

5. **All subsequent shell commands run inside `$WORKTREE_PATH`.** Pass it as CWD to subagents.

6. **Move all subtasks to "In Progress"** (skip if `PLANNING_REQUIRED` — Phase 0.6c does this after creating them):
   ```
   For each subtask ID: mcp__backlog__task_edit(id=task_id, status="In Progress")
   ```

### Phase 0.6: Planning (basic stories only)

Runs ONLY when Phase 0 set `PLANNING_REQUIRED=true`. All planning runs **inside the worktree** so every file reference is written against the code that will actually be modified.

#### a. Architecture — finalize the story
- Spawn `product-architecture-spec` (Opus), CWD = `$WORKTREE_PATH`, passing the FULL basic story content (or build-plan row + milestone goal) and its Obsidian path. Instruct it to operate per its "Expanding a Basic Story" section.
- It rewrites the story file in Obsidian to final state: system design, **## Implementation Subtasks** (`[<STORY-ID>-NN]` with Category / Dependencies / Description / Acceptance Criteria / Order / Test-first classification + Test Specs tables for `Test-first: yes`), and a **## Decisions** section appending every assumption it made where the story was silent.
- **Traceability rule (hard):** every derived AC and subtask must trace to the Objective or a Core AC (or the milestone's "Ships when true"). Nothing in Out of Scope may appear in any subtask.
- **Checkpoint:** `STORY_FINALIZED`

#### b. QA-Verify debate — UNATTENDED disposition
- Run the `/qa-verify` protocol against the finalized story: `product-qa-spec` critic (Sonnet) vs `product-architecture-spec` architect (Opus), ≤3 rounds, citation-required — including the Intent-Integrity checks (AC→Objective traceability, Out-of-scope leakage = mechanical).
- Use the protocol's **Unattended Mode** disposition table: mechanical+resolved+cited → auto-apply; judgment / unresolved / uncited → **conservative default** (option closest to the story's explicit text, smaller scope) + prominent log entry. NEVER block on the user.
- Append the run to the story's `… QA Debate Log.md`, marking each disposition `auto-applied | conservative-default (reason)`.
- **Checkpoint:** `PLAN_VERIFIED`

#### c. Subtask generation — Backlog tasks
- Execute the `/subtask-generator` logic for the finalized story, passing the story explicitly: spawn parallel `product-architecture-spec` agents → one Backlog task per subtask (description, acceptance_criteria, implementation_plan, references, labels `["story:<slug>", ...]`), then wire dependencies between the created task IDs.
- Move all created subtasks to "In Progress".
- Topo-sort by dependencies → linear execution order. **Log the plan**: story title, branch slug, ordered subtask list, count of Decisions + conservative-default dispositions.
- **Checkpoint:** `SUBTASKS_READY`

#### d. Decisions surfacing (non-blocking)
- When spawning the FIRST subtask's executor (Phase 1), instruct it to include in the draft PR description: the story's **## Decisions** section (PM defaults + architect assumptions + conservative-default dispositions) and a pointer to the QA Debate Log. This is the user's review surface — the run does NOT wait for input; completion gates remain CI + Phase 3.5.

Then proceed to Phase 1 exactly as for a pre-planned story.

### Phase 1: Sequential Subtask Execution

For each subtask, in dependency order, execute the stages below. Delegate to subagents — never implement code directly.

#### Subagent Mapping (MANDATORY)
| Stage | Subagent Type | Usage |
|-------|--------------|-------|
| Architecture | product-architecture-spec | Always — review/enhance the implementation plan (data models, API contracts, file paths, edge cases; per-subtask `Test-first: yes/no` classification + a Test Specs table for logic-bearing subtasks) |
| Explore | Explore | Always — verify files, patterns, and placement |
| Test-Spec | product-qa-spec | **`Test-first: yes` subtasks only** — author the architect's Test Specs as runnable Go tests and confirm they fail (RED) before implementation (Mode A) |
| Execution | product-executor | Always — implement all code changes; for `Test-first: yes` subtasks, drive the red tests to green |
| QA Verify | product-qa-spec | Always — verify implementation correctness (skeptical by default). For test-first subtasks, confirm AC tests are green + meaningful and add adversarial/edge coverage (Mode B) |

> A second, **story-level** deploy gate runs once after all subtasks complete — see **Phase 3.5**.

**Test-first is the strong default here.** This codebase lives on adversarial RLS / exactly-once / audit-immutability suites; logic-bearing work (rules engine, tax-math, state machines, RLS policies, validation) is prime test-first territory. `Test-first: no` is for pure UI/copy/config subtasks whose oracle is the Phase 3.5 deploy gate (smoke/topology/demo script), not unit tests.

#### If Subagent Spawning Fails: retry, then HALT — NEVER perform the stage yourself
Retry the Task call up to **twice** (fresh spawns; transient API/credit errors often clear). If the third attempt fails: **HALT the run** — leave the subtask "In Progress", report which stage's spawn failed and the error. Do NOT execute the stage in-context. A same-context QA pass of your own work is worthless evidence. **Sole exception:** the Explore stage (read-only Glob/Grep/Read) may be performed in-context — it produces no work product to self-grade.

#### Stage 1: Architecture
- Spawn `product-architecture-spec` via Task, CWD = `$WORKTREE_PATH`.
- Pass FULL subtask details from Backlog. If the plan is detailed, validate it and identify file paths; if thin, enhance with data models, Go interfaces/signatures, migration shape, edge cases, error handling.
- The architect self-validates (AC coverage, edge cases, test strategy).
- **Update the Backlog task** via `mcp__backlog__task_edit` to populate `implementation_plan`.
- **Checkpoint:** `ARCHITECTURE_DONE`

#### Stage 2: Explore Verification
- Spawn `Explore`, passing `$WORKTREE_PATH` as CWD.
- Verify referenced files exist; Go package layout, imports, and placement match; the `internal/platform` seams (config, `WithinTenantTx`, queue, audit) are used correctly.
- **For any Go signature/API change:** grep callers across `cmd/` and `internal/` and enumerate every caller + test that must be updated as a deliverable of this subtask.
- **For any UI-touching subtask:** grep `e2e/` (smoke + topology) for the changed routes/testids/labels and enumerate every matching spec as a required-update deliverable.
- If gaps found, update the Backlog task's implementation_notes.
- **Checkpoint:** `EXPLORE_DONE`

#### Stage 2.5: Test-Spec (`Test-first: yes` subtasks only)
- **Skip entirely if `Test-first: no`.** Run for logic-bearing subtasks.
- Spawn `product-qa-spec` (Mode A), CWD = `$WORKTREE_PATH`, passing the architect's Test Specs table.
- It transcribes each Test Spec row into a runnable Go test (unit / table-driven / DB-backed integration). DB-backed suites need `make dev-db` up and run via the `make test-rls|test-queue|test-audit` harness or `go test` with the env-gated DSN.
- Run the suite to confirm the new tests **FAIL for the right reason** — assertion / not-implemented, NOT compile or setup errors.
- Commit the red tests inside the worktree.
- **Checkpoint:** `TESTS_RED`

#### Stage 3: Execution
- Spawn `product-executor`, CWD = `$WORKTREE_PATH`. Pass COMPLETE Backlog subtask details.
- For `Test-first: yes` subtasks, drive the Stage 2.5 red tests to green without weakening, skipping, or deleting any (if a test itself is wrong, flag it). Author no *new* tests (QA adds those in Stage 4).
- **Migrations:** goose is timestamp-ordered (no Alembic-style `down_revision` to hand-set). Scaffold with `make migrate-create name=<slug>` **inside the worktree** so the timestamp is fresh relative to `main`; every tenant-owned table is born with `tenant_id` + the FORCE-RLS policy template; write a working `-- +goose Down`. The gateway applies migrations on deploy — a bad migration crash-loops the shared dev backend, so verify `make migrate-up` + the reversibility round-trip locally first.
- The executor handles all reads/edits/creation inside the worktree, commits, and (per its FIRST/MIDDLE/FINAL `Order` logic) handles `git push` and the PR draft/ready transitions.
- After the executor finishes, run the relevant suites inside the worktree:
  ```bash
  (cd "$WORKTREE_PATH" && go build ./... && go vet ./... && go test ./...)
  (cd "$WORKTREE_PATH" && make test-rls && make test-queue && make test-audit)   # DB-backed; needs `make dev-db`
  (cd "$WORKTREE_PATH" && pnpm -r typecheck && pnpm -r build)                     # SPAs
  ```
- **Checkpoint:** `EXECUTION_DONE`

#### Stage 4: QA Verification
- Spawn `product-qa-spec` (Mode B) in its default critique disposition (skeptical, anchors on acceptance criteria not the diff, cites evidence per verdict).
- Pass: acceptance criteria, implementation plan, changed files, Definition of Done.
- For `Test-first: yes` subtasks, confirm the Stage 2.5 AC tests are now green and still meaningful (would fail if behavior regressed), then *add* adversarial / edge / negative coverage (including a cross-tenant RLS refusal assertion for any new tenant-owned table).
- Backend: verify tests pass, model/schema/RLS correctness. Frontend: Playwright MCP visual verification against the deployed dev SPA once available.
- If issues found: spawn product-executor to fix, then re-verify.
- Update the Backlog task's implementation_notes with QA findings.
- **Checkpoint:** `QA_VERIFIED`

After each subtask, pick the next in dependency order and repeat stages 1–4.

### Phase 2: PR Lifecycle

The PR is managed by `product-executor` via the subtask `Order` field — the orchestrator does NOT manage PR state directly:

- Order = "1 of N (FIRST)" → executor pushes the branch and creates the **DRAFT** PR (draft PRs skip `dev-env.yml`, so the dev env isn't touched mid-story).
- Order = "K of N" (middle) → executor pushes only; PR stays draft.
- Order = "N of N (FINAL)" → executor pushes, runs `gh pr ready` (this fires `dev-env.yml` — see Phase 3.5).

The orchestrator never runs `git checkout -b`, `gh pr create`, or `gh pr ready` directly.

### Phase 3: CI & CodeRabbit

After the FINAL subtask's Stage 4 completes:

1. **Monitor CI** — poll `gh pr checks [PR_NUMBER]` every 270 s (CI Monitoring Protocol below). Wait for the aggregate **`CI`** check green.
2. **Review CodeRabbit comments** once `CI` is green:
   ```bash
   gh pr view [PR_NUMBER] --comments
   gh api repos/{owner}/{repo}/pulls/{PR_NUMBER}/comments
   ```
   - Agree → fix in the worktree, commit, push. Disagree / N/A → skip.
3. After CodeRabbit fixes pushed, monitor CI again until green.
4. **Proceed to Phase 3.5** — do NOT move subtasks to "Done" or emit completion until the deploy gate is green.

### Phase 3.5: Story-Level Deploy Gate

Runs **once per story**, after `CI` is green and CodeRabbit is addressed. This is the second, story-altitude pass: it verifies the *assembled feature against the original objective*, not per-subtask diffs. It is **not** an agent-driven browsing pass with a lease/label handshake — `dev-env.yml` fires automatically when the PR is marked ready and deploys the whole coherent fleet to the shared dev env, running smoke + topology (and any milestone demo script) E2E in CI.

1. **Read the original acceptance criteria** from the Obsidian parent story (the original objective — NOT the possibly-edited subtask ACs), plus the milestone's "Ships when true" bullets from the build plan.
2. **Ensure the deploy gate fires.** Marking the PR ready (Phase 2 FINAL) triggers `dev-env.yml` (event `ready_for_review`). If it didn't fire (e.g. the PR was already ready), re-trigger by pushing a commit, or dispatch manually:
   ```bash
   BRANCH="$(git -C "$WORKTREE_PATH" rev-parse --abbrev-ref HEAD)"
   gh workflow run dev-env.yml --ref "$BRANCH"
   ```
   - **Freshness check (mandatory):** `git -C "$WORKTREE_PATH" fetch origin` — if `origin/main` has commits not in the branch, `git merge origin/main`, push, and let `CI` + the deploy gate re-run on the merged head. A base missing main's migrations crash-loops the shared dev backend (the gateway is the migrator).
3. **Wait for the `dev-env.yml` run** on this branch and watch it to conclusion (it serializes behind the `dev-preview-shared` concurrency lock; a queued run may wait for a prior one):
   ```bash
   RUN_ID="$(gh run list --workflow dev-env.yml --branch "$BRANCH" --limit 1 --json databaseId -q '.[0].databaseId')"
   gh run watch "$RUN_ID" --exit-status   # or poll `gh run view "$RUN_ID" --json status,conclusion` per CI Monitoring Protocol
   ```
   A green run means: fleet deployed, gateway migrated (health-gate), all 8 backends up (fleet-gate), dev DB reset+seeded, and the **smoke + topology E2E passed** — including cross-tenant isolation.
4. **Spawn `product-qa-spec`** (default critique disposition) to verify **each** original acceptance criterion against the green run:
   - Backend / data / RLS ACs → cite the passing CI job or E2E assertion (topology proves cross-tenant refusal; the milestone demo script — e.g. M3-11 — proves the wedge flow).
   - **UI ACs (rendered surfaces)** → drive the deployed dev SPA read-only with the standalone Playwright MCP, authenticated as the seeded user, and capture each touched surface (including interactive states) to `$WORKTREE_PATH/.ralph/fidelity/<surface>-<state>.png`. Diff live `getComputedStyle` / layout against the Claude Design **prototype** (`.dc.html`, deployed to Netlify — confirm the file→surface mapping first) and the design system. A delta citing a design-system rule or a prototype CSS rule is a real fail; uncited taste is advisory → escalate to the user, never bounce the executor.
   - A holistic "looks done" is not allowed — every AC needs its own evidence (a passing job/assertion, or a screenshot).
5. **Fix loop (cap 2 cycles):** batch ALL fails (failed ACs + real fidelity deltas) into one report → spawn `product-executor` to fix inside the worktree → push (this re-fires `dev-env.yml` on `synchronize`) → **wait** for the new run → re-verify only the failed ACs / unresolved deltas. Every bounce must cite an AC id, a design-system rule, or a prototype CSS rule. After **2** cycles, stop and **escalate remaining fails to the user** — each dev-env run holds the shared env and is expensive.
6. **Log** to the story's `… QA Debate Log.md` under `## Post-Deploy QA — <date>`: per-AC verdict + evidence (CI job / E2E assertion / screenshot), fidelity delta references (for UI stories), fix cycles used, the `dev-env.yml` run id(s), and any design-system citations / advisory notes.
7. **On PASS** (all original ACs pass on a green `dev-env.yml` run, no unresolved bounces, and — for UI stories — fidelity evidence exists with no unresolved real deltas):
   - Move all subtasks to "Done": `For each subtask ID: mcp__backlog__task_edit(id=task_id, status="Done")`
   - Output `<promise>ALL_TASKS_COMPLETE</promise>`.

   **On unresolved fails after 2 cycles (or a red `dev-env.yml` run that fix-loops can't green):** leave subtasks "In Progress", do NOT emit completion, surface the escalation to the user.

### Phase 4: Worktree Cleanup

After the PR merges (manual, or via `/gh-merge-pr`), run `/post-merge-cleanup <STORY-ID>` — it removes the worktree + branch, marks subtasks Done in Backlog, and archives the story in Obsidian (`User Stories/Archive/<Mn>/`). Or manually:

```bash
git -C "$MAIN_CHECKOUT" worktree remove "$WORKTREE_PATH"
git -C "$MAIN_CHECKOUT" branch -d "$BRANCH"
```

`dev-env-cleanup.yml` scales all 11 dev services to 0 on PR close automatically.

---

## Session Continuity

Each worktree maintains its own `.claude/handoff.yaml` at `$WORKTREE_PATH/.claude/handoff.yaml`.

### On Start
Check the worktree's handoff first; if it exists and is recent, restore from it.

### During Work (every 2–3 subtasks)
Update `.claude/handoff.yaml`:

```yaml
timestamp: <iso>
workflow: ralph
story_id: M3-04
branch: feature/m3-04-validation-v1
worktree_path: /Users/samosipov/Downloads/invoice-os/.claude/worktrees/m3-04
pr_number: null   # set after the FIRST subtask creates the PR
current_subtask: M3-04-02
stage: execution   # architecture | explore | test-spec | execution | qa-verify | deploy-gate
completed_subtasks:
  - M3-04-01
blockers: []
decisions: []
```

### On Compaction
A PreCompact hook (`.claude/hooks/pre-compact.sh`) auto-saves state. After compaction, READ the handoff to continue.

---

## CI Monitoring Protocol

Use this whenever waiting for CI or the deploy gate after a push.

### Poll every 270 s (the single poll constant everywhere in this pipeline)
```bash
gh pr checks [PR_NUMBER]
```

### Decision tree

**1. Any check FAILED?**
```bash
gh run view [RUN_ID] --log 2>&1 | tail -60
gh run cancel [RUN_ID]   # optional
# Fix in the worktree, commit, push — CI (and, on a ready PR, dev-env.yml) restart on push
git -C "$WORKTREE_PATH" add ... && git -C "$WORKTREE_PATH" commit -m "fix: ..." && git -C "$WORKTREE_PATH" push
```

**2. The aggregate `CI` check green?** → proceed to CodeRabbit, then to the Phase 3.5 deploy gate.

**3. The `dev-env.yml` run green?** → the deploy gate passed (fleet up + reset/seed + smoke + topology). Proceed to Phase 3.5 step 4 (per-AC verification).

### Get the current run IDs
```bash
gh run list --branch "$BRANCH" --workflow ci.yml       --limit 1 --json databaseId,status,conclusion -q '.[0]'
gh run list --branch "$BRANCH" --workflow dev-env.yml   --limit 1 --json databaseId,status,conclusion -q '.[0]'
```

---

## Completion Rules

1. **Local tests green ≠ done** — the aggregate `CI` check must pass on the PR.
2. **A green `dev-env.yml` run IS a blocker** — completion requires the deploy gate (deploy fleet → migrate → fleet-gate → reset/seed → smoke + topology E2E) to conclude green on the PR head.
3. **Subtask status transitions**: To Do → In Progress (Phase 0.5 / 0.6c) → Done (ONLY after Phase 3.5 passes).
4. **Story-Level Deploy Gate (Phase 3.5) must pass** before completion — all original acceptance criteria verified against the green `dev-env.yml` run (plus, for UI stories, fidelity evidence vs the prototype), no unresolved bounces.
5. Output `<promise>ALL_TASKS_COMPLETE</promise>` ONLY after Phase 3.5 passes AND subtasks are moved to "Done".

---

## Anti-Patterns

| Anti-Pattern | Correct Approach |
|-------------|------------------|
| Lead doing file edits directly | Delegate to product-executor / Explore subagents |
| Skipping architecture / explore / QA stages | Every stage is mandatory (Test-Spec mandatory for `Test-first: yes`, skipped only for `Test-first: no`) |
| Implementing a logic-bearing subtask before its AC tests are red | Author failing AC tests in Stage 2.5 first (`TESTS_RED`), then drive them green |
| Weakening/skipping/deleting a red test to force green | Fix the implementation, not the test; if the test is wrong, flag it |
| Executor authoring its own test coverage | Executor drives the architect's reds to green; QA owns new coverage (Stage 4) |
| Adding a tenant-owned table without RLS + a cross-tenant refusal test | Every tenant table born with `tenant_id` + FORCE-RLS policy; QA adds the adversarial refusal assertion |
| Hand-setting a goose migration order or shipping an untested `Down` | `make migrate-create` in the worktree; verify `migrate-up` + reversibility round-trip locally (the gateway migrates on deploy) |
| Querying as superuser to "get past" RLS | Run inside `WithinTenantTx` as `invoice_app`; RLS isolation is the product |
| Working in main checkout | Always work in `$WORKTREE_PATH`; main is the user's space |
| Running multiple stories in parallel against dev | The dev env is single-shared/single-agent — run stories serially (dev-env.yml queues, last deploy wins the env) |
| Mixing subtasks from different stories on one branch | One story (build-plan task) per branch, always |
| Title-parsing Backlog tasks to find a story's subtasks | Use the `story:<slug>` label |
| Erroring on a story with zero Backlog subtasks | Zero subtasks + Objective/Core ACs (or a build-plan row) = BASIC → run Phase 0.6 |
| Blocking on the user during Phase 0.6 | Unattended disposition: defaults + conservative options, recorded in ## Decisions / QA Debate Log; user reviews via PR |
| Architect inventing scope while expanding a basic story | Every derived AC/subtask traces to Objective/Core AC/"Ships when true"; Out-of-scope leakage = mechanical fail |
| Orchestrator running `git checkout -b` / `gh pr ready` | Use `git worktree add -b`; the executor handles push + PR transitions |
| Deploying a draft PR to dev | Draft PRs skip `dev-env.yml` by design — deploy fires on `gh pr ready` (Phase 2 FINAL) |
| Outputting ALL_TASKS_COMPLETE before `CI` + `dev-env.yml` are green | Both gates are blocking (Completion Rules) |
| Sleeping/polling tightly for CI | Poll every 270 s (the single poll constant) |
| Bouncing the executor on uncited taste | UI fails must cite a design-system rule or a prototype CSS rule; pure taste is advisory → escalate |
| Grinding the deploy-gate fix loop past 2 cycles | Cap at 2; escalate unresolved fails to the user (each redeploy holds the shared env) |
| Not cleaning up the worktree after merge | Run `/post-merge-cleanup <STORY-ID>` |
