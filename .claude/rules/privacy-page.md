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

## AI processing of invoice documents

- Quote the OpenRouter host from `AI_PROVIDER_HOST`. Never retype it.
- Keep the `Model` prefixes of the AI clients and the vendors named on the page in step. `Privacy.claims.test.tsx` enforces it.
- Change the page section and `AI_DISCLOSURE` in the same PR as any change to the AI clients.
- Make no training, retention, region or deletion claim about OpenRouter, Google or TypeSafe. Wait until one vendor statement backs every provider behind a request.
- The request pins no routing. `provider.data_collection` and `zdr` are unset, so OpenRouter may route to any endpoint of the model.
- Keep the check date in this file only. The page carries no date.
- Leave the Jev terms to the user (`openrouter-clients.md`).

## AI provider terms, checked 2026-10-09

| Provider | URL | What it says | Plan |
|---|---|---|---|
| OpenRouter | https://openrouter.ai/privacy (last updated Aug 31, 2026) | "OpenRouter does not use your Inputs or Outputs for model training" for Enterprise and API users. "we transmit your Inputs to the Model Provider(s) you select". Providers "may, depending on their own terms and data practices, retain and use your Inputs and Outputs for their own purposes". No retention period for prompts. "Some Model Providers may use your Inputs and Outputs for model training or improvement." | No claim. Its own no-training line does not cover the providers behind it. |
| OpenRouter routing | https://openrouter.ai/docs/guides/routing/provider-selection ; https://openrouter.ai/docs/features/zdr | `data_collection` defaults to `allow` ("providers which store user data non-transiently and may train on it"). `zdr` has no effect unless `true`. Unclear policy: OpenRouter "assume[s] that the endpoint both retains and trains on data". Our request sets neither. | No claim. Routing is not pinned. |
| Google, Gemini 3.5 Flash Lite | https://openrouter.ai/api/v1/models/google/gemini-3.5-flash-lite/endpoints ; https://ai.google.dev/gemini-api/terms (modified 2026-04-28) ; https://docs.cloud.google.com/vertex-ai/generative-ai/docs/data-governance (updated 2026-10-07) | The model has Google (Vertex global/eu/us) and Google AI Studio endpoints. AI Studio unpaid terms: Google "uses the content you submit ... to provide, improve, and develop"; paid terms: no product training, prompts logged "for a limited period of time". Vertex: "won't use your data to train or fine-tune any AI/ML models without your prior permission". The AI Studio endpoints are absent from OpenRouter's ZDR list (`/api/v1/endpoints/zdr`); Vertex ones are present. | No claim. We do not know which endpoint serves a call or OpenRouter's tier with Google. |
| TypeSafe, Jev | https://typesafe.ai/legal/privacy-policy (last updated Nov 19, 2025) ; https://docs.typesafe.ai (no data-handling page) | Policy: "will not train or fine tune any artificial intelligence or machine learning models on Input"; no Input retention period; "hosted in the United States". It does not name Jev. OpenRouter lists one TypeSafe endpoint, and it is on the ZDR list. | No claim. The user decides whether to add a TypeSafe sentence. |
