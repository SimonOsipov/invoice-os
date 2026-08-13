# Ralph Workflow — ASComply Africa (invoice-os)

## Overview

Automated execution of a single build-plan task through per-subtask quality gates (Architecture → Explore → Test-Spec* → Execution → QA Verify; *Test-Spec runs only for logic-bearing `Test-first: yes` subtasks), followed by a story-level deploy gate (Phase 3.5) that verifies the assembled feature against the original objective via the **`dev-env.yml`** run on the PR (deploy the whole fleet to the PR's own ephemeral Railway environment — created by `dev-env.yml`'s `prepare-env` job as a fork of `development`, M4-23; its database is bootstrapped, migrated and seeded at gateway boot, M4-21-04 — → fleet health → smoke + topology E2E). Runs in an isolated git worktree so the main checkout stays clean.

**Story unit:** one **build-plan task** = one story = one branch = one PR (e.g. `M3-04` "Validation v1"). RALPH decomposes it into sub-subtasks (`M3-04-01`, …) internally. This matches exactly how M1/M2 shipped (`task-20` → `task-20.1–.4`).

Stories arrive in one of two states: **basic** (intent-only — Objective, Core ACs, Out of Scope; produced by `/pm-story`, or by `/pm-epic` for one story of a researched epic; zero Backlog subtasks) or **pre-planned** (Backlog subtasks already exist, or the Obsidian story is already architect-level like the M1/M2 stories). Basic stories are planned **in-run** by Phase 0.6; pre-planned stories skip Phase 0.6.

**Invocation**: `/ralph <STORY-ID>` (e.g., `/ralph M3-04`). Each invocation runs one story, in its own worktree and branch. Since M4-23, every PR deploys to its **own ephemeral Railway environment** (`dev-env.yml` concurrency is keyed per-PR — `dev-preview-<PR#|ref>` — not a shared lock), so multiple `/ralph` invocations MAY run concurrently, each verifying its own story against its own isolated environment with no cross-story interference.

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
Never guess, assume, or reduce functionality. When something is unclear, what you do depends on the run:

- **Interactive run** (the user is present and you are not inside a `/ralph` phase) — ask.
- **Unattended run** — which is every phase of `/ralph` — take the conservative default: the option closest to the story's explicit text, smaller scope. Record it in `## Decisions` and log it prominently. **Never block on the user** — with exactly one exception, the critical-fork gate in Phase 0.6d.

Neither path licenses a silent feature cut. This rule holds in every phase, not only Phase 0.6.

| WRONG | RIGHT |
|-------|-------|
| "The rules table is empty, so skip validation" | "The rules table is empty, so seed the MBS v1 rule-set" |
| "RLS makes this hard, let's query as superuser" | "RLS is the point — run inside `WithinTenantTx` as `invoice_app`" |
| "The CEL escape hatch is complex, drop it" | "Implement the CEL rule type properly with a golden test" |

### 3. CI Gate + Deploy Gate (BLOCKING)
Cannot output `ALL_TASKS_COMPLETE` until BOTH (a) the aggregate **`CI`** check passes on the PR AND (b) the **`dev-env.yml`** run on the PR concludes **green** (see Phase 3.5). The deploy gate deploys the whole fleet to the PR's own ephemeral Railway environment and runs smoke + topology E2E — it is required for completion.

```bash
gh pr checks [PR_NUMBER]
# Aggregate per-push check (ci.yml): CI  — rolls up: go, frontend, clean-clone,
#   migrations, docker-canary, rls, queue, audit (each gated on `changes`).
# Deploy gate (dev-env.yml, fires when the PR is marked ready): await-ci +
#   prepare-env (parallel, creates — or reuses — the PR's own ephemeral Railway
#   env by forking `development`) → deploy-gateway (migrator) → health-gate →
#   deploy-context ×7 + deploy-spas ×3 → fleet-gate → e2e (smoke + api +
#   topology + demo). Required for completion.
```

