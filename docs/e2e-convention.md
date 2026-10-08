# E2E Testing Convention

Governs the Playwright suites in `e2e/`. They run against a **deployed** environment
only (no local server) as the post-deploy step in `.github/workflows/dev-env.yml`, and
are auto-collected via `testMatch **/*.spec.ts` — a new spec needs no workflow edit.
The workflow's `E2E gate` job reports the suites' verdict to GitHub as one check.

## Organize by capability, not by date

Browser E2E is organized by **product capability / feature** — never by milestone or
demo date. There are **no `dayN.spec.ts` files**.

- A milestone's "moment of value" is proven by **extending the relevant capability
  flow**, not by adding a new dated end-to-end journey.
- Test files carry feature names, never a milestone or a demo date.
- Why: dated demos accrete and overlap — each re-walks the previous one's steps as a
  prefix, so the suite grows one full journey per milestone forever. Feature-named flows
  are extended in place instead.

Target capability flows: `auth`, `portfolio`, `validation`, `import`,
`invoice-lifecycle`, `dashboard`. These live in `e2e/topology/` (the de-facto capability
layer — `auth.spec.ts`, `import-wizard.spec.ts`,
`invoice-surfaces.spec.ts`, `portfolio.spec.ts`, `isolation.spec.ts`); no dated file
remains (M4-14). The `validation` capability is driven by
`e2e/topology/invoice-surfaces.spec.ts`, which asserts the rule-set version against the
Compliance card's header chip. Exact file/directory layout is the implementation's
choice — the rule is the *organizing axis* (capability, not date), not a fixed tree.

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

**chromium-only, `workers: 1` per unit.** No multi-browser matrix. The api suite and each
topology unit run serial on one worker. Topology runs as four units in parallel, as listed
in `e2e/topology/shards.ts`: a `serial-lane` of the specs that stay on the seeded persona
tenants (1111 / 2222), plus one shard per big file
(`import-wizard`, `import-wizard-2`, `invoice-surfaces`), each on its own seeded tenant pair
(`db/seed.e2e-shards.sql`). The import-wizard tests run as two files, split for time:
`import-wizard.spec.ts` (unit `import-wizard`) and `import-wizard-2.spec.ts` (unit
`import-wizard-2`). The helpers both use live in `e2e/topology/importWizardShared.ts`.
`playwright.topology.config.ts` builds one project per unit, and `--project=<unit>` runs one.
A new topology spec file must be added to a unit in `shards.ts`, or every topology run fails
at config load.

**A browser spec that needs an app session on a seeded tenant signs in with `signInAs(page, id, { tenantId })`**
(`e2e/personaSession.ts`). It drives the landing "Platform login" form as the tenant's e2e member (`e2e/realAccounts.ts`:
`e2e-member-<tenantId>@example.com`, an admin that `ensureMember` registers and admits through
`POST /auth/mock/member`, once per worker) and waits for the app to draw. `ensureMember` also takes a role:
`e2e-member-<tenantId>-<role>@example.com` for `preparer` and `reviewer`, which `ctl login` uses (`e2e/README.md`). It fails a sign-in whose
stored session is not a hand-off session bound to that tenant. `tenantId` defaults to the seeded
tenant of the kind (1111 firm, 2222 in-house); a shard passes its own.

**A spec that needs the token of an invite sets it through `setInvitationToken` (`e2e/api/client.ts`).**
It calls `POST /auth/mock/invitation-token`, beside `POST /auth/mock/member`, which replaces a pending
invite's token with one the spec chose; `inviteWithToken` invites through the tenancy API, sets a fresh
token and returns it. A fork's sender captures mail, so no spec can read a real token. Only the mock build serves the route.

**Every run gets a database of its own, and shares it across all three suites.**

