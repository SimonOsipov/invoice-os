# In-app routing (ROUTE-01)

**Audience:** anyone building ROUTE-02 through ROUTE-06, or adding a 14th `View`. The
seam is `frontend/app/src/lib/route.ts` (13-row path table, `routePath`, `parseRoute` —
pure, no DOM); the wiring is in `Workspace` (`frontend/app/src/App.tsx`). Read the code
for the how; this page is the contract and the two gotchas that cost real time here.

## The 13 paths

Read from `lib/route.ts`'s `ROUTE_PATHS`, not retyped by hand:

| View | Path |
|---|---|
| `dashboard` | `/` |
| `invoices` | `/invoices` |
| `approvals` | `/approvals` |
| `rules` | `/rules` |
| `customers` | `/customers` |
| `reports` | `/reports` |
| `workflows` | `/workflows` |
| `clients` | `/clients` |
| `audit` | `/audit` |
| `settings` | `/settings` |
| `create` | `/create` |
| `detail` | `/invoice` |
| `extraction` | `/extraction` |

Two surprises: `dashboard` is the bare root `/`, not `/dashboard` — the landing hand-off
and the persona strip both land on that pathname. `detail` is `/invoice`, singular —
`/invoices/:id` is reserved for ROUTE-02's drill-down.

## The one-rule URL writer

Every write is `routePath(view) + window.location.hash`. `location.search` is never
read by the seam. This is a security fence, not a style choice: `App.tsx` once left
`?persona=` in the URL after sign-in, which turned it into a credential-free sign-in
link — Back to that history entry walked back into the workspace with no OTP. Echoing
`location.search` into a pushed URL would reopen that hole on every navigation.

The two pre-existing history writers — the review-hash mirror (`App.tsx:543`, which
does echo `search`) and the persona-strip clear (`App.tsx:1650`) — are deliberately left
alone and pinned byte-identical by `App.routeReviewHash.test.tsx`.

## Why `Workspace` can't mount while `?persona=` is live

`App.tsx:1518`: `const [seat, setSeat] = useState<Session | null>(() => (autoPersona ?
null : resolveBootSession()))`. Whenever the URL names an openable persona, `seat`
initialises to `null`, so `App` renders `<SignInLoading>` and never mounts `Workspace` —
which owns every line of the router — on that commit. This is structural, not an effect-
ordering guarantee to remember: there is no child to order against. It's what makes the
one-rule writer above actually hold, and it's pinned by
`App.routePersonaOrdering.test.tsx`, not by comment.

## Boot precedence

`initialView` (DEMO-06 persona-switch carry) → review hash (`#review/<uuid>`) → path →
`dashboard`. See `App.tsx:320-321`.

## The ROUTE-02..06 boundary

- **ROUTE-02** — drill-down ids (`/invoices/:id`, `/extraction/:jobId`), and cold-boot
  seeding for `/invoice` and `/extraction` (see Limitations below).
- **ROUTE-03** — migrates `#review/<uuid>` into the path (`/imports/:batchId/review`,
  epic Q7). Also inherits a known defect: Back into an older review batch after opening
  a second one in the same tab relinks the wrong batch onto that history entry. An
  externally-held link (copied, bookmarked) is unaffected — it still cold-loads its own
  batch. See `decision [second-batch-relinks-an-older-entry]` in `.ralph/ROUTE-01-final.md`.
- **ROUTE-04** — filters and sub-tabs in the URL.
- **ROUTE-05** — signed-out deep links (the landing front door).
- **ROUTE-06** — the stale-state sweep. ROUTE-01 closed exactly one atom
  (`extractionJobId`, cleared in `switchClient`) as a prerequisite; everything else is
  ROUTE-06's.

## Limitations ROUTE-01 leaves open

- `/invoice` cold-booted has no selection — `InvoiceDetail` renders its `EmptyState`.
- `/extraction` cold-booted has no `extractionJobId` — nothing renders.

Both are ROUTE-02's cold-boot seeding to close.

## Two things that cost time here

**The review-hash mirror fires on every view change, not just review exits.** The effect
at `App.tsx:540-546` is keyed on `[view, createStep, reviewBatchIds]` and runs on *any*
of the three changing — including a plain sidebar nav that has nothing to do with
review. Any test asserting on history writes around a nav must account for this second
writer firing in the same commit.

**jsdom's environment is per test file, not per test.** `window.history` survives across
`it()` blocks in one file. A test that pushes a URL leaks it into the next test's boot
seed unless `beforeEach` resets it with `window.history.replaceState(null, '', '/')`. A
static guard in `App.routeNavigate.test.tsx`
(`guard_everyAppRenderingTestFileResetsTheJsdomUrl`) enforces this across all 10 files
that render `<App />`.