Three other Railway workflows exist and are NOT part of this gate:
`dev-env-teardown.yml` (deletes the PR's environment on close), `dev-env-sweeper.yml`
(daily backstop that reaps orphaned per-PR environments), and `railway-invariants.yml`
(asserts on every PR — drafts included — that Railway's PR Environments stay OFF and no
deployment trigger exists). Environments are torn down by CI; never reach for destructive
Railway MCP calls to clean one up.

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
Reference before making changes to related areas. Run `ls docs/` for the authoritative list — every file in it is a Stage 4 sweep target.
- `docs/migrations.md` — goose harness, roles, gateway-as-migrator, GUC helper contract
- `docs/deploy-model.md` — Railway dev deploy model, scale-to-zero
- `docs/topology-e2e.md` — the unified dev-env deploy+verify workflow, prerequisites, secrets
- `docs/add-a-service.md` — config-as-code recipe for a new Railway service
- `docs/e2e-convention.md` — how the browser suites are organized, and what a spec may assume about the database
- `docs/mock-app-adapter.md` — the mock Access Point Provider's reserved TINs and scripted outcomes

Design references (for UI stories): the Claude Design **prototype** project `6269a212-5677-4abd-b8a9-08aad10b1c65` (`InvoiceOS Africa.dc.html` = landing, `Platform.dc.html` = `frontend/app`, `Ops Console.dc.html` = `frontend/ops-console`; all deployed to Netlify) and the **design system** project `999b7034-9f23-43d4-9229-51af7dde9f62`.

---

## Workflow

### Phase 0: Story Resolution

1. **Validate the story arg.** `/ralph` requires exactly one story reference. Two forms are accepted:
   - a **sysmap feature** — `F-192`, or its slug (`notifications.notice-failure`). **One feature = one story = one branch = one PR.** This is the current form; a feature is where new work is decided (`/pm-review` step 6.5), so it is the unit `/ralph` builds.
   - a **build-plan task ID** — `M3-04`. The historical form, kept because stories M1–M5 shipped under it and their Obsidian files are the record.

   Error and exit if missing. If the arg resolves as **both** a feature and a build-plan row, prefer the feature and say so — the map is the live record.
2. **Read the story source.** *Feature form:*
   - `mcp__sysmap__sysmap_feature_show {"feature": "<ARG>"}`. This IS a **basic** story and needs no Obsidian file: the feature's `name` + `description` are the Objective, its **acceptance criteria are the Core ACs**, its `screen` says where the surface lives, and `depends_on` names what must already exist. Set `PLANNING_REQUIRED=true` and `STORY_SOURCE=sysmap`.
   - Refuse to build a feature whose status is not `planned` or `building` — anything else already has code, and `/ralph` would be re-implementing it. Say which status you found.
   - Set its status to `building` before Phase 1 (`sysmap_feature_status_set`), so a concurrent invocation and the queue both see it is in flight.

   *Build-plan form:*
   - `mcp__obsidian-mcp-tools__get_vault_file` matching `Simon Vault/Projects/ASComply Africa/User Stories/<Mn>/<STORY-ID>*.md` (use `list_vault_files` against `.../User Stories/<Mn>/` to disambiguate; also check `.../User Stories/Archive/<Mn>/`). Set `STORY_SOURCE=obsidian`.
   - If no Obsidian story exists yet, fall back to the build plan: read `Simon Vault/Projects/ASComply Africa/Build Plan — 0 to MVP.html`, find the `<STORY-ID>` row (Task / Layer / Size / Depends / the milestone's "Ships when true"), and treat that row + the milestone goal as a **basic** story (set `PLANNING_REQUIRED=true`).
3. **Derive the branch slug.** If the story has a `## Branch Strategy` section, use it. Otherwise synthesize `BRANCH=feature/<lowercase-story-id>-<kebab-title>` (e.g. `feature/m3-04-validation-v1`). For a feature, the id is its tag lower-cased and the title is its name: `F-192 Notice a submission failure` → `feature/f-192-notice-a-submission-failure`.
4. **Query Backlog for subtasks**:
   ```
   mcp__backlog__task_list({ labels: ["story:<lowercase-story-id>"], status: "To Do" })
   ```
5. **Refuse a story that still carries unanswered questions**: if the Obsidian story has a `## Blocking Questions` section with any entry left in it, stop immediately and print those entries. Phase 0.6d wrote them and a previous run halted on them. Answering them is the only way forward — never default them and never delete the section to proceed. (`## Open Questions` is a different, non-blocking section in full-mode PRDs. Do not treat it as this gate.)
6. **Classify the story state**:
   - **`STORY_SOURCE=sysmap`** → always **BASIC**. A feature carries intent (name, description, acceptance criteria) and never subtasks — sysmap is not a task tracker. Its Backlog subtasks, if any, are labelled `story:f-192`.
   - **Zero subtasks + Objective/Core ACs present (or build-plan fallback)** → **BASIC** → set `PLANNING_REQUIRED=true`; topo-sort + plan-logging happen at the end of Phase 0.6.
   - **Subtasks returned (or an architect-level Obsidian story with a Subtasks section)** → **PRE-PLANNED** → topo-sort by `dependencies` → linear execution order, log the plan, skip Phase 0.6.
   - **Neither** → error: "story <ID> is neither basic (no Objective/Core ACs, not in the build plan) nor pre-planned (no Backlog subtasks) — run /pm-story first, or /pm-epic if the topic needs several stories."

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

4. **Symlink the repo `CLAUDE.md` into the worktree** (best-effort, never fails the run). `CLAUDE.md` is gitignored (`.gitignore:43`), so `git worktree add` never creates it — every subagent running with `CWD=$WORKTREE_PATH` would otherwise see NO project instructions at all (verified absent in `m4-21`, `m4-06`, `m4-08-map-step` before this fix; Decision `[claude-md-symlink]`):
   ```bash
   [ -f "$MAIN_CHECKOUT/CLAUDE.md" ] && ln -sfn "$MAIN_CHECKOUT/CLAUDE.md" "$WORKTREE_PATH/CLAUDE.md"
   ```
   A **symlink**, not a copy — a copy silently goes stale the moment the source is edited. A missing source file is a silent no-op, never a failure.

5. **Bootstrap dependencies** — Go needs no per-worktree install (the module cache is shared); the SPAs and the DB-backed tests do:
   ```bash
   (cd "$WORKTREE_PATH" && pnpm install --frozen-lockfile) &
   # Local dev Postgres (compose) → bootstrap roles → migrate → seed. Required for the
   # RLS / queue / audit suites (make test-rls|test-queue|test-audit). Each worktree runs
   # its OWN stack on a distinct host port (DEV_DB_PORT, M4-21-01) — check for an
   # already-running stack first (`docker compose ps` / `lsof -iTCP -sTCP:LISTEN`), then
   # pick an unused port, e.g. `DEV_DB_PORT=5433 make dev-db`. NEVER reuse or tear down
   # another worktree's stack — teardown of THIS story's own stack is owned by
   # `/post-merge-cleanup` (Step 3b), not here (Decision [worktree-db-isolation]). Skip
   # entirely if this story touches neither Go DB code nor migrations.
   (cd "$WORKTREE_PATH" && DEV_DB_PORT=<unused-port> make dev-db) &
   wait
   ```

6. **All subsequent shell commands run inside `$WORKTREE_PATH`.** Pass it as CWD to subagents.

7. **Move all subtasks to "In Progress"** (skip if `PLANNING_REQUIRED` — Phase 0.6c does this after creating them):
   ```
   For each subtask ID: mcp__backlog__task_edit(id=task_id, status="In Progress")
   ```

### Phase 0.6: Planning (basic stories only)

Runs ONLY when Phase 0 set `PLANNING_REQUIRED=true`. All planning runs **inside the worktree** so every file reference is written against the code that will actually be modified.

#### a. Architecture — finalize the story
- Spawn `product-architecture-spec` (Opus), CWD = `$WORKTREE_PATH`, passing the FULL basic story content (or build-plan row + milestone goal) and its Obsidian path. Instruct it to operate per its "Expanding a Basic Story" section.
- **Diff every new control against its siblings.** When the story adds a control to an existing bar, panel or surface, compare its visibility and disabled treatment against the controls already there before the plan is final. A sibling's shipped decision is the spec; contradicting it on the same surface is a defect, not a choice. INVED-02 shipped a hidden button beside disabled ones and the decision was reversed after it was built.
- **Re-measure every fact the story asserts.** A basic story states facts, not only goals: a root cause, a mechanism, a count, another PR's shipped state, a precedent's preconditions, whether the prescribed fix can work. Treat each one as a hypothesis. Re-measure it inside the worktree. Paste the command and its output into `## Decisions`, one entry per fact, tagged `premise — verified` or `premise — CORRECTED: story said X, actually Y`. Cite what you ran, never the conclusion alone. BUG-02 asserted three mechanisms and all three proved wrong; APPR-04's ground truth was wrong in thirteen places. When a corrected premise carries the story's scope, Phase 0.6d stops the run for it.
- It rewrites the story file in Obsidian to final state: system design, **## Implementation Subtasks** (`[<STORY-ID>-NN]` with Category / Dependencies / Description / Acceptance Criteria / Order / Test-first classification + Test Specs tables for `Test-first: yes`), and a **## Decisions** section appending every assumption it made where the story was silent.
- **Traceability rule (hard):** every derived AC and subtask must trace to the Objective or a Core AC (or the milestone's "Ships when true"). Nothing in Out of Scope may appear in any subtask.
- **Checkpoint:** `STORY_FINALIZED`

#### b. QA-Verify debate — UNATTENDED disposition
- Run the `/qa-verify` protocol against the finalized story: `product-qa-spec` critic (Sonnet) vs `product-architecture-spec` architect (Opus), ≤3 rounds, citation-required — including the Intent-Integrity checks (AC→Objective traceability, Out-of-scope leakage = mechanical).
- Use the protocol's **Unattended Mode** disposition table: mechanical+resolved+cited → auto-apply; judgment / unresolved / uncited → **conservative default** (option closest to the story's explicit text, smaller scope) + prominent log entry. NEVER block on the user here — Phase 0.6d re-tests these defaults and is the only step that may stop for one.
- **A finding that falsifies a premise is not a wording fix.** Record it in `## Decisions` as `premise — CORRECTED`, then let Phase 0.6d judge it. Repairing the sentence and auto-applying it is how M4-03 buried one.
- Append the run to the story's `… QA Debate Log.md`, marking each disposition `auto-applied | conservative-default (reason)`.
- **Checkpoint:** `PLAN_VERIFIED`

#### c. Subtask generation — Backlog tasks
- Execute the `/subtask-generator` logic for the finalized story, passing the story explicitly: spawn parallel `product-architecture-spec` agents → one Backlog task per subtask (description, acceptance_criteria, implementation_plan, references, labels `["story:<slug>", ...]`), then wire dependencies between the created task IDs.
- Move all created subtasks to "In Progress".
- Topo-sort by dependencies → linear execution order. **Log the plan**: story title, branch slug, ordered subtask list, count of Decisions + conservative-default dispositions.
- **Checkpoint:** `SUBTASKS_READY`

#### d. Critical-fork gate — the ONE place the run stops

A conservative default is right for a technical fork and wrong for a policy one. It is also wrong for a fact that turned out false. Before any code exists, test every entry in `## Decisions` — the QA-debate conservative defaults and Phase 0.6a's `premise —` entries included — against four questions:

- Does it decide **who is allowed** to do something?
- Does it decide **what the system claims** to an outside party: the authority, the customer, the audit record?
- Does it let the system **silently override a human's action**?
- Does a **corrected premise** take away something this story's scope needs: a shipped screen, an endpoint, a merged PR, a seeded row?

Any "yes" makes that fork **critical**. Everything else proceeds untouched. The test is deliberately narrow — expect zero to two per story. BUG-07 tripped two of twenty-five: who may mark an invoice resolved outside the system, and whether the authority's verdict wipes that mark. M4-03 would have tripped the fourth: its QA debate recorded "PR #54 is OPEN, not shipped", graded it MECHANICAL, softened three provenance labels, and shipped a feature the user could not reach.

**First, check whether the user already answered it.** A story that came from an epic may inherit decisions taken once for the whole epic. Look in the story's own epic folder (`User Stories/<EPIC>/`) for a file whose frontmatter carries `type: decision-log`; fall back to a filename matching `*Decision Log*.md`. Most stories have no such file — then this paragraph does nothing and the gate proceeds exactly as below.

When one exists, read it and, for each critical fork:

- **The log answers this same question** → it is not blocking. Record it in `## Decisions` as `user — <the choice> (<decision-log file>, <its decided: date>)`. Cite the file and the date, never the choice alone, so a reader can check the reasoning and the rejected alternatives.
- **The log is silent on it** → still blocking. Ask it.
- **Your default contradicts the log** → blocking, and say so in the question. An epic-level decision that a story wants to overturn is exactly what the user must see.

Match on the question, not on keywords. A log entry that merely mentions the same nouns has not answered the fork — treat that as silent and ask.

With no critical fork left, continue to Phase 0.6e. With one or more:

1. Write each into the story file under `## Blocking Questions`: the question in one line, the default you will take if unanswered, and the alternative. Use that exact heading — `## Open Questions` already exists as a non-blocking PRD section and must not be reused here.
2. Print that same list and **halt the run.** This is the only place `/ralph` waits for the user.
3. **Checkpoint:** `AWAITING_ANSWERS`

When the user answers, record each choice in `## Decisions` tagged `user — <what they chose>`, delete the `## Blocking Questions` section, then continue. Do not re-plan.

**Stated boundary:** the gate runs where planning runs. A pre-planned story skips Phase 0.6 and therefore skips this gate, so critical forks already defaulted inside an architect-level story are not caught here.

#### e. Decisions surfacing (non-blocking)
- When spawning the FIRST subtask's executor (Phase 1), instruct it to include in the draft PR description: the story's **## Decisions** section (PM defaults + architect assumptions + conservative-default dispositions + Phase 0.6a's `premise —` entries) and a pointer to the QA Debate Log. Phase 0.6d already cleared every critical fork, so this is a review surface, not a gate — the run does NOT wait for input; completion gates remain CI + Phase 3.5.

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

**"No honest oracle exists" is a finding, not a waiver.** When a subtask is classified `Test-first: no` because no test can see the failure — not because the work is trivial — record it in the story's `## Decisions` and name in the PR body which Phase 3.5 evidence artifact stands in for the missing test. The user then reads "this subtask has no test oracle, its only evidence is a screenshot" at the first human gate, instead of discovering it after the defect ships. BUG-03-05 is the case: the only bug-03 subtask with no red commit, and the one that shipped the bug.

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
- **For any change to a JSON wire shape**, extend that grep to `e2e/` and the SPA wire mirrors (`frontend/*/src/lib/*.ts`). A response struct has hand-maintained TypeScript copies that no compiler links to it: adding a Go field does not break `pnpm -r typecheck` on a mirror that merely lacks it, so the per-subtask suite cannot catch the drift. Enumerate every mirror as a deliverable — `e2e/api/client.ts` is the one that gets forgotten.
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
- **Migrations:** goose is timestamp-ordered (no Alembic-style `down_revision` to hand-set). Scaffold with `make migrate-create name=<slug>` **inside the worktree** so the timestamp is fresh relative to `main`; every tenant-owned table is born with `tenant_id` + the FORCE-RLS policy template; write a working `-- +goose Down`. The gateway applies migrations on deploy — a bad migration crash-loops the PR's own environment's backend, so verify `make migrate-up` + the reversibility round-trip locally first.
- The executor handles all reads/edits/creation inside the worktree, commits, and (per its FIRST/MIDDLE/FINAL `Order` logic) handles `git push` and the PR draft/ready transitions.
- After the executor finishes, run the relevant suites inside the worktree:
  ```bash
  (cd "$WORKTREE_PATH" && go build ./... && go vet ./... && go test ./...)
  (cd "$WORKTREE_PATH" && make test-rls && make test-queue && make test-audit)   # DB-backed; needs `make dev-db`
  (cd "$WORKTREE_PATH" && pnpm -r typecheck && pnpm -r build)                     # SPAs
  # 2265 unit tests, ~13s. `pnpm -r test` alone would launch Playwright, because
  # e2e's `test` script IS the browser suite — hence the exclusion and test:unit.
  (cd "$WORKTREE_PATH" && pnpm -r --filter '!@invoice-os/e2e' test && pnpm --filter @invoice-os/e2e test:unit)
  ```
  Run them yourself. Do not accept a subagent's report of a suite as the suite's
  result: BUG-06 had two subagents report 1466 unit tests from the main checkout
  when the branch had 1489, and METR-01's subtask 05 reported all tests passing
  while one was failing and red was already pushed.
- **Checkpoint:** `EXECUTION_DONE`

#### Stage 4: QA Verification
- Spawn `product-qa-spec` (Mode B) in its default critique disposition (skeptical, anchors on acceptance criteria not the diff, cites evidence per verdict).
- Pass: acceptance criteria, implementation plan, changed files, Definition of Done.
- For `Test-first: yes` subtasks, confirm the Stage 2.5 AC tests are now green and still meaningful (would fail if behavior regressed), then *add* adversarial / edge / negative coverage (including a cross-tenant RLS refusal assertion for any new tenant-owned table).
- Prove every AC test can fail. Change one source line so the behaviour breaks. Run that test. Record `<file:line changed> -> FAIL <TestName>` in the QA findings, one row per AC. A test that stays green under its own mutation does not prove its AC. Report that test as a QA failure. Mode-B tests need this most: nothing gave them a red phase.
- When a test asserts over a collection, assert the collection is not empty. An empty collection satisfies every assertion inside the loop.
- Prove any scan that reports an ABSENCE can still find something. A grep, a source walk or a forbidden-string guard that stops matching returns zero hits, and zero hits reads exactly like a clean repo. Defend it two ways, both already used here: a **control needle** that must be found (`filename_removed_test.go` searches for `func NewStore(` beside the banned symbol), and a **floor** on the population scanned (`envPosture.test.ts` requires at least 20 files). A scan asserting a POSITIVE — exactly N sites, this anchor exists — proves itself and needs neither. Offer no "zero hits" as evidence until you have shown that same command finding a planted hit. M4-04 burned five instruments this way, each blind to a different dimension, and every green was false.
- Re-read every comment and doc your change made false. Fix them in the same commit. "It still says what it said" is not the test. The test is whether it is still TRUE. Sweep three places, in cost order:
  1. Comments your diff did not edit, in files it did. `git diff main...HEAD -U15` lists them. BUG-02 swept all 12 sites it rewrote and still shipped a false 22P02 claim, which CodeRabbit caught.
  2. Comments in files you never opened. No diff shows these, and they cost the most. Name the FACT your change altered. Grep the whole tree for that fact. The 2026-07-28 per-PR database reset touched 8 files, none of them a test, and left 25 e2e comments describing the world it had just replaced.
  3. Every file in `docs/` — run `ls docs/` for the list. That same commit updated two of them and missed `docs/e2e-convention.md`, the file every spec cites.
  Treat your own Stage 2.5 future-tense notes as suspects. Treat any comment a deferral left describing unbuilt work the same way.
- State a shared fact in one place and cite that place. Do not copy the fact into every comment that depends on it. Twenty-five comments each holding a copy of "the dev database is never reset" is why one change produced twenty-five falsehoods.
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

Runs **once per story**, after `CI` is green and CodeRabbit is addressed. This is the second, story-altitude pass: it verifies the *assembled feature against the original objective*, not per-subtask diffs. It is **not** an agent-driven browsing pass with a lease/label handshake — `dev-env.yml` fires automatically when the PR is marked ready and deploys the whole coherent fleet to the PR's own ephemeral Railway environment, running smoke + topology (and any milestone demo script) E2E in CI.

1. **Read the original acceptance criteria** — NOT the possibly-edited subtask ACs. `STORY_SOURCE=sysmap`: the feature's acceptance criteria from `sysmap_feature_show`, which are standing invariants and are exactly what must hold on the deployed fleet. `STORY_SOURCE=obsidian`: the Obsidian parent story's original objective, plus the milestone's "Ships when true" bullets from the build plan.
2. **Ensure the deploy gate fires.** Marking the PR ready (Phase 2 FINAL) triggers `dev-env.yml` (event `ready_for_review`). If it didn't fire (e.g. the PR was already ready), re-trigger by pushing a commit, or dispatch manually:
   ```bash
   BRANCH="$(git -C "$WORKTREE_PATH" rev-parse --abbrev-ref HEAD)"
   gh workflow run dev-env.yml --ref "$BRANCH"
   ```
   - **The dispatch fallback is not equivalent to the PR gate.** A `workflow_dispatch` run
     targets `development`, not this PR's environment, so a green dispatch run does not
     prove the PR's fleet deploys. Use it only to diagnose; the gate is the `pull_request`
     run.
   - **`dev-env.yml` is paths-filtered** (`frontend/ packages/ e2e/ cmd/ internal/
     migrations/ db/ tools/prenv/ scripts/ci/`, go.mod/sum, Dockerfile, Caddyfile,
     package.json, pnpm-*, `.github/workflows/dev-env.yml`). `docs/**` and this file are
     NOT listed, so a docs-only PR never fires the gate at all — do not dispatch-fake it
     green; escalate to the user instead.
   - **Freshness check (mandatory):** `git -C "$WORKTREE_PATH" fetch origin` — if `origin/main` has commits not in the branch, `git merge origin/main`, push, and let `CI` + the deploy gate re-run on the merged head. A base missing main's migrations crash-loops the PR's own environment's backend (the gateway is the migrator).
3. **Wait for the `dev-env.yml` run** on this branch and watch it to conclusion (concurrency is keyed per-PR — `dev-preview-<PR#|ref>` — so this run does not queue behind any other story's deploy; only a second push to this SAME PR would supersede it):
   ```bash
   RUN_ID="$(gh run list --workflow dev-env.yml --branch "$BRANCH" --limit 1 --json databaseId -q '.[0].databaseId')"
   gh run watch "$RUN_ID" --exit-status   # or poll `gh run view "$RUN_ID" --json status,conclusion` per CI Monitoring Protocol
   ```
   A green run means: fleet deployed to the PR's own environment, gateway migrated (health-gate) and the DB bootstrapped + seeded fresh at boot (M4-21-04), all 8 backends up (fleet-gate), and the **smoke + topology E2E passed** — including cross-tenant isolation.
4. **Spawn `product-qa-spec`** (default critique disposition) to verify **each** original acceptance criterion against the green run:
   - Backend / data / RLS ACs → cite the passing CI job or E2E assertion (topology proves cross-tenant refusal; the milestone demo script — e.g. M3-11 — proves the wedge flow).
   - **UI ACs (rendered surfaces)** → drive the deployed dev SPA read-only with the standalone Playwright MCP, authenticated as the seeded user, and capture each touched surface (including interactive states) to `$WORKTREE_PATH/.ralph/fidelity/<surface>-<state>.png`. Diff live `getComputedStyle` / layout against the Claude Design **prototype** (`.dc.html`, deployed to Netlify — confirm the file→surface mapping first) and the design system. A delta citing a design-system rule or a prototype CSS rule is a real fail; uncited taste is advisory → escalate to the user, never bounce the executor.
   - **Assert the relationship, not the dimension.** A layout AC is satisfied by what the number encodes — gutter symmetry, containment, alignment to a sibling — never by the raw measurement. A width assertion passes on the very bug it should catch: a cap and its placement are two facts, and measuring the cap proves nothing about placement (BUG-03-05 shipped 32% dead space under a green `width <= 1080`). **This fires whenever the diff adds or changes a layout constant** — a width, a grid track, a clearance, an overflow, an alignment — not only when an AC names layout. Three of these shipped from stories whose ACs never mentioned layout: a 26px input, a label overflowing its pill, a 360px menu clearance under a 189.73px menu. **Measure widest first.** `e2e/topology/layout.ts` sweeps 2560 / 1920 / 1440 / 1280 and returns the numbers to attach. A cap strands only what the window gives it room to strand — the same defect leaves 588px at 1920 and 1228px at 2560 — and every other sweep in `e2e/` stops at 1280.
   - A holistic "looks done" is not allowed — every AC needs its own evidence (a passing job/assertion, or a screenshot).
5. **Fix loop (cap 2 cycles):** batch ALL fails (failed ACs + real fidelity deltas) into one report → spawn `product-executor` to fix inside the worktree → push (this re-fires `dev-env.yml` on `synchronize`) → **wait** for the new run → re-verify only the failed ACs / unresolved deltas. Every bounce must cite an AC id, a design-system rule, or a prototype CSS rule. After **2** cycles, stop and **escalate remaining fails to the user** — each dev-env run provisions/rebuilds a full 11-service environment from scratch and is expensive.
6. **Log** to the story's `… QA Debate Log.md` under `## Post-Deploy QA — <date>`: per-AC verdict + evidence (CI job / E2E assertion / screenshot), fidelity delta references (for UI stories), fix cycles used, the `dev-env.yml` run id(s), and any design-system citations / advisory notes.
7. **On PASS** (all original ACs pass on a green `dev-env.yml` run, no unresolved bounces, and — for UI stories — fidelity evidence exists with no unresolved real deltas):
   - Move all subtasks to "Done": `For each subtask ID: mcp__backlog__task_edit(id=task_id, status="Done")`
   - Output `<promise>ALL_TASKS_COMPLETE</promise>`.

   **On unresolved fails after 2 cycles (or a red `dev-env.yml` run that fix-loops can't green):** leave subtasks "In Progress", do NOT emit completion, surface the escalation to the user.

### Phase 4: Worktree Cleanup

After the PR merges (manual, or via `/gh-merge-pr`), run `/post-merge-cleanup <STORY-ID>` — it removes the worktree + branch, marks subtasks Done in Backlog, archives the story in Obsidian (`User Stories/Archive/<Mn>/`), and in step 8 rescans sysmap and moves the feature off `building`. That status move is what advances a `/ralph-goal` loop to its next step, so **a goal loop does not progress until post-merge has run.** Or manually:

```bash
git -C "$MAIN_CHECKOUT" worktree remove "$WORKTREE_PATH"
git -C "$MAIN_CHECKOUT" branch -d "$BRANCH"
```

Teardown is repo-side, in two layers (M4-23). `dev-env-teardown.yml` deletes the PR's whole ephemeral environment (Postgres included) via `environmentDelete` on `pull_request: [closed]` — proven by experiment to fire on an *unmerged* close (PR #68, control run 29660556112 `opened`, decisive run 29660575019 `closed`/`merged=false`). It is the fast path and is best-effort: `pull_request: closed` is documented as firing inconsistently. `dev-env-sweeper.yml` is the authority — daily cron `17 4 * * *` plus `workflow_dispatch`, reaping only on positive evidence that a PR is closed or merged, never on age, never `development`. **Known limitation: `schedule:` runs only from the default branch, so the cron itself is unprovable until this merges; before then only the dispatch path is exercisable.** A `workflow_dispatch` run against the persistent `development` environment is unaffected and stays up. See `docs/deploy-model.md`.

---

## Session Continuity

Each worktree maintains its own `.claude/handoff.yaml` at `$WORKTREE_PATH/.claude/handoff.yaml`.

### On Start
Check the worktree's handoff first; if it exists and is recent, restore from it.

### During Work (every 2–3 subtasks)

**Tie this to a checkpoint you already emit.** Write the handoff immediately after each `QA_VERIFY_DONE`, in the same step that moves the subtask on. Without that anchor this step does not happen: the `PreCompact` hook fires and stamps the file, but `story_id`, `stage`, `branch` and `completed_subtasks` sit at `null` — a handoff that records nothing is worse than none, because On Start restores from it and believes it.

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

**3. The `dev-env.yml` run green?** → the deploy gate passed (fleet up + migrate+seed at boot + smoke + topology). Proceed to Phase 3.5 step 4 (per-AC verification).

### Get the current run IDs
```bash
gh run list --branch "$BRANCH" --workflow ci.yml       --limit 1 --json databaseId,status,conclusion -q '.[0]'
gh run list --branch "$BRANCH" --workflow dev-env.yml   --limit 1 --json databaseId,status,conclusion -q '.[0]'
```

---

## Editing This File

An agent parses these instructions with no one to ask when a sentence is ambiguous. Write for that reader.

- **One word, one meaning.** Pick one verb per action and reuse it. Do not rotate `check` / `verify` / `confirm` / `ensure` for the same act — reserve `check` for a CI check, `assert` for a test assertion.
- **One instruction per sentence**, twenty words or fewer for a procedure. Long compound sentences are where agents drop a clause.
- **State each rule once**, in the phase that owns it, and reference it from anywhere else. A rule stated six ways is six rules to keep in sync.
- **Prefer a positive statement to a prohibition.** The Anti-Patterns table is for defects with no natural home in a phase — not a mirror of rules already stated above.

## Completion Rules

1. **Both gates blocking** — as defined once in **Core Rule 3** above: the aggregate `CI` check and the `dev-env.yml` run must both be green on the PR head. Local tests green ≠ done.
2. **Subtask status transitions**: To Do → In Progress (Phase 0.5 / 0.6c) → Done (ONLY after Phase 3.5 passes).
3. **Story-Level Deploy Gate (Phase 3.5) must pass** before completion — all original acceptance criteria verified against the green `dev-env.yml` run (plus, for UI stories, fidelity evidence vs the prototype), no unresolved bounces.
4. Output `<promise>ALL_TASKS_COMPLETE</promise>` ONLY after Phase 3.5 passes AND subtasks are moved to "Done".

---

## Anti-Patterns

| Anti-Pattern | Correct Approach |
|-------------|------------------|
| Lead doing file edits directly | Delegate to product-executor / Explore subagents |
| Skipping architecture / explore / QA stages | Every stage is mandatory (Test-Spec mandatory for `Test-first: yes`, skipped only for `Test-first: no`) |
| Weakening/skipping/deleting a red test to force green | Fix the implementation, not the test; if the test is wrong, flag it |
| Adding a tenant-owned table without RLS + a cross-tenant refusal test | Every tenant table born with `tenant_id` + FORCE-RLS policy; QA adds the adversarial refusal assertion |
| Hand-setting a goose migration order or shipping an untested `Down` | `make migrate-create` in the worktree; verify `migrate-up` + reversibility round-trip locally (the gateway migrates on deploy) |
| Querying as superuser to "get past" RLS | Run inside `WithinTenantTx` as `invoice_app`; RLS isolation is the product |
| Working in main checkout | Always work in `$WORKTREE_PATH`; main is the user's space |
| Treating stories as needing to run serially against dev | Each PR gets its own ephemeral Railway environment (M4-23) — running multiple stories' `/ralph` invocations in parallel is the intended mode; nothing shared queues or races |
| Title-parsing Backlog tasks to find a story's subtasks | Use the `story:<slug>` label |
| Erroring on a story with zero Backlog subtasks | Zero subtasks + Objective/Core ACs (or a build-plan row) = BASIC → run Phase 0.6 |
| Blocking on the user in any unattended phase | Unattended disposition: defaults + conservative options, recorded in ## Decisions / QA Debate Log; user reviews via PR. Phase 0.6d is the single exception |
| Architect inventing scope while expanding a basic story | Every derived AC/subtask traces to Objective/Core AC/"Ships when true"; Out-of-scope leakage = mechanical fail |
| Bouncing the executor on uncited taste | UI fails must cite a design-system rule or a prototype CSS rule; pure taste is advisory → escalate |
| Renaming a variable and checking only that the rename landed | Grep every OTHER variable's rendered value for the OLD name before merge — M4-22's DSNs still interpolated the deleted names and rendered empty |
| Re-running the deploy gate only BEFORE applying a variable deletion | Re-run it AFTER the deletion is applied — M4-22's own gates passed because they ran before, which is why this went unnoticed until M4-08 |
| Not cleaning up the worktree after merge | Run `/post-merge-cleanup <STORY-ID>` |
