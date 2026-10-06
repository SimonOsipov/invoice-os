# Ralph Workflow — ASComply Africa (invoice-os)

## Overview

`/ralph <STORY>` builds one story: plan once (Phase 0.6), then per subtask Test-Spec* → Execution → QA Verify (*Test-Spec only for `Test-first: yes`), then a story-level deploy gate (Phase 3.5). The deploy gate is the `dev-env.yml` run on the PR: it deploys the whole fleet to the PR's own ephemeral Railway environment (forked from `development`; its database is bootstrapped, migrated, demo-purged and seeded at gateway boot), checks fleet health, and runs smoke + topology E2E. Work runs in an isolated git worktree.

**One story = one branch = one PR.** RALPH splits it into subtasks (`<STORY>-01`, …) internally.

A story arrives **basic** (Objective, Core ACs, Out of Scope; from `/pm-story` or `/pm-epic`; no subtasks in `hm subtask list`) or **pre-planned** (`hm subtask list` shows subtasks, or the story already has an `## Implementation Subtasks` section). Phase 0.6 plans basic stories; pre-planned stories skip it.

Each PR has its own environment, so several `/ralph` runs MAY run concurrently.

## Agents and models

The architect and the story writer run on Opus. The executor and QA run on Sonnet: QA's Mode A, Mode B and the plan critique. The model is set in each agent's own definition; do not pass `model:` on a spawn.

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
- **Unattended run** (every `/ralph` phase) — take the default: the option that the acceptance criterion's text supports. When the text supports a larger option, take the larger option. Otherwise take the smaller scope. Record it in `## Decisions`. Never block, except at the critical-fork gate (Phase 0.6d) and the Phase 3.5 escalation.

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
# dev-env.yml (on a ready PR): prepare-env (ci-watch alongside; red CI stops the run) → deploy-gateway (migrator) → health-gate →
#   deploy-context ×7 + deploy-spas ×3 → fleet-gate → e2e (smoke + api) → topology ×4 (serial-lane, import-wizard, import-wizard-2, invoice-surfaces).
```

Not part of the gate: `dev-env-teardown.yml` (deletes the PR environment on close), `dev-env-sweeper.yml` (daily reaper of orphaned PR environments), `railway-invariants.yml` (asserts Railway PR Environments stay OFF). CI tears environments down; never use destructive Railway MCP calls.

### 4. One branch, one PR per story
All subtasks share one feature branch, one draft PR and one worktree.

### 5. Keep going
Every `/ralph` phase is unattended. Put each status note in the same message as the next action.
End a turn only in these cases:
- Phase 0.6d sends its questions to the coordinator (`AWAITING_ANSWERS`).
- A spawn fails a third time (HALT, Phase 1).
- Phase 3.5 sends its escalation to the coordinator.
- The run outputs `ALL_TASKS_COMPLETE`.
- A background command or Monitor you started is still running. Its completion wakes you.

A loop that waits for a file has a deadline: `until <check> || [ $SECONDS -gt 3600 ]; do sleep 15; done`. A deleted worktree removes the file, and a loop without a deadline never ends.

In every other case, take the next step. Do not end a turn on "Starting X now", "Next: subtask 4", or an offer to continue.

---

## MCP servers
| Server | Use |
|--------|-----|
| Context7 | `mcp__context7__*` — check before writing library code (pgx, River, CEL, goose, Vite, React) |
| Playwright | `mcp__playwright__*` — UI verification against the deployed PR environment |
| Sentry | `mcp__sentry__*` — deployed errors |
| Railway | `mcp__railway-mcp-server__*` — read-only; deploys happen in `dev-env.yml` |
| Obsidian | `mcp__obsidian-mcp-tools__*` — story files |
| sysmap | `mcp__sysmap__*` — feature records; read-only, the PM writes |

## Subtasks
Harbourmaster keeps the story's subtasks. Ralph runs only as a Harbourmaster worker, so `hm` is on the PATH.

| When | Command |
|------|---------|
| The story file has its `## Implementation Subtasks` (Phase 0.5 step 7, Phase 0.6c), or a plan change rewrites them | `hm subtask import "<story file>"` |
| The plan or a subtask's plan changes after Phase 1 started | `hm signal B7 <STORY> "<what changed>"` |
| Read the story's subtasks and their status | `hm subtask list <STORY>` |
| Read one subtask whole | `hm subtask show <ID>` |
| The first stage of a subtask starts: Test-Spec or Execution | `hm subtask status <ID> doing` |
| QA Verify of the subtask passes | `hm subtask status <ID> done` |
| QA finds a defect in the subtask | `hm subtask note <ID> "<the defect in one line>"` |
| A fix starts on a subtask that is `done` | `hm subtask status <ID> doing` |

