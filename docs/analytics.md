# Landing analytics — GA4 (LAND-03)

**Audience:** whoever operates GA4 property `G-E409H76XYY`, and whoever edits
`frontend/landing/src/analytics.ts`. The event table below mirrors the senders in that file;
change both in the same PR. Nothing here applies to the three SPAs — GA4 ships on the **public
landing page only**.

## What ships

A GA4 `gtag.js` tag, injected at runtime by `ensureTag`, behind a gate with three parts that
must **all** hold (`shouldLoadTag`, `analytics.ts:22`):

1. the browser is on a production hostname — exact match against `PRODUCTION_HOSTNAMES`
   (`frontend/landing/src/hubspot.ts:9`), which today holds `www.ascomply.com` alone;
2. analytics consent is granted — a versioned `localStorage` record (`src/consent.ts`), whose
   default when no record is stored is **denied** (`CONSENT_DEFAULT_ANALYTICS`), so a first-time
   visitor loads no tag until they accept;
3. `VITE_GA_MEASUREMENT_ID` was baked into the build.

Any one of the three missing and `https://www.googletagmanager.com/gtag/js` is never requested.
That is what keeps every preview and PR environment dark: none of them carries the variable.

Out of scope, by decision: **Google Consent Mode v2** in any form (no `gtag('consent', …)` call
ships — Q2), the cookie notice and the privacy policy (LAND-04 / LAND-05), and all GA-console
configuration, which is why the operator checklist below exists.

## Events

| Event | Kind | Parameters | Fires when |
|---|---|---|---|
| `page_view` | GA4 automatic | *(none set by us)* | `gtag('config', id)` sends it; GA4 derives the traffic source from the referrer and `utm_*`. Never sent manually — a manual one would double-count. |
| `demo_open` | custom | `cta_location`: `nav` \| `hero` \| `audience` \| `pricing` \| `demo_cta` \| `footer` | A visitor opens the Book-a-demo modal. The value is bound per call site in `App.tsx`'s `book(source)`. |
| `generate_lead` | GA4 recommended | `form_name`: `book_a_demo` | A demo submission **reaches HubSpot and succeeds**. |
| `demo_submit_failed` | custom | `form_name`: `book_a_demo` | That same HubSpot call rejects (non-2xx, timeout, network). |
| `scroll_depth` | custom | `percent_scrolled`: `25` \| `50` \| `75` \| `100` | The visitor first crosses each milestone. Once each per page load, ascending. |

Three notes on what does **not** fire:

- A **honeypot-trapped** submission fires **neither** `generate_lead` nor `demo_submit_failed`.
  It routes through the shared stub, so a bot sees exactly what a human sees and reports nothing.
- A submission on a **closed gate** (any non-production hostname) also fires neither: no HubSpot
  call is made, so there is no outcome to report.
- Six `cta_location` values cover **ten** buttons. `Audience`'s three persona tabs all report
  `audience`, and `Pricing`'s three tiers all report `pricing`.

## Configuration

| Where | Variable | Value |
|---|---|---|
| Railway → project `ASComply` → environment `production` → service `landing` | `VITE_GA_MEASUREMENT_ID` | `G-E409H76XYY` |

Set **nowhere else**. Leaving it unset in every other environment is half of what keeps previews
dark; the hostname gate is the other half.

Vite bakes `VITE_*` at **image build time**, so changing the value needs a landing redeploy, not a
restart. `frontend/landing/Dockerfile` carries the matching `ARG VITE_GA_MEASUREMENT_ID` +
`ENV VITE_GA_MEASUREMENT_ID=$VITE_GA_MEASUREMENT_ID` pair — without **both**, Railway's injected
build arg is silently dropped and `vite build` bakes an empty string.

## Operator checklist

Six items. None of them is dischargeable by CI, and the first is load-bearing.

1. **Set `VITE_GA_MEASUREMENT_ID=G-E409H76XYY`** on the production landing service (project
   `ASComply`, environment `production` `6c864094-6a06-452f-8495-be77d8a94fe7`, service `landing`
   `21e62d5d-82c9-48d8-8f6b-b2a53104c046`), then redeploy so `vite build` bakes it.
   **Done — measured live 2026-08-16.** The variable is set, the deployed bundle carries
   `G-E409H76XYY`, `https://www.googletagmanager.com/gtag/js?id=G-E409H76XYY` returns 200, a browser
   load of `https://www.ascomply.com/` holds `_ga` cookies and hits `region1.google-analytics.com`.
   Nothing local or in CI catches a regression here — every automated check passes on a dark build
   by construction, so re-measure in a browser after any landing redeploy. **That measurement was
   taken under the old granted-by-default gate.** From LAND-05 the tag loads only for a browser that
   has accepted analytics, so re-measure with consent granted; a clean profile now correctly holds
   no `_ga`.
2. **GA4 data retention → 14 months** (Admin → Data settings → Data retention). The default is
   2 months, which makes year-on-year comparison impossible after the fact.
   **Done — operator-confirmed 2026-08-16.**
3. **Google Signals → off** (Admin → Data settings → Data collection).
   **Done — operator-confirmed 2026-08-16.**
