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
  - "frontend/app/src/App.tsx"
  - "frontend/app/src/lib/signInState.ts"
  - "frontend/landing/src/App.tsx"
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
- Answer a new, a repeat and a confirmed address on `/auth/register` with the same 202.
- Answer `POST /auth/invitation` with `account` set to `none`, `unconfirmed`, `confirmed` or `unknown`. Map any other tenancy value to `unknown`.
- Answer `/auth/invitation/register` with 409 `account_exists` when the preview reads `confirmed` and 409 `account_unconfirmed` when it reads `unconfirmed`. Send both before the claim and the GoTrue call.
- Map GoTrue's 200 with empty `identities`, 422 `user_already_exists` and 422 `email_exists` to 409 `account_exists` on `/auth/invitation/register` only.
- Answer a resend or reset request with the same 202 for every outcome after the 400 checks. `/auth/invitation/resend` is the exception (next rule).
- Answer `POST /auth/invitation/resend` for a live token with 200 `sent` when GoTrue mailed.
- Answer it with 200 `held` on GoTrue's 60 s cooldown 429.
- Answer it with 200 `maybe` when the account state is `unknown`.
- Answer 502 `invitation resend is unavailable` for any other GoTrue failure.
- Answer `/auth/invitation/resend` with 409 `account_exists` when the preview reads `confirmed` and 409 `account_missing` when it reads `none`. Send both before the GoTrue call.
- Spend the resend per-address and per-IP budgets on `/auth/invitation/resend` and refund a GoTrue 4xx.
- Wait `AUTH_REGISTER_MIN_RESPONSE` before each register, resend or reset answer that passes the 400 checks. `/auth/invitation/resend` does not wait.
- Spend one shared budget on resend and password-reset requests.
- Log no email address, password, token, code or client IP.
- Read the confirm token and the `state` from the POST form body only. The GET page makes no GoTrue call.
- Answer `GET /auth/verify` with 303 to `<AUTH_SITE_URL>/?confirm=1#token=<token>` for a confirm link, when the request carries no valid `state`.
- Answer it with 303 to `<AUTH_SITE_URL>/?confirm=invite#token=<token>` for `invite=1`, when the request carries no valid `state`.
- Render a page only for a 43-character base64url `state`.
- Store the verify session in the hand-off store and redirect with `handoff=<code>` only when the form holds a valid `state` and GoTrue returns both tokens. Otherwise redirect to `?verified=1` with no code.
- Hand an invitee's session off from `POST /auth/invitation/password` only after the global sign-out, as a password grant with the password just set. Reserve no throttle attempt for it.
- Carry a hand-off code in the URL, never a token. A code is single use and expires in `HandoffTTL`.
- Bind a hand-off code to the `state` that minted it.
- Send a sessionless app visit with exactly `?via=library` to `<landing>/?state=<s>&register`. Any other value takes the sign-in path. An earlier front-door case wins: `?auth=start`, a confirm hop, a pending hand-off or a join offer. `frontDoor_authStartWinsOverVia` and `frontDoor_aPendingHandoffWinsOverVia` pin it.
- Expect `?register` to open the create-an-account modal, or "Book a demo" when `registrationOpen()` is false.
- Expect landing's `?demo` to open the book-a-demo modal.
- Expect a `?signin` outcome to suppress `?register` and `?demo` in `readDeepLink`. `?register` wins over `?demo`.
- Reserve a sign-in throttle attempt before the GoTrue call.
- Accept a tenant-less token only on four routes: `POST /api/tenancy/v1/workspaces`, `POST /api/tenancy/v1/invitations/accept`, `GET /api/tenancy/v1/invitations/mine` and `POST /api/tenancy/v1/invitations/{id}/accept`.
- Admit the two join routes only when GoTrue confirmed the email and it equals the token's `email` claim after trim and case folding.
- Refuse a join route with 403 for a token with no `session_id`, an unreadable `/user` answer or a null `email_confirmed_at`.
- Never read `user_metadata` for an email check. Any session holder can write it.
- Match the tenant-less routes on the escaped path.
- Check every session with GoTrue `GET /user` before a request reaches a service. Read `error_code` from a refusal and `email` and `email_confirmed_at` from a 200, within 16 KiB.
- Answer 503 when the session check cannot reach GoTrue. Never treat that failure as a revoked session.
- Evict the subject's session-check cache on sign-out.
- Mint the staff claim from `public.staff_members` in the access-token hook. Never read `user_metadata` for it.
- Accept an invite only for the account whose verified email equals the invited address.
- Answer a join with 404 `this invite is no longer valid` for every unusable invite. Never tell the cases apart.
- Answer a join with 409 `you already belong to a workspace` for a caller who holds a membership of any status.
- Keep the live stored session over a verify hand-off code. Show the confirmed notice and post no exchange.
- Offer the invites from `GET /api/tenancy/v1/invitations/mine` when `/me` answers 403 and no invite is held.
- Set an invitee's password only after proof of the invited mailbox: the confirmation mail's set-password page.
- Sign up an invite-link registration with a random password. Never store, log or return it.
- Create no account on register for an address with a pending invite. Answer it as any other register.
- Back one registration with each invite token.

- Ship mock-issuer code only behind the `mockissuer` build tag. `TestProductionGatewayBinaryCannotMint` must stay green.
- Serve the mock routes only when `GATEWAY_MOCK_ISSUER` is `true` and `ENVIRONMENT` is not `production`.
- Check the staff claim on the server in any console that reads real data.
- Fail the console gate closed when `VITE_LANDING_URL` is unset.
- Open a console only for a session that the gateway has just renewed or exchanged, with `app_metadata.staff` true.