- `<STORY>` is the prefix of the subtask ids: `AUTH-18` for `AUTH-18-01`.
- `import` keeps each subtask's status and notes. It names each subtask that the plan dropped. Remove it with `hm subtask rm <ID>`.
- When an `hm subtask` command fails, run it again once. On a second failure, HALT and report the command and its error.

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
   - *Feature:* `sysmap_feature_show`. Name + description = Objective; acceptance criteria = Core ACs; `screen` = surface; `depends_on` = prerequisites. Set `PLANNING_REQUIRED=true`, `STORY_SOURCE=sysmap`. Refuse a feature whose status is not `planned` or `building`, and name the status. Do not change its status; the PM sets `building`.
   - *Obsidian:* `get_vault_file` on `Simon Vault/Projects/ASComply Africa/User Stories/<EPIC>/<STORY>*.md`; also check `User Stories/Archive/<EPIC>/`. Set `STORY_SOURCE=obsidian`. If no file exists, error: "run /pm-story first".
3. **Branch slug.** Use the story's `## Branch Strategy` if present. Otherwise `feature/<lowercase-id>-<kebab-title>` (`F-192 Notice a submission failure` → `feature/f-192-notice-a-submission-failure`).
4. **Subtasks:** `hm subtask list <STORY>`. "has no subtasks" means zero subtasks.
5. **Refuse a story with unanswered questions.** If `## Blocking Questions` has any entry, send the open ones to the coordinator (Phase 0.6d rules). Never default them or delete the section to proceed. (`## Open Questions` is a different, non-blocking section.)
6. **Classify the story state:**
   - `STORY_SOURCE=sysmap` → **BASIC**.
   - Zero subtasks + Objective/Core ACs → **BASIC** → `PLANNING_REQUIRED=true`.
   - Subtasks returned, or an `## Implementation Subtasks` section → **PRE-PLANNED** → topo-sort by dependencies, log the plan, skip Phase 0.6.
   - Neither → error: "story <ID> is neither basic nor pre-planned — run /pm-story, or /pm-epic for a multi-story topic."

### Phase 0.5: Worktree bootstrap

1. **Paths:** `MAIN_CHECKOUT=/Users/samosipov/Downloads/invoice-os`, `WORKTREE_PATH="$MAIN_CHECKOUT/.claude/worktrees/<lowercase-id>"`, `BRANCH=<slug>`.
   **Base:** `BASE` is the branch that your Harbourmaster role file names under "Your base branch". When the role file names none, `BASE=main`. In an epic run, `BASE` is the epic branch: the PR targets it, and the coordinator merges it.
2. **Pre-flight:** if `$WORKTREE_PATH` exists and `git worktree list` shows it, another run owns this story — error and exit. If it exists but is not listed, it is stale — tell the user to run `/post-merge-cleanup`, and exit.
3. **Create:**
   ```bash
   git -C "$MAIN_CHECKOUT" fetch origin main "$BASE"
   git -C "$MAIN_CHECKOUT" worktree add -b "$BRANCH" "$WORKTREE_PATH" "origin/$BASE"
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
   Record `DEV_DB_PORT`. Every DB-backed suite passes it; without it a suite hits port 5432, which may be another worktree's database. `make test-idp` derives its container names and ports from it.
6. All later commands run inside `$WORKTREE_PATH`. Pass it as CWD to every subagent.
7. **Scratch:** write every scratch file (scripts, harnesses, logs, corpora) to `$WORKTREE_PATH/.ralph/scratch/`. Never write to a fixed `/tmp/<name>` path: a parallel run can overwrite it. Pass this rule to every subagent.
8. Pre-planned stories with an `## Implementation Subtasks` section: `hm subtask import "<story file>"`.

### Phase 0.6: Planning (basic stories only)

Runs only when `PLANNING_REQUIRED=true`, inside the worktree.

