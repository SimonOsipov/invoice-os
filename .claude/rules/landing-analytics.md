---
paths:
  - "frontend/landing/src/analytics*"
  - "frontend/landing/src/consent*"
  - "frontend/landing/src/gaCookies*"
  - "frontend/landing/src/hubspot*"
  - "frontend/landing/Dockerfile"
  - "e2e/smoke/landing-*.spec.ts"
---
# Landing analytics

- Ship GA4 on the public landing page only. Never add GA to the app or the consoles.
- Load the `gtag.js` tag only when `shouldLoadTag` holds: a production hostname, granted analytics consent and a baked measurement id.
- Match the hostname exactly against `PRODUCTION_HOSTNAMES` in `hubspot.ts`. The hostname gate does not depend on the measurement id.
- Keep `CONSENT_DEFAULT_ANALYTICS` false. A visitor with no stored record loads no tag.
- Set `VITE_GA_MEASUREMENT_ID` on the production landing service only. An unset variable elsewhere keeps previews dark.
- Declare `ARG` and `ENV` for every `VITE_*` variable in `frontend/landing/Dockerfile`. Railway drops a build arg that has no `ARG`.
- Redeploy landing to change a `VITE_*` value. Vite bakes it at build time.
- Never send `page_view` by hand. `gtag('config', id)` sends it, and a manual event double-counts.
- Push an `arguments` object to `dataLayer`, never an array. GA4 ignores an array.
- Send fixed literals as event parameters: `cta_location`, `form_name` and `percent_scrolled`. Never send form data.
- Send every event through `send`. It returns early when the tag is not loaded or the visitor has rejected.
- Keep the sender functions for `generate_lead` and `demo_submit_failed` module-private. Wrap only the HubSpot call in `trackedHubSpotSubmit`.
- Report no event for a honeypot or closed-gate submission. Only a HubSpot outcome reports.
- Add a new CTA source to `DEMO_CTA_SOURCES` and to its `book(source)` call site.
- Fire each scroll milestone once per page load, ascending.
- Apply a consent choice in `applyChoice` on every choice, not only when it injects the tag. A reject sets the revoked flag and clears the `_ga` cookies.
- Ship no `gtag('consent', ...)` call. Google Consent Mode is out of scope.
- Seed a granted consent record in `openLanding()` before the landing e2e navigates. A denied default makes the production-host assertion false.
