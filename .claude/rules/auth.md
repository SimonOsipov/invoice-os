---
paths:
  - "sidecar/auth/**"
  - "internal/tools/idppin/**"
  - "internal/platform/auth/**"
  - ".github/workflows/idp-release-watch.yml"
  - "internal/gateway/register*.go"
  - "internal/gateway/resend_verification*.go"
  - "internal/gateway/password_reset*.go"
  - "internal/gateway/reset_password*"
  - "internal/gateway/invitation_password*"
  - "internal/gateway/verify_page*"
  - "internal/gateway/signin*.go"
  - "internal/gateway/signout*.go"
  - "internal/gateway/refresh*.go"
  - "internal/gateway/session_check*.go"
  - "internal/gateway/handoff*.go"
  - "internal/gateway/invitation*.go"
  - "internal/gateway/freemail*.go"
  - "internal/gateway/mock*.go"
  - "cmd/gateway/mockissuer.go"
  - "packages/console-session/**"
---
# Identity provider

- Pin the identity provider on the `FROM` line of `sidecar/auth/Dockerfile` only. Tag and digest live nowhere else.
- Bump the tag and the digest together.
- Keep a sha256 digest on the `FROM` line. `idppin` refuses a `FROM` without one.
- Pull the image from `ghcr.io` or `public.ecr.aws`, never Docker Hub. The Railway builder cannot reach Docker Hub.
- Print the pinned tag with `go run ./internal/tools/idppin tag sidecar/auth/Dockerfile`.

- Upgrade the pin with the steps below, in order.

1. Pick the release that the `supabase/auth patch due` issue names. Read its changelog and every advisory it fixes.
2. Read the new digest from `ghcr.io` for the tag. No `latest` tag exists.
   ```
   token=$(curl -fsS "https://ghcr.io/token?scope=repository:supabase/auth:pull" | jq -r .token)
   curl -fsSI -H "Authorization: Bearer $token" \
     -H 'Accept: application/vnd.oci.image.index.v1+json' \
     https://ghcr.io/v2/supabase/auth/manifests/<tag> | grep -i docker-content-digest
   ```
3. Bump the tag and the digest on the `FROM` line of `sidecar/auth/Dockerfile`. Change nothing else in the commit, unless the release notes require a new `ENV`.
4. Open a PR. It runs the `idp` CI job, the release watch and the PR deploy gate.
5. Confirm that `TestIdP_HealthReportsPinnedTag` reports the new tag.
6. Merge the PR. The push run deploys `auth` to production.
7. Close the patch-due issue.

- Write every production Railway variable with skip-deploys. A write without it rebuilds the service from `main` without the build stamp.
- Seal only the names in `SEALED_ALLOWLIST` in `scripts/ci/railway-env.sh`. A seal cannot be undone.
- Never write a script that writes over a sealed variable.
- Keep `AUTH_ADMIN_PASSWORD` unsealed. The `auth` service renders it into its `DATABASE_URL`.
- Print a secret as `<redacted>`. Never print its value.
- Compose `GOTRUE_JWT_KEYS` as a JSON array of ES256 keys with exactly one signing key.
- Validate a composed `GOTRUE_JWT_KEYS` offline before you paste it. A malformed value logs the private key at boot.

- Keep GoTrue private. The gateway calls fixed GoTrue paths and forwards no client path or query.
- Answer a new, a repeat and a confirmed address on register with the same 202.
- Answer a resend or reset request with the same 202 for every outcome after the 400 checks.
- Wait `AUTH_REGISTER_MIN_RESPONSE` before each register, resend or reset answer that passes the 400 checks.
- Spend one shared budget on resend and password-reset requests.
- Log no email address, password, token, code or client IP.
- Read the confirm token from the POST form body only. The GET page makes no GoTrue call.
- Carry a hand-off code in the URL, never a token. A code is single use and expires in `HandoffTTL`.
- Bind a hand-off code to the `state` that minted it.
- Reserve a sign-in throttle attempt before the GoTrue call.
- Accept a tenant-less token only on `POST /api/tenancy/v1/workspaces` and `POST /api/tenancy/v1/invitations/accept`.
- Match the tenant-less routes on the escaped path.
- Check every session with GoTrue `GET /user` before a request reaches a service.
- Answer 503 when the session check cannot reach GoTrue. Never treat that failure as a revoked session.
- Evict the subject's session-check cache on sign-out.
- Mint the staff claim from `public.staff_members` in the access-token hook. Never read `user_metadata` for it.
- Accept an invite only for the account whose verified email equals the invited address.

- Ship mock-issuer code only behind the `mockissuer` build tag. `TestProductionGatewayBinaryCannotMint` must stay green.
- Serve the mock routes only when `GATEWAY_MOCK_ISSUER` is `true` and `ENVIRONMENT` is not `production`.
- Check the staff claim on the server in any console that reads real data.
- Fail the console gate closed when `VITE_LANDING_URL` is unset.
- Open a console only for a session that the gateway has just renewed or exchanged, with `app_metadata.staff` true.