#### a. Architecture — finalize the story
Spawn `product-architecture-spec` with the full basic story and its Obsidian path, per its "Expanding a basic story" section. It rewrites the story in place: design, `## Implementation Subtasks`, and `## Decisions`. Pass these rules:

- **Diff every new control against its siblings.** A control added to an existing bar or panel matches the siblings' visibility and disabled treatment. A sibling's shipped decision is the spec.
- **A layout constant ships a relationship assertion.** When a subtask adds or changes a width, grid track, clearance, overflow or alignment, name a topology layout assertion as its deliverable. It asserts a relationship: the element fits, aligns or does not overlap. It never asserts a raw dimension. Phase 3.5 step 4 owns the rule "Assert the relationship, not the dimension". `e2e/topology/layout.ts` sweeps `WIDE_WIDTHS` (2560/1920/1440/1280); `e2e/topology/invoice-surfaces.spec.ts` is the worked example. Planning it here puts it in the first deploy-gate run.
- **Re-measure every fact the story asserts** — a root cause, a mechanism, a count, another PR's state, whether the prescribed fix can work. Measure it in the worktree. Paste the command and its output into `## Decisions`, one entry per fact, tagged `premise — verified` or `premise — CORRECTED: story said X, actually Y`.
- **Measure a fact about the deployed app on the deployed app** (browser output, sidecar responses, production data): Playwright MCP or Railway logs. Paste the output into the same `premise —` entry. Never escalate an unmeasured premise.
- **An AC that already holds at head is not work.** Record the proving test in `## Decisions`; write no subtask.
- **Traceability:** every derived AC and subtask traces to the Objective or a Core AC. Nothing in Out of Scope appears in a subtask.
- **Checkpoint:** `STORY_FINALIZED`

#### b. Plan review — unattended
Run `/qa-verify` in **unattended** disposition. Judgement and unresolved findings take the conservative default; never block here — Phase 0.6d re-tests them. A finding that falsifies a premise is recorded as `premise — CORRECTED`, not repaired as wording. The log goes to the story's `… QA Debate Log.md`.
- **Checkpoint:** `PLAN_VERIFIED`

#### c. Subtask generation
Run `hm subtask import "<story file>"` on the finalized story. Topo-sort into execution order. Log the plan: title, branch, ordered subtasks, count of Decisions and conservative defaults.
- Do not emit `SUBTASKS_READY` while a fact the story asserts lacks a `premise —` entry with pasted output.
- **Checkpoint:** `SUBTASKS_READY`

#### d. Critical-fork gate — the one place the plan asks a question
Test every `## Decisions` entry (conservative defaults and `premise —` entries included) against five questions:

- Does it decide **who is allowed** to do something?
- Does it decide **what the system claims** to an outside party: the authority, the customer, the audit record?
- Does it let the system **silently override a human's action**?
- Does a **corrected premise** remove something the scope needs: a shipped screen, an endpoint, a merged PR, a seeded row?
- After it, does the **outcome that a Core AC promises** (or a subtask AC taken from one) no longer happen for the user? A narrower or a wider reading after which the outcome still happens is not a fork.

Any "yes" makes the fork **critical**. Expect zero or one per story.
A plan-review finding tagged `escalated→critical fork` is critical: it names a defect, and its fix changes the scope.

**First check whether it is already answered.** A source answers a fork when it decides the same question. Match on the question, not on shared nouns. Read these sources:
- the epic's decision log: frontmatter `type: decision-log` in the story's epic folder (fallback: filename `*Decision Log*.md`);
- the `## Decisions` of each sibling story that shipped (`User Stories/Archive/<EPIC>/`): a sibling's shipped decision is the spec;
- the story's own Constraints and Out of Scope;
- the sysmap notes of the story's features.

For each critical fork:
- A source answers it → not blocking. Record `<source> — <choice> (<file or feature>)`.
- No source answers it → blocking.
- Your default contradicts a source → blocking; say so in the question.

With critical forks left:
1. Write each under `## Blocking Questions` (exact heading): the question in one line, the default, the alternative.
2. Send the coordinator all of them in one message, each in plain words with 2–3 options and the default marked. End the turn. **Checkpoint:** `AWAITING_ANSWERS`.
3. The answers arrive as one message from the coordinator. An answer that says "your call" takes the default.

