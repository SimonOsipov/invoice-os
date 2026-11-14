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

- `Verifier.Verify` in `internal/platform/auth/verify.go` checks the JWT signature against the issuer's JWKS, then the issuer and claims in `validate`.
- `injectIdentity` in `internal/gateway/gateway.go` deletes every client `X-User-*` header, sets identity from the verified token only, and sets `X-User-Address` from `clientIP`.
- Every gateway call to GoTrue but the session check and the fleet probe sets `X-User-Address` from `clientIP`. `GOTRUE_RATE_LIMIT_HEADER` names it (`TestDockerfileRateLimitHeader_NamesTheHeaderTheGatewaySends`).
- A service trusts identity headers only behind `App.RequireGateway` in `internal/platform/peer.go`. It refuses a request without the gateway token.
- `auth.RequireStaff` admits only a caller whose verified identity has `Staff`.
- The mock issuer routes (`/auth/login`, `/auth/mock/*`, `/.well-known/jwks.json`) exist only in a `-tags mockissuer` build. `MockIssuerEnabled` also needs `GATEWAY_MOCK_ISSUER=true` and an environment other than `production`. `cmd/gateway/build.tags` is empty on `main`.
- On the nightly, the mock POST routes and a no-`Origin` OPTIONS to `/auth/login` need `X-Mock-Issuer-Token`. `/.well-known/jwks.json` stays open. PR previews serve the routes open (SECAUD-47).

## Network

- Only the gateway and the static sites have a public domain. Every other service is on the private network only.
- `auth` admits the gateway and the worker.
- `probedServices` in `cmd/gateway/main.go` (docling, auth, worker) get no public proxy route. `TestGatewayHandlersPublishNoProxyRouteForAProbedService` asserts this.
- The docling sidecar has no authentication. It relies on the private network. A public route to it is HIGH.
- Cloud Armor on the production load balancer throttles register, invitation register, resend, reset, invitation resend, `/contacts/demo-request`, `/blog/*` and the two `/readiness-check/*` routes per client IP. `POST /auth/verify`, `/auth/reset-password` and `/auth/invitation/password` share one budget of 30 an hour per IP (rule `link-redeem`, priority 1080). No Armor rule covers sign-in or refresh. GoTrue limits `/token` per client address. Its rules are in the private repo invoice-os-infra, `terraform/armor.tf`.
- The gateway also counts the three routes per client IP as `link-redeem-ip` (`TestTwoInstances_TheLinkRedeemLimitCountsAcrossInstances`).
- On Cloud Run every service but the gateway and the static sites is ingress-internal and admits only a caller with a Google ID token. `platform.IDTokenTransport` adds it in `X-Serverless-Authorization`; `TraceTransport` includes it.

## Postures

- `platform.Posture` reads `DEPLOY_ENV`: empty is local, `pr-<N>` is preview, the exact name `nightly` is nightly, any other name is hosted.
- Only a preview logs the gateway's per-address limits without refusing. A preview captures account mail.

## Untrusted entry points

- `POST /v1/documents` (`extraction.UploadHandler`, submission): uploaded file bytes.
- docling `POST /v1/read` (`sidecar/docling/app.py`): document bytes forwarded by submission.
- `/v1/imports*` (`importer` handlers, invoice): CSV, spreadsheet and document input.
- `POST /auth/export-download` (gateway): a form body with an export code. It runs without the verifier until the code is redeemed.
- Public gateway routes in `cmd/gateway/main.go` outside `/api/`: `/auth/*`, `/contacts/demo-request`, `/blog/*`, `/emails/*`, `/readiness-check/*`, `/unsubscribe`. They run without the verifier. `/healthz/fleet` runs without the verifier and needs `X-Gateway-Token`.
- `POST /readiness-check/run` and `/mapping` (gateway): a visitor's multipart file, address and mapping. The gateway keeps the file in memory (`MultipartReader` only, 15 MiB, 4 uploads per instance) and streams it to the worker. It counts 10 runs per client IP in 24 h (IPv6 per /64), holds an upload slot at most 90 s and logs no address, IP or file name.
- `GET` and `POST /readiness-check/confirm` (gateway) and `POST /internal/public-checks/confirm` (worker): a 256-bit confirm token. The GET changes nothing; the POST consumes the link once within 24 hours and sends the held results. Accepted ceiling: no Armor rule covers the route, since a random token cannot be enumerated.
- The held results (`readiness_check_requests`): invoice amounts and buyer details of an unconfirmed address, kept at most 25 hours. The file and the client IP are never stored. `invoice_gateway` has no grant on the table.
- `POST /v1/validate/batch` and `POST /v1/custom-rules/check` (validation): open past `RequireGateway`, guarded by `S2SMiddleware` with the shared `S2S_TOKEN`. They carry no identity and read no tenant. A custom rule's CEL runs under a fixed cost limit (`celCostLimit`) and stops when the request ends.
- `POST /internal/public-checks/run` and `/mapping` (worker): a visitor's file, address and mapping, forwarded by the gateway. They sit behind `RequireGateway` and the ID token, keep the file in memory (`MultipartReader` only) and log no address, file name or result data. They answer 503 `ceiling` once the day's NULL-tenant AI spend reaches `AISpendCeilingUSD`.

## Accepted ceilings

- `ceiling:` in `migrations/20261006162416_invitation_accept.sql`: `invoice_app` can name one invite per known token across tenants.
- `ceiling:` in `migrations/20261026120002_membership_removed.sql`: `provision_workspace` and every `accept_invitation` and `accept_invitation_by_id` overload are the membership writers behind the identity lock. `SetMembershipStatus` and `RemoveMember` take no identity lock; they change a status in the caller's tenant and never make a non-`removed` row.
- `ceiling:` in `migrations/20261026120001_purge_tenant.sql`: any `invoice_app` service can purge the tenant it binds.
- `ceiling:` in `migrations/20261027010037_erase_invitation_emails.sql`: any `invoice_app` service can erase the invitations of one address in every tenant.
- `ceiling:` in `internal/platform/auth/gotrueadmin`: the worker holds GoTrue's signing key.
- `ceiling:` in `cmd/gateway/main.go` and `cmd/worker/main.go`: a boot error quotes a raw internal service URL.
- `ceiling:` in `internal/accountmail/accountmail.go`: the account-mail token rides in the query string.
- `ceiling:` in `internal/gateway/unsubscribe_page.go`: the unsubscribe token is a 122-bit random UUID in the query string with no throttle; it reaches load-balancer and Caddy logs.
- The production edge buffers the whole upload before the gateway reads it. The 90 s upload limit on `/readiness-check/*` guards local runs and a future HTTP/2 end-to-end port.
- Any other `ceiling:` comment on a path above. List them with `git grep -n "ceiling:"`.
