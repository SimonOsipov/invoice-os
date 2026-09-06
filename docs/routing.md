# In-app routing

**Audience:** anyone building ROUTE-03 or ROUTE-06, or adding a 14th `View`. The seam is
`frontend/app/src/lib/route.ts` (13-row path table, `routePath`, `parseRoute`, `routeUrl`,
`routeQuery`, `parseLocation` — pure, no DOM); the wiring is in `Workspace`
(`frontend/app/src/App.tsx`). Read the code for the how; this page is the contract and the
gotchas that cost real time here.

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
| `detail` (drill-down) | `/invoices/:id` |
| `extraction` (drill-down) | `/extraction/:jobId` |

Two surprises: `dashboard` is the bare root `/`, not `/dashboard` — the landing hand-off
and the persona strip both land on that pathname. `detail` is `/invoice`, singular, not
`/invoices` — segment count is what tells the two-segment drill-down form apart (below).

The last two rows aren't a 14th/15th `View`: `ROUTE_PATHS` stays a 13-row table, total over
`View` (`[route-grammar-extends]`). `/invoices/<id>` and `/invoices` can't collide because
segment count discriminates them — one segment reads the list, two reads the drill-down.

## The codec

`parseRoute(pathname): Route | null` returns `{ view, id }` — one codec widened for the
drill-downs, not a second `parseDrillId` (`[one-codec]`). `routePath(view, id?)` is its
inverse for the path alone.

`routeUrl(view, params)` is the writer's inverse: path plus the owned query, never a hash.
`routeQuery(view, params)` returns only the query half, for the one caller that stores path
and query in separate fields (the signed-out capture, below).

`parseLocation(pathname, search)` is boot's total reader. It returns six fields —
`{ view, invoiceId, jobId, settingsTab, q, auditInvoice }` — with `view` never null, falling
back to `dashboard` rather than throwing on an unparseable path.

## Owned params

| View | Owns | URL |
|---|---|---|
| `invoices` | `q` (query) | `/invoices?q=<term>` |
| `audit` | `invoice` (query) | `/audit?invoice=<uuid>` |
| `settings` | its tab (path segment) | `/settings/<tab>` |
| `detail`, `extraction` | its id (path segment) | `/invoices/:id`, `/extraction/:jobId` |

Every other view owns nothing, so nothing else is ever emitted or read.

**R1 — Owned params only.** `routeUrl` emits only what the view it is serialising owns,
built from its arguments; it never reads `window.location.search`. This is ROUTE-01's
`[one-writer-rule]` unrelaxed — the fence was always "never echo the live query string", not
"no query string". `?foo=1` is owned by nobody, so it is dropped rather than carried.

**R2 — Omit the default.** `q=''`, `invoice=null` and the `members` tab serialise to nothing
at all: `/invoices`, `/audit`, `/settings`. It is `dashboard → /` applied one level down.