When every question is answered or defaulted, record each as `<who> — <choice>` in `## Decisions`, with the name that the answer gives (`coordinator`, `pm` or `user`). Delete `## Blocking Questions`, and continue without re-planning.

**Boundary:** pre-planned stories skip Phase 0.6 and so skip this gate.

#### e. Decisions surfacing (non-blocking)
Tell the FIRST subtask's executor to put the story's `## Decisions` section and a pointer to the QA Debate Log in the draft PR description.

### Phase 1: Sequential subtask execution

For each subtask in dependency order, run the stages below. Stage numbers start at 2.5 because code comments cite them. Run one stage agent at a time, and run no suite while a stage agent runs: agents in one worktree share its files and its dev Postgres.

Before every spawn, run `git -C "$WORKTREE_PATH" status --short` and `git -C "$WORKTREE_PATH" log --oneline -3`. A report that says "committed" is not evidence; the log is. Commit orphaned work under its own subtask's message with explicit paths, never `git add -A`.

After a context compaction, also run `hm subtask list <STORY>` and `gh pr checks` before the next spawn. The summary says where the run was; git, `hm subtask` and CI say where it is.

Every stage brief (Test-Spec, Execution, QA Verify) says: **terse comments** — one or two lines for the non-obvious why, per `CLAUDE.md` "Code Comments". Do not copy the density of the file being edited.

If a spawn fails, retry twice. On a third failure, HALT: leave the subtask `doing` and report the stage and error. Never perform a stage yourself — a same-context QA pass of your own work is worthless evidence.

**Test-first is the default for logic-bearing work** (rules engine, tax maths, state machines, RLS, validation). `Test-first: no` is for UI, copy and config whose oracle is the deploy gate.

**"No honest oracle exists" is a finding, not a waiver.** When `Test-first: no` is chosen because no test can see the failure, record it in `## Decisions` and name in the PR body which Phase 3.5 artifact stands in.

#### Stage 2.5: Test-Spec (`Test-first: yes` only)
- Spawn `product-qa-spec` (Mode A) with the subtask's Test Specs.
- It writes runnable tests and confirms each fails on its target assertion, not on a compile or setup error. DB-backed suites use `make test-rls|test-queue|test-audit DEV_DB_PORT=…` or `go test` with the env-gated DSN.
- It commits the red tests.
- **Checkpoint:** `TESTS_RED`

#### Stage 3: Execution
Spawn `product-executor` with the output of `hm subtask show <ID>` and the story file's path; the story's design sections are shared context. Pass these orientation rules; the executor does them before it edits:

- **Search with `breaklist`:** `go run ./internal/tools/breaklist '<Go regexp>' [path ...]` from the worktree root. Report the command and its `TOTAL` line. Do not substitute `grep`/`git grep` or pipe through `head` — they drop NUL-byte files, ignore `\b` and truncate silently. A search finds only code that names the thing, not a test that depends on the behaviour.
- **Go signature/API change:** enumerate every caller and test across `cmd/` and `internal/` as a deliverable.
- **JSON wire-shape change:** also search `e2e/` and the SPA wire mirrors (`frontend/*/src/lib/*.ts`). No compiler links a Go struct to its TypeScript copies. `e2e/api/client.ts` is the one that gets forgotten.
- **UI-touching change:** search `e2e/` (smoke + topology) for changed routes, testids and labels; each match is a deliverable.
- **A backend change to a value the frontend branches on is UI-touching** (a reason code, a doubt scope, an enum value, a URL parameter). Find the frontend code that reads it, then search `e2e/` for the testids it renders.
- **Migrations:** goose is timestamp-ordered. Scaffold with `make migrate-create name=<slug>` inside the worktree. Every tenant-owned table is born with `tenant_id` + the FORCE-RLS policy template and a working `-- +goose Down`. Verify `make migrate-up` and the down/up round-trip locally; a bad migration crash-loops the PR environment.
- **A spec copies a backend value from its Go constant**, and names the constant beside it.
- For `Test-first: yes`, drive the red tests green without weakening, skipping or deleting any.

The executor commits and handles push and PR state per its `Order` field (Phase 2).

