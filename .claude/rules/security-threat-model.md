---
paths:
  - "internal/platform/db/**"
  - "internal/platform/auth/**"
  - "internal/platform/peer.go"
  - "internal/gateway/**"
  - "internal/tenancy/**"
  - "internal/validation/s2s.go"
  - "migrations/**"
  - "db/bootstrap.sql"
  - "cmd/*/main.go"
  - "sidecar/**"
---
# Security threat model

The security reviewer reads this file before each review. Treat a break of a guarantee below as a finding.

## Severity

- Rate a cross-tenant read or write HIGH.
- Rate a forged identity, a forged staff flag or an unauthenticated call to a gated route HIGH.
- Do not report an accepted ceiling (last section).

## Tenancy

- Row-level security isolates tenants. Policies key on `app.current_tenant`; entity policies also read `app.entity_scope`.
- `WithinRequestTenantTx` and `WithinRequestTenantTxOpts` in `internal/platform/db/tenant.go` set `app.current_tenant` in the transaction, in an earlier statement than the RLS-scoped membership SELECT.
- Every database role is NOSUPERUSER and NOBYPASSRLS (`db/bootstrap.sql`). Its guard fails the bootstrap otherwise.
- `restore_auditor` (`db/restore-auditor.sql`) is the one BYPASSRLS role. It is NOLOGIN, reads only the restore-check tables, and only `postgres` may SET ROLE to it.
- Reach tenant data only through the seam in `.claude/rules/tenant-seam.md`. `scPoolAllowlist`, `scCoreAllowlist` and `scRouteVerdicts` in `internal/platform/db/seam_coverage_test.go` list every exception.
- Give a tenant table ENABLE and FORCE row-level security. FORCE binds the table owner too.
- A table with ENABLE and no FORCE is a finding, except `house_company`. It is a global singleton, not a tenant table: only `auth_hook_reader` reads it.
- Review each new or changed `SECURITY DEFINER` function. It must check the tenant against `app.current_tenant`, or match one exact key such as a token hash or a confirmed email.

## Identity

- The IAP verifier (`internal/platform/auth/iap.go`) checks the assertion signature, issuer and `IAP_AUDIENCES`. The gateway and the worker each verify the assertion (double verification); the worker mints the recovery token only for its own verified result.
- `Verifier.Verify` in `internal/platform/auth/verify.go` checks the JWT signature against the issuer's JWKS, then the issuer and claims in `validate`.
- `injectIdentity` in `internal/gateway/gateway.go` deletes every client `X-User-*` header, sets identity from the verified token only, and sets `X-User-Address` from `clientIP`.
- Every gateway call to GoTrue but the session check and the fleet probe sets `X-User-Address` from `clientIP`. `GOTRUE_RATE_LIMIT_HEADER` names it (`TestDockerfileRateLimitHeader_NamesTheHeaderTheGatewaySends`).
- A service trusts identity headers only behind `App.RequireGateway` in `internal/platform/peer.go`. It refuses a request without the gateway token.
- `auth.RequireStaff` admits only a caller whose verified identity has `Staff`.
- The mock issuer routes (`/auth/login`, `/auth/mock/*`, `/.well-known/jwks.json`) exist only in a `-tags mockissuer` build. `MockIssuerEnabled` also needs `GATEWAY_MOCK_ISSUER=true` and an environment other than `production`. `cmd/gateway/build.tags` is empty on `main`.
- On a preview and the nightly, the mock POST routes and a no-`Origin` OPTIONS to `/auth/login` need `X-Mock-Issuer-Token`. `/.well-known/jwks.json` stays open.

## Network