4. **Register `cta_location` and `form_name` as GA4 custom dimensions** (Admin → Custom
   definitions → Create custom dimension, scope *Event*). Both are collected without this and
   **invisible in every standard report** — the call-site attribution this story exists to deliver
   would silently not appear.
5. **Confirm in GA4 DebugView after deploy** that a real visit to `https://www.ascomply.com`
   reports `page_view` with a traffic source. **Accept analytics on that visit first** — from
   LAND-05 the gate's consent arm is closed by default, so a clean profile that has not accepted
   reports nothing at all, and an empty DebugView then means the gate is working rather than the
   tag being broken. Also confirm that a real submission reports `generate_lead`, that
   **all six** `cta_location` values appear: `nav`, `hero`, `audience`, `pricing`, `demo_cta`,
   `footer`, and that scrolling the page to the bottom reports `scroll_depth` once each at
   `percent_scrolled` 25, 50, 75 and 100 — four events, no repeats on scrolling back up.
   Not optional polish. A mutation making `App.tsx`'s `book()` ignore its argument and hardcode one
   source **survives every test in the repo**: `analytics.test.ts` matches the six literal call
   sites against `App.tsx` as *text*, `analytics.dom.test.ts` calls `trackDemoOpen` directly rather
   than through `book`, and no CI run loads the tag, so the e2e suite sees no payload. DebugView is
   the only oracle that exists for it.
6. **Optional — re-prove the hostname gate on a live PR environment.** The e2e biconditional in
   `e2e/smoke/landing-demo.spec.ts` asserts that `gtag.js` is requested **iff** the target is
   `www.ascomply.com`; on every PR both sides are false. To watch it go red, mutate **data, not
   logic**, in one push:
   - set a throwaway `VITE_GA_MEASUREMENT_ID=G-TESTONLY0` on the **PR** environment's `landing`
     service (never the real id — the fulfilling route absorbs the request, and the real property
     is never named);
   - append that PR's own `landing-pr-<n>-<suffix>.up.railway.app` host to `PRODUCTION_HOSTNAMES`
     (`frontend/landing/src/hubspot.ts:9`).

   Both changes together open the gate on the preview host, and all seven tests in the file go red.
   Then revert both.

   Two things that look simpler and do **not** work. Weakening `isProductionHost` to
   `endsWith`/`includes` turns `hubspot.test.ts`'s impostor-hostname test red, so `await-ci`
   refuses to deploy and the e2e job never runs. Setting the variable alone proves nothing either:
   `ensureTag` returns at its `!id` check before the host gate is consulted, so with no variable
   there is no request either way. Every existing unit test survives the allowlist mutation — their
   pinned hostnames all differ from a live PR host. Budget two extra full 11-service rebuilds.

## Verifying the classifier

One test in `e2e/smoke/landing-demo.spec.ts` takes no `page` fixture, so Playwright launches no
browser and it completes in 2-4ms. That makes it the one part of this story's deployed proof
anyone can re-run locally in seconds, without a Railway environment:

```
cd e2e && LANDING_URL=https://www.ascomply.com \
  OPS_CONSOLE_URL=https://ops.example \
  APP_URL=https://app.example \
  SUPPORT_CONSOLE_URL=https://support.example \
  npx playwright test -g "the analytics-host classifier accepts GA hosts and nothing else"
```

All four variables are required, not just `LANDING_URL`: Playwright imports every `*.spec.ts` in
`testDir` before applying `-g`, and `smoke/apps.ts:41,52` resolves `OPS_CONSOLE_URL`,
`SUPPORT_CONSOLE_URL` and `APP_URL` at module scope. With only `LANDING_URL` set, the run throws
`OPS_CONSOLE_URL is not set` before collecting a single test. The three placeholder hosts are
never contacted; only `LANDING_URL` is parsed, and only for its hostname.

Verified 2026-08-16 against three mutations of `isGoogleAnalyticsHost`, restoring the source after
each:

| Mutation | Assertion that fails |
|---|---|
| `return true` unconditionally | `https://fonts.googleapis.com/css2?family=Inter should NOT be recognised` |
| `return false` unconditionally | `https://www.googletagmanager.com/gtag/js?id=G-E409H76XYY should be recognised` |
| `endsWith('.google-analytics.com')` → `endsWith('google-analytics.com')` | `https://fake-google-analytics.com/g/collect should NOT be recognised` |

Every other GA assertion in that spec file consumes this predicate. A predicate matching nothing
would leave the request sink permanently empty and every assertion permanently green while
observing nothing; one matching too much would turn them permanently red, because
`frontend/landing/index.html:12-15` requests `fonts.googleapis.com` and `fonts.gstatic.com` on
every run.

## See also

- `frontend/landing/src/analytics.ts` — the gate, the tag injection and all four senders.
- `frontend/landing/src/consent.ts` — the versioned storage contract the gate reads.
- `frontend/landing/src/hubspot.ts` — `PRODUCTION_HOSTNAMES` and `isProductionHost`, shared with
  the Book-a-demo submit gate.
- `e2e/smoke/landing-demo.spec.ts` — the deployed proof: the analytics-host classifier, the
  request sink, the fulfilling safety net and the biconditional. `openLanding()` seeds a granted
  consent record before navigating; without it the biconditional would be false on production.
- `docs/e2e-convention.md` — why that proof lives in a browser suite at all.
</content>