Then run the suites yourself. Run this command in `$WORKTREE_PATH`, with `run_in_background: true` and `timeout: 7200000`:
```bash
DEV_DB_PORT=<your DEV_DB_PORT> hm suite run scripts/dev/suites.sh
```
- `hm suite run` runs one worktree's suites at a time on the machine. Parallel suites starve each other of CPU until their tests time out. A queued run prints the story whose suites it waits for.
- `scripts/dev/suites.sh` writes one log per suite to `$WORKTREE_PATH/.ralph/`. It prints one `<suite>=<exit code>` line per suite and the summary lines. It exits non-zero when a suite failed.
- Omit `DEV_DB_PORT` only when Phase 0.5 started no dev Postgres. The script then prints `db=skipped`.
- `wait=1`: a `go test` still runs in this worktree, and no suite ran. Wait for it to end, then run the command again.
- Exit 1 with "the suites did not run": the queue did not move in an hour. Stop and report the error. It names the run that holds the queue.
- Never kill a running suite: a killed DB suite skips `t.Cleanup` and leaves an orphan tenant and a stuck `river_job`. Its exit wakes you.
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
  For an AC that states a rule, at least one row changes a comparison, a branch or a computation. A row that only edits a literal the test restates proves only a value AC.
  An AC item with no row is a QA failure. A Playwright spec cannot replay locally: cite its assertion for Phase 3.5 and write no row.
- **Assert a collection is non-empty** before asserting over its items.
- **Source scans:** the QA agent's rules "A source scan is the last resort" and "Never test a test" apply. A source scan also needs a `## Decisions` entry that names the failure no runtime test can observe.
- **Every source scan** (a grep, a source walk, a forbidden-string guard, a site count):
  1. strips comments before it matches (TypeScript: `stripComments` from `@invoice-os/api-client/strip-comments`; Go: `go/ast` or strip first);
  2. reads only the function or block it guards, not the whole file;
  3. matches every letter case, unless case is the point — then a comment says so.
  Prove it with one break: delete the guarded code, keep its comment, run the scan, and it must go red. Record that as a mutation row.
- **An absence scan** also needs a control needle that must be found and a floor on the population scanned. Offer no "zero hits" as evidence until the same command has found a planted hit.
- **Re-read every comment and doc your change made false**, and fix them in the same commit. Sweep in cost order: (1) comments your diff did not edit in files it did (`git diff "origin/$BASE...HEAD" -U15`); (2) comments in files you never opened — name the fact your change altered and search the whole tree for it; (3) every file in `docs/`. Treat your own earlier future-tense notes as suspects.
- **State a shared fact in one place** and cite that place.
- **Report test deletions.** List each test this subtask made redundant in the QA report. Delete it in the same commit.
- **A fix to a false comment deletes the false clause and adds no new clause.** A needed new claim names the test or command that proves it.
- Frontend: Playwright MCP verification against the deployed PR environment once it exists.

When QA returns, **replay the mutation rows yourself**, with no other agent running: `go run ./internal/tools/mutationreplay .ralph/mutations-<SUBTASK-ID>.jsonl`. It edits source in place and restores the exact bytes. Any `NOT-PROVEN` or `INVALID` row fails QA; send it back. Before you send it back, record each `NOT-PROVEN` row: `hm signal B6 <STORY> --subtask <SUBTASK-ID> "<ac>"`. A row is a claim; the replay is the evidence.

If issues are found, spawn `product-executor` to fix, then re-verify. Record each QA finding with `hm subtask note`. Record each return to the executor with `hm signal B5 <STORY> --subtask <SUBTASK-ID> "<one line>"`.
When QA passes, tick each acceptance criterion that QA proved: `hm subtask check <SUBTASK-ID> <n>`. Leave a criterion that QA did not prove unticked. hm records it at merge.
- **Checkpoint:** `QA_VERIFIED`

After each subtask, wait for `CI` on the pushed commit: `hm ci wait <PR>` (CI Monitoring Protocol).
A red run stops the next subtask until it is green. Then take the next subtask.
A red run caused by a defect is recorded per "A defect in a verified subtask".

#### A defect in a verified subtask
A defect is wrong behaviour in product code or its tests. These are not defects:
- a flaky run or an infrastructure failure;
- the wording of a comment or a doc;
- a lint finding on a comment, for example the line-number citation check;
- code that the story did not add or change.

A defect escaped QA when a later finder finds it in a subtask that reached `QA_VERIFIED`. The finders are CI, a later subtask's QA Verify, the Phase 3 review and the Phase 3.5 gate.

