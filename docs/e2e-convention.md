# E2E Testing Convention

Governs the Playwright suites in `e2e/`. They run against a **deployed** environment
only (no local server) as the post-deploy step in `.github/workflows/dev-env.yml`, and
are auto-collected via `testMatch **/*.spec.ts` — a new spec needs no workflow edit.

## Organize by capability, not by date

Browser E2E is organized by **product capability / feature** — never by milestone or
demo date. There are **no `dayN.spec.ts` files**.

- A milestone's "moment of value" is proven by **extending the relevant capability
  flow**, not by adding a new dated end-to-end journey.
- The Day-30 / Day-60 / Day-90 roadmap narrative lives **only** in the Build Plan
  (`Build Plan — 0 to MVP.html`). Test files carry feature names.
- Why: dated demos accrete and overlap — each re-walks the previous one's steps as a
  prefix, so the suite grows one full journey per milestone forever. Feature-named flows
  are extended in place instead.

Target capability flows: `auth`, `portfolio`, `validation`, `import`,
`invoice-lifecycle`, `dashboard`. These live in `e2e/topology/` (the de-facto capability
layer — `auth.spec.ts`, `validation.spec.ts`, `import-wizard.spec.ts`,
`invoice-surfaces.spec.ts`, `portfolio.spec.ts`, `isolation.spec.ts`); no dated file
remains (M4-14). Exact file/directory layout is the implementation's choice — the rule
is the *organizing axis* (capability, not date), not a fixed tree.

## Keep the browser layer thin (the pyramid)

Behaviour coverage lives at the **base** — Go unit/integration tests and the
`e2e/api/` contract suite (asserted through the gateway). The Playwright browser layer
is deliberately **thin**: a small set of capability flows plus smoke render checks.

Do **not** grow it into broad per-screen coverage — that duplicates the base and is the
slowest, most fragile layer. The browser layer exists to prove the deployed stack
integrates end to end, not to exhaustively cover UI states.

## Functional only — no visual regression

Assertions are on **DOM / state**, plus a **console-error gate** (any `console.error`
or `pageerror` during a journey fails it).

- **No screenshot / pixel-diff / visual-snapshot / Chromatic testing.**
- Rationale: the console-error gate already fails on broken asset / CSS / JS loads, so
  the only thing a pixel diff adds is *silent* CSS regressions — a narrow band that does
  not justify per-run baseline maintenance on still-churning UIs. (If ever wanted,
  screenshots may be captured as **non-blocking artifacts** — never a gate.)

## One browser, serial

**chromium-only, `workers: 1`.** No multi-browser matrix, no sharding.

**Every run gets a database of its own, and shares it across all three suites.**

