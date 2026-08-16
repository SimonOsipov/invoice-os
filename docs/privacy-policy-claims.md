# Privacy page — claim ledger (LAND-04)

**Audience:** whoever edits `frontend/landing/src/components/Privacy.tsx`, and whoever has
to defend a sentence on the privacy page to a member of the public.

Every factual claim the page makes about a visitor's data appears below with the evidence
that makes it true. **A claim with no evidence row does not ship.** If you change a
sentence on the page, change its row here in the same commit. If you change the code the
page describes, re-check the row that cites it.

The page is a plain-English description of what the code does. It is not legal advice and
it does not claim to satisfy any particular data-protection law — the page says so itself.

## Evidence classes

| Class | Means |
|---|---|
| **CODE** | A `file:line` in this repository proves it. |
| **PRODUCTION-OBSERVED** | Measured on `https://www.ascomply.com` on 2026-08-16 with a real browser, `curl` and Railway variable inspection. |
| **OPERATOR-CONFIRMED** | Only a console setting or a human commitment can make it true, and the operator has confirmed it. |
| **VENDOR-ASSERTED** | The third party's own terms or documentation say it. Not observable from here. |

---

## Table 1 — the page's factual claims (C1–C22)

| # | Claim | Class | Evidence |
|---|---|---|---|
| C1 | We use Google Analytics 4 to measure how visitors use this marketing site. | CODE + **PRODUCTION-OBSERVED** | `analytics.ts:26-58` — `tagSrc` requests `googletagmanager.com/gtag/js`, `ensureTag` injects it. **Live:** the deployed bundle `/assets/index-BygN6ifn.js` contains `G-E409H76XYY` and `googletagmanager` (1 match each); a browser load of `https://www.ascomply.com/` yields `typeof window.gtag === 'function'`, `window.dataLayer.length === 6`, and `GET https://www.googletagmanager.com/gtag/js?id=G-E409H76XYY => 200`. |
| C2 | Analytics runs on this public site only, never inside the signed-in product. | CODE | `analytics.ts` is imported by `main.tsx:12` and `App.tsx:16` alone. `grep -rl "gtag\|googletagmanager\|google-analytics\|VITE_GA_" frontend/app/src frontend/ops-console/src frontend/support-console/src` returns nothing (re-run 2026-08-16). |
| C3 | It is active on `www.ascomply.com` alone. Preview and test builds send nothing. | CODE | `hubspot.ts:9` `PRODUCTION_HOSTNAMES = ['www.ascomply.com']`; `analytics.ts:22-24` `shouldLoadTag` requires `isProductionHost`. **The hostname gate is the load-bearing one** and it is independent of the measurement id — a preview that inherits `VITE_GA_MEASUREMENT_ID` still sends nothing because its hostname is not in the allowlist. Do not re-derive this claim from "the id is unset somewhere". |
| C4 | Google receives: which pages you viewed, where you arrived from, how far down a page you scrolled, when you opened the demo form, and whether that form succeeded or failed. | CODE | `analytics.ts:79` `demo_open`, `:83` `generate_lead`, `:87` `demo_submit_failed`, `:130` `scroll_depth`; `:54` `gtag('config')` sends `page_view`. Mirrored in `docs/analytics.md:28-34`. |
| C5 | We never send Google your name, email, company, or any answer you gave the demo form. | CODE | `analytics.ts:73-88` — every `send()` parameter is a fixed literal (`cta_location`, `form_name`, `percent_scrolled`); `trackedHubSpotSubmit` (`:93-101`) passes no lead data. |
| C6 | Google sets cookies on your device, named `_ga` and `_ga_…`. **This is happening now.** | **PRODUCTION-OBSERVED** | A real browser load of `https://www.ascomply.com/` reports `document.cookie` = `_ga=GA1.1.1504972201.1786907531; _ga_E409H76XYY=GS2.1.…`. Nothing in this repo writes them — `gtag.js` does; see E3. |
| C7 | Google Analytics data is collected through Google's regional collection endpoints, and Google LLC processes and stores it, including in the United States. Using this site therefore transfers data about your visit outside Nigeria and the EEA. | VENDOR-ASSERTED + **PRODUCTION-OBSERVED** | US processing: Google's own processing terms; no code evidence exists or can. Regional collection: the third-party hosts observed on one production page load are `www.googletagmanager.com` ×1, `region1.google-analytics.com` ×3, `fonts.googleapis.com` ×2. **The copy must never say "sent directly to servers in the United States"** — the observed collection host is regional and that sentence would be contradicted by anyone with a network tab open. Collection endpoint and processing location are different facts; the copy states both, in that order. |
| C8 | Google keeps it for **14 months**, then deletes it. | **OPERATOR-CONFIRMED** | User confirmed the GA4 property is set to 14-month retention (2026-08-16). One constant, `GA_RETENTION_MONTHS`, interpolated once. `docs/analytics.md` operator checklist item 2. Precision on what is deleted: see E4. **Standing exposure:** no code and no CI job can read the GA console. If the property is ever changed, this sentence becomes false with nothing to catch it. |
| C9 | We have Google Signals off, so this is not used for cross-device advertising. | **OPERATOR-CONFIRMED** | User confirmed Google Signals is off (2026-08-16). `docs/analytics.md` operator checklist item 3. Same standing exposure as C8. |
| C10 | Google also serves the fonts this site is typeset in. Loading a font tells Google your IP address and which site asked for it. This happens on every page, whatever you choose about analytics. | CODE + **PRODUCTION-OBSERVED** | `frontend/landing/index.html:13-16` (two `preconnect` + two `stylesheet` tags), duplicated by `@import` at `packages/design-tokens/tokens/typography.css:6` and `packages/design-tokens/app-layer.css:16`. Unconditional — no gate, no consent check, and it fires before any React runs. Confirmed live: `fonts.googleapis.com` appears twice in the production resource timing. **"Which site", not "which page":** `index.html` sets no `<meta name="referrer">`, so the browser default `strict-origin-when-cross-origin` policy governs the cross-origin font request — it sends only the origin (`https://www.ascomply.com/`) as `Referer`, never the path. |
| C11 | If you book a demo, your answers go to HubSpot, our CRM. | CODE + **PRODUCTION-OBSERVED** | `hubspot.ts:110-124` `submitDemoLead`; `api-eu1` is present once in the deployed bundle, and `VITE_HUBSPOT_PORTAL_ID` / `VITE_HUBSPOT_FORM_GUID` are set on the production landing service. |
| C12 | Exactly what HubSpot receives: your first and last name, work email, company, and — unless you cleared them — role, taxpayer size and monthly invoice volume. | CODE | `hubspot.ts:80-88`, a fixed literal of seven property names (`firstname`, `lastname`, `email`, `company`, `jobtitle`, `company_size`, `monthly_invoice_volume`) built from six answers; `firstname`/`lastname` are split from the single "Full name" answer by `splitLeadName` (`hubspot.ts:69-72`). The payload is built by walking that list, so an eighth key is structurally impossible. |
| C13 | Optional answers you left blank are not sent at all. | CODE | `hubspot.ts:92-96` — empty values are dropped, never sent blank. See E1 for what "blank" means in practice. |
| C14 | The exact sentence you ticked is stored alongside your details. | CODE | `demoForm.ts:39-40` `CONSENT_TEXT`, one constant read by both the checkbox label (`DemoModal.tsx:431`) and the payload (`hubspot.ts:104`). The privacy page quotes the same constant rather than retyping it, so all three can never disagree. |
| C15 | HubSpot holds it on their EU servers. | CODE + **VENDOR-ASSERTED** | CODE proves only the submission host: `hubspot.ts:44-46` posts to `https://api-eu1.hsforms.com/...`, the EU-region Forms endpoint — that proves where the record is SENT, not where it is STORED afterward. VENDOR-ASSERTED covers storage: HubSpot's own data-residency documentation states EU-portal data is hosted on EU infrastructure; not observable from this repo. |
| C16 | We do not send HubSpot your browsing history, the pages you visited, or their tracking cookie. | CODE | `hubspot.ts:99-106` — no `context` key, so no `hutk`, no `pageUri`, no `pageName`. Asserted by `hubspot.test.ts:193` (`expect(result).not.toHaveProperty('context')`). |
| C17 | We run no advertising network and set no advertising cookies. | CODE + **PRODUCTION-OBSERVED** | Source: the landing references exactly four external hosts — `fonts.googleapis.com`, `fonts.gstatic.com`, `www.googletagmanager.com`, `api-eu1.hsforms.com` — none an ad network. No `XMLHttpRequest` and no `sendBeacon` anywhere in `frontend/landing/src`. Live: the third-party hosts actually contacted on one production page load are `googletagmanager`, `region1.google-analytics.com` and `fonts.googleapis.com`. A source grep and a network trace agree. |
| C18 | **Withdrawal** — what a visitor can actually do today. | CODE + honest gap | `consent.ts:8` `CONSENT_DEFAULT_ANALYTICS = true`; `consent.ts:66` `writeConsent` has **zero production call sites** (only `consent.ts` and `consent.test.ts` mention it). Expanded mechanism by mechanism in Table 2. |
| C19 | Contact address for a data request: `sam@ascomply.com`. | **OPERATOR-CONFIRMED** | User supplied the address on 2026-08-16 at the planning fork gate. No contact address for a data request appeared anywhere in this project's copy before this page was written. **The rendered page must never contain `e.iroha@ascomply.com`** — that is a mock sign-in persona (`frontend/landing/src/auth.ts:53`) whose inbox nobody reads, and it is one keystroke away from a real address. A test pins its absence permanently. |
| C20 | Google also receives your device, browser and operating system, your screen size and your language. | **PRODUCTION-OBSERVED** | A real GA4 `page_view` collect hit from `https://www.ascomply.com/`, observed 2026-08-16, carries `sr=1800x1169` (screen resolution), `uap=macOS` / `uapv=26.2.0` (OS + version), `uafvl=Google Chrome;151.0.7922.138` (browser + version), `uaa=arm` / `uab=64` (platform architecture/bitness) and `ul=en-us` (language). None of these parameters are set by this codebase (see C22's evidence) — `gtag.js` attaches them itself. |
| C21 | Google attaches a randomly generated identifier that lets it recognise the same browser on a later visit. | **PRODUCTION-OBSERVED** | The same collect hit carries `cid`, a client identifier `gtag.js` persists client-side (backed by the `_ga` cookie value, see C6/E3) — observed 2026-08-16. Not set, read, or referenced by any code in this repo. |
| C22 | The only details we attach ourselves are which button was used, which form it was, and how far down the page was scrolled — the rest is collected by Google's own code. | CODE | `analytics.ts:79` `{ cta_location: source }` (demo_open), `:83` `{ form_name: FORM_NAME }` (generate_lead), `:87` `{ form_name: FORM_NAME }` (demo_submit_failed), `:130` `{ percent_scrolled: m }` (scroll_depth) — every custom-event parameter this codebase sends is one of these four fixed literals. The rest of the "What Google receives" list (page URL, referrer, device/browser/OS, `cid`) is attached by `gtag.js` itself, never by application code. |

---

## Table 2 — withdrawal, mechanism by mechanism (expands C18)

There is no consent control on this site. `CONSENT_DEFAULT_ANALYTICS` is `true`, so a
visitor with no stored record is treated as consenting, and `writeConsent` is never
called by anything that ships. The tag is live, so that default is not a dormant setting:
it is what is currently granting consent on behalf of every visitor, and the `_ga` cookie
in the production measurement is its output.

The page therefore may not mention a notice, a banner, a Reject button or a preference
centre in the present tense. Every mechanism below is one a visitor can use today, and
each row records what it does **and what it does not** stop.

| # | Mechanism | What it stops | What it does **not** stop | Class | Evidence |
|---|---|---|---|---|---|
| W1 | *(disclosure, not a mechanism)* Analytics is on unless the visitor turns it off in their browser. | — | — | CODE | `consent.ts:8` `CONSENT_DEFAULT_ANALYTICS = true`; `consent.ts:81` `analyticsAllowed` returns that default when no record is stored. Shipped as the exported constant `ANALYTICS_DEFAULT_SENTENCE`, pinned by a unit assertion so softening it requires editing a test. |
| W2 | *(disclosure)* The site has no privacy control of its own yet; one is being built. | — | — | CODE | `writeConsent` (`consent.ts:66`) has zero production call sites. LAND-05 owns the notice; this story does not build it and the copy does not describe it in the present tense. |
| W3 | Block or clear cookies for this site in the browser's own settings. | Deletes the `_ga` / `_ga_…` identifiers and stops new ones being issued, so Google can no longer link this visit to earlier ones. | The measurement itself. `gtag.js` still loads and still sends the hit, so Google still receives the page URL and the IP address the request comes from. | CODE + inherent | `analytics.ts:46-54` injects the script unconditionally once the gate opens; the cookie is written by `gtag.js`, not by us (E3). The IP is inherent to any HTTP request the browser makes. |
| W4 | Install Google's own opt-out browser add-on, `https://tools.google.com/dlpage/gaoptout`. | Google Analytics from sending anything, on this site and every other site that uses it. | The fonts flow (C10) and the demo form (C11). | VENDOR-ASSERTED | Google's own download page, fetched and read 2026-08-16: the add-on gives visitors "the ability to prevent their data from being used by Google Analytics" and is listed for **Chrome, Firefox, Safari and Microsoft Edge**. The page lists no mobile browser, which is why the copy says there is no version for a phone. |
| W5 | Block `googletagmanager.com` with a content blocker. | Everything: the analytics code never arrives, so nothing is sent. The most complete of the three browser controls. | The fonts flow — a different Google host (C10) — and the demo form (C11). | CODE | `analytics.ts:27` `tagSrc` is the only analytics network call the site makes; nothing is sent before it loads (`analytics.ts:74` `if (!loaded) return`). |
| W6 | Write to `sam@ascomply.com`. | — (a request to us, not a browser control) | — | **OPERATOR-CONFIRMED** | The address is the one the user supplied (C19). The commitment to act on a request, to delete a HubSpot record on request, and that a person reads the address, are the operator's — see E5. |
| W7 | *(closing note)* None of W3–W6 stops Google Fonts; only a content blocker aimed at `fonts.googleapis.com` does. | — | — | CODE | C10's evidence. The fonts request is issued from `index.html` before any script runs, so no in-page control can reach it. |

---

## Table 3 — elaborations the copy adds inside an existing claim

Sentences the drafted copy carries that sharpen a C-row rather than assert something new.
Each traces to its parent claim. Listed separately so a reviewer can see exactly what was
added beyond the story's ledger.

| # | Sentence | Parent | Class | Evidence |
|---|---|---|---|---|
| E1 | The three optional questions come with an answer already selected, so unless you change them the pre-selected answer is what gets sent. | C13 | CODE | `DemoModal.tsx:46-54` `DEFAULT_FORM` ships `role: 'Finance or Accounting lead'`, `size: DEFAULT_TAXPAYER_SIZE`, `volume: '1k–10k'`. Without this sentence, C13 ("blank answers are not sent") reads as though the selects start empty. They do not. |
| E2 | Ticking the box does not add you to a marketing list. | C14 | CODE | `hubspot.ts:104` — `communications: []`, always empty. Process consent only, no subscription. |
| E3 | Our own code sets no cookies; the analytics cookies come from Google's script. | C6 | CODE | Zero `document.cookie`, `sessionStorage` and `indexedDB` occurrences in `frontend/landing/src` (grep, 2026-08-16). The one client-storage key the codebase defines is `asc_consent` in `localStorage` (`consent.ts:4`) and nothing writes it. Attribution matters: "we set no cookies" alone would be true of our code and false of what the visitor's browser ends up holding. |
| E4 | What Google deletes after 14 months is the underlying record of the visit. | C8 | VENDOR-ASSERTED | GA4's data-retention setting governs user-level and event-level data; aggregated standard reports are not covered by it. Saying "Google deletes it" unqualified would overstate what the setting does. |
| E5 | A person at ASComply Africa reads `sam@ascomply.com`; a demo record held in HubSpot can be deleted on request; analytics data sits with Google under an identifier not tied to your name. | C19 / W6 | **OPERATOR-CONFIRMED** | The first two are the operator's commitment, made when the address was supplied (2026-08-16). The third is a restatement of C5 (no identifying detail is ever sent to Google). |

---

## What the page deliberately does not say

Recorded so the next reader knows these are decisions, not oversights.

- **No "last updated" date.** Nothing in the repo or in CI keeps such a date true, and a
  stale date on a privacy page is itself a misleading statement. If the user wants one, it
  needs an owner and a rule for updating it.
- **Nothing about our own server logs.** No claim in this ledger covers what the hosting
  layer records. The page describes the two third parties that receive visitor data; it
  does not describe first-party log retention, because nothing measured here establishes
  what that is.
- **No cookie table and no per-category breakdown.** There is one non-essential cookie
  family (`_ga` / `_ga_…`) and one purpose. A category matrix would invent structure the
  site does not have. (Story `## Out of Scope`.)
- **No description of the consent notice.** It does not exist yet (LAND-05). Describing it
  in the present tense would be a false statement to the public.

## See also

- `frontend/landing/src/components/Privacy.tsx` — the page, and the constants the tests pin.
- `docs/analytics.md` — the GA4 gate, the event table and the operator checklist.
- `frontend/landing/src/analytics.ts`, `consent.ts`, `hubspot.ts` — the three modules this
  ledger cites by line.