1. Find the subtask whose commit added the faulty code: `git log -L` or `git blame` in `$WORKTREE_PATH`.
2. Before the fix starts, record `hm signal V1 <STORY> --subtask <SUBTASK-ID> "<finder>: <one line>"`.
3. `<finder>` is `ci`, `qa <SUBTASK-ID>`, `review` or `gate`.
4. When no single subtask added the faulty code, use the subtask that found it. Write `source unknown` in the line.
5. Record one V1 per defect. A defect that several finders or subtasks show is still one V1.
6. When the Phase 0.6b plan review found the defect and closed it by a conservative default, add `--defaulted <F-id>`. The signal then blames the plan.

### Phase 2: PR lifecycle

`product-executor` manages PR state from the subtask `Order` field. Put `Base branch: <BASE>` in every executor brief:
- `1 of N (FIRST)` → push and create the **draft** PR. `dev-env.yml` runs on a draft, and its `E2E gate` fails by design until the PR is ready. hm does not count those runs.
- `K of N` → push only.
- `N of N (FINAL)` → `git fetch origin`, merge `origin/<BASE>` if behind, and also `origin/main` when `BASE` is not `main`. Push. The PR stays draft.

The orchestrator never runs `git checkout -b` or `gh pr create`. Phase 3.5 step 3 marks the PR ready.

### Phase 3: CI

After the FINAL subtask's QA, wait for the aggregate `CI` per the CI Monitoring Protocol.

While it runs, review the whole diff: run `/code-review high <PR_NUMBER>`, never with `--fix`. Give `product-executor` every finding that would block the merge, in one batch: file and line, why it is wrong, how to show it fails. Add the other findings to the PR body as advisory (`gh pr edit`). Run one review cycle. No other automated review runs on this repo. A blocking finding that is a defect is recorded per "A defect in a verified subtask" (Phase 1).

### Phase 3.5: Story-level deploy gate

Runs once per story, after `CI` is green. It verifies the assembled feature against the original objective.

1. **Read the original acceptance criteria**, not the possibly-edited subtask ACs. `STORY_SOURCE=sysmap`: the feature's acceptance criteria (standing invariants). `STORY_SOURCE=obsidian`: the story's Objective and Core ACs.
2. **Freshness:** `git -C "$WORKTREE_PATH" fetch origin`. If `origin/$BASE` or `origin/main` has commits the branch lacks, merge them and push. A base missing main's migrations crash-loops the gateway.
3. **Ready flip and gate:**
   - First, wait for `CI` on the PR head: `hm ci wait <PR>`.
   - Then, if `gh pr view <PR> --json isDraft -q .isDraft` prints `true`, run `gh pr ready <PR>`.
   - Then wait for the gate: `hm ci wait <PR> --gate` (CI Monitoring Protocol).
   - A `no-run` verdict on a docs-only PR is expected: `dev-env.yml` is paths-filtered. Escalate to the user rather than faking it green.
   - `gh workflow run dev-env.yml` is for diagnosis only. It targets `development`, not the PR environment, so it proves nothing about this PR.
   - Re-run a red gate whole: `gh run rerun <run id>`, never `--failed`. The database resets only when the gateway deploys.
   - A spec this PR changed that passed only on retry fails the `e2e` job or the `E2E topology (<shard>)` leg that ran it. Fix the spec or the race; do not re-run for luck.
   - After `gh pr ready`, push only a gate fix (Protocol item 1 or step 5) or a base merge that `hm` demands.
   Green means: fleet deployed, gateway migrated, DB bootstrapped + demo-purged + seeded, all 8 backends up, smoke + topology E2E passed, including cross-tenant isolation.
4. **Spawn `product-qa-spec`** to verify **each** original AC against the green run:
   - Quote each AC beside its evidence. Evidence of different behaviour than the quoted text fails that AC.
   - Backend / data / RLS ACs → cite the passing CI job or E2E assertion.
   - **UI ACs** → drive the deployed SPA read-only with Playwright MCP as the seeded user. Capture each touched surface and state to `$WORKTREE_PATH/.ralph/fidelity/<surface>-<state>.png`. Diff live `getComputedStyle` and layout against the prototype (`.dc.html`; confirm the file→surface mapping first) and the design system. A delta citing a design-system rule or a prototype CSS rule is a fail; uncited taste is advisory: list it in the final report, never bounce.
   - **Assert the relationship, not the dimension.** A layout AC is satisfied by what the number encodes — gutter symmetry, containment, alignment to a sibling. A width assertion passes on the very bug it should catch. This applies whenever the diff adds or changes a layout constant, not only when an AC names layout. **Measure widest first:** `e2e/topology/layout.ts` sweeps 2560/1920/1440/1280; every other sweep in `e2e/` stops at 1280.
   - **A pixel figure derived from source is a guess.** Measure it on the gate run with `e2e/topology/layout.ts` and cite the run id before a CSS edit, a bounce or an escalation.
   - No holistic "looks done": every AC needs its own evidence.