The suites run only on a pull request (`dev-env.yml`'s `e2e` job), against that PR's own
ephemeral Railway environment. That environment's Postgres is a *fork* of the persistent
environment's volume, so it is born holding everything that environment holds — and the
gateway TRUNCATEs the tenant-data tables and re-seeds the curated demo state at boot, on
every deploy (Decision [pr-only-reset], 2026-07-28, `internal/platform/db/reset.go`). A run
therefore starts from the seed, never from another run's leftovers, and the health-gate
fails the run outright if that reset did not happen — it is armed by a hand-set Railway
variable that otherwise fails closed and silent.

What a spec still cannot assume is an empty table:

- smoke → api → topology run in that order against ONE deployment with **no reset between
  them**, and `api/perf.spec.ts` alone creates 500 invoices before topology reads a list;
- a Playwright retry re-runs a failed test against everything its first attempt left behind;
- the tables holding admin CRUD — `workflow_roles`, `workflow_role_members`, `memberships`,
  the approval-policy tables — are deliberately **excluded** from the reset (`resetTables`'s
  own EXCLUDED block says why), so writes there outlive the run that made them.

So the rule is unchanged, and `workers: 1` still holds: every spec creates per-run-unique
data (fresh TINs, random UUIDs, high offsets for empty-state), acts on rows it created, and
asserts containment or a live-read comparison rather than a literal count.

## Target surface

Four frontends deploy — the `landing` front door and the three SPAs it hands off to — and
they are **not equally testable**. The line that matters is not *which SPA* a test drives
but **what backs the assertion**:

| surface | backing | what a browser assertion may claim |
|---|---|---|
| `app` SPA | gateway-wired (real API, real DB) | a **contract**: rendered state matches what the API returned |
| `ops-console` | mock data, no backend | **fixture behaviour** — that the console's own client-side logic works |
| `support-console` | mock data, no backend | same |
| `landing` | static marketing | render, plus client-side navigation |

The `app` SPA remains the only place a browser test can prove the **stack** integrates end
to end. The consoles and the landing page carry functional coverage of their own
client-side behaviour because a browser is the only place it can be observed: every
frontend vitest project defaults to `node`, and the files that opt into jsdom per-file
(`// @vitest-environment jsdom`) get a DOM with no layout engine — so a control's
geometry, a route guard and a scroll-spy are still browser-only.

**Mock-backed assertions pin fixtures, not contracts — and the spec must say so in-file.**
A spec asserting an ops-console counter asserts that a seeded fixture and a pure function
over it still agree; it will need revisiting when a real endpoint lands. Writing such an
assertion as though it proved a contract is the failure this rule prevents; refusing to
write it at all leaves a shipped screen untested. So: write it, and label it.

The approval-policy list was the other example until APPR-09, and is no longer one: the
endpoints are real (`docs/approvals.md`), `App.tsx` fetches them, and both specs over that
surface have been re-derived against live data (APPR-09-07/08). `e2e/topology/workflows.spec.ts`
holds the firm half — it creates its own policy through the UI, imports no fixture, and never
publishes (`[topology-never-publishes]`: a publish seals a version permanently and takes the
tenant's one active slot on a shared deployment). The decision is scoped to policy
**identity**, not the verb: a topology spec must never publish a policy it authored and must
never change which policy governs the tenant, but restoring the tenant's own already-seeded
policy to the active slot it already owns (`ensureFirmPolicyActive`,
`e2e/api/contract-helpers.ts`) is convergence, not publication — every firm-tenant submitting
spec self-heals through it in a `beforeAll` (APPR-14-07). `e2e/topology/roles.spec.ts` builds and
deletes its own policy on the same terms. `persona-surfaces.spec.ts` holds the in-house half,
now a heading, a tenant-driven subtitle and a settle on either terminal arm of the list —
`internal/demopolicy` seeds an active policy onto BOTH persona tenants (plus an unpublished
draft on the in-house one), and `persona-surfaces.spec.ts` names none of them, nor any other
pre-existing row;
`workflows.spec.ts` and `roles.spec.ts` name only rows they create. The mock fixture
module that predated those live reads, `policyFixtures.ts`, was deleted by APPR-10.

`invoice-surfaces.spec.ts` extends this to the invoice-level decision controls and trail
card, but covers only the **unarmed** case for those controls specifically — both disabled,
the trail empty. (Its own submitting fixtures, and `import-wizard.spec.ts`'s, do arm and
close a run since APPR-14-07 — over the API side channel via `approveUntilClosed`, never
through the UI's own Approve/Reject buttons.) The **armed, UI-driven** approve/reject
journey still lives in `e2e/api/contract-invoice.spec.ts` instead, because
`[topology-never-publishes]` still bars a topology spec from publishing the dedicated probe
policy that journey would need.

That trail card reads its run on mount, and that GET answers 404 when there is no run —
which Chromium logs as a console error, tripping the `collectErrors` gate in every
detail-page test. The gate carries the exception (`consoleGate.ts`, matched on the
message's resource URL so no other 404 is masked), not the API: the uniform 404 is a
deliberate no-oracle property, and a 200-with-null-run would leak cross-tenant existence.

Mock-only `app` surfaces follow the same rule. **Reports and Settings** carry
functional coverage as sidebar surfaces of the persona that owns them (see below). The
company switcher and onboarding dashboard are not nav surfaces and hold no
coverage cell — note that the switcher *is* **operated** by `workflows.spec.ts` and
`persona-surfaces.spec.ts` as the mechanism for changing the active client, which is not the
same as being covered by them. Submission/transmit is real (M5-09): it is covered via the
invoices **list**'s batch-select-and-submit path (`invoice-surfaces.spec.ts`), and via that
same file's "detail surface: submit one invoice from its own page" test, which covers a
single invoice submitted straight from its own **detail** page. The XML/UBL viewer is real
too (BUG-04): it stays a non-nav surface with no coverage cell, and is covered by that same
file's two `View UBL/XML` tests plus `api/contract-ubl.spec.ts`.

**What "smoke only" covered, and still does.** Render checks, plus client-side behaviour
that has no other harness. That was always a floor rather than a licence for per-screen
coverage, and it still is: *keep the browser layer thin* and *functional only — no visual
regression* apply here unchanged, and the browser layer's size is bounded by the persona
surface catalogue below, not by the number of screens that exist. **The guard below enforces
only the floor** — that no persona-scoped surface ships uncovered. Nothing mechanical
enforces the ceiling; keeping the layer thin stays a review judgement.

## Persona is an axis, not a constant

`?persona=` is the single sign-in front door for all four personas, and the suite treats it
as a **parameter** rather than a constant baked into each spec.

- **`e2e/personas.ts`** is the registry: four personas, the three destinations they route
  to, the app SPA's 10 nav surfaces, and a **coverage map** naming which persona is proven
  on which surface by which spec.
- **`e2e/personas.test.ts`** makes it load-bearing. **G3** asserts the catalogue matches
  `Sidebar.tsx`'s live `navGroups`; **G6** asserts the rendered (surface, persona) pairs and
  the coverage cells are the same set — in both directions, so a stale cell fails too. A new
  persona, or a new persona-scoped surface, cannot ship uncovered: the guard goes red first.

Two coverage grades, and no third:

| grade | what it buys | what it does not |
|---|---|---|
| `drives` | the spec signs in as that persona, opens that surface, and asserts **rendered content** | — |
| `nav-only` | the spec proves the surface is **present** in that persona's sidebar, and that the others are absent | says nothing about what the surface renders for that persona |

There is deliberately no `pending`/`planned`/`todo` grade: a cell exists and names a spec
that exists, or it does not exist. A surface may only be downgraded to `nav-only` by editing
`EXPECTED_NAV_ONLY` inside `personas.test.ts` — a visible diff in the file whose job is to
prevent quiet erosion.

A spec may be named for the **axis** it varies (`api/persona-inhouse.spec.ts`) as well as
for a capability: *organize by capability, not by date* forbids **dated** files, not files
named for their subject.
