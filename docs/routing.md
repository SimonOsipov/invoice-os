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
`create`'s owned "query" is a whole path segment instead: `params.reviewBatchIds` serialises
through `reviewPath` to `/imports/<ids>/review`, the same shape `routePath` already has for
`detail`/`extraction`. `routeQuery(view, params)` returns only the query half, for the one
caller that stores path and query in separate fields (the signed-out capture, below).

`parseLocation(pathname, search)` is boot's total reader. It returns seven fields —
`{ view, invoiceId, jobId, settingsTab, q, auditInvoice, reviewBatchIds }` — with `view`
never null, falling back to `dashboard` rather than throwing on an unparseable path.

## Owned params

| View | Owns | URL |
|---|---|---|
| `invoices` | `q` (query) | `/invoices?q=<term>` |
| `audit` | `invoice` (query) | `/audit?invoice=<uuid>` |
| `settings` | its tab (path segment) | `/settings/<tab>` |
| `detail`, `extraction` | its id (path segment) | `/invoices/:id`, `/extraction/:jobId` |
| `create` (review) | its batch ids (path segment) | `/imports/:batchIds/review` |

Every other view owns nothing, so nothing else is ever emitted or read. The review ids are a
single comma-joined path segment (`/imports/<a>,<b>/review`), capped at
`REVIEW_PATH_MAX_IDS` (5, mirroring `importRun.ts`'s `MAX_RUN_FILES`). `parseReviewPath` is
all-or-nothing: one bad segment rejects the whole list, and the cap is a rejection, never a
truncation — a run of six ids parses to `null`, not a silently truncated five.

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
replace. The `popstate` handler writes nothing on the ordinary path — the browser already
moved the URL, and a write there would push a duplicate entry on every Back press. Its one
write is the ROUTE-06 identity clamp below, and that is a `replaceState`. A *committed* search pushes
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
owned params from the same `params` object, then pushes `routeUrl(...)` — path plus owned
query, never a hash. State and the address bar come from one object, so they cannot diverge.

No path writer reads `window.location.search`. This is a security fence, not a style choice:
`App.tsx` once left `?persona=` in the URL after sign-in, which turned it into a
credential-free sign-in link — Back to that history entry walked back into the workspace
with no OTP. R1 keeps that fence intact now that URLs carry a query: the writer never echoes
the live search, it re-serialises only the params the codec owns, and `persona` is owned by
no view.

Two writers sit outside `navigate` and are pinned rather than folded in. The review-path
mirror in `Workspace` (the `replaceState` keyed on `[view, createStep,
reviewBatchIds.join(',')]`) rebuilds only `create`'s own path from state and never reads
`location.search` — it joined the ten writer bodies `lib/routeWriterGuard.test.ts` proves
clean, rather than staying that guard's one deliberate exception. The persona-strip clear
(the effect commented *"Drop the consumed `?persona=` from the URL"*) is now that guard's
sole reader of `search`, and its `new URLSearchParams(window.location.search)` is the control
needle proving the other ten assertions can see a match at all. Both writers' own write
lines are still pinned byte-identical by `App.routeReviewHash.test.tsx`'s
`guard_theTwoExistingHistoryWritersAreUnchanged`.

## The company stamp on a history entry

Every `Workspace` history write carries `{ e: <entityId> }` as its state — nine sites, and
`App.routeNavigate.test.tsx`'s `guard_everyWorkspaceHistoryWriteCarriesTheStamp` allows zero
still passing a literal `null`. The value is `active.entityId` — the memo that resolves
`null` to `clients[0]` — never the raw `activeEntityId` atom: that atom stays `null` until
`switchClient`'s own write, so stamping it directly would mark every pre-first-switch entry
unknown and the clamp could never fire. `switchClient` stamps its `id` **parameter** instead
of either state read: `setActiveEntityId(id)` one line above has not committed. `signOut` and
the persona strip stay `null`-stamped; both live in `App`, outside the slice that guard
counts.

Two of the nine are new machinery. The **stamp backfill** fills the boot entry once the
portfolio resolves — the mount alignment runs before the entities fetch lands, so without it
a cold deep link would stamp `null` forever and be permanently unclampable. It also mirrors
`active.entityId` into a ref, because the `popstate` effect's deps are `[]` and a closure
read there freezes at mount, when `clients` is still `[]`. It copies the null-gated
fill-only idiom already in this file — the import-target re-seed effect at `App.tsx:529-533`,
which only ever fills a stale `null` and never overwrites a resolved value — applied here to
the stamp instead of the import target.

The **clamp** is the second. `popstate` reads the restored entry's stamp; when both sides are
known and differ, it re-derives the path through `carryView` at the TOP of the handler, so
every setter below reads the clamped path. A tail rewrite would leave each atom armed for a
frame and force an enumeration of the atoms to clear — and that enumeration is what missed
`auditPrefilter`. The view is carried; only the selection is dropped. An entry naming no
company (state `null`, or state with no `e`) is unknown, never stale, and never clamps.

### Sign-out closes the same leak, and RLS is the backstop underneath it

`signOut` also scrubs only the current entry (`window.history.replaceState(null, '', '/')`,
`App.tsx:1707`; `clearDestination()`, `App.tsx:1712`), so a buried `/invoices/<id>` entry
survives into the next session. Both shipped personas are different tenants (`auth.ts:45`,
`auth.ts:58`), so a re-sign-in resolves a different `clients[0]` and the stamp differs — the
clamp above fires with no code written for it.

That is a consequence, not the safety net: the real backstop is RLS, and it only covers the
cross-tenant case. `invoices` is `ENABLE` + `FORCE ROW LEVEL SECURITY`
(`migrations/20260714103137_invoices.sql:68-69`) under a policy with no `TO` clause, so it
binds every role (`:76`). Its `USING` scopes by `tenant_id` alone (`:77`) — there is no
`business_entities`-row check — so a same-tenant company switch, the case this story fixes,
is invisible to the database: two entities under one firm share a tenant, and RLS lets either
row through. The clamp above is what stops a Back press from leaking the previous company's
data within one tenant; RLS cannot do that job.

## The per-atom audit

Every `Workspace` atom is audited against the routes that can reach a screen reading it, now
that ROUTE-01..05 made previously-unreachable state reachable by URL. Full reasoning and
citations live in `frontend/app/src/App.atomAudit.data.ts`, not retyped here — this table is
kept honest by `App.atomAudit.test.tsx`'s `guard_theDocsTableMatchesTheAuditedAtoms`, which
fails if a name, its `switchClient` reset, or its verdict disagrees with the module.

| Atom | Reset by `switchClient`? | Routes | Verdict |
|---|---|---|---|
| `suspended` | no | all 13 | `correctly-reset` |
| `clients` | no | all 13 | `correctly-reset` |
| `activeEntityId` | yes | all 13 | `correctly-reset` |
| `bootPath/bootSearch` | no | none | `correctly-reset` |
| `seed` | no | none | `correctly-reset` |
| `view` | yes | all 13 | `correctly-reset` |
| `draft` | yes | /create | `correctly-reset` |
| `handOffDocumentId` | yes | none | `correctly-reset` |
| `createStep` | yes | /create, /imports/<ids>/review | `stale-and-reachable` |
| `reviewBatchIds` | yes | /create, /imports/<ids>/review | `stale-and-reachable` |
| `groups` | yes | /create | `correctly-reset` |
| `groupIndex` | yes | /create | `correctly-reset` |
| `armedField` | no | /create | `stale-but-unreachable` |
| `dragField` | no | /create | `stale-but-unreachable` |
| `detailInvoiceId` | yes | /invoice, /invoices/<id> | `stale-and-reachable` |
| `auditPrefilter` | yes | /audit, /audit?invoice=<id> | `stale-and-reachable` |
| `extractionJobId` | yes | /extraction, /extraction/<jobId> | `stale-and-reachable` |
| `invoiceQuery` | no | all 13, /invoices?q=<text> | `deliberate` |
| `switcherOpen` | yes | all 13 | `correctly-reset` |
| `sandbox` | no | all 13, /settings/<tab> | `correctly-reset` |
| `settingsTab` | no | /settings, /settings/<tab> | `deliberate` |
| `connectors` | no | /settings, /settings/<tab> | `correctly-reset` |
| `connectorMappings` | no | /settings, /settings/<tab> | `correctly-reset` |
| `customRuleStore` | no | /rules | `correctly-reset` |
| `openRuleKey` | yes | /rules | `correctly-reset` |
| `policies` | no | /workflows, /settings/<tab> | `correctly-reset` |
| `editingPolicyId` | yes | /workflows | `correctly-reset` |
| `members` | no | /workflows, /settings/<tab>, /invoice, /invoices/<id> | `correctly-reset` |
| `roles` | no | /workflows, /settings/<tab> | `correctly-reset` |
| `entityId` | yes | /create | `correctly-reset` |
| `pickedFiles` | yes | /create | `correctly-reset` |
| `filesRefusal` | yes | /create | `correctly-reset` |
| `run` | yes | /create, /imports/<ids>/review | `correctly-reset` |
| `documentStages` | no | /create, /imports/<ids>/review | `stale-but-unreachable` |
| `importError` | yes | /create | `correctly-reset` |
| `filing` | no | /create | `deliberate` |
| `filingError` | yes | /create | `correctly-reset` |
| `activeEntityIdRef` | no | none | `correctly-reset` |
| `reqInFlight` | no | none | `deliberate` |

## Why `Workspace` can't mount while `?persona=` is live

`App.tsx`'s `seat` initializer: `const [seat, setSeat] = useState<Session | null>(() =>
(autoPersona ? null : resolveBootSession()))`. Whenever the URL names an openable
persona, `seat` initialises to `null`, so `App` renders `<SignInLoading>` and never
mounts `Workspace` — which owns every line of the router — on that commit. This is
structural, not an effect-ordering guarantee to remember: there is no child to order
against. It's what makes the writer rule above actually hold, and it's pinned by
`App.routePersonaOrdering.test.tsx`, not by comment.

**A path segment survives the strip; a query does not.** The strip rebuilds the URL as
`pathname` alone, discarding the whole search string, and it runs at `App`'s mount while
`<SignInLoading>` renders — before `Workspace`, which owns the boot seed, exists. So
`?persona=` cannot coexist with an owned query param, and the deployed `?q=` and `?invoice=`
deep-link specs in `e2e/topology/invoice-surfaces.spec.ts` sign in first and navigate
second, never in one goto. The review batch ids are the second worked example: they live in
`pathname` too (`/imports/<ids>/review`), so a `?persona=firm` visit to a review link loses
only the query, never the ids — the same guarantee `/invoices/:id` gets, extended to
`create`.

## Boot precedence

`initialView` (DEMO-06 persona-switch carry) → path → `dashboard`. See the `view` lazy
initializer in `Workspace`. Three tiers, not four: the review hash was its own tier only
because it was a second carrier the path tier couldn't see, and now that review is a path,
`seed.view` resolves it directly. The path tier reads the `{ path, search }` boot seed, not
`window.location.pathname` directly — ROUTE-05 substitutes a restored deep-link destination
there when the live path is the bare root; it is not a new precedence tier. See the section
below. A booted review path seeds `createStep`/`reviewBatchIds` the same way, off `bootPath`
rather than the live pathname, so a signed-out `/imports/<ids>/review` visit round-trips
through the signed-out deep link below for free.

Both drill-down ids — and the review batch ids — gate on the *winning* view, `bootView`,
never `seed.view` directly (`[ids-gate-on-the-winning-view]`) — only `initialView` can
outrank the path now, and a `create` boot must never inherit a review batch, an invoice id
or a job id from a URL that lost. The mount-alignment effect then serialises that same boot
state back into the URL
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
first; `parseLocation` then decodes it into the seven fields. A signed-out visit to
`/invoices/<id>` is captured, restored after sign-in, and seeds both the detail view and its
id — not just the view. Pinned by `App.signedOutDeepLink.test.tsx`'s `Workspace boot: a
restored destination carries its drill-down id too (ROUTE-02 merge)` block.

## The ROUTE-02..06 boundary

- **ROUTE-02** — **shipped.** Drill-down ids (`/invoices/:id`, `/extraction/:jobId`) and
  cold-boot seeding for both. `carryView` collapses `detail`/`extraction`
  back to `invoices` on a persona switch, id included: a remounted identity cannot resume a
  selection it may not be entitled to (`[carryview-drops-the-id]`).
- **ROUTE-03** — **shipped.** Migrated the retired hash form into the path
  (`/imports/:batchIds/review`, epic Q7): one URL scheme, not two, with no back-compat for a
  bookmarked hash. Also inherits a known defect: Back into an older review batch after
  opening a second one in the same tab relinks the wrong batch onto that history entry. An
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
- **ROUTE-06** — **shipped.** Every `Workspace` history write now stamps the company that
  minted it, and a mismatched stamp clamps the entry on `popstate` (the company-stamp
  section above) — the general fix for the same shape one level up from ROUTE-01/02's two
  special cases. ROUTE-01 closed one atom (`extractionJobId`,
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

  ROUTE-03 adds a third instance of the same shape: `[stale-review-ids-on-a-bare-create-entry]`.
  The walk: `openCreate` pushes a bare `/create` entry with `reviewBatchIds` cleared; a `nav`
  away leaves it behind; a second `openCreate` pushes another bare `/create` entry; an import
  finishes there and `applyRoute` sets `reviewBatchIds` to the new batch, which the mirror
  above writes onto that second entry; a `nav` away leaves those ids live too, because
  `navigate` is not one of `setReviewBatchIds`'s three callers (`switchClient`, `resetImport`,
  `applyRoute`); three Back presses land on the first entry, still bare `/create` in the
  browser's own history — but `reviewBatchIds` in memory still names the second batch, and
  the mirror rewrites the just-restored entry with that stale batch's path. This **predates
  this story**: the identical walk once wrote the retired hash form — `/create` with the
  batch named in the fragment, not the path — over the restored entry instead of
  `/imports/<id>/review`; same defect, new spelling, because the mirror always rebuilt its
  output from whatever `reviewBatchIds` held, hash or path. **Closed in ROUTE-06-04**: the
  `popstate` handler's `else if (at.view === 'create')` arm, next to the existing
  ids-present arm, clears `reviewBatchIds` and demotes `createStep` off `'review'`
  (functional setter, never a plain write — the handler's `[]` deps make a closure read of
  `createStep` freeze at mount) whenever the restored path is bare `/create`, so the mirror's
  next write reproduces the bare path instead of re-attaching the stale batch. Pinned by
  `App.routeReviewHash.test.tsx`'s `popstate_aRestoredBareCreateEntryDoesNotGrowAStaleBatchPath`
  and `popstate_aRestoredBareCreateEntryLeavesNoEmptyReviewScreen`.

  Two of this story's own planning premises were already shipped before it began, confirmed
  by code reading rather than fixed here: `switchClient` already cleared `extractionJobId`
  (`App.tsx:721`) and `signOut` already cleared the URL and the stored deep-link destination
  (`App.tsx:1707`, `:1712`).

## Two things that cost time here

**The review-path mirror only ever writes while `view === 'create'`.** The effect is keyed
on the same `[view, createStep, reviewBatchIds.join(',')]` triple as before and still runs on
every commit where any of the three changes — including a nav that leaves `create` — but its
first line is `if (view !== 'create') return`, so a nav to any other view hits that return
and writes nothing (pinned by `App.routeReviewHash.test.tsx`'s
`mirror_theMirrorIsInertOffTheCreateView`). `navigate` owns every view but `create`'s own
path, and the early return is what stops the two from fighting over the same URL; it is also
what lets the `reviewBatchIds` in `[stale-review-ids-on-a-bare-create-entry]` above go stale
instead of self-correcting on a plain nav away. Any test asserting on history writes *on*
`create` itself — leaving review via `restartImport`/`skipUpload`/`enterByHand`, or a Back
landing on a review path — must still account for this writer firing in the same commit as
`setView`/`setCreateStep`.

**jsdom's environment is per test file, not per test.** `window.history` survives across
`it()` blocks in one file. A test that pushes a URL leaks it into the next test's boot
seed unless `beforeEach` resets it with `window.history.replaceState(null, '', '/')`. A
static guard in `App.routeNavigate.test.tsx`
(`guard_everyAppRenderingTestFileResetsTheJsdomUrl`) enforces this across all 16 files
that render `<App />`.