5. **Fix loop (cap 2 cycles):** batch all fails into one report → `product-executor` fixes → push (re-fires `dev-env.yml`) → wait → re-verify only the failed items. Every bounce cites an AC id, a design-system rule or a prototype CSS rule. After 2 cycles, send the coordinator the rest as one question: continue the fix loop, or stop. Each gate run rebuilds an 11-service environment. A fail that is a defect is recorded per "A defect in a verified subtask" (Phase 1).
6. **Log** under `## Post-Deploy QA — <date>` in the QA Debate Log: per-AC verdict + evidence, fidelity deltas, fix cycles, run ids, advisory notes.
7. **On PASS** (all original ACs pass on a green run, no unresolved bounces, fidelity evidence for UI stories): end with a short report in this order: **Needs you** (merge PR #N; each default or advisory finding a reviewer should see), **Changed** (subtasks, PR), **Found** (corrected premises; what you could not confirm and where you looked). Then output `<promise>ALL_TASKS_COMPLETE</promise>`.
   **Otherwise:** do not emit completion. Send the coordinator the open failures as one question.

### Phase 4: Worktree cleanup

After the PR merges (manually or via `/gh-merge-pr`), run `/post-merge-cleanup <STORY>`. It removes the worktree and branch and archives the story. The PM updates sysmap. A `/ralph-goal` loop advances only after this runs.

Teardown of the PR environment is repo-side: `dev-env-teardown.yml` on PR close (best-effort), `dev-env-sweeper.yml` daily as the authority. See `docs/deploy-model.md`.

---

## CI Monitoring Protocol

Wait with `hm ci wait <PR>` for `CI` and `hm ci wait <PR> --gate` for the deploy gate. Run it through Bash with `run_in_background: true`. Its exit wakes you. Your Harbourmaster role file ("Waiting for CI and the deploy gate") names each verdict and what to do.

The verdict line names a short SHA after `at`. If it is not the start of `git rev-parse HEAD`, push HEAD, then run the wait again.

Foreground `sleep` is blocked. Never end a turn on a wait you did not start.

1. **`failed`?** Read the failed jobs and the log tail it prints. Fix in the worktree, commit, push, and wait again.
2. **`CI` passed?** → In Phase 3, go to Phase 3.5 once every blocking review fix is pushed. In any other phase, continue the step that waited.
3. **The gate passed?** → Phase 3.5 step 4.

---

## Editing This File

An agent parses these instructions with no one to ask. Write for that reader.

- **One word, one meaning.** Reuse one verb per action. Reserve `check` for a CI check and `assert` for a test assertion.
- **One instruction per sentence**, twenty words or fewer for a procedure.
- **State each rule once**, in the phase that owns it.
- **No incident narratives.** A rule states what to do; the test or command that enforces it is the only citation.

## Completion Rules

1. Both gates green on the PR head (Core Rule 3). Local tests green ≠ done.
2. Subtask status: todo → doing (its first stage starts) → done (its QA Verify passes).
3. Output `<promise>ALL_TASKS_COMPLETE</promise>` only after Phase 3.5 passes and every subtask is `done`.

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
| Blocking in an unattended phase | Default + `## Decisions`; only Phase 0.6d and Phase 3.5 send questions, to the coordinator |
| Architect inventing scope | Every derived AC traces to the Objective / a Core AC |
| Bouncing the executor on uncited taste | Cite a design-system or prototype rule; taste is advisory |
| Renaming a variable and checking only the rename | Search every other variable's rendered value for the old name before merge |
| Running the deploy gate only before a variable deletion | Re-run it after the deletion is applied |
| Leaving the worktree after merge | `/post-merge-cleanup <STORY>` |