- Caddy on each console host proxies one path, the IAP session request, to the gateway.
- Only the gateway and the static sites have a public domain. Every other service is on the private network only.
- `auth` admits the gateway and the worker.
- `probedServices` in `cmd/gateway/main.go` (docling, auth, worker) get no public proxy route. `TestGatewayHandlersPublishNoProxyRouteForAProbedService` asserts this.
- The docling sidecar has no authentication. It relies on the private network. A public route to it is HIGH.
- Every production backend has a Cloud Armor policy: `api` on the gateway, `sites` on the seven static sites. Both enforce `scannerdetection-v422-stable` and `protocolattack-v422-stable` at sensitivity 1 (rules `scanner-detection` 900 and `protocol-attack` 910, answer 403), before any rate limit. Two rules are opted out of protocol-attack: `owasp-crs-v042200-id921120-protocolattack` and `owasp-crs-v042200-id921150-protocolattack`, because readiness-check uploads matched them. The rule sets read at most the first 64 kB of a body. No SQL-injection or XSS rule set and no adaptive protection is on. A rule change took effect between 46 s and 193 s after the apply.
- Cloud Armor on the production load balancer throttles register, invitation register, resend, reset, invitation resend, `/contacts/demo-request`, `/blog/*` and the two `/readiness-check/*` routes per client IP. The retired `/auth/verify` path (now 405), `/auth/reset-password` and `/auth/invitation/password` share one budget of 30 an hour per IP (rule `link-redeem`, priority 1080). `POST /auth/sign-in`, `/auth/refresh` and `/auth/exchange` each have their own budget per IP per 10 minutes: 30, 120 and 60 (rules `sign-in`, `refresh`, `exchange`, priorities 1090-1110). An IP over a budget gets 429 on that rule's routes for the rest of the interval. Every rate-limit rule is `rate_based_ban` with `ban_duration_sec` 600, and a ban keeps the 429 for 10 minutes after. The nine hourly and blog rules (priorities 1000-1080) ban only above twice the budget in one interval (`ban_threshold`), and below it they throttle. Sign-in, refresh and exchange ban at the first excess. The CORS rules are in `terraform/lb.tf`. They add `Access-Control-Allow-Origin` only for an exact origin in `local.cors_allowed_origins`, so only those origins can read a 429. The link-redeem routes get no such header, so no cross-origin page can read their 429. On refresh and exchange, which send credentials, even those origins cannot, because the load balancer adds no `Access-Control-Allow-Credentials`. GoTrue also limits `/token` per client address, one budget for sign-in and refresh. Its rules are in the private repo invoice-os-infra, `terraform/armor.tf`.
- `ops` and `sup` are guarded by Identity-Aware Proxy with the Google-managed OAuth client, configured in invoice-os-infra `lb.tf` (the backends' `iap` block); prodcheck's `console hosts refuse a read without Google sign-in` step reports whether it is on. IAP admits only principals granted `roles/iap.httpsResourceAccessor`: named `ascomply.com` Google accounts, `deploy-prod` (runner checks) and `ctl-console` (agents' `ctl`, minted through the user's gcloud account). The gateway's staff check still guards every console API call.
- The gateway also counts `/auth/reset-password`, `/auth/invitation/password` and every confirm-link address lookup per client IP as `link-redeem-ip`. A found lookup is refunded. No Armor rule covers `GET /auth/verify`, so this count is the only limit on a lookup (`TestTwoInstances_TheLinkRedeemLimitCountsAcrossInstances`, `TestVerifyPage_AMissedLookupSpendsTheLinkRedeemCount`).
- On a hosted gateway, every reset request that passes the per-address count spends an instance-wide reset share (3 an hour, 20 per UTC day) and a per-client-IP bound inside it (`reset-mail-ip-hour` 1 an hour, `reset-mail-ip-day` 3 per 24 h, IPv6 per /64). Neither reads whether the address has an account.
- ERP grant secrets: in production only `run-worker` holds `erpGrantSecretVersions` (delete, add-version, access, disable on `erp-grant-*`; no create); portfolio holds none. The preview runtimes hold `erpGrantSecrets`, which adds create and is limited to the slot's own prefix. The production create binding comes with the first real-connector story.
- On Cloud Run every service but the gateway and the static sites is ingress-internal and admits only a caller with a Google ID token. `platform.IDTokenTransport` adds it in `X-Serverless-Authorization`; `TraceTransport` includes it.

## Postures

- `platform.Posture` reads `DEPLOY_ENV`: empty is local, `pr-<N>` is preview, the exact name `nightly` is nightly, any other name is hosted.
- Only a preview logs the gateway's per-address limits without refusing. A preview captures account mail.
- Only a preview, the nightly and a local run with `ERP_FAKE=true` or `ERP_SANDBOX=true` register the fake ERP connector. Only the nightly and a local `ERP_SANDBOX=true` run also register `quickbooks`; a hosted boot refuses both flags.

## Untrusted entry points

- `POST /v1/documents` (`extraction.UploadHandler`, submission): uploaded file bytes.
- docling `POST /v1/read` (`sidecar/docling/app.py`): document bytes forwarded by submission.
- `/v1/imports*` (`importer` handlers, invoice): CSV, spreadsheet and document input.
- `POST /auth/export-download` (gateway): a form body with an export code. It runs without the verifier until the code is redeemed.
- `POST /auth/iap-session` (gateway): the IAP assertion header only. It hands back a console code, never a token (`TestIAPSession_StaffGetsACodeAndNoToken`). Worker `/internal/console-sessions/recovery-token` sits behind `RequireGateway` and the ID token, takes no email from the body, and answers a hash only for a verified, confirmed, unbanned account (`TestRecoveryToken_NoAccountUnconfirmedOrBannedIs403`).
- Public gateway routes in `cmd/gateway/main.go` outside `/api/`: `/auth/*`, `/contacts/demo-request`, `/blog/*`, `/emails/*`, `/readiness-check/*`, `/unsubscribe`. They run without the verifier. `/healthz/fleet` runs without the verifier and needs `X-Gateway-Token`. `/health` and `/healthz` answer status only without `X-Gateway-Token`.
- Register, resend, reset, invitation register, invitation resend, `/contacts/demo-request`, the two `/blog/subscriptions` POSTs, `/auth/sign-in`, `/auth/iap-session`, `/auth/exchange` and the invitation preview `/auth/invitation` (gateway) answer 415 after the method check and before any other check when the `Content-Type` is not `application/json`, so a foreign page cannot send them without a preflight (`TestPublicJSONRoutes_RefuseANonJSONBodyBeforeAnyDownstreamCall`).
- `POST /readiness-check/run` and `/mapping` (gateway): a visitor's multipart file, address and mapping. The gateway keeps the file in memory (`MultipartReader` only, 15 MiB, 4 uploads per instance) and streams it to the worker. It counts 10 runs per client IP in 24 h (IPv6 per /64), holds an upload slot at most 90 s and logs no address, IP or file name.
- `GET` and `POST /readiness-check/confirm` (gateway) and `POST /internal/public-checks/confirm` (worker): a 256-bit confirm token. The GET changes nothing; the POST consumes the link once within 24 hours and sends the held results. Accepted ceiling: no Armor rule covers the route, since a random token cannot be enumerated.
- The held results (`readiness_check_requests`): invoice amounts and buyer details of an unconfirmed address, kept at most 25 hours. The file and the client IP are never stored. `invoice_gateway` has no grant on the table.
- `GET /erp/callback` and `GET /erp/fake/consent` (gateway, only when the ERP mode is not off, the second only when the mode runs the fake: fake or sandbox) and `POST /internal/erp/callback` (worker): the vendor's redirect carries a 256-bit single-use state, a code of at most 2048 bytes, an error and a `realmId` of at most 64 bytes. The gateway checks the shape, forwards the four fields (state, code, error, `realm_id`) with the gateway token and an ID token, and renders a fixed page that echoes nothing. The worker route sits behind `RequireGateway`, takes at most 4 KiB and answers only an outcome. No log line carries the state, code, realm or a token. Accepted ceiling: no Armor rule covers the callback, since a random state cannot be enumerated.
- `POST /erp/webhooks/<system>` (gateway, one literal route per registered system, today only `fake` when the ERP mode is fake or sandbox) and `POST /internal/erp/webhooks/{system}` (worker): a vendor's change notification. The gateway reads at most 64 KiB of body and forwards at most 16 KiB of JSON-encoded headers, drops cookies and address headers, makes no database call, and forwards the headers and body with the gateway token and an ID token. The worker verifies the signature with the connector before any database read, answers 401 on failure, and enqueues only `erp.pull` for the active connections `public.erp_webhook_targets` returns. No log line carries the body, a header, the key or an address. Accepted ceiling: no Armor rule or throttle covers the route; production mounts none until a real connector ships.
- `POST /v1/validate/batch` and `POST /v1/custom-rules/check` (validation): open past `RequireGateway`, guarded by `S2SMiddleware` with the shared `S2S_TOKEN`. They carry no identity and read no tenant. A custom rule's CEL runs under a fixed cost limit (`celCostLimit`) and stops when the request ends.
- `POST /internal/public-checks/run` and `/mapping` (worker): a visitor's file, address and mapping, forwarded by the gateway. They sit behind `RequireGateway` and the ID token, keep the file in memory (`MultipartReader` only) and log no address, file name or result data. They answer 503 `ceiling` once the day's NULL-tenant AI spend reaches `AISpendCeilingUSD`.

## Accepted ceilings

- `ceiling:` in `migrations/20261006162416_invitation_accept.sql`: `invoice_app` can name one invite per known token across tenants.
- `ceiling:` in `migrations/20261122220218_erp_webhook_targets.sql`: `invoice_app` can list the connection ids of one vendor company key across tenants (`public.erp_webhook_targets`).
- `ceiling:` in `migrations/20261026120002_membership_removed.sql`: `provision_workspace` and every `accept_invitation` and `accept_invitation_by_id` overload are the membership writers behind the identity lock. `SetMembershipStatus` and `RemoveMember` take no identity lock; they change a status in the caller's tenant and never make a non-`removed` row.
- `ceiling:` in `migrations/20261026120001_purge_tenant.sql`: any `invoice_app` service can purge the tenant it binds.
- `ceiling:` in `migrations/20261027010037_erase_invitation_emails.sql`: any `invoice_app` service can erase the invitations of one address in every tenant.
- `ceiling:` in `internal/platform/auth/gotrueadmin`: the worker holds GoTrue's signing key.
- `ceiling:` in `internal/platform/db/account_state_read.go`: any `invoice_app` service can delete one account by id that is unconfirmed 7 days after its last mail.
- `ceiling:` in `internal/platform/db/account_state_read.go`: `invoice_gateway` can read the masked address of the account a confirm-token hash belongs to (`auth.confirm_link_address`).
- `ceiling:` in `cmd/gateway/main.go` and `cmd/worker/main.go`: a boot error quotes a raw internal service URL.
- `ceiling:` in `internal/accountmail/accountmail.go`: the account-mail token rides in the query string.
- `ceiling:` in `internal/gateway/unsubscribe_page.go`: the unsubscribe token is a 122-bit random UUID in the query string with no throttle; it reaches load-balancer and Caddy logs.
- `ceiling:` in `internal/gateway/signin_throttle.go`: anyone who knows an address can keep it locked out, the owner's right password included (`SignInMaxFailures` per `SignInWindow`). A completed password reset or invitation set-password clears the current count (`TestResetPassword_EvictsTheSubjectAndClearsSignInFailures`, `TestInvitationPassword_EvictsTheSubjectAndClearsSignInFailures`); the stranger can fill it again at once. `Reset` folds case and outer whitespace through `signInKey`, in-process (`TestSignInThrottle_ResetNormalises`) and in Postgres across instances (`TestRLS_GatewayThrottle_AResetOnOneInstanceClearsTheOther`, which calls `Reset` directly).
- `ceiling:` in `internal/gateway/reset_mail.go`: anyone can spend the reset mail share from 3 client IPs (IPv6 per /64) in an hour, or 7 in 24 h, and stop every user's reset mail until the window turns; staff get the `reset mail share spent` alert.
- `ceiling:` in `internal/gateway/refresh.go`: a refresh with the active refresh token's parent answers the active token, so a holder of the previous token follows each rotation while at most one behind (`TestIdP_RefreshRotationAndReuse`). GoTrue v2.198.0 has no setting to refuse it. The 720 h session timebox, counted from sign-in, is the outer bound; a parent-token holder is refused past it (`TestIdP_ParentTokenPastTheTimeboxIsRefused`). A token two rotations old refuses every refresh token of the session (`TestIdP_RefreshRotationAndReuse`); that relies on rotation enabled and a 0 s reuse interval, both pinned in the IdP image (`TestDockerfileCommitsTheD3EnvBlockAndNoSecret`); an access token already issued still passes the session check until its `exp` (`TestIdP_FamilyRevocationLeavesAnIssuedAccessTokenLive`; `GOTRUE_JWT_EXP=3600`, `TestDockerfileCommitsTheD3EnvBlockAndNoSecret`). Sign-out with the active token or its parent (`TestIdP_RevokedTokenNeverReachesAService`, `TestIdP_SignOutWithTheParentRefreshToken`) and a password reset (`TestIdP_ResetLinkOpensAPageThenTheFormEndsEverySession`) end the session.
- `ceiling:` in `internal/extraction/anchor_store.go`: learned rules are tenant-wide, so a member limited to one client teaches every client's copy of the layout (`TestRLS_LearnedRulesTenantWide_ALimitedMembersCorrectionTeachesAnotherClientsLayout`).
- `ceiling:` in `internal/tenancy/invitations.go`: the admin's invitation list shows whether each invited address has an account (`TestIdP_InvitationsListCarriesTheAccountState`).
- `ceiling:` in `internal/document/document.go`: an upload refusal (`ErrHeldElsewhere`) tells the caller the bytes exist on a document they cannot read, and an uncited document is readable by every member, so a hit on one still answers its id ([dedupe-ceiling], SECAUD-42-01).
- The production edge buffers the whole upload before the gateway reads it. The 90 s upload limit on `/readiness-check/*` guards local runs and a future HTTP/2 end-to-end port.
- The recovery link overwrites the account's stored recovery token, so a pending reset link dies when a console opens.
- Each IAP mint makes one GoTrue session; sessions are not reused.
- Three overlapping mints for one account can leave the third at 502 (`TestIAPSession_SecondSpentTokenIs502`); the retry covers two (`TestIAPSession_RetriesOnceWhenAConcurrentMintSpentTheToken`).
- A shared browser profile keeps one stored console session, so the next Google account on it opens as the stored identity until Sign out or "Use a different Google account".
- The IAP mint's account event carries the console service's egress address as its IP, not the staff member's.
- Any other `ceiling:` comment on a path above. List them with `git grep -n "ceiling:"`.