The suites run only on a pull request (`dev-env.yml`'s `e2e` and `topology` jobs), against that PR's own
ephemeral Railway environment. That environment's Postgres is a *fork* of the persistent
environment's volume, so it is born holding everything that environment holds — and the
gateway TRUNCATEs the tenant-data tables, purges the demo tenants and re-seeds the curated
demo state at boot, on every deploy (boot order: bootstrap → migrate → reset → purge →
seed → shard seed, the last on PR forks only; Decision [pr-only-reset], 2026-07-28, `internal/platform/db/reset.go`, and DEMO-04,
`internal/platform/db/demopurge.go`). A run
therefore starts from the seed, never from another run's leftovers, and the health-gate
fails the run outright if that reset did not happen — it is armed by a hand-set Railway
variable that otherwise fails closed and silent. The purge needs no such variable: it is
gated like the seed, by `GATEWAY_DB_BOOTSTRAP` and the `ENVIRONMENT` that CI's
`fork-vars-after-urls` sets in every PR fork.

What a spec still cannot assume is an empty table:

- smoke → api → topology run in that order against ONE deployment with **no reset between
  them**, and `api/perf.spec.ts` alone creates 500 invoices before topology reads a list.
  The topology units run at the same time as each other, after api;
- a Playwright retry re-runs a failed test against everything its first attempt left behind;
- the tables holding admin CRUD split two ways since DEMO-04, and the difference matters
  when you reason about what a spec inherits:
  - `memberships` and the approval-policy tables are excluded from the reset
    (`resetTables`'s own EXCLUDED block says why) **and** from the purge
    (`purgeExcludedTables` in `demopurge.go`), so writes there still outlive the run that
    made them;
  - `workflow_roles` and `workflow_role_members` are excluded from the reset but ARE
    purged, on the demo tenants only — `db.Seed` restores all 14 seeded roles and 13
    seeded staffing rows in the same `Provision` call (Decision [include-workflow-roles]).
    A role or staffing row a spec creates at runtime on a demo tenant does NOT survive the
    next deploy; the seeded ones always come back.
- Specs leave rows that persist across pushes to one PR environment, in `auth.users`
  (GoTrue's schema, which neither the reset nor the purge touches), in `staff_members` (in
  neither `resetTables` nor the purge) and in `tenants` and `memberships` (the reset excludes
  both, and the purge touches the four demo tenants only):
  - `api/registration.spec.ts`: each run's `auth.users` row, and the tenant and membership
    its fork chain provisions.
  - `topology/auth.spec.ts`: each real-account journey (`provisionRealAccount` in
    `api/client.ts`) leaves an `auth.users` row, a tenant and a membership. The two
    add-company journeys also leave one `business_entities` row and its audit row; the next
    deploy's reset truncates both, so they live only until the next push. The registration
    journey (`deployed journey: a stranger registers ...`) registers through the landing UI, so
    it leaves one `auth.users` row, tenant and membership per kind; its repeat registration adds none.
  - `api/session-handoff.spec.ts`: each registering test leaves an `auth.users` row only; it
    provisions no workspace. The staff-claim test also leaves the tenant and membership of
    its `provisionRealAccount` call and one `staff_members` row.
  - Every `provisionStaffAccount` call (`api/client.ts`) leaves one `auth.users` row and one
    `staff_members` row, and no workspace: the smoke
    console specs (`staffSession.ts`), `topology/ops-console.spec.ts` and
    `topology/support-console.spec.ts` call it; so does each console test in `topology/design-system.spec.ts`
    and each console journey in `topology/auth.spec.ts`.
    `POST /auth/mock/staff`, which writes the row, exists only in the mock build that every PR
    fork runs.
  - `signInAs` leaves one `auth.users` row and one admin `memberships` row per tenant: the
    stable e2e member (`ensureMember`, `e2e/realAccounts.ts`) registers once and
    `POST /auth/mock/member` (`internal/gateway/mockmember.go`) upserts its membership. A later
    call or push adds none, and it provisions no tenant. Only the mock build serves the route.
  - `ctl login` (`e2e/ctl/login.ts`, agents only): per PR environment, the first call registers 8 accounts. Two are
    the e2e admins that `signInAs` already uses (1111 and 2222) and add no rows. The other six leave 4 `auth.users` and 4 `memberships` rows
    (preparer and reviewer on 1111 and 2222) and 2 `auth.users` and 2 `staff_members` rows (developer, support). A rebuilt environment or a retry of a failed
    `provisionAll` re-creates the two staff accounts and adds 2 more `auth.users` and 2 more `staff_members` rows.
  - `api/invitation-accept.spec.ts` and the accept-page tests of `topology/auth.spec.ts`: each admin
    workspace leaves an `auth.users` row, a tenant and a membership, each invite an `invitations`
    row (the reset excludes `invitations`; the purge covers the demo tenants only), each invitee
    or stranger account an `auth.users` row, and each accepted invite an active `reviewer` membership. A suspended-member
    test also leaves a `suspended` membership in a second fork workspace. `POST /auth/mock/invitation-token`
    (`internal/gateway/mockinvitation.go`) writes only the invite's token hash. Only the mock build serves it.

  This is harmless: every other run registers a fresh address and provisions for a fresh
  subject, and the e2e member rows are at most three per tenant (admin, preparer, reviewer), all
  accepted by `isE2eMemberEmail`.

So the rule is unchanged, and `workers: 1` per unit still holds: every spec creates per-run-unique
data (fresh TINs, random UUIDs, high offsets for empty-state), acts on rows it created, and
asserts containment or a live-read comparison rather than a literal count.

## Target surface

Five frontends deploy — the `landing` front door and four more SPAs — and
they are **not equally testable**. The line that matters is not *which SPA* a test drives
but **what backs the assertion**:

| surface | backing | what a browser assertion may claim |
|---|---|---|
| `app` SPA | gateway-wired (real API, real DB) | a **contract**: rendered state matches what the API returned |
| `ops-console` | mock data; the gateway backs only the staff session | **fixture behaviour** — that the console's own client-side logic works. Entry is a **contract**: a real staff session opens it |
| `support-console` | mock data; the gateway backs only the staff session | same |
| `library` | static content, no backend | render only: the page renders its headline with no console error; plus the consent notice, the stored answer, the privacy link and the tag requested on the production host only (`e2e/smoke/library-consent.spec.ts`) |
| `landing` | marketing, plus a gateway-backed sign-in form | render and client-side navigation; the sign-in form is a **contract** — a real account signs in and lands in its workspace, or, for a staff account that came from a console, in that console |

The `app` SPA, and the landing sign-in form that hands off to it or to a console, remain the
only places a browser test can prove the **stack** integrates end to end. A console is entered
the way a staff member enters it: the smoke and console topology specs seed a real staff session
(`seedStaffSession`, `e2e/staffSession.ts`, a staff account from `provisionStaffAccount`),
and the first topology journey in `auth.spec.ts` signs in through landing (its renewal and
sign-out journeys seed a session). The consoles and the rest
of the landing page carry functional coverage of their own
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

`invoice-surfaces.spec.ts` extends this to the invoice-level decision controls and the
approval card. The firm tenant's policy is active tenant-wide (`ensureFirmPolicyActive`'s `beforeAll`
convergence), so a validated firm invoice always arms a run — the suite's decision-block
test covers the **armed, read-only** case: both controls disabled because the driving
persona holds neither workflow role, the AXIS-2 reason rendered beside them, and the
approval card's holder line naming the open run's pending step, its role and its holder. The
**no-run empty state** is still reachable on any never-validated draft, and is asserted there
instead. (This
file's own submitting fixtures, and `import-wizard.spec.ts`'s, arm and close a run since
APPR-14-07 — over the API side channel via `approveUntilClosed`, never through the UI's
own Approve/Reject buttons.) The **UI-driven approve/reject journey itself** — a persona
who does hold the role clicking through to a decision — still lives in
`e2e/api/contract-invoice.spec.ts` instead, because `[topology-never-publishes]` still bars
a topology spec from publishing the dedicated probe policy that journey would need.

The PAGE reads that run on mount — `LiveInvoiceDetail`'s own `useAsync`, which the state
strip and the approval card both consume — and that GET answers 404 when there is no run —
which Chromium logs as a console error, tripping the `collectErrors` gate in every
detail-page test. The gate carries the exception (`consoleGate.ts`, matched on the
message's resource URL so no other 404 is masked), not the API: the uniform 404 is a
deliberate no-oracle property, and a 200-with-null-run would leak cross-tenant existence.

Mock-only `app` surfaces follow the same rule. **Reports and Settings** carry
functional coverage as sidebar surfaces of the persona that owns them (see below). The
company switcher and the add-company task (covered by the two `auth.spec.ts` add-company journeys) are not nav surfaces and hold no
coverage cell — note that the switcher *is* **operated** by `workflows.spec.ts` and
`persona-surfaces.spec.ts` as the mechanism for changing the active client, which is not the
same as being covered by them. Submission/transmit is real (M5-09): it is covered via the
invoices **list**'s batch-select-and-submit path (`invoice-surfaces.spec.ts`), and via that
same file's "detail surface: submit one invoice from its own page" test, which covers a
single invoice submitted straight from its own **detail** page. The XML/UBL viewer is real
too (BUG-04): it stays a non-nav surface with no coverage cell. It opens from the detail
rail's UBL document card, and is covered by that same file's "invoice detail: the UBL
document card fetches nothing on open, Download saves the server's own bytes, and View
renders the same document" and "invoice detail: an incomplete invoice's UBL document card
prints the server's own reason and offers no action" tests, plus `api/contract-ubl.spec.ts`.

**What "smoke only" covered, and still does.** Render checks, plus client-side behaviour
that has no other harness. That was always a floor rather than a licence for per-screen
coverage, and it still is: *keep the browser layer thin* and *functional only — no visual
regression* apply here unchanged, and the browser layer's size is bounded by the persona
surface catalogue below, not by the number of screens that exist. **The guard below enforces
only the floor** — that no persona-scoped surface ships uncovered. Nothing mechanical
enforces the ceiling; keeping the layer thin stays a review judgement.

## Persona is an axis, not a constant

The suite treats the persona as a **parameter** rather than a constant baked into each spec.
The four ids are axis labels: workspace kind (`firm`, `inhouse`) and staff destination
(`developer` opens the ops console, `support` the support console). An app session is a real
account in a workspace of one kind, signed in through the landing form (`signInAs`). A console
takes a staff session (Target surface). The `persona` query parameter is no credential at any
destination: a visit that carries it and no session is sent back to the landing page, and a
visit that carries it over a live session keeps that session and mints nothing.

- **`e2e/personas.ts`** is the registry: four personas, the three destinations they route
  to, the app SPA's 10 nav surfaces, a **coverage map** naming which persona is proven
  on which surface by which spec, and the **boundary matrix**, which records all 12
  (persona, destination) pairs as refusals (`smoke/persona-boundaries.spec.ts`).
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