**R3 — The parse is total and never produces an unrenderable state.** An unknown settings
tab resolves to `members` in `parseLocation`; an unavailable one (`company` outside an
in-house workspace) is clamped the same way by `availableSettingsTab`. A non-UUID `invoice`
is dropped — the audit reader 400s on a malformed id, so forwarding it would render an error
state where the ordinary empty state is correct. An over-long `q` goes through
`clampFilterText` (200 UTF-8 bytes, the server's cap). Every string in the world yields a
renderable result, including the empty one.

**R4 — Push on a navigation, replace on a correction.** `navigate` pushes. The boot
alignment, a settings-tab click, a search-box clear and an in-screen audit filter edit all
replace. The `popstate` handler writes nothing — the browser already moved the URL, and a
write there would push a duplicate entry on every Back press. A *committed* search pushes
exactly one entry; the header box is a `<form onSubmit>`, so there is no per-keystroke write
path and therefore no debounce and no settle timer.

### `/settings/:tab`

Six addressable tabs: `members`, `roles`, `connectors`, `api`, `signing`, `company`. The
strip shows `company` only when `mode === 'inhouse'`, so the addressable set and the
available set differ by one member in a firm workspace; an unavailable tab is an unknown tab
(R3).

`members` is the default, so R2 omits it: Members is bare `/settings`, never
`/settings/members`. Both halves of that omission are pinned —
`App.routeNavigate.test.tsx`'s `settings_returningToMembersWritesTheBarePath` for the
writer, and the `SETTINGS_TAB_URL` map inside `e2e/topology/roles.spec.ts`'s
`openSettingsTab` for the browser.

## The URL writer

One writer per navigation: `navigate(view, params?)` sets the view and the destination's
owned params from the same `params` object, then writes `routeUrl(...) + window.location.hash`.
State and the address bar come from one object, so they cannot diverge.

No path writer reads `window.location.search`. This is a security fence, not a style choice:
`App.tsx` once left `?persona=` in the URL after sign-in, which turned it into a
credential-free sign-in link — Back to that history entry walked back into the workspace
with no OTP. R1 keeps that fence intact now that URLs carry a query: the writer never echoes
the live search, it re-serialises only the params the codec owns, and `persona` is owned by
no view.

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
against. It's what makes the writer rule above actually hold, and it's pinned by
`App.routePersonaOrdering.test.tsx`, not by comment.

**A path segment survives the strip; a query does not.** The strip rebuilds the URL as
`pathname + hash`, discarding the whole search string, and it runs at `App`'s mount while
`<SignInLoading>` renders — before `Workspace`, which owns the boot seed, exists. So
`?persona=` cannot coexist with an owned query param, and the deployed `?q=` and `?invoice=`
deep-link specs in `e2e/topology/invoice-surfaces.spec.ts` sign in first and navigate
second, never in one goto.

## Boot precedence

`initialView` (DEMO-06 persona-switch carry) → review hash (`#review/<uuid>`) → path →
`dashboard`. See the `view` lazy initializer in `Workspace`. The path tier reads the
`{ path, search }` boot seed, not `window.location.pathname` directly — ROUTE-05
substitutes a restored deep-link destination there when the live path is the bare root; it
is not a new precedence tier. See the section below.

Both drill-down ids gate on the *winning* view, `bootView`, never `seed.view` directly
(`[ids-gate-on-the-winning-view]`) — `initialView` or a review hash can outrank the path,
and a `create` boot must never inherit an invoice id from a URL that lost. The
mount-alignment effect then serialises that same boot state back into the URL
(`[alignment-must-carry-the-id]`): without it, a correct deep link renders right and then
silently rewrites its own address bar one tick later — right panel, wrong link to copy. The
alignment re-emits the owned params from state and drops the unowned ones, which is where
R1 gets applied to a URL nobody in-app wrote.

## The `auditPrefilter` invariant

**`auditPrefilter` and the `invoice` param of the current `/audit` URL are the same fact.**
There is no hand-off to consume, so ROUTE-01's clear-on-mount effect is gone.

The atom has **screen lifetime**: `navigate` to any view other than `audit` clears it,
because the pill that edits it lives inside `AuditView` and unmounts with the screen.
`invoiceQuery` and `settingsTab` are **durable** instead — the header search box stays
mounted, so clearing the term on nav would leave the box reading `acme` over an unfiltered
list. The rule behind both: a filter lives exactly as long as the control that edits it
(`[a-filter-outlives-nothing-but-its-control]`).

Four things enforce the invariant, none of them a comment:

1. **Every writer writes both.** `openAuditForInvoice` and `setAuditInvoiceFilter` each set
   the atom and write the URL in one statement block; `navigate` clears both together on the
   way to any other view.
2. **A static guard counts the bare setter.**
   `App.routeNavigate.test.tsx`'s `guard_everyAuditPrefilterWriteGoesThroughAUrlWriter`
   allows exactly four call sites. `guard_everyInvoiceQueryWriteGoesThroughAUrlWriter` and
   `guard_theRawQueryAndTabSettersHaveExactlyThreeCallSitesEach` do the same for the other
   atoms and for the raw `_`-suffixed setters underneath them.
3. **`popstate` re-derives it from the URL**, so Back cannot produce a URL that lies about
   the screen it is showing.
4. **The old suite's behaviour is re-asserted, not deleted.**
   `App.auditPrefilter.test.tsx`'s `AC-4: the atom lives exactly as long as the /audit URL`
   keeps both load-bearing claims — a manual nav to Audit lands unfiltered, no stale atom
   off the Audit screen — and adds the one that used to be forbidden,
   `platformCtx_theAtomSurvivesARerenderOnAudit`.

`invoiceNumber` stays out of the URL: `openAuditForInvoice` knows it, a bookmark does not,
and `invoiceFilterPillLabel(null)` already renders `One invoice`.

## The signed-out deep link

A sessionless visit to a real path is remembered across the landing round trip in
`sessionStorage`, never in a URL. `frontend/app/src/lib/deepLink.ts` owns the key.

| | |
|---|---|
| Key | `invoice-os.deepLink` |
| Value | `{ v: 2, path, query, at: <epoch ms> }` |
| TTL | 10 minutes (`DEEP_LINK_TTL_MS`). Absent, expired, corrupt, future-dated and version-mismatched all read as "no destination". |
| Stored | an app-relative path — never `/`, never `//host` — plus the query `routeQuery` authored |

- **Capture** — the front-door effect (the one commented *"The single front door"*), in the
  same statement block as the `window.location.href` that destroys the page. It runs the live
  location through `parseLocation` and re-serialises with `routeQuery`, so an unowned param
  is discarded before storage is touched.
- **Restore** — `Workspace`'s one-source `{ path, search }` boot seed, and only when the live
  pathname is the bare root. That is the shape the `?persona=` hand-off always arrives in
  (`destUrl` carries no path); a live non-root path is the URL the browser is showing and
  always wins. Path and query resolve in one statement, so a restored destination's query can
  never be paired with the live root's.
- **Clear** — `Workspace`'s mount-alignment effect (consume-once, on every mount) and
  `signOut`.

The restore cannot re-attach a consumed `?persona=`: `Workspace` cannot mount while the param
is live (section above), and the capture stores only the query `routeQuery` authored — `persona`
is owned by no view. `sessionStorage` is user-writable, so the restore re-validates through
the same `parseLocation` a live URL gets rather than trusting the blob.

**Schema version 2.** `readDestination` compares `parsed.v === DEEP_LINK_SCHEMA_VERSION`
strictly. A `v: 1` blob left by the previously-deployed build reads as "no destination", and
a `v: 2` blob is rejected by an old build still loaded in another tab or served from a
rollback. In that window the user signs in and lands on the dashboard instead of the page
they asked for — the pre-ROUTE-05 behaviour, not a crash and not a wrong screen. The TTL
bounds the window to ten minutes. `lib/deepLink.test.ts` pins one version below and one
above current.

The round trip crosses two origins, so nothing below the browser can observe it. Its two
oracles are both in `e2e/topology/auth.spec.ts`: `deployed app: a signed-out deep link
returns to its destination after sign-in` (the path) and `deployed app: a signed-out deep
link returns to its FILTER after sign-in` (the query — the only spec anywhere that exercises
a restored query end to end).

**The merged shape (ROUTE-02 merge).** The `{ path, search }` seed resolves the destination
first; `parseLocation` then decodes it into the six fields. A signed-out visit to
`/invoices/<id>` is captured, restored after sign-in, and seeds both the detail view and its
id — not just the view. Pinned by `App.signedOutDeepLink.test.tsx`'s `Workspace boot: a
restored destination carries its drill-down id too (ROUTE-02 merge)` block.

## The ROUTE-02..06 boundary

- **ROUTE-02** — **shipped.** Drill-down ids (`/invoices/:id`, `/extraction/:jobId`) and
  cold-boot seeding for both. `carryView` collapses `detail`/`extraction`
  back to `invoices` on a persona switch, id included: a remounted identity cannot resume a
  selection it may not be entitled to (`[carryview-drops-the-id]`).
- **ROUTE-03** — migrates `#review/<uuid>` into the path (`/imports/:batchId/review`,
  epic Q7). Also inherits a known defect: Back into an older review batch after opening
  a second one in the same tab relinks the wrong batch onto that history entry. An
  externally-held link (copied, bookmarked) is unaffected — it still cold-loads its own
  batch. That second half is pinned by `App.routeReviewHash.test.tsx`'s
  `link_anExternallyHeldReviewLinkStillColdLoadsItsOwnBatch`, which names `decision
  [second-batch-relinks-an-older-entry]`. The decision's own text is in ROUTE-01's run log,
  which never ships — `.ralph/` is gitignored.
