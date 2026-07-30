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

The suite runs against **one shared deployed dev database with no reset** between runs,
so parallel runs would corrupt each other's data. Every spec must create per-run-unique
data (fresh TINs, random UUIDs, high offsets for empty-state).

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
client-side behaviour because they have no other harness: every frontend vitest project
runs in `node`, so the repo has no DOM component-test layer, and a browser check is the
only place a control, a route guard, or a scroll-spy can be exercised at all.

**Mock-backed assertions pin fixtures, not contracts — and the spec must say so in-file.**
A spec asserting an ops-console counter or an approval-policy list asserts that a seeded
fixture and a pure function over it still agree; it will need revisiting when a real
endpoint lands (`frontend/app/src/lib/workflows.ts:9` — "There is no approvals endpoint").
Writing such an assertion as though it proved a contract is the failure this rule prevents;
refusing to write it at all leaves a shipped screen untested. So: write it, and label it.

Mock-only `app` surfaces follow the same rule. **Workflows, Reports and Settings** carry
functional coverage as sidebar surfaces of the persona that owns them (see below). The
company switcher, XML/UBL preview and onboarding dashboard are not nav surfaces and hold no
coverage cell — note that the switcher *is* **operated** by `workflows.spec.ts` and
`persona-surfaces.spec.ts` as the mechanism for changing the active client, which is not the
same as being covered by them. Submission/transmit is real (M5-09): it is covered via the
invoices **list**'s batch-select-and-submit path (`invoice-surfaces.spec.ts`) — the invoice
**detail** surface deliberately carries no submit control in any status.

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
