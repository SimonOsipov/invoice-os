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
  - "internal/notifications/newsletter*.go"
  - "internal/platform/sentryfilter.go"
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
  - "internal/tenancy/account_deletion.go"
  - "internal/tenancy/workspace_deletion.go"
  - "internal/workspacepurge/**"
  - "internal/notifications/erase.go"
  - "internal/notifications/vendor_erase.go"
  - "internal/platform/db/tenant.go"
  - "internal/platform/auth/gotrueadmin/**"
  - "internal/actor/**"
  - "frontend/app/src/lib/accountDeletion.ts"
  - "frontend/app/src/lib/workspaceDeletion.ts"
  - "frontend/app/src/lib/workspaceExport.ts"
  - "internal/archive/workspace.go"
  - "sidecar/auth/Dockerfile"
  - "internal/tenancy/store.go"
  - "internal/tenancy/tenancy.go"
  - "migrations/20260709161252_invitations.sql"
  - "migrations/20261010080000_staff_audit_log*.sql"
  - "migrations/20261026004500_workspace_deletion.sql"
  - "migrations/20261026045351_deletion_purge_started.sql"
  - "frontend/app/src/components/Sidebar.tsx"
  - "frontend/app/src/components/AccountView.tsx"
  - "frontend/app/src/components/WorkspaceSettings.tsx"
  - "frontend/app/src/components/DeleteWorkspaceDialog.tsx"
  - "frontend/app/src/components/WorkspaceDeletionNotice.tsx"
  - "frontend/app/src/data.tsx"
  - "frontend/app/src/glyphs.tsx"
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
- Add no cookie table and no per-category breakdown. One Accept covers the two non-essential cookie families, Google's and HubSpot's.
- Add no step-by-step tour of the cookie notice. Name the control and say what each button does.
- Claim no compliance with a named data-protection law. The page says it is not legal advice.
- Disclose each new browser network sender on the page. List the external hosts the landing contacts.
- Treat an ungated flow as a page fact. Fonts and error reports load whatever the visitor chooses about analytics.
- Scope each analytics sentence outside the Library section to the landing, the Feature Library, the Blog and the signed-in product. Never scope it to the landing alone. Keep the Blog section as pinned (`BLOG_SECTION`).
- Back the sentence that the signed-in product runs Google Analytics with the operator's confirmation of the copy. The user gave it on 2026-10-23 (CHAT-04 Answers Q1). No app code backs it: the C2 code scan in `Privacy.render.test.tsx` finds none.
- Keep the Library section as pinned (`LIBRARY_SECTION`). It names the landing and the Library only.
- Say in the Blog section that one analytics choice covers the landing, the Library and the Blog. A choice made on any of the three applies to all.
- Name `asc_consent` in one paragraph only, the cookie-notice paragraph. Say there that it is our own cookie on `SHARED_COOKIE_DOMAIN`.
- List what the Library sends Google: each page viewed, Book the Demo, the tour start and Open in Platform.
- List what the Blog sends Google: each page viewed and Book the Demo.
- Say that Open in Platform carries the feature or group. It is a content id, never visitor input.
- Say in the Blog section that Reject on any of the three sites deletes the shared `_ga` cookies. Google's script can re-create them until reload.
- Name the "Cookie choices" control in the Blog section. The Blog's control sits at the foot of its sidebar, beside the Library's. It brings the notice back.
- Describe the chat in the "Live chat" section: what HubSpot receives, that it loads only after Accept, and its domains.
- Say in the Live chat section that the app tells HubSpot the account email and that a sign-out deletes HubSpot's cookies. Quote the app host from `APP_HOSTNAMES`.
- Quote the chat domains from `HUBSPOT_CHAT_DOMAINS` and the script host from `CHAT_SCRIPT_HOST`. Never retype them.
- Claim no region for chat data. HubSpot's data-hosting FAQ does not name chat data (checked 2026-10-21).
- Name HubSpot's seven cookies and `__hs_do_not_track` in the cookie section, by purpose.
- Say in the Library section and in the Blog section that each shows the chat after Accept, and point to Live chat for what HubSpot receives. `LIBRARY_SECTION` and `BLOG_SECTION` pin the words.
- Put `data-hs-do-not-collect="true"` on any `<form>` in the library or the blog, or add none. `frontend/blog/src/package.test.ts` and `frontend/library/src/package.test.ts` enforce it.