- **ROUTE-04** — **shipped.** Filters and sub-tabs in the URL: the owned-param table, R1–R4
  and the `auditPrefilter` invariant above. The signed-out **filter** was closed here, not in
  ROUTE-05 — ROUTE-05 restored a path only.
- **ROUTE-05** — **shipped.** A signed-out visit to a real path returns there after sign-in;
  the destination rides `sessionStorage`, not the URL (section above). ROUTE-02 inherited it
  for free: the boot seed carries the captured path as a raw string, so the widened
  `parseRoute` is all a restored `/invoices/:id` needs to resolve. The captured query now
  rides beside it and is re-validated through the same `parseLocation` a live URL gets.
- **ROUTE-06** — the stale-state sweep. ROUTE-01 closed one atom (`extractionJobId`,
  cleared in `switchClient`); ROUTE-02 additionally scrubs the drill-down id off the URL on
  that same call (`[switchclient-scrub]`). The general case is still ROUTE-06's, and its
  shape is **stale Back**: a history entry outlives the identity or the screen state that
  created it. ROUTE-04 falsified an "unreachable" argument on that mechanism twice, and both
  times the argument had enumerated how a URL gets *created* and stopped there — a sync
  effect that `replaceState`d over the entry a Back press had just restored (now pinned by
  `AuditView.test.tsx`'s `auditView_theSyncEffectNeverWritesTheUrlBack`), and a Company
  settings entry from an earlier in-house session resurfacing in a firm workspace (now
  clamped by `App.routeBoot.test.tsx`'s
  `popstate_aStaleCompanyEntryIsClampedForAFirmWorkspace`). Reachability here is tested, not
  argued.

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
(`guard_everyAppRenderingTestFileResetsTheJsdomUrl`) enforces this across all 15 files
that render `<App />`.
