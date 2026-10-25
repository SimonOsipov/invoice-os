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
- `WithinRequestTenantTx` and `WithinRequestTenantTxOpts` in `internal/platform/db/tenant.go` set both settings in the transaction, before the membership SELECT.
- Every database role is NOSUPERUSER and NOBYPASSRLS (`db/bootstrap.sql`). Its guard fails the bootstrap otherwise.
- `restore_auditor` (`db/restore-auditor.sql`) is the one BYPASSRLS role. It is NOLOGIN, reads only the restore-check tables, and only `postgres` may SET ROLE to it.
- Reach tenant data only through the seam in `.claude/rules/tenant-seam.md`. `scPoolAllowlist`, `scCoreAllowlist` and `scRouteVerdicts` in `internal/platform/db/seam_coverage_test.go` list every exception.
- Give a tenant table ENABLE and FORCE row-level security. FORCE binds the table owner too.
- A table with ENABLE and no FORCE is a finding, except `house_company`. It is a global singleton, not a tenant table: only `auth_hook_reader` reads it.
- Review each new or changed `SECURITY DEFINER` function. It must check the tenant against `app.current_tenant`, or match one exact key such as a token hash or a confirmed email.

## Identity

- `Verifier.Verify` in `internal/platform/auth/verify.go` checks the JWT signature against the issuer's JWKS, then the issuer and claims in `validate`.
- `injectIdentity` in `internal/gateway/gateway.go` deletes every client `X-User-*` header and sets identity from the verified token only.
- A service trusts identity headers only behind `App.RequireGateway` in `internal/platform/peer.go`. It refuses a request without the gateway token.
- `auth.RequireStaff` admits only a caller whose verified identity has `Staff`.
- The mock issuer routes (`/auth/login`, `/auth/mock/*`, `/.well-known/jwks.json`) exist only in a `-tags mockissuer` build. `MockIssuerEnabled` also needs `GATEWAY_MOCK_ISSUER=true` and an environment other than `production`. `cmd/gateway/build.tags` is empty on `main`.

## Network

- Only the gateway and the static sites have a public domain. Every other service is on the private network only.
- `probedServices` in `cmd/gateway/main.go` (docling, auth, worker) get no public proxy route. `TestGatewayHandlersPublishNoProxyRouteForAProbedService` asserts this.
- The docling sidecar has no authentication. It relies on the private network. A public route to it is HIGH.
- Cloud Armor on the production load balancer throttles register, invitation register, resend, reset, invitation resend, `/contacts/demo-request` and `/blog/*` per client IP. Sign-in and refresh have no per-IP limit. Its rules are in the private repo invoice-os-infra, `terraform/armor.tf`.
- On Cloud Run every service but the gateway and the static sites is ingress-internal and admits only a caller with a Google ID token. `platform.IDTokenTransport` adds it in `X-Serverless-Authorization`; `TraceTransport` includes it.

## Untrusted entry points

- `POST /v1/documents` (`extraction.UploadHandler`, submission): uploaded file bytes.
- docling `POST /v1/read` (`sidecar/docling/app.py`): document bytes forwarded by submission.
- `/v1/imports*` (`importer` handlers, invoice): CSV, spreadsheet and document input.
- Public gateway routes in `cmd/gateway/main.go` outside `/api/`: `/auth/*`, `/contacts/demo-request`, `/blog/*`, `/emails/*`. They run without the verifier. `/healthz/fleet` runs without the verifier and needs `X-Gateway-Token`.
- `POST /v1/validate/batch` and `POST /v1/custom-rules/check` (validation): open past `RequireGateway`, guarded by `S2SMiddleware` with the shared `S2S_TOKEN`. They carry no identity and read no tenant.

## Accepted ceilings

- `ceiling:` in `migrations/20261006162416_invitation_accept.sql`: `invoice_app` can name one invite per known token across tenants.
- `ceiling:` in `migrations/20261006162416_invitation_accept.sql`: `provision_workspace` and `accept_invitation` are the only membership writers behind the identity lock.
- `ceiling:` in `migrations/20261025065442_purge_tenant.sql`: any `invoice_app` service can purge the tenant it binds.
- `ceiling:` in `cmd/gateway/main.go` and `cmd/worker/main.go`: a boot error quotes a raw internal service URL.
- `ceiling:` in `internal/accountmail/accountmail.go`: the account-mail token rides in the query string.
- Any other `ceiling:` comment on a path above. List them with `git grep -n "ceiling:"`.
