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

The two pre-existing history writers — the review-hash mirror in `Workspace` (the
`replaceState` that rebuilds `pathname + search + hash`, the one writer that does echo
`search`) and the persona-strip clear (the effect commented *"Drop the consumed
`?persona=` from the URL"*) — are deliberately left alone and pinned byte-identical by
`App.routeReviewHash.test.tsx`.

## Why `Workspace` can't mount while `?persona=` is live

`App.tsx`'s `seat` initializer: `const [seat, setSeat] = useState<Session | null>(() =>
(autoPersona ? null : resolveBootSession()))`. Whenever the URL names an openable
persona, `seat` initialises to `null`, so `App` renders `<SignInLoading>` and never
mounts `Workspace` — which owns every line of the router — on that commit. This is
structural, not an effect-ordering guarantee to remember: there is no child to order
against. It's what makes the one-rule writer above actually hold, and it's pinned by
`App.routePersonaOrdering.test.tsx`, not by comment.

## Boot precedence

`initialView` (DEMO-06 persona-switch carry) → review hash (`#review/<uuid>`) → path →
`dashboard`. See the `view` lazy initializer in `Workspace`. The path tier reads
`bootPath` (`App.tsx:320-322`), not `window.location.pathname` directly — ROUTE-05-03
substitutes a restored deep-link destination there when the live path is the bare root;
it is not a new precedence tier. See the section below.

## The signed-out deep link (ROUTE-05)

A sessionless visit to a real path is remembered across the landing round trip in
`sessionStorage`, never in a URL. `frontend/app/src/lib/deepLink.ts` owns the key.

| | |
|---|---|
| Key | `invoice-os.deepLink` |
| Value | `{ v: 1, path: '/audit', at: <epoch ms> }` |
| TTL | 10 minutes (`DEEP_LINK_TTL_MS`). Absent, expired, corrupt and future-dated all read as "no destination". |
| Stored | app-relative paths only — never `/`, never `//host` |

- **Capture** — the front-door effect (the one commented *"The single front door"*), in the
  same statement block as the `window.location.href` that destroys the page.
- **Restore** — `Workspace`'s `bootPath` initializer, and only when the live pathname is the
  bare root. That is the shape the `?persona=` hand-off always arrives in (`destUrl` carries
  no path); a live non-root path is the URL the browser is showing and always wins.
- **Clear** — `Workspace`'s mount-alignment effect (consume-once, on every mount) and
  `signOut`.

The restore cannot re-attach a consumed `?persona=`: `Workspace` cannot mount while the param
is live (section above), and the one-rule writer never echoes `location.search`.

The round trip crosses two origins, so nothing below the browser can observe it. Its only
oracle is `e2e/topology/auth.spec.ts`'s `a signed-out deep link returns to its destination
after sign-in`.

## The ROUTE-02..06 boundary

- **ROUTE-02** — drill-down ids (`/invoices/:id`, `/extraction/:jobId`), and cold-boot
  seeding for `/invoice` and `/extraction` (see Limitations below).
- **ROUTE-03** — migrates `#review/<uuid>` into the path (`/imports/:batchId/review`,
  epic Q7). Also inherits a known defect: Back into an older review batch after opening
  a second one in the same tab relinks the wrong batch onto that history entry. An
  externally-held link (copied, bookmarked) is unaffected — it still cold-loads its own
  batch. That second half is pinned by `App.routeReviewHash.test.tsx`'s
  `link_anExternallyHeldReviewLinkStillColdLoadsItsOwnBatch`, which names `decision
  [second-batch-relinks-an-older-entry]`. The decision's own text is in ROUTE-01's run log,
  which never ships — `.ralph/` is gitignored.
- **ROUTE-04** — filters and sub-tabs in the URL.
- **ROUTE-05** — **shipped.** A signed-out visit to a real path returns there after sign-in;
  the destination rides `sessionStorage`, not the URL (section above). ROUTE-02 inherits it
  for free: `bootPath` carries the captured path as a raw string, so a widened `parseRoute`
  is all a restored `/invoices/:id` needs to resolve. ROUTE-02 will separately need the
  mount-alignment write to stop collapsing `/invoices/<id>` to `routePath('invoices')`.
- **ROUTE-06** — the stale-state sweep. ROUTE-01 closed exactly one atom
  (`extractionJobId`, cleared in `switchClient`) as a prerequisite; everything else is
  ROUTE-06's.

## Limitations ROUTE-01 leaves open

- `/invoice` cold-booted has no selection — `InvoiceDetail` renders its `EmptyState`.
- `/extraction` cold-booted has no `extractionJobId` — nothing renders.

Both are ROUTE-02's cold-boot seeding to close.

## Two things that cost time here

**The review-hash mirror fires on every view change, not just review exits.** The
review-hash mirror effect is keyed on `[view, createStep, reviewBatchIds.join(',')]` and
runs on *any* of the three changing — including a plain sidebar nav that has nothing to
do with review. Any test asserting on history writes around a nav must account for this
second writer firing in the same commit.

**jsdom's environment is per test file, not per test.** `window.history` survives across
`it()` blocks in one file. A test that pushes a URL leaks it into the next test's boot
seed unless `beforeEach` resets it with `window.history.replaceState(null, '', '/')`. A
static guard in `App.routeNavigate.test.tsx`
(`guard_everyAppRenderingTestFileResetsTheJsdomUrl`) enforces this across all 14 files
that render `<App />`.
