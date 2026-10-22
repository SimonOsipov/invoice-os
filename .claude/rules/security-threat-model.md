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
- Every database role is NOSUPERUSER and NOBYPASSRLS (`db/bootstrap.sql`).
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
- `probedServices` in `cmd/gateway/main.go` (docling, auth, reconciliation) get no public proxy route. `TestGatewayHandlersPublishNoProxyRouteForAProbedService` asserts this.
- The docling sidecar has no authentication. It relies on the private network. A public route to it is HIGH.

## Untrusted entry points

- `POST /v1/documents` (`extraction.UploadHandler`, submission): uploaded file bytes.
- docling `POST /v1/read` (`sidecar/docling/app.py`): document bytes forwarded by submission.
- `/v1/imports*` (`importer` handlers, invoice): CSV, spreadsheet and document input.
- Public gateway routes in `cmd/gateway/main.go` outside `/api/`: `/auth/*`, `/contacts/demo-request`, `/blog/*`, `/emails/*`, `/healthz/fleet`. They run without the verifier.
- `POST /v1/validate/batch` and `POST /v1/custom-rules/check` (validation): open past `RequireGateway`, guarded by `S2SMiddleware` with the shared `S2S_TOKEN`. They carry no identity and read no tenant.

## Accepted ceilings

- `ceiling:` in `migrations/20261006162416_invitation_accept.sql`: `invoice_app` can name one invite per known token across tenants.
- `ceiling:` in `migrations/20261006162416_invitation_accept.sql`: `provision_workspace` and `accept_invitation` are the only membership writers behind the identity lock.
- `ceiling:` in `cmd/gateway/main.go` and `cmd/submission/main.go`: a boot error quotes a raw internal service URL.
- `ceiling:` in `internal/accountmail/accountmail.go`: the account-mail token rides in the query string.
- Any other `ceiling:` comment on a path above. List them with `git grep -n "ceiling:"`.