## Blog newsletter

- Write a newsletter sentence only when `privacyCodeFacts.test.util.ts` reads its fact from `internal/notifications/newsletter*.go`. A sentence without a reader does not ship.
- Say that every newsletter email carries a one-click unsubscribe only while `contacts.md` puts `{{{RESEND_UNSUBSCRIBE_URL}}}` in every blog broadcast and `Confirm.tsx` says it.
- Say that staff follow the chosen frequency when they send by hand. The source is BLOG-00 Decision Log Q7. No test can read it.
- Name HubSpot in the Blog section for the chat and to say the newsletter sends it nothing. No `newsletter*.go` file names HubSpot.
- Quote the stop route from `PRIVACY_CONTACT`. The 48-hour expiry follows the interval in `newsletter.go`.
- Resend vendor documents were checked 2026-10-18. The privacy policy names no storage region. The audience-hygiene page has no data-residency statement.

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
- Quote the hosting region from `HOSTING_REGION`. `productionRegion` reads the same value from `deploy-prod.yml`, and C37 fails when they differ.
- Say that the sign-in service's error reports may include the account email while `scrubAuthEvent` keeps it. `TestAuthEvent_CarriesTheAccountEmail` pins the code side.

## Deletion and retention

Each line names a sentence of `DELETION_SENTENCES` and its backing. Check date: 2026-10-26.

- `W2`, `A1`: the sidebar name card calls `ctx.nav('account')`. `appLabels()` reads the labels.
- `A2`: `DeleteMyAccount` ends the membership and calls `notifications.EnqueueErasure` in one transaction.
- `A3`: `EraseWorker` runs the steps sweep, account, hubspot and resend. Say "asks" for a vendor, never that the vendor holds no copy.
- `A3` HubSpot: its `gdpr-delete` page reads "Permanently delete a contact and all associated content to follow GDPR" (checked 2026-10-26).
- `A4`: `actor.Resolve` picks `actor.RemovedName` for a removed membership. The audit reader and the invoice history call it.
- `A5`: `WorkspaceSettings.tsx`, `DeleteWorkspaceDialog.tsx` and `SETTINGS_TABS`.
- `A6`, `R2`: the `tenants_deletion_guard` trigger sets the 30 days. `ErrWorkspaceLocked`, `CancelWorkspaceDeletion` and `workspacepurge.Purger` do the rest.
- `A6` lock clause: `DeleteMyAccount` runs in a read-write request transaction, which a locked workspace refuses with `ErrWorkspaceLocked`. `deleteAccountRefusedWhileLocked()` pins it.
- Read the last definition of a trigger function across all migrations. A later `CREATE OR REPLACE` wins.
- `A7`: `decideAccountDeletion` and `eraseMembers`.
- `R3`: Cloud SQL `prod` has `retainedBackups` 7 and `transactionLogRetentionDays` 7. Source: infra `terraform/sql.tf`. Read with `gcloud sql instances describe prod --project ascomply-prod --format='yaml(settings.backupConfiguration,region,state)'`.
- `R4`: bucket `ascomply-prod-documents` has `soft_delete_policy.retentionDurationSeconds` 604800. Source: infra `terraform/storage.tf`. Read with `gcloud storage buckets describe gs://ascomply-prod-documents --project ascomply-prod --format='yaml(name,location,soft_delete_policy)'`.
- `R5`: GoTrue v2.197.0 `adminUserDelete` writes `user_deleted` with `user_email` to `auth.audit_log_entries` before it destroys the user. `gotrueVersion()` pins the version. `nothingPrunesSigninAuditLog()` pins that no file in this repo names the table.
- `R7`: `workspacepurge.purgeRows` writes the staff row. `staffLogEntryFacts()` and `staffLogIsAppendOnly()` pin the payload keys and the append-only trigger.
- Change `BACKUP_DAYS`, `FILE_RECOVERY_DAYS` and `R3`..`R4` together with the infra.
- Change `R2` and `A6` in the PR that adds a staff path to start or purge a deletion (DEL-09).
- Change the retention paragraphs when the sign-in service version changes. Re-read `adminUserDelete` first.
