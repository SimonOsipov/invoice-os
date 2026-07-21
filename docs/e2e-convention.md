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
`invoice-lifecycle`, `dashboard`. Today these live mostly in `e2e/topology/` (the
de-facto capability layer — `import-wizard.spec.ts`, `invoice-surfaces.spec.ts`,
`topology.spec.ts`); the one legacy dated file, `e2e/demo/day30.spec.ts`, is pending
conversion (M4-14). Exact file/directory layout is the implementation's choice — the
rule is the *organizing axis* (capability, not date), not a fixed tree.

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

The **tenant-facing `app` SPA** (gateway-wired) is the **only** functional-E2E target.

- `ops-console` (mock) and `landing` (static marketing) get **smoke only** — no live
  backend to exercise.
- Mock-only `app` surfaces (Customers, Reports, Settings, company switcher, XML/UBL
  preview, single-document approve/transmit, onboarding dashboard) are **out** — a
  browser test there asserts nothing real.
