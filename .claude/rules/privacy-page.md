---
paths:
  - "frontend/landing/src/components/Privacy*"
  - "frontend/landing/src/components/DemoLeadForm*"
  - "frontend/landing/src/components/MarketingConsent*"
  - "frontend/landing/src/components/demoForm*"
  - "frontend/landing/src/analytics*"
  - "frontend/landing/src/consent*"
  - "frontend/landing/src/gaCookies*"
  - "frontend/landing/src/hubspot*"
  - "frontend/landing/index.html"
  - "packages/monitoring/src/**"
  - "internal/notifications/hubspot.go"
  - "internal/notifications/resend.go"
  - "internal/notifications/worker.go"
  - "internal/notifications/store.go"
  - "internal/platform/ai/**"
  - "internal/platform/jev/**"
  - "internal/extraction/aiimage.go"
  - "internal/extraction/aireading.go"
  - "internal/extraction/ailines.go"
  - "internal/extraction/jevcheck.go"
  - "internal/importer/suggest.go"
  - "internal/importer/jevcheck.go"
  - "internal/invoice/explain.go"
  - "internal/invoice/handlers_explain.go"
  - "frontend/app/src/components/CreateUpload.tsx"
---
# Privacy page

- Write a factual sentence only when code, an operator confirmation or a vendor statement backs it. Without that backing, the sentence does not ship.
- Change the page sentence in the same PR as the code it describes.
- Quote the consent sentences, the retention months, the hostnames and the contact address from their code constants. Never retype them.
- Never render `e.iroha@ascomply.com` on the page. It is an unmonitored demo address.
- Never claim a vendor's data region without a vendor citation. Code proves only the host that receives a record.
- Describe the cookie notice, its Accept and Reject buttons, the footer "Cookie choices" control and the `asc_consent` record. All four ship.
- Promise no preference centre and no per-category toggle. Neither exists.
- Add no "last updated" date. Nothing keeps such a date true.
- Add no claim about first-party server logs. Nothing in the repository establishes their retention.
- Add no cookie table and no per-category breakdown. The site sets one non-essential cookie family for one purpose.
- Add no step-by-step tour of the cookie notice. Name the control and say what each button does.
- Claim no compliance with a named data-protection law. The page says it is not legal advice.
- Disclose each new browser network sender on the page. List the external hosts the landing contacts.
- Treat an ungated flow as a page fact. Fonts and error reports load whatever the visitor chooses about analytics.
- Scope each analytics sentence to the landing and the Feature Library, never to the landing alone.
- Say that one analytics choice covers the landing and the Library. A choice made on either site applies to both.
- Name `asc_consent` in one paragraph only, the cookie-notice paragraph. Say there that it is our own cookie on `SHARED_COOKIE_DOMAIN`.
- List what the Library sends Google: each page viewed, Book the Demo, the tour start and Open in Platform.
- Say that Open in Platform carries the feature or group. It is a content id, never visitor input.
- Say that Reject on either site deletes the shared `_ga` cookies. Google's script can re-create them until reload.
- Name the Library's own "Cookie choices" control at the foot of its sidebar. It brings the notice back.

## AI processing of invoice documents

- Quote the OpenRouter host from `AI_PROVIDER_HOST`. Never retype it.
- Keep the `Model` prefixes of the AI clients and the vendors named on the page in step. `Privacy.claims.test.tsx` enforces it.
- Change the page section and `AI_DISCLOSURE` in the same PR as any change to the AI clients' document reading. Disclose the explain call on the page only, never in `AI_DISCLOSURE` and never beside Explain (Q28).
- Change the explanation section in the same PR as any change to when the explain call runs or what it sends.
- Make no training, retention, region or deletion claim about OpenRouter, Google or TypeSafe. Wait until one vendor statement backs every provider behind a request.
- The request sets only `provider.require_parameters`. `data_collection` and `zdr` are unset, so OpenRouter may route to any endpoint of the model.
- Keep the check date in this file only. The page carries no date.
- Leave the Jev terms to the user (`openrouter-clients.md`).

## AI provider terms, checked 2026-10-09

Each row holds the URL, one finding and the plan. The PR body holds the full vendor quotes.

| Provider | URL | Finding | Plan |
|---|---|---|---|
| OpenRouter | https://openrouter.ai/privacy | Its no-training line covers its own use. Model providers may retain and train. | No claim. |
| OpenRouter routing | https://openrouter.ai/docs/guides/routing/provider-selection | `data_collection` defaults to `allow`. Our request leaves it unset. | No claim. Only `require_parameters` is set. |
| Google, Gemini 3.5 Flash Lite | https://ai.google.dev/gemini-api/terms | AI Studio unpaid terms permit product training. Vertex terms do not. | No claim. The serving endpoint is unknown. |
| TypeSafe, Jev | https://typesafe.ai/legal/privacy-policy | The policy forbids training on Input. It does not name Jev. | No claim. The user decides. |
