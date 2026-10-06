# Identity provider (`auth`, supabase/auth)

The `auth` Railway service runs a pinned, unmodified `supabase/auth` (GoTrue) image. It
signs ES256 access tokens, serves their public keys at `/.well-known/jwks.json`, and
projects the tenant into `app_metadata.tenant_id` through the Postgres access-token hook.
It is private-network only (`http://auth.railway.internal:8080`); it has no public domain.
The gateway reaches it for JWKS, for the fleet probe, for GoTrue's `/signup` and
`/verify` on behalf of the two public registration routes (see Registration), for
GoTrue's password grant on behalf of the public sign-in route (see Sign-in and hand-off),
for its refresh-token grant on behalf of the public refresh and sign-out routes (see
Renewal and Revocation), for GoTrue's `GET /user` on every checked `/api/` request, cached
for 30 s (see Revocation), and for GoTrue's `POST /logout?scope=global` on behalf of the
public sign-out route (see Revocation).

Related: [migrations.md](./migrations.md) §1 (the `supabase_auth_admin` and
`auth_hook_reader` roles), [deploy-model.md](./deploy-model.md) (where `auth` deploys),
[topology-e2e.md](./topology-e2e.md) (how a fork gets its own issuer),
[add-a-service.md](./add-a-service.md) (the sidecar appendix).

## The pin

- **Where:** `sidecar/auth/Dockerfile`, the `FROM` line (tag and digest), and nowhere else.
  Print the tag with `go run ./internal/tools/idppin tag sidecar/auth/Dockerfile`.
- **Who reads it:** `internal/tools/idppin`. `idppin tag sidecar/auth/Dockerfile` prints the
  tag. `idppin latest-check sidecar/auth/Dockerfile <tag>` exits 0 on a match, 1 on a
  mismatch and 2 on a malformed call or an unreadable pin. The parser refuses a `FROM`
  without a sha256 digest and a Docker Hub reference.
- **Why ghcr.io:** the Railway builder cannot reach `registry-1.docker.io`. The same digest
  is published on `public.ecr.aws/supabase/auth`; if the builder fails at `load metadata`
  on ghcr.io, switch the host and keep the digest.
- **Where it is checked:**
  - CI job `idp` builds the image and `TestIdP_HealthReportsPinnedTag` compares the
    container's `GET /health` `version` with `idppin tag`.
  - `idp-release-watch.yml` (below) compares the pin with upstream every day.
  - The deploy gate does **not** check it: the fleet roll-up probes `auth` at
    `/.well-known/jwks.json`, publishes no version, and fleet-gate exempts `auth` from the
    `build` check by name. `ceiling:` a stale `auth` on an older tag is invisible to the
    deploy gate; revisit if the fleet roll-up gains an authenticated view.

## Release watch

`.github/workflows/idp-release-watch.yml` runs daily, on `workflow_dispatch`, and on a
`pull_request` that touches itself, `sidecar/auth/Dockerfile` or `internal/tools/idppin/**`.

1. It reads the pinned tag with `idppin tag` and the pinned release's `published_at` from
   `repos/supabase/auth/releases/tags/<pin>`.
2. It passes `repos/supabase/auth/releases/latest` (prereleases excluded) to
   `idppin latest-check`.
3. Independently of step 2, it lists advisories from
   `repos/supabase/auth/security-advisories` published after the pinned release.
4. Either finding fails the run, naming the pinned tag, the latest tag and the advisory IDs.
5. On `schedule` and `workflow_dispatch` only, any failure of the compare step also opens
   or updates the issue titled `supabase/auth patch due`. The body opens with
   `Kind: finding.` or `Kind: error.` (an API error, a missing release or an unreadable pin).
   The issue is found by exact title, open or closed, so a rerun never opens a second one;
   a closed one is reopened.

`schedule` and `workflow_dispatch` run only from the default branch. The PR run is the
pre-merge evidence; it cannot write the issue.

## Upgrade procedure

1. Pick the release (the issue names it). Read its changelog and any advisory it fixes.
2. Read the new digest from ghcr.io, for the tag, not `latest` (none is published):
   ```
   token=$(curl -fsS "https://ghcr.io/token?scope=repository:supabase/auth:pull" | jq -r .token)
   curl -fsSI -H "Authorization: Bearer $token" \
     -H 'Accept: application/vnd.oci.image.index.v1+json' \
     https://ghcr.io/v2/supabase/auth/manifests/<tag> | grep -i docker-content-digest
   ```
3. Bump the tag **and** the digest on the `FROM` line of `sidecar/auth/Dockerfile`. Change
   nothing else in the same commit unless the release notes require a new `ENV`.
4. Open a PR. It runs the `idp` CI job (the path filter includes `sidecar/auth/**`), the
   release watch, and the PR deploy gate, which deploys the new image into the fork.
   `TestIdP_HealthReportsPinnedTag` must report the new tag.
5. Merge. The push run deploys `auth` to production. Close the patch-due issue.

## Variables

Nothing below is a secret value; secrets are named, never shown.

### Committed in the image (`ENV` in `sidecar/auth/Dockerfile`, same in every environment)

| Variable | Value | Why |
|---|---|---|
| `GOTRUE_DB_DRIVER` | `postgres` | |
| `GOTRUE_DB_MAX_POOL_SIZE` | `10` | GoTrue shares the application database. `ceiling:` raise it when GoTrue's logs show pool waits. |
| `GOTRUE_JWT_AUD` | `authenticated` | The verifier's audience constant |
| `GOTRUE_JWT_DEFAULT_GROUP_NAME` | `authenticated` | The `role` claim. v2.197.0 logs a deprecation warning for it |
| `GOTRUE_JWT_EXP` | `3600` | Access-token lifetime, seconds |
| `GOTRUE_DISABLE_SIGNUP` | `true` | Production stays closed until registration U3. PR forks (`set-fork-auth`) and the CI `idp` containers override it to `false` |
| `GOTRUE_MAILER_AUTOCONFIRM` | `false` | PR forks (`set-fork-auth`) override it to `true` |
| `GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_ENABLED` | `true` | |
| `GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_URI` | `pg-functions://postgres/public/custom_access_token_hook` | Only schema and function are read |
| `GOTRUE_SMTP_HOST` | `smtp.resend.com` | Forks override it to empty |
| `GOTRUE_SMTP_PORT` | `465` | Implicit TLS |
| `GOTRUE_SMTP_USER` | `resend` | |
| `GOTRUE_SMTP_ADMIN_EMAIL` | `no-reply@ascomply.com` | |
| `GOTRUE_SMTP_SENDER_NAME` | `ASComply` | |
| `GOTRUE_LOG_LEVEL` | `info` | |

### Per environment, on the `auth` service

| Variable | Production | PR fork (written by `set-fork-auth` / `set-fork-auth-site`) |
|---|---|---|
| `PORT` | `8080` | `8080` |
| `DATABASE_URL` | reference `postgresql://supabase_auth_admin:${{gateway.AUTH_ADMIN_PASSWORD}}@${{Postgres.RAILWAY_PRIVATE_DOMAIN}}:5432/${{Postgres.PGDATABASE}}` | the same reference |
| `API_EXTERNAL_URL` | `http://auth.railway.internal:8080` | the same |
| `GOTRUE_SITE_URL` | `https://www.ascomply.com` | the fork's landing URL |
| `GOTRUE_JWT_ISSUER` | `urn:ascomply:auth:production` | `urn:ascomply:auth:pr-<N>` |
| `GOTRUE_SMTP_HOST` | (image value) | empty: no mailer |
| `GOTRUE_DISABLE_SIGNUP` | unset (image value `true`) until registration U3, then `false` | `false` |
| `GOTRUE_MAILER_AUTOCONFIRM` | unset (image value `false`) | `true`: a fork sends no mail, so a registration is confirmed at once and can sign in |
| `GOTRUE_MAILER_URLPATHS_CONFIRMATION` | `https://api.ascomply.com/auth/verify` after registration U2; unset before it | not written; after U2 a fork inherits production's value, inert because a fork sends no mail |
| `GOTRUE_MAILER_TEMPLATES_CONFIRMATION` | `https://api.ascomply.com/emails/confirmation.html` after mail U1; unset before it | not written; after mail U1 a fork inherits production's value, inert because a fork sends no mail |
| `GOTRUE_MAILER_SUBJECTS_CONFIRMATION` | `Confirm your ASComply account` after mail U1; unset before it | not written; inherited after mail U1, inert |
| `GOTRUE_JWT_KEYS` | **secret, sealed** | freshly generated per fork |
| `GOTRUE_JWT_SECRET` | **secret, sealed** | freshly generated per fork |
| `GOTRUE_SMTP_PASS` | **secret, sealed**; the Resend API key, unset until U4 | empty |

### Per environment, on the `gateway` service

| Variable | Production | PR fork |
|---|---|---|
| `AUTH_URL` | `http://auth.railway.internal:8080` (fleet probe; required at boot) | the same |
| `AUTH_ISSUER` | `urn:ascomply:auth:production` after U3b; `https://mock.ascomply.dev` before it | `https://mock.ascomply.dev` (the fork's mock stays primary) |
| `AUTH_JWKS_URL` | `http://auth.railway.internal:8080/.well-known/jwks.json` after U3b | `http://127.0.0.1:8080/.well-known/jwks.json` (the mock) |
| `AUTH_ADDITIONAL_ISSUERS` | unset: production trusts one issuer (`/healthz` `auth_issuers=1`) | the fork's GoTrue issuer and JWKS URL (`auth_issuers=2`) |
| `AUTH_ADMIN_PASSWORD` | **secret, not sealed**: 64 hex characters, rendered into `auth.DATABASE_URL` | freshly generated per fork |
| `AUTH_SITE_URL` | `https://www.ascomply.com` after registration U1; unset before it | the fork's landing URL (`set-fork-auth-site`) |
| `AUTH_REGISTER_MIN_RESPONSE` | unset: the default `2s`, until registration U4 tunes it | not written; a fork inherits production's value |

`AUTH_ADMIN_PASSWORD` stays unsealed: the `auth.DATABASE_URL` reference renders it, and
every fork overwrites it with its own.

`AUTH_SITE_URL` is optional at boot. Unset, the gateway logs one warning and both
registration routes answer 503 `registration is not configured`. A value that is not an
absolute `http(s)` URL, or that carries user info, a query or a fragment, stops the
gateway at boot.

`AUTH_REGISTER_MIN_RESPONSE` is a Go duration (`2s`, `3500ms`): the shortest time any
`POST /auth/register` answer except a 400 takes. Unset means `2s`. A value that does not parse
or is not above zero stops the gateway at boot with an ERROR that names the variable and does
not echo the value. There is no upper bound.

### Per environment, on the `tenancy` service

| Variable | Production | PR fork |
|---|---|---|
| `RESEND_SENDING_KEY` | **secret, sealed**: the Resend sending-only key; unset until invite U1 | absent (a sealed variable does not fork); posture `preview` → `capture` |

## Script behaviour on writes

- `set-fork-auth`, `set-fork-auth-site` and `set-production-auth` write with
  `skipDeploys: true`, so a write never redeploys a service by itself.
- `set-fork-auth` and `set-fork-auth-site` write through `set_service_vars`
  (`variableCollectionUpsert`, only the names that differ). `set-production-auth` writes one
  variable at a time; a secret goes through `upsert_secret_variable`. Both print
  `label.NAME = <redacted>` for a secret and never a value or a length.
- Every write is re-read. A secret re-read compares the stored value with the written value
  inside the shell, exactly, and prints nothing. `GOTRUE_JWT_KEYS` must also parse as exactly
  one ES256 signing key (`prenv jwk-check`).
- **A production variable written any other way redeploys.** A Railway variable write on
  production that does not skip deploys makes Railway rebuild that service from GitHub
  `main`, without the build-SHA stamp. Measured 2026-09-24: gateway deployment `a97b3979`
  rebuilt from `main` `a724bef7` and reported `/healthz` build `dev`. Pass `--skip-deploys` to `railway variables --set`
  on production when no redeploy is wanted.

## First-time production setup (U1–U4)

Production writes are the user's. The environment id is
`6c864094-6a06-452f-8495-be77d8a94fe7`.

| Step | When | Production writes |
|---|---|---|
| U1 | before the PR's first deploy gate | create the `auth` service (no variables) |
| U2 | before merge | two roles, one schema, grants (Postgres) |
| U3a | before merge | one gateway variable, `AUTH_URL` |
| U3b | right after merge | `auth.*`, gateway `AUTH_ADMIN_PASSWORD`, `AUTH_ISSUER`, `AUTH_JWKS_URL`, then the seals |
| U4 | any time; gates registration | Resend domain verification and API key |

**U1 — Create the `auth` service in the production environment**, per
[add-a-service.md](./add-a-service.md) §5 and its sidecar appendix.
- Create it empty: `serviceCreate` with only `projectId` and `name`, no GitHub source. CI
  deploys `auth` with `railway up` (a tarball), so it needs no source. A sourceless service
  gets no deployment trigger, so a variable or dashboard edit cannot rebuild it from GitHub
  `main`. A source-connected service was measured doing that rebuild on 2026-09-24.
- Set `dockerfilePath=sidecar/auth/Dockerfile`, `healthcheckPath=/health`,
  `restartPolicyType=ON_FAILURE` and `watchPatterns: []` on the instance, because
  `railwayConfigFile` is rejected for new services.
- `healthcheckPath` is GoTrue's own `/health`. `serviceInstanceUpdate` rejects
  `/.well-known/jwks.json` with "Error in healthcheckPath - Invalid input". The gateway's
  fleet probe of `auth` still reads `/.well-known/jwks.json`; that is a separate check.
- Done 2026-09-24: service id `0cf7f5d8-23de-4879-9a2d-fa603ab966b6`.
- **No public domain. No variables.** The service stays undeployed until U3b and the first
  push after merge.
- **Needed before the PR's first deploy gate**, because forks inherit services from
  production and `expected_json` fails on a missing `auth`.
- Until done, the PR gate is red at prepare-env's `set-fork-auth` step, which cannot resolve
  a service named `auth`.
- This is the **only** production action the PR gate needs. The fork writes everything else
  itself. It is not the only one before a production deploy of this code: `GATEWAY_TOKEN`
  must be written first (`set-production-gateway-token`, `docs/add-a-service.md` §5).

**U2 — Create the roles and schema on production Postgres by hand**, as superuser through
`railway ssh --service Postgres` (production Postgres is private-only). Generate the
password with `openssl rand -hex 32` and keep it for U3b. Run only these lines:

```sql
CREATE ROLE supabase_auth_admin LOGIN PASSWORD '<pw>';
ALTER ROLE supabase_auth_admin NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
CREATE SCHEMA IF NOT EXISTS auth AUTHORIZATION supabase_auth_admin;
ALTER ROLE supabase_auth_admin SET search_path = auth;
GRANT USAGE ON SCHEMA public TO supabase_auth_admin;
CREATE ROLE auth_hook_reader NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
GRANT USAGE, CREATE ON SCHEMA public TO auth_hook_reader;
GRANT auth_hook_reader TO invoice_migrator WITH INHERIT FALSE, SET TRUE;
```

This is **mandatory before merge**. Otherwise the access-token hook migration's grants fail,
the production gateway crash-loops at `MigrateUp`, and no service deploys. The lines are
inert until the migration runs: nothing logs in as the new role before U3b.

**U3a — Write production's gateway `AUTH_URL` before merge**:

```
bash scripts/ci/railway-env.sh set-production-auth --pre-merge 6c864094-6a06-452f-8495-be77d8a94fe7
```

- It writes only `gateway.AUTH_URL=http://auth.railway.internal:8080`, with skip-deploys,
  and re-reads it.
- **Why before merge:** the merged gateway refuses to boot without `AUTH_URL` once `auth` is
  a probed service. The running production gateway ignores the variable, so the write is
  inert until the post-merge deploy. It also forks into other open PRs, whose older gateways
  ignore it.
- Until done, the push health-gate after merge fails and the fleet does not deploy.

**U3b — Write production's auth configuration right after merge**:

```
read -rs AUTH_ADMIN_PASSWORD && export AUTH_ADMIN_PASSWORD   # paste the U2 password; not echoed, not in history
read -rs RESEND_API_KEY && export RESEND_API_KEY             # only once U4 is done; skip the line otherwise
AUTH_JWT_SECRET="$(openssl rand -hex 32)" AUTH_JWT_KEYS="$(go run ./tools/prenv jwk-es256)" \
bash scripts/ci/railway-env.sh set-production-auth --post-merge 6c864094-6a06-452f-8495-be77d8a94fe7
```

- On `auth` it writes `PORT`, `DATABASE_URL` (the reference), `API_EXTERNAL_URL`,
  `GOTRUE_SITE_URL=https://www.ascomply.com`, `GOTRUE_JWT_ISSUER=urn:ascomply:auth:production`,
  `GOTRUE_JWT_KEYS`, `GOTRUE_JWT_SECRET`, and `GOTRUE_SMTP_PASS` only if the Resend key is
  given.
- On `gateway` it writes `AUTH_ADMIN_PASSWORD`, `AUTH_ISSUER=urn:ascomply:auth:production`
  and `AUTH_JWKS_URL=http://auth.railway.internal:8080/.well-known/jwks.json`.
  `AUTH_ADDITIONAL_ISSUERS` stays unset.
- Every write skips deploys.
- It refuses an `AUTH_ADMIN_PASSWORD` that is not 64 lowercase hex characters, and runs
  `jwk-check` on `AUTH_JWT_KEYS` before any write.
- It first reads `isSealed` for its three secret targets and refuses if any is already
  sealed. A re-run after sealing is a dashboard edit, not this command.
- It re-reads every value. Secrets are compared with the written value exactly, without
  printing.
- **Then seal, in this order, immediately after the write and before any PR run** (the lead
  pauses PR pushes for the duration):
  1. In the dashboard, on production's `auth` service, seal `GOTRUE_JWT_KEYS`, then
     `GOTRUE_JWT_SECRET`, then `GOTRUE_SMTP_PASS` if it was written. Seal nothing else: a
     seal cannot be undone.
  2. Run `bash scripts/ci/railway-env.sh audit-sealed-variables` by hand (read-only). It must
     exit 0 and list the sealed names it allowed, which must be the two or three just sealed.
- Then re-run the push `dev-env` run for the merge commit as a whole run
  (`gh run rerun <id>`, not `--failed`), so `auth` deploys. If that re-run gates on stale
  containers, push an empty commit instead.
- **Effect on other open PRs:** a new PR run checks out the PR's merge ref with `main`, so it
  carries the allowlisting audit and passes. A re-run of a run created before merge uses its
  old merge SHA and fails at "Audit sealed variables"; push to the PR, or re-run from a fresh
  event, instead.
- **Why after merge:** production already answers 401 to every `/api/` call, so re-pointing
  `AUTH_ISSUER` earlier gains nothing. Written earlier, it forks into every other open PR
  whose code predates `set-fork-auth`; their mock would then mint
  `urn:ascomply:auth:production` against an `auth` nobody deployed, and every E2E would
  return 401.
- **Expected until done:** the first push run after merge is red at fleet-gate, naming `auth`
  down (it has no database URL or key). The gateway and the other services deploy and pass
  health-gate. Production's user-facing state is unchanged, since it already refuses every
  `/api/` call.

**U4 — Verify `ascomply.com` in Resend (SPF and DKIM DNS records) and supply the API key.**
Production signup stays closed until registration U3 (below), so no confirmation mail goes
out before then; this step gates registration, not the provider. Until
done, `GOTRUE_SMTP_PASS` stays unset on production, and GoTrue boots with the Resend host
configured but sends nothing. Once the key exists after U3b's seals, add it as a dashboard
edit on `auth` (`GOTRUE_SMTP_PASS`), then seal it: `set-production-auth` refuses to run once
any of its targets is sealed.

No GitHub secret is needed. The CI `idp` job and every fork generate their own keys at run
time. The production key exists only in the Railway variable U3b writes and then seals.

## Registration

A verified registrant reaches HubSpot and Resend through the gateway hand-off: see
[contact-sync.md](./contact-sync.md).

GoTrue stays private. The gateway is the only public surface, and it calls GoTrue under
`AUTH_URL` at: `/signup` and `/verify` for registration, `/token?grant_type=password` for
sign-in (see Sign-in and hand-off), `/token?grant_type=refresh_token` for renewal and
sign-out (see Renewal and Revocation), `GET /user` for the session check on every checked
`/api/` request (cached 30 s; only `error_code` is read from the answer), and
`POST /logout?scope=global` for sign-out (see Revocation). It forwards no client path or
query, so no other GoTrue route (`/recover`, `/otp`, `/admin/*`, `/logout` with any other
scope, or `/token` with any other grant) is reachable from outside.

**The flow:**
1. The client posts `{"email","password"}` to `POST /auth/register` on the gateway. The body
   may also carry `workspace_name`, `display_name`, `kind?` and `marketing_consent_text?`
   (optional; the marketing sentence the person ticked, 1 to 500 characters; the gateway
   stores it as `data.marketing_consent{text,at}` with the server's time). When any of the
   first three is present,
   the gateway validates them with the tenancy rules (trimmed, 1 to 200 characters, no NUL,
   `kind` `firm` or `in_house`) and posts them to GoTrue `/signup` as
   `data.registration`; GoTrue stores them as `user_metadata.registration`. A free-mail
   address answers 400 first, and GoTrue is not called. GoTrue creates an unconfirmed user and
   mails a confirmation link through Resend. Every answer except a 400 arrives no earlier
   than `AUTH_REGISTER_MIN_RESPONSE` after the request reached the handler, so a new address
   and a known one take the same time while GoTrue answers faster than that (see Ceilings).
2. The link targets `GOTRUE_MAILER_URLPATHS_CONFIRMATION`, which is the gateway's
   `GET /auth/verify`, a page with one confirm button. A relative value would resolve against `API_EXTERNAL_URL`, a private
   host, so production sets an absolute URL.

   The confirmation mail is the branded template (`internal/accountmail`) that GoTrue fetches
   from `GOTRUE_MAILER_TEMPLATES_CONFIRMATION`, the gateway's public
   `GET /emails/confirmation.html`; the subject is `GOTRUE_MAILER_SUBJECTS_CONFIRMATION`. The
   gateway serves two `/emails/` routes: `GET /emails/confirmation.html` (the template) and
   `GET /emails/mark.png` (the logo, `accountmail.LogoURL`). GoTrue fetches a template once
   and caches it for 10 minutes (`TemplateMaxAge`). When that first fetch fails, GoTrue
   silently sends its own unbranded mail for up to 10 minutes and logs only
   `templatemailer_template_body_http_error`. So the template URL is checked before it
   reaches `auth`: `prenv mail-template-check <url>` loads it, and `railway-env.sh
   check-mail-templates <environment-id>` runs that check on every non-empty
   `GOTRUE_MAILER_TEMPLATES_*` of an environment's `auth`. It also parses and executes every
   non-empty `GOTRUE_MAILER_SUBJECTS_*` (`prenv mail-subject-check`), because a subject that
   fails to parse makes GoTrue send its default subject and body. It runs in the `fleet-gate` job of
   `dev-env.yml` (see [deploy-model.md](./deploy-model.md)) and by hand in mail U1. It reads
   variables unrendered, so a Railway reference (`${{...}}`) in a template URL is fetched
   literally and fails loudly. It also fails (`variables are unreadable`) when
   `GOTRUE_SITE_URL` is absent from `auth`'s variables, which means the token cannot read
   them.
3. The registrant opens the link and clicks "Confirm my email". The button submits a form to
   `POST /auth/verify`, which posts `{"type":"signup","token_hash":<token>}` to GoTrue
   `/verify`, discards the session GoTrue returns, and redirects the browser to
   `AUTH_SITE_URL`. Opening the link verifies nothing. No token reaches a landing URL.
4. The verified user signs in through `POST /auth/sign-in` and redeems the code at
   `POST /auth/exchange` (see Sign-in and hand-off). The first token carries no tenant: the
   access-token hook projects a tenant only for exactly one active membership.
5. With that tenant-less token the client calls `POST /api/tenancy/v1/workspaces`
   `{"workspace_name","display_name","kind"?}`. The gateway lets a tenant-less token through
   on this one method and path only. Tenancy creates the tenant and its first active admin
   in one transaction through `public.provision_workspace`
   ([migrations.md](./migrations.md) §1) and answers 201 `{tenant:{id,name,kind}, user:{id,role}}`. An absent
   `kind` stores `in_house`. `provision_workspace` refuses an identity that
   already holds any membership, in any workspace and in any status (unique violation,
   constraint `one_workspace_per_identity`; the gateway answers 409), and takes a
   per-identity advisory lock so two concurrent calls cannot both pass. The same
   transaction writes one `workspace.provisioned` audit event.
6. After every 201 the caller holds exactly one active membership, so the next token (a
   refresh grant or a new sign-in) carries `app_metadata.tenant_id`.

**`POST /auth/register`**, outside `/api/`, no verifier, in every build. It is CORS-wrapped, with an `OPTIONS /auth/register` preflight route:

| Outcome | Answer |
|---|---|
| GoTrue 200 (a new address, an unconfirmed repeat, or a confirmed address) | 202 `{"status":"verification_pending"}` |
| GoTrue `user_already_exists` or `email_exists` | the same 202 |
| GoTrue `over_email_send_rate_limit` (an unconfirmed repeat within 60 s, or the instance mail cap) | the same 202, logged at WARN |
| GoTrue 5xx whose `code` is SQLSTATE `23505` (the loser of two concurrent signups for one address) | the same 202, logged at WARN |
| a malformed body, or an empty email or password | 400 `{"error"}` |
| an answer field is present but `workspace_name` or `display_name` is missing, blank, over 200 characters or holds a NUL byte, or `kind` (even `""`) is not `firm` or `in_house` | 400 with the tenancy wording (`workspace_name must be 1 to 200 characters`, `workspace_name must not contain a NUL byte`, `kind must be "firm" or "in_house"`, and the `display_name` equivalents); GoTrue is not called |
| an address at a listed free-mail domain or its subdomain | 400 `{"error":"a business email address is required; personal email providers are not accepted"}`; GoTrue is not called |
| GoTrue `validation_failed`, `weak_password`, `email_address_invalid` | 400 with GoTrue's `msg` |
| GoTrue `signup_disabled` | 503 `registration is closed` |
| any other GoTrue 429 | 429 `too many requests` |
| GoTrue unreachable, or any other answer | 502 `registration is unavailable`, logged |
| `AUTH_SITE_URL` unset | 503 `registration is not configured` |

A preflight (an OPTIONS with an `Origin`) is answered by CORS; any other non-POST, including an
OPTIONS without an `Origin`, answers 405 `method not allowed` with `Allow: POST` at once, before the minimum
wait and without a GoTrue call. Guarded by `cmd/gateway/registration_routes_test.go`
`TestRegisterOptionsWithoutOriginIsNotARegistration`.

The four 202 rows answer identically, so the response never tells whether an address
already has an account. The answer never carries the user id or any GoTrue field except
`msg`.

The four 202 rows and the 503 `registration is closed`, 429 and 502 rows wait for
`AUTH_REGISTER_MIN_RESPONSE` (default `2s`), counted from when the request reached the handler. The 400 rows
answer at once. The 503 `registration is not configured` answers at once too, because the
route is not wired. If the client disconnects during the wait, no answer is written. Each
waiting request logs `registration: signup timing` with `upstream_ms` (how long GoTrue took)
and `min_ms` (the minimum): INFO while `upstream_ms` is below `min_ms`, WARN at or above it.

The free-mail list lives in `internal/gateway/freemail.go` `freeMailDomains`. To extend it,
add one lower-case domain; its subdomains are refused too. Fullwidth, ideographic-dot and
inner-whitespace forms of a listed domain are refused by GoTrue's own format check (400),
guarded by `TestIdP_FreeMailVariantsAreNotAccepted`.

**`POST /contacts/demo-request`**, outside `/api/`, no verifier, in every build. CORS-wrapped, with an `OPTIONS` preflight route. Body `{"email","name","company","marketing_consent_text"?}`; other keys are ignored and not forwarded. The body limit is 4096 bytes. Fields are trimmed; `marketing_consent_text` is forwarded as sent.

| Outcome | Answer |
|---|---|
| forwarded once to notifications (no retry) | 202 `{"status":"accepted"}` |
| a malformed or oversized body | 400 `{"error":"invalid request body"}` |
| email not 3 to 254 bytes, not exactly one `@`, or holds whitespace | 400 `email is invalid` |
| `name` or `company` not 1 to 200 characters, or holds a NUL byte | 400 `name must be 1 to 200 characters`, `name must not contain a NUL byte`, and the `company` equivalents |
| `marketing_consent_text` present but blank, over 500 characters or holding a NUL byte | 400 `marketing_consent_text must be 1 to 500 characters` |
| notifications fails, answers anything but 202, or exceeds 5 s | 502 `demo request is unavailable`; the log carries the status only |

The handler sets `Cache-Control: no-store` on every answer it writes. Any method but POST and a preflight answers 405. The route is public and unthrottled; the `ceiling:` line in `internal/gateway/contacts.go` `DemoRequestHandler` names the limit. Guarded by `internal/gateway/contacts_test.go` (`TestDemoRequest_*`).

The `/api/` router answers 404 for any path whose first segment after the service is `internal`, before authorization, on the decoded path, both raw and after `path.Clean`, so a dot-dot or empty segment that resolves to `internal`, or a raw `internal/..` prefix, is refused for every method, CONNECT included. Guarded by `internal/gateway/gateway_test.go` `TestRouter_InternalPathNeverReachesUpstream`.

**`GET /auth/verify?token=…&type=signup`** (the page), outside `/api/`:

| Outcome | Answer |
|---|---|
| a `token` of 1 to 256 bytes and `type=signup` | 200 `text/html`: one form with the token and type as hidden fields and a "Confirm my email" button; GoTrue is not called |
| an empty or over-long `token`, or a `type` other than `signup` | 303 to `<AUTH_SITE_URL>/?verify=failed`; no page |
| HEAD | as GET, without the body |
| any method but GET, HEAD and POST | 405 from the router, `Allow: GET, HEAD, POST` |
| `AUTH_SITE_URL` unset | 503 `registration is not configured` |

The page handler holds no GoTrue client. It sets `Cache-Control: no-store`,
`Referrer-Policy: no-referrer` and a `Content-Security-Policy` that allows one inline script by
its hash. The script blocks a second submit of the form. The page reveals nothing beyond the
token. `redirect_to` and every other query value are ignored and never rendered. The page shape
is fixed for the signup link; whether other links can reuse it is unmeasured.

**`POST /auth/verify`** (the act), form `token=…&type=signup`, outside `/api/`:

| Outcome | Answer |
|---|---|
| GoTrue `/verify` 200 | 303 to `<AUTH_SITE_URL>/?verified=1`; one contact hand-off |
| a form that does not parse, is over 1 KiB, is not `application/x-www-form-urlencoded`, or carries an empty or over-256-byte `token` or a `type` other than `signup` | 303 to `<AUTH_SITE_URL>/?verify=failed`; GoTrue is not called |
| a GoTrue refusal, or GoTrue unreachable | 303 to `<AUTH_SITE_URL>/?verify=failed`, logged at WARN (the upstream status, or the error) |
| any method but GET, HEAD and POST | 405 from the router, `Allow: GET, HEAD, POST`; GoTrue is not called |
| any method but POST, sent to the handler | 405 `{"error":"method not allowed"}`, `Allow: POST`; GoTrue is not called |
| `AUTH_SITE_URL` unset | 503 `registration is not configured` |

The handler reads the token from the form body only, never from the URL, and sets
`Cache-Control: no-store`. The route sets no CORS headers and carries no CSRF token: it uses no
cookie, and whoever holds the token can already post it. The redirect target is always the
gateway's own `AUTH_SITE_URL`.

**`POST /api/tenancy/v1/workspaces`:** 201 with `{tenant:{id,name,kind}, user:{id,role}}`;
400 for a malformed body, a name outside 1–200 characters, or a `kind` other than `firm` or
`in_house` (an absent `kind` is valid and stores `in_house`); 401 for no caller or a subject that is not a UUID; 409 `this account already has a workspace` when the token
already carries a tenant, when the caller already provisioned one, or when the caller holds
any membership in any workspace in any status; 500
otherwise. The tenant id is a UUIDv5 of the caller's subject; the membership guard in
`provision_workspace` is what holds one identity to one workspace. Every 201 writes one
`workspace.provisioned` audit event in the same transaction.

**Ceilings:**
- `ceiling:` GoTrue's per-request rate limiters, `/verify` and `/token` included, are off in
  this fleet. On v2.197.0 they key on the header named by `GOTRUE_RATE_LIMIT_HEADER` and do
  nothing while it is unset (`middleware.go` `performRateLimitingWithHeader`); no image,
  script or runbook sets it. Sign-in has the gateway's own per-address throttle instead (see
  Sign-in and hand-off); the gateway throttles neither registration nor verify. A
  per-client-IP limit needs a client-IP header the gateway can trust, and Railway's
  `X-Forwarded-For` handling is unmeasured. Revisit before registration U3.
- `ceiling:` `RATE_LIMIT_EMAIL_SENT` (30 per hour) is instance-wide, so production sends
  about 30 confirmation mails per hour. A registrant during the cap gets 202 and no mail; the
  WARN log line `registration: gotrue email send rate limit` is the only signal. Set
  `GOTRUE_RATE_LIMIT_EMAIL_SENT` when signup traffic approaches it.
- `ceiling:` the registration minimum hides GoTrue's timing only while GoTrue answers faster
  than `AUTH_REGISTER_MIN_RESPONSE`. A slower answer still leaks timing. Revisit when
  `registration: signup timing` logs WARN, and raise the minimum (registration U4 step 5).
- `ceiling:` each waiting register request holds a connection for up to the minimum, and
  register has no per-client limit. Revisit with the per-client-IP limit owed before
  registration U3.
- `ceiling:` the session GoTrue issues on verify is discarded but stays live in
  `auth.sessions` and `auth.refresh_tokens`. No route revokes it by itself; a global
  sign-out or a staff cut-off of the account deletes it with the account's other sessions
  (see Revocation and Cutting an account off). Its tokens never reach anyone.

**Accepted risks of the emailed link:**
- *First registrant's answers.* GoTrue does not update an unconfirmed user on a repeat signup,
  so the answers (`user_metadata.registration`) of the **first** registrant stay, whoever
  confirms. A victim who registers after an attacker provisions the attacker's workspace
  name and kind. This adds no exposure beyond the hijack below, which already hands over the
  account.
- *Pre-account hijack.* GoTrue does not update an existing unconfirmed user on a repeat
  signup; it re-sends the confirmation mail for the **first** registrant's password. An
  attacker who registers `victim@corp` first causes a mail to the victim. If the victim then
  registers, their 202 is identical, and their click confirms the **attacker's** password.
  The attacker then owns a verified account at the victim's address. No password-recovery
  path exists yet, so the victim cannot take it back.
- *Link scanners.* A mail scanner that prefetches the link with GET or HEAD gets the confirm
  page and spends nothing. The hijack
  half above stays: closing it needs password recovery, or a delete-and-re-create of an
  unconfirmed user on a repeat signup.

**Accepted risks of user-editable metadata:**
- *Self-asserted consent.* A user can edit their own GoTrue `user_metadata`, so
  `user_metadata.marketing_consent` is self-asserted. The hand-off forwards it, and the contact
  store records only the first tick.

## Sign-in and hand-off

The landing SPA is the sign-in surface, and it is a different origin from the app. No cookie
or other channel is shared between them, so the session crosses in the URL as a short-lived,
single-use code, never as a token. The code is bound to a `state` that the app, or a console,
minted in the same tab, so a code minted in another browser signs nobody in. The target of the
hand-off is the app by default and a console when the visitor came from one (Console sessions).

**The flow:**
1. A signed-out app tab calls `ensureSignInState` (`frontend/app/src/lib/signInState.ts`). It
   reuses a live state or mints 32 random bytes as 43 base64url characters, and stores
   `{v:1, s, at}` at `sessionStorage['invoice-os.signInState']` for 10 minutes. It never reads
   a state from a URL.
2. The app goes to `<landing>/?state=<s>[&signin=<outcome>]`: from the front-door redirect,
   from the start bounce (step 3) with `signin=ready`, and from a failed hand-off (step 7).
   A console adds `console=ops` or `console=support`. Landing keeps the state and the console
   target in memory only and strips the params it read at boot.
3. A visitor who opened landing directly has no state. The modal then shows "Continue with
   email", which goes to `<app>?auth=start`, or to the held console's `?auth=start`. The app
   or console ensures a state and returns to landing with `signin=ready`, which opens the
   modal with the form.
4. Landing posts `{"email","password","state"}` to `POST /auth/sign-in`. The gateway posts
   `{"email","password"}` to GoTrue `/token?grant_type=password`. On a 200 it keeps the access
   token and the refresh token with `sha256(state)` and answers a code.
5. Landing navigates to `<app>?handoff=<code>`, or to `<console>?handoff=<code>` for a held
   console target. The app or console strips the param at mount, before redemption resolves.
6. The app reads and removes its stored state (`consumeSignInState`) and posts
   `{"code","state"}` to `POST /auth/exchange`. It then calls `GET /api/tenancy/v1/me` with
   the access token and stores the session at `localStorage['invoice-os.session']` with
   `handoff: true`, the refresh token (`refresh_token`) and `received_at` (epoch ms): the
   local time the exchange was sent, backdated by `HandoffTTL` (60 s) because the token may
   have waited that long in the store. A tab with no live state makes no exchange call and
   goes to step 7. When `/me` answers 403 and the token's `user_metadata.registration`
   holds both names (and a `kind` that is absent, `firm` or `in_house`), the app posts them to
   `POST /api/tenancy/v1/workspaces` with the same token, silently and without a
   confirmation screen. After a 201, or a 409 (the identity already holds a membership), it
   posts the exchange's refresh token to `POST /auth/refresh` and calls `/me` with the new
   access token. The session then holds the refreshed access and refresh tokens, and
   `received_at` is the local time of the refresh, not backdated. One 15 s timeout covers the
   whole chain. Without answers the 403 stands. A provisioning 400 or 5xx, a failed refresh or an
   exchange without a refresh token ends in step 7 as `signin=failed`. An account whose
   workspace an operator deleted re-provisions at its next sign-in (accepted; revisit when
   workspace deletion ships).
7. On any failure the app returns to landing with `signin=no-workspace` (the `/me` call
   answered 403) or `signin=failed` (anything else), carrying the state `ensureSignInState`
   returns: a newly minted one, because step 6 removed the old. Landing opens
   the modal with "This account has no workspace yet." or "We couldn't open your workspace.
   Sign in again." A console redeems the same way, but instead of `/me` it reads the token:
   one without the staff claim returns `signin=not-staff` ("This account cannot open the
   ASComply consoles."). An unknown `signin` value is stripped and ignored.

**Workspace mode.** A hand-off session takes its mode from `/me` `tenant.kind`: `firm` opens
the firm workspace, `in_house` the in-house one. A `/me` answer, or a stored hand-off record,
without a known `kind` fails the redemption (step 7, `signin=failed`) or drops the record. A
session from the in-app picker of a build with no landing URL (the standalone showcase build and
local dev) keeps the picked persona's mode.

**Identity card.** A hand-off session's card shows `/me` `user.display_name`, else
`user.email`, else nothing; its initials follow the same order. A picker session shows the picked
persona's name. A stored record without the name keeps a blank card: renewal does not re-read `/me`, so only a new sign-in fills it.

The access token travels only in the exchange and refresh answers and the `Authorization`
header; the refresh token travels only in the exchange answer, the refresh request and
answer, and the sign-out request (see Revocation). Landing never holds either. Landing renders the form only when `VITE_GATEWAY_URL` and `VITE_APP_URL` are set. The create-account entry also needs `VITE_REGISTRATION_OPEN=true`: `reconcile-urls` writes it on every PR fork, and production leaves it unset until registration U3. A hand-off to a console also needs landing's
`VITE_OPS_URL` or `VITE_SUPPORT_URL`; without it landing does not navigate. The app ignores `?handoff=` when its
`VITE_GATEWAY_URL` is unset.

**`POST /auth/sign-in`** `{"email","password","state"}`, 4 KiB body limit, outside `/api/`,
no verifier, wrapped in CORS, in every build:

| Outcome | Answer |
|---|---|
| GoTrue 200 with a non-empty `access_token` and `refresh_token` | 200 `{"code":"<43 characters>"}` |
| a malformed body, or one over 4 KiB | 400 `invalid request body`; GoTrue is not called |
| an empty email or password | 400 `email and password are required`; GoTrue is not called |
| `state` missing or not 43 base64url characters | 400 `state is required`; GoTrue is not called |
| an email longer than 254 bytes | 400 `invalid email address`; GoTrue is not called |
| the address is over the throttle, or the throttle is full and the address is new | 429 `too many requests`; GoTrue is not called |
| GoTrue `invalid_credentials` or `user_banned` | 401 `invalid email or password` |
| GoTrue `email_not_confirmed` | 403 `email address not verified` |
| GoTrue 429 (reachable only if `GOTRUE_RATE_LIMIT_HEADER` is ever set) | 429 `too many requests` |
| GoTrue 200 without `access_token` or `refresh_token`, any other answer, or GoTrue unreachable | 502 `sign-in is unavailable`, logged at WARN with the upstream status or the error only |
| GoTrue 200 with both tokens while the hand-off store is full (`HandoffMaxLive`) | 503 `sign-in is unavailable`, logged at WARN; the reservation is refunded |

A banned address answers exactly like a wrong password. The gateway never logs the email,
the password, the code or either token.

**`POST /auth/exchange`** `{"code","state"}`, 1 KiB body limit, the same wrapping:

| Outcome | Answer |
|---|---|
| a live code with the state that minted it | 200 `{"access_token":"<jwt>","refresh_token":"<opaque>"}`; the code is gone |
| an unknown, expired, already redeemed, empty or malformed code; a missing or wrong state; a malformed body | 400 `invalid or expired code` |

A code redeems only with the state that minted it. A missing or wrong state answers the same
400 as an unknown code, and it spends the code: `Take` deletes the entry under the lock
before it checks expiry and compares `sha256(state)` in constant time.

Both routes set `Cache-Control: no-store` and use the flat `{"error"}` envelope. Each is
registered twice, POST and OPTIONS, because a POST-only route answers the CORS preflight
with 405. The CORS layer answers a preflight; any other non-POST request answers 405.

**The code** (`internal/gateway/handoff.go` `HandoffStore`): 32 random bytes, 43 base64url
characters, stored only as `sha256(code)`, live for 60 s (`HandoffTTL`), single use.

**The throttle** (`internal/gateway/signin_throttle.go` `SignInThrottle`), keyed by the
lower-cased, trimmed address: at most 10 attempts in a 15-minute window counted from its
first attempt (`SignInMaxFailures`, `SignInWindow`).
- An attempt is reserved before GoTrue is called, so a parallel burst cannot pass the limit.
- `invalid_credentials` and `user_banned` keep the reservation. A 200 clears the address. Any
  other outcome refunds it, because it did not test a password.
- The map holds at most 100,000 addresses (`SignInMaxKeys`). Expired keys are swept at most
  once a minute, or at once when the map is full. When it is still full, a new address gets
  429 and the gateway logs one WARN per minute; an address already counted keeps its count.

**Precedence in the app.** A live stored hand-off session wins over `?handoff=`: the code is
stripped and not acted on, so a URL never replaces a real session. A
user signed in as A who signs in on landing as B arrives back in A's workspace with no
message; B's code expires unused. Sign out first to switch accounts. A stored hand-off
session whose access token has expired loses to `?handoff=`, even when it carries a refresh
token. Guarded by `App.sessionRenewal.test.tsx` "a ?handoff= code wins over an expired
renewable hand-off session".

**Ceilings:**
- `ceiling:` the code store and the throttle are in-process. A gateway restart drops
  unredeemed codes (the user signs in again) and clears the counts, and a second replica
  would refuse a code minted on the other. Move both to Postgres before the gateway runs
  more than one replica.
- `ceiling:` 10 wrong attempts every 15 minutes, about 960 requests a day, keep an address
  locked out indefinitely, correct password included, because the 429 comes before GoTrue.
- `ceiling:` there is no per-client-IP limit, so credential stuffing across many addresses is
  not slowed, and about 111 new addresses per second fill the throttle map and block sign-in
  for new addresses. GoTrue applies no limit of its own (Registration, Ceilings). Revisit
  with a measured client-IP header before registration U3.
- `ceiling:` an unknown or banned address answers faster than a known one with a wrong
  password, because GoTrue returns before it checks the password. Response time can show
  that an account exists. Revisit before registration U3.
- `ceiling:` sign-out revokes every session of the account in GoTrue only when the app's
  `POST /auth/sign-out` succeeds. When it fails (gateway unreachable, the 5 s timeout, any
  non-2xx answer), the app still signs out locally and tells the user nothing, and a copy of
  the refresh token taken before sign-out keeps renewing until the account is signed out
  again or cut off (see Revocation, The app's rule).
- `ceiling:` the Railway edge access log records the app's boot URL with the code and
  landing's boot URL with the state, and the browser's global history keeps the app URL
  (`replaceState` does not purge it). The code is dead after 60 s or one use, and useless
  without its state. Someone who can read the edge log within a state's 10 minutes could bind
  their own code to a victim's state and send the victim the link; that needs privileged log
  access.

## Renewal

A hand-off session renews itself before its access token expires, so the user is not sent
back to landing after `GOTRUE_JWT_EXP`. A picker session does not renew: `/auth/login` answers
no refresh token, so it ends after one hour (a build with no landing URL only).

**`POST /auth/refresh`** `{"refresh_token"}`, 1 KiB body limit, outside `/api/`, no verifier
(it must work with an expired access token), wrapped in CORS, POST and OPTIONS, in every
build. The gateway posts `{"refresh_token"}` to GoTrue `/token?grant_type=refresh_token`.

| Outcome | Answer |
|---|---|
| GoTrue 200 with a non-empty `access_token` and `refresh_token` | 200 `{"access_token","refresh_token"}` and no other key |
| a malformed body, or one over 1 KiB | 400 `invalid request body`; GoTrue is not called |
| an empty or missing `refresh_token` | 400 `refresh_token is required`; GoTrue is not called |
| GoTrue 429 (reachable only if `GOTRUE_RATE_LIMIT_HEADER` is ever set) | 429 `too many requests` |
| any other GoTrue 4xx (`refresh_token_not_found`, `refresh_token_already_used`, `session_not_found`, `session_expired`, `user_banned`, …) | 401 `invalid or expired refresh token` |
| GoTrue 200 missing `access_token` or `refresh_token`, any other non-4xx answer, or GoTrue unreachable | 502 `renewal is unavailable`, logged at WARN with the upstream status or the error only |
| an OPTIONS without an `Origin` | 405 `method not allowed`, `Allow: POST` |
| any other method but POST | 405 from the router, `Allow: OPTIONS, POST` |

A preflight (an OPTIONS with an `Origin`) is answered by CORS. Every answer the handler
writes sets `Cache-Control: no-store`; the router's 405 does not. The gateway never logs
either token. Guarded by `internal/gateway/refresh_test.go` (`TestRefresh_*`),
`cmd/gateway/handoff_routes_adversarial_test.go` `TestHandoffPreflightIsAnsweredByCORS`,
the CI `idp` job's `TestIdP_RefreshRefusalsAnswer401`, and `e2e/api/session-handoff.spec.ts`
"sign-in hand-off (API E2E, over the deployed gateway) › the exchange answers a refresh
token, and a refresh renews it". No test pins the router's 405.

**The app's rule** (`frontend/app/src/lib/renewal.ts`):
- **On demand.** Every request that needs a token asks the renewer first. The renewer
  renews when `now ≥ received_at + 0.8 × (exp − iat) × 1000` (ms), then answers with the
  new token. There is no timer. `exp − iat` is the server's lifetime in seconds;
  `received_at` is the local time the refresh request was sent, or for a hand-off session
  the time the exchange was sent less `HandoffTTL` (60 s). Device clock skew cancels out,
  and the code's wait in the store cannot push the deadline past the token's real `exp`.
  Concurrent requests share one renewal.
- **At boot.** A stored session whose access token has expired is kept when it carries a
  refresh token. If it is due, the app shows "Opening your workspace…" and renews before the
  workspace mounts.
- **Refused:** a 400 or 401, a renewed token whose `app_metadata.tenant_id` is not the
  session's tenant, or a renewed token without a readable `iat`/`exp`. The session ends at
  once and the stored record is removed. The front door captures the current path, sends the
  user to landing with a sign-in state (Sign-in and hand-off, step 1), and restores the path after the next
  sign-in.
- **Transient:** anything else (a network error, the 15 s timeout, 429, 5xx, a 200 without
  both tokens). Before the deadline, `received_at + (exp − iat) × 1000` (ms), the request
  proceeds with the current token and the next request tries again. At or after the
  deadline, or when the times are unreadable, the session ends as if refused but the stored
  record stays: the refresh token may still be valid, so the next boot renews. Guarded by
  `renewal.test.ts` "transient at the deadline ends it" and `App.sessionRenewal.test.tsx`
  "transient after the deadline ends the session".
- **Tenant check.** The app decodes the renewed token's tenant without verifying it; the
  gateway verifier stays the authority. The access-token hook drops the tenant when the
  membership stops being the one active membership, and a tenant-less token would 403 on
  every tenant route.
- Guarded by `frontend/app/src/lib/renewal.test.ts`,
  `frontend/app/src/App.sessionRenewal.test.tsx`, and the deployed
  `e2e/topology/auth.spec.ts` "deployed app: a real session renews itself past the access
  token's lifetime" and "deployed app: a refused renewal returns to landing and keeps the
  destination".
  `TestIdP_RenewalOutlivesTheAccessTokenTTL` renews after a real expiry against the CI
  container `idp-short` (`GOTRUE_JWT_EXP=5`, port 9996, `scripts/ci/idp-up.sh`).

**Two tabs.** Tabs of the app origin share one `invoice-os.session` record. Before it
renews, the renewer re-reads the record:
- absent, or another user → the session ended or changed in another tab; this tab's session
  ends and the record is left untouched;
- the same user with a different refresh token that is not yet due → another tab already
  renewed; this tab adopts it with no request;
- otherwise → renew with the stored refresh token.

Two tabs that renew together send the same refresh token. GoTrue answers a revoked token
that is the parent of the active token with the active token (GoTrue v2.197.0
`tokens/service.go`, at any age; `TestIdP_RefreshRotationAndReuse` pins it for a fresh
parent). A token two or more
generations old revokes the whole session for every tab (400 `refresh_token_already_used`,
which the gateway answers 401). Guarded by `TestIdP_RefreshRotationAndReuse`.

**Where the refresh token is stored, and what that does and does not protect against.**
The refresh token is stored in the app origin's `localStorage`, key `invoice-os.session`,
field `refresh_token`, in the same record as the access token. For its 60 s in the hand-off
store it is in the gateway's memory, beside the access token. It crosses the network only
inside JSON bodies over TLS: the exchange answer, the refresh request, the refresh answer and
the sign-out request. The record also holds the account's display name and email, inside the
stored `/me` answer.

It protects against:
- **Other origins.** Landing, both consoles and every other site cannot read it:
  `localStorage` is per origin.
- **URLs.** It never appears in a URL, so browser history, the Railway edge access log and a
  `Referer` header cannot carry it. The deployed topology journeys scan every recorded URL
  for it.
- **Logs.** The gateway never logs it (`TestSignIn_NeverLogsSecrets`,
  `TestRefresh_NeverLogsSecrets`).
- **A stale copy.** GoTrue rotates the token on every renewal; presenting a token two or more
  generations old revokes the whole session.
- **Parity.** A script that can read it can already read the access token. The refresh token
  adds no new reader.

It does not protect against:
- **Any script running on the app origin**, whether an XSS, a compromised dependency or a
  browser extension with page access. It can read both tokens. With the refresh token it can
  keep the session alive after the tab closes, from another machine, until the session is
  revoked. GoTrue here sets no session timebox or inactivity timeout (see GoTrue's session
  values below). The session ends when its holder signs out successfully or an operator cuts
  the account off (see Revocation and Cutting an account off). Before renewal the same theft
  bought at most one hour.
- **Someone with the device's browser profile** (malware, a shared machine left signed in).
- **A thief who renews in step with the victim.** GoTrue's parent rule answers the previous
  token with the active one, so two holders who alternate are not detected. Detection needs
  a token two generations old.
- **A sign-out that fails.** Sign-out revokes every session of the account through
  `POST /auth/sign-out`. When that call fails, the stored copy is removed but not revoked,
  and the user is not told (see Revocation, The app's rule).
- **Script access by design.** The token is not `HttpOnly`. A gateway cookie was rejected:
  on every PR fork the app and the gateway are different sites, so it would be a third-party
  cookie.

**GoTrue's session values** (v2.197.0 defaults; production sets none of these, measured
2026-09-27). They are recorded, not a designed policy:
- refresh-token rotation on; reuse interval 0 s;
- v1 refresh tokens (12 characters, 60 bits);
- `GOTRUE_SESSIONS_TIMEBOX` unset: no absolute session lifetime;
- `GOTRUE_SESSIONS_INACTIVITY_TIMEOUT` unset: no idle timeout;
- `GOTRUE_JWT_EXP` 3600 s (the image value).

A session therefore lives until it is revoked (a successful sign-out, or a staff cut-off;
see Revocation) or a stale token revokes its family. There is no timebox; the user accepted
this (AUTH-00 Decision Log Q30). Re-read the values (`P` and `E` as in "Opening registration in
production") with
`railway variables -p "$P" -e "$E" -s auth --json | jq 'with_entries(select(.key|test("GOTRUE_(JWT_EXP|SECURITY|SESSIONS)")))'`;
`{}` means no override.

**Ceilings:**
- `ceiling:` `POST /auth/refresh` is not throttled, and GoTrue applies no limit in this fleet
  (Registration, Ceilings). A v1 refresh token holds 60 bits. Revisit with the per-client-IP
  limit sign-in defers.
- `ceiling:` a copy of the refresh token taken before a sign-out whose server call failed
  keeps renewing until the account is signed out again or cut off (see Revocation). After a
  successful sign-out the copy answers 401.
- `ceiling:` during a gateway outage every request tries one refresh first while a renewal
  is due, which doubles the failed calls. Revisit if outage logs show it.
- `ceiling:` a device clock changed by hand between receipt and use shifts the renewal time.
  A clock moved backwards renews late, and the gateway's 401 on the expired token ends the
  session (`endRevokedSession`, see Revocation).
- `ceiling:` an offline user past the deadline is signed out on the next request; the front
  door's navigation then fails offline.
- `ceiling:` the load-to-save of the stored record is not atomic across tabs (`localStorage`
  has no compare-and-set). A sign-out in another tab inside that one synchronous step is not
  seen. A record resurrected this way holds revoked credentials after a successful
  sign-out: the edge refuses its access token and `/auth/refresh` refuses its refresh token
  (see Revocation). Revisit if a sign-out whose server call failed is reported to leave a
  usable record.
- `ceiling:` a request from a chain that outlives an ended session is refused only while no
  session is tracked.
- `ceiling:` a byte download (evidence bundle, page image, source document) that finds the session ended sends nothing, and its error card can show until the front door's navigation unloads the page; revisit if a user reports it.
- `ceiling:` on a transient failure the renewer keeps the tracked session's token and deadline even when another tab's newer stored pair supplied the refresh token, so repeated transient failures can end a session while storage holds a newer valid pair (the next boot adopts it); revisit if two-tab users report early sign-outs.

## Revocation

Signing out ends every session of the account, on every device and tab, server-side. The
app, or a console, sends its refresh token to `POST /auth/sign-out`; the gateway turns it into GoTrue's
global logout, which deletes every row of the account in `auth.sessions` (its refresh tokens
cascade). The edge then refuses every access token of those sessions, because the gateway
asks GoTrue whether a token's session is still live before any `/api/` request reaches a
service. Nothing depends on another tab or origin being open: each learns at its next
request. Staff can do the same to an account without its holder (see Cutting an account
off).

**`POST /auth/sign-out`** `{"refresh_token"}`, outside `/api/`, no verifier (an idle tab's
access token has often expired; the refresh token is the credential the app always holds),
wrapped in CORS, POST and OPTIONS, in every build. The body limit is the `/auth/refresh`
one: the decoder reads the first JSON value, at most 1 KiB, and ignores any trailing bytes.
The gateway posts `{"refresh_token"}` to GoTrue `/token?grant_type=refresh_token`, then
`POST /logout?scope=global` with the minted access token as the bearer. On success it evicts
every cached session-check entry whose subject is that token's `sub` (read from GoTrue's own
answer, unverified), then answers.

| Outcome | Answer |
|---|---|
| refresh grant 200 with a non-empty `access_token`, then logout any 2xx | 204, no body; every cached entry for the subject evicted before the answer |
| logout 401/403 whose `error_code` is `session_not_found`, `user_not_found`, `user_banned` or `session_expired` (the session vanished between the two calls) | the same 204; evicted |
| refresh grant 200 whose `access_token` is not a JWT or has no `sub` | the logout still runs with it and the answer follows the logout step; nothing is evicted, one WARN is logged |
| a malformed body, or a first JSON value over 1 KiB | 400 `invalid request body`; GoTrue is not called |
| an empty or missing `refresh_token` | 400 `refresh_token is required`; GoTrue is not called |
| refresh grant 429, or logout 429 | 429 `too many requests`; nothing evicted |
| refresh grant any other 4xx (the session is already gone, or the token is stale) | 401 `invalid or expired refresh token`; logout is not called |
| logout any other 401/403 (for example `bad_jwt`), any other 4xx, or any 3xx | 502 `sign-out is unavailable`; nothing evicted |
| refresh grant 200 without a non-empty `access_token`, any other 2xx, any 3xx; any 5xx or GoTrue unreachable at either step | 502 `sign-out is unavailable`, logged at WARN with the step and the upstream status or the error only; nothing evicted |
| an OPTIONS without an `Origin` | 405 `method not allowed`, `Allow: POST` |
| any other method but POST | 405 from the router |

- A logout 401/403 counts as success only with one of those four codes, because only then
  is the session gone. Any other refusal revoked nothing.
- Once the refresh grant succeeds it has rotated the refresh token, so the logout step runs
  on a context the client cannot cancel: an app that gives up at its 5 s timeout does not
  stop a logout already under way. `ceiling:` that app then reports a failed sign-out even
  though the logout may still complete.
- Every answer the handler writes sets `Cache-Control: no-store`. The gateway never logs
  either token or the subject. A preflight is answered by CORS.
- Anyone holding a session's refresh token can sign the whole account out; they could
  already use the session. `ceiling:` the route is not throttled, like `/auth/refresh`.
- Guarded by `internal/gateway/signout_test.go` (`TestSignOut_*`; the mapping by
  `TestSignOut_GoTrueMapping`), the CI `idp` job's `TestIdP_SignOutEndsEverySession` and
  `TestIdP_SignOutWithTheParentRefreshToken`, and `e2e/api/session-handoff.spec.ts`
  "sign-out revokes every session of the account".

**The edge check** (`internal/gateway/session_check.go` `SessionChecker`) runs on `/api/`
after the verifier and before the router. For a token with a `session_id` claim it calls
GoTrue `GET /user` with the caller's own `Authorization` header and reads only `error_code`
from the first 1 KiB of the answer:
- **200** → live; the request proceeds.
- **401/403 with `session_not_found`, `user_not_found`, `user_banned` or
  `session_expired`** → revoked: 401 `{"error":"unauthorized"}` with
  `WWW-Authenticate: Bearer`, the verifier's own refusal bytes. No service is reached.
- **Anything else** → 503 `{"error":"session check unavailable"}`: `bad_jwt` and every other
  4xx code, 404, any 3xx (the checker never follows a redirect), a 2xx other than 200, 429,
  5xx, a transport error, and the 5 s timeout (`SessionCheckTimeout`). `bad_jwt` is not
  treated as revoked: the verifier already accepted the token, so GoTrue refusing to parse
  it means clock skew at `exp` or a misconfiguration, and a 401 would sign a live user out.
  A mis-set `AUTH_URL` path (404) is likewise a 503, never a mass sign-out.

The cache:
- Keyed by `session_id`, 30 s (`SessionCheckTTL`). Both verdicts are cached: a deleted
  session never comes back. A 503 is never cached.
- Concurrent misses for one session share one GoTrue call.
- At most 100,000 entries (`SessionCheckMaxEntries`). When full, expired entries are swept;
  when still full, the check runs without caching.
- A sign-out through the gateway evicts every entry of the subject and flags that subject's
  in-flight calls as evicted. A flagged call still answers the requests already waiting on
  it, but caches nothing, so a `/user` call that began before the sign-out cannot cache
  "live" after it.

**GoTrue unreachable: fail closed** (user decision, AUTH-07 CF4). A live cached session keeps
working until its entry is 30 s old. After that, and at once for every uncached session,
`/api/` answers 503 until GoTrue answers again. The app's 401 path does not fire on a 503,
so an outage signs nobody out. An outage of GoTrue therefore stops sign-in, renewal and
sign-out at once, and the API after 30 s.

**Tokens without `session_id` skip the check.** Mock-issuer tokens carry none, so
a picker session never calls GoTrue. Production has no mock issuer. A
GoTrue-signed token without `session_id` can be minted only with GoTrue's private key, which
can forge any claim. `TestIdP_AccessTokenCarriesSessionID` fails if a GoTrue upgrade drops
the claim. `ceiling:` the check keys on the claim, not the issuer; revisit if a second real
issuer is trusted.

Guarded by `internal/gateway/session_check_test.go` (`TestSessionCheck_*`; the refusal never
reaching a service by `TestSessionCheck_RevokedNeverReachesUpstream`, the stale window by
`TestSessionCheck_TTLBoundary`, the evicted flag by
`TestSessionCheck_EvictionBeatsAnInFlightCheck`), and the CI `idp` job's
`TestIdP_RevokedTokenNeverReachesAService`.

**The cost of the check.** A miss costs one GoTrue `GET /user`; a hit is a map read. A
session costs at most one GoTrue call per 30 s.
- Design time (local GoTrue container, 2026-09-29): `GET /user` sequential p50 20.5 ms,
  p95 25.6 ms (n=1000). Ten concurrent callers saturate GoTrue near 233 requests per second
  (pool size 10). An uncached check would add ~20 ms to every `/api/` request and cap the
  API at GoTrue's throughput; that is why the cache exists.
- CI, `TestIdP_SessionCheckCost` (the `idp` job): it drives the real `SessionChecker`
  against the `idp-es256` container, 200 misses and 10,000 hits, and asserts verdicts and
  GoTrue call counts, never latency. It prints p50/p95/max in the test log and appends them
  to the `idp` job's step summary (`$GITHUB_STEP_SUMMARY`), because the CI gate shows test
  output only on failure. A local run measured misses p50 ~21.5 ms, p95 ~25 ms, and hits
  ~83 ns. CI run `36554514886`: misses (n=200) p50 22.99 ms, p95 24.47 ms, max 28.57 ms;
  hits (n=10,000) p50 183 ns, p95 414 ns, max 57.6 µs.
- These figures exclude ES256 verification (the test hands the checker an already verified
  identity) and Railway's private-network hop.
- Deployed figures: `e2e/api/session-handoff.spec.ts` "the session check's deployed cost"
  times, for 10 fresh sessions, the first `/api/` call after sign-in (a miss) and the second
  (a hit), and attaches the medians. Deploy gate run `36619314593`, Railway PR env `pr-281`,
  2026-09-29, one gateway replica: client-measured `/me` through the gateway (n=10 pairs),
  misses median 173.2 ms (max 185.2 ms), hits median 117.0 ms (max 124.4 ms); a miss costs
  ~56 ms over a hit. GoTrue `GET /user` duration from the auth service logs during that test
  (n=10): median 51.5 ms, max 55.6 ms. A hit makes no GoTrue call: 20 `/me` calls produced
  exactly 10 `/user` calls. The deployed miss cost is ~2x the CI miss p50 (23 ms), and nearly
  all of it is the GoTrue round trip.

**The worst-case stale window** (how long a revoked session's access token still passes the
edge):
- **Sign-out through the gateway: 0 s** on the gateway that answered, for every request whose
  check starts after the eviction. The eviction runs before the 204, and the evicted flag
  stops an older in-flight `/user` answer from caching "live". A request already past the
  check, or already waiting on an in-flight check, when the eviction runs still completes.
- **A revocation the gateway did not make** (a staff cut-off, a session GoTrue removes
  itself, a sign-out answered 204 whose minted token named no subject): **30 s**
  (`SessionCheckTTL`).
- A revoked session's refresh token is refused at once in every case: GoTrue answers 400
  `refresh_token_not_found`, which `/auth/refresh` answers 401.
- `ceiling:` a second gateway replica does not see another replica's eviction, so its window
  is 30 s too. The gateway runs one replica; the hand-off store has the same ceiling (Sign-in
  and hand-off, Ceilings).
- `ceiling:` GoTrue's reuse detection revokes a refresh-token family but leaves the session
  row, so that session's access tokens pass the check until their `exp` (at most 3600 s).
  Revisit if reuse detection is ever the only revocation a stolen token meets.

**The app's rule** (`frontend/app/src/lib/revoke.ts`, `frontend/app/src/App.tsx` `signOut`
and `endRevokedSession`):
- **Revoke, then clear.** Both Sign out buttons (the Sidebar's and the suspended notice's)
  call `signOut`. For a hand-off seat with a refresh token it first awaits
  `revokeSessions`, which posts `{"refresh_token"}` to `/auth/sign-out` with a 5 s timeout
  (`REVOKE_TIMEOUT_MS`) and never rejects. It sends the stored record's refresh token when
  the record belongs to the seat's user (another tab may have rotated it), else the seat's
  own. Only then does it clear state, the stored record and the destination, and navigate to
  landing: the navigation would cancel the request. The worst wait is 5 s.
- A picker seat sends nothing: it has no server session.
- **A failed revoke is silent** (user decision, AUTH-07 CF5). Any failure (gateway
  unreachable, the timeout, any non-2xx answer) still completes the local sign-out and logs
  `console.warn('[session] sign-out could not reach the server; other sessions stay signed in')`.
  `ceiling:` the user is not told; other devices stay signed in until their holder signs out
  or staff cut the account off. Revisit if a user reports a session that outlived sign-out.
- **One sign-out at a time.** A ref (`signingOut`) is set for the whole sign-out. While it is
  set, a second click does nothing and `endRevokedSession` does nothing: the eviction runs
  before the 204, so a poll from the signing-out tab can meet a 401 while the revoke is in
  flight, and that 401 must not start a second navigation. The ref stays set once sign-out
  navigates to landing. A build with no landing URL (the showcase build) does not navigate
  and shows the in-app picker, so there the ref is cleared at the end of sign-out, and the
  next sign-in can sign out again.
- **A 401 ends a revoked session without revoking** (`endRevokedSession`, the
  `onUnauthorized` callback of every authed fetch and of the import client). It sends
  nothing (the session is already dead), clears the destination, resets the URL to `/` and
  navigates to landing, as sign-out does. It keeps the stored record only when both the
  stored token and the ended session's token carry a readable `session_id` and the two
  differ: the record then belongs to a newer sign-in in another tab, and it stays
  byte-identical. In every other case (the same `session_id`, a missing or non-JWT token, a
  JWT with no `session_id` such as a mock-issuer token) it clears the record. After revocation a
  401 is the normal way another tab learns its session ended; without this rule that tab's
  401 would wipe the record the first tab just wrote on signing in again.
- **Where the tab ends up.** Both handlers navigate to landing. For a hand-off seat the front
  door then redirects once more to `<landing>/?state=<43 characters>` and stores no
  destination.
- **Another tab that learns through a renewal** (user decision, AUTH-07 CF6). After tab 1
  signs out, tab 2 ends at whichever comes first: its next request (edge 401 →
  `endRevokedSession`, destination cleared) or its next due renewal, which finds the stored
  record absent and ends as a refused renewal (Renewal, Two tabs). That path keeps tab 2's
  destination and restores it after the next sign-in in tab 2, per AUTH-06's refused-renewal
  rule. The destination is per tab and is only a path. A tab that sends no request keeps
  showing its screen until it does.
- Guarded by `frontend/app/src/App.signOut.test.tsx`,
  `frontend/app/src/App.signedOutDeepLink.test.tsx` (`signOut_clearsAStoredDestination`,
  `signOut_capturesNothingOnTheWayOut`), and the deployed `e2e/topology/auth.spec.ts`
  "deployed app: signing out on one device ends the session on every device".

**A fabricated or copied storage entry.** What revocation closes, and what it does not:
- **Closed:** a stored record carrying a real token after that session is revoked, whether a
  copy taken before sign-out or one written back by hand. Its access token is refused at the
  edge (401 → `endRevokedSession` → landing, record cleared), and its refresh token cannot
  mint (`/auth/refresh` 401). The deployed oracle is the same topology journey: the
  pre-sign-out record replayed in a fresh browser context mounts, 401s and returns to
  landing with no record.
- **Unchanged:** a record with a fabricated token still mounts the app shell from storage
  before its first request, then 401s and ends. The shell shows only what the record itself
  holds. Gating the mount on a network check would add a round trip to every boot.
- **Consoles:** a console renews its stored record at every load, so a fabricated record or a
  forged token is refused by the renewal and the record is cleared, and a session revoked
  anywhere ends at the console's next load (Console sessions). A `v:1` record opens nothing.

## Cutting an account off

Staff end every session of an account without its holder signing out. "Staff" is the
operator with production database access; an in-product staff tool is out of scope (user
decision, AUTH-07 CF2). Production Postgres is private-only, so the statement runs inside
the Postgres container:

1. `railway ssh --service Postgres`
2. `psql`
3. Run, replacing `<address>` and then `<id>`:
   ```sql
   SET ROLE supabase_auth_admin;
   SELECT id FROM auth.users WHERE email = lower('<address>');
   DELETE FROM auth.sessions WHERE user_id = '<id>';
   ```
   `SET ROLE` runs the statement as GoTrue's own role, the one GoTrue's global logout runs
   as. The `DELETE` reports how many sessions it removed.

**Effect and timing.**
- Every refresh token of the account is refused at once: its rows cascade with the
  sessions, and `/auth/refresh` answers 401.
- Every access token of the account is refused at the edge within 30 s
  (`SessionCheckTTL`): at once for a session the gateway has not cached, and when the cache
  entry ages out for one it has. The app then ends the session through its 401 path.
- The gateway is not told, so no cache entry is evicted. That is the 30 s stale window in
  Revocation.

**It does not ban** (user decision, AUTH-07 CF3). The person can sign in again with their
password, and gets a new session. Keeping someone out is membership suspension, a different
thing. Stated plainly: a compromised account whose password the attacker knows cannot be
kept out by any path here when it is its workspace's sole active admin, because suspension
refuses the last active admin (`internal/tenancy/store.go` `ErrLastActiveAdmin`). The
cut-off stops a stolen token or refresh token, not a stolen password. Neither sign-out nor
the cut-off can lock a workspace's only admin out, so the last-active-admin guard stays the
only lock-out path.

**Pinned by** the CI `idp` job's `TestIdP_StaffCutOffEndsEverySession`, which runs the
`SET ROLE` and the `DELETE` against the pinned GoTrue's schema and asserts: an uncached
access token refused at once, a cached one live until 30 s and refused at 30 s, both
refresh tokens refused, and a new sign-in live. `ceiling:` the statement depends on GoTrue's
schema at the pinned tag; an upgrade that changes `auth.sessions` fails that test.

## Staff role

A staff account is an `auth.users` row with a row in `public.staff_members`. The access-token
hook (`public.custom_access_token_hook`, migration `staff_members`) reads that table and puts
`app_metadata.staff: true` into the token. GoTrue runs the hook on every sign-in and every
refresh, so a grant or a removal reaches the account's next token with no other change. The
consoles read this claim and nothing else (Console sessions). It is not a Postgres role: the
verifier binds the top-level `role` claim, and `app_metadata` is the namespace the hook owns.

- `public.staff_members (user_id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT
  now())`. RLS is enabled without `FORCE`: the table has no tenant, and its only writers are
  its owner (the migrator) and a superuser. It has no foreign key to `auth.users`, because
  GoTrue creates that table at its own boot, outside `goose`. A row for a deleted account
  grants nothing, since no token can be minted for it.
- `auth_hook_reader` holds `SELECT (user_id)` and the policy `staff_hook_lookup`. No other
  role holds a grant: `invoice_app`, `invoice_tenant_reader` and `supabase_auth_admin` are
  refused. Registration and `provision_workspace` write no row. No user-supplied field reaches
  the hook's staff decision: `user_metadata` is user-writable and is not read.
- The table is not in `resetTables` and not in the demo purge, so a grant on a PR fork
  survives a redeploy of that fork.

**Hook contract.** The staff step runs after the unchanged tenant step. A non-staff token gains
no key, so `TestIdP_TokenShapeMatchesGolden` stays valid.

| `staff_members` has `user_id` | incoming `app_metadata` | outgoing `app_metadata` |
|---|---|---|
| yes | an object | incoming keys (tenant step applied) plus `"staff": true` |
| yes | absent | `{"staff": true}`, plus `tenant_id` when the tenant step projects one |
| yes | not an object (`null`, a string) | `{"staff": true}` |
| no | has `staff` (any JSON value) | `staff` removed, other keys kept |
| no | no `staff` | unchanged (no key added) |
| no | not an object | unchanged |
| `user_id` not a UUID | | the hook call fails (SQLSTATE 22P02) and GoTrue refuses the token (`TestRLS_CustomAccessTokenHookMalformedUserID`) |
| `user_id` `null` or absent | has `staff` | no row matches: `staff` removed, other keys kept (`TestRLS_CustomAccessTokenHookStripsStaffForANullOrAbsentUserID`) |

`ceiling:` a staff user whose incoming `app_metadata` is not an object loses a projected
tenant. GoTrue always sends an object. A leaked `supabase_auth_admin` DSN can call the hook and
so also learns the staff bit (migrations.md, `auth_hook_reader`).

Guarded by the `rls` job's `staff_members_rls_test.go` (`TestRLS_StaffMembers*`,
`TestRLS_CustomAccessTokenHook*Staff*`, `TestRLS_ProvisionWorkspaceGrantsNoStaff`) and the
`idp` job's `TestIdP_RegisteredAccountCarriesNoStaffClaim`,
`TestIdP_StaffGrantReachesTheNextToken` and `TestIdP_StaffClaimSurvivesRenewalPastTheTTL`
(`idp-short`, `GOTRUE_JWT_EXP=5`).

## Granting staff

Registration creates the account; one statement grants the role. "Staff" here is the operator
with production database access, as in "Cutting an account off". Production Postgres is
private-only, so run it inside the Postgres container:

1. `railway ssh --service Postgres`
2. `psql` (a superuser)
3. Run, replacing `<address>`:
   ```sql
   INSERT INTO public.staff_members (user_id) SELECT id FROM auth.users WHERE email = lower('<address>');
   ```
   It must answer `INSERT 0 1`. `INSERT 0 0` means no account has that address.

To remove the role:

```sql
DELETE FROM public.staff_members WHERE user_id = (SELECT id FROM auth.users WHERE email = lower('<address>'));
```

A removal reaches the next token only. A console that is already open keeps rendering until it
reloads (Console sessions). When the person must leave at once, also run the cut-off in
"Cutting an account off".

**Pinned by** `TestIdP_StaffGrantReachesTheNextToken`, `TestIdP_StaffRemovalReachesTheNextToken`
and `TestIdP_StaffGrantStatementMatchesTheAddressInAnyCase` (`internal/platform/auth/idp_staff_integration_test.go`).
They run the two statements above verbatim, as a superuser, against the real `supabase/auth`
containers. The grant and the removal each reach the account's next refresh and next sign-in
and leave other accounts alone. The grant statement matches an address typed in any case and
answers `INSERT 0 0` for an unregistered one.

An in-product staff screen is out of scope. On production no account can be created while
signup is closed, so the first staff account waits for registration U3 or for console U3.

## The mock issuer

A gateway built with `-tags mockissuer` (`cmd/gateway/mockissuer.go`) can serve the mock issuer:
`POST /auth/login`, the mock JWKS, `POST /auth/mock/staff` and `POST /auth/mock/member`. The
production binary is built without the tag, so it carries none of that code and
`cmd/gateway/nomockissuer.go` registers nothing. In a tagged build `gateway.MockIssuerEnabled`
serves the routes only when `GATEWAY_MOCK_ISSUER=true` and `ENVIRONMENT` is not `production`
(trimmed, any case). The build tag and `MockIssuerEnabled` are the whole control. `MockLoginHandler`
keeps no identity allowlist and no posture check: wherever it is wired it mints for any identity,
an empty body included. The in-app sign-in picker of a build with no landing URL calls it. The
deployed apps carry no in-app identity switch.

## Granting a membership in a mock build

A PR fork's mock gateway serves `POST /auth/mock/member`
`{"user_id","tenant_id","role","display_name","email"}`. It upserts an active `memberships`
row with the migrator DSN, so an e2e spec can admit a registered account to a tenant. It
answers 204, 400, 405 or 502. Production has no such route: `TestProductionGatewayBinaryCannotMint`
requires `MockMemberHandler` and `db.GrantMembership` absent from that binary. The control is
the `mockissuer` build tag, not an environment variable.

## Console sessions

Both consoles (`ops-console`, `support-console`) render one shared gate, `StaffGate` from
`@invoice-os/console-session`. Each console passes its own storage key and target (`ops` or
`support`). A console opens only for a session the gateway has just renewed or exchanged and
whose token carries `app_metadata.staff === true`.

**Honest limit.** The consoles render mock data compiled into a public bundle, and their
only gateway calls are the session's exchange, renewal and sign-out. The gate makes the door
right, and nothing server-side enforces staff access to console data, because there is none. A console that reads real data must check the staff claim
on the server. The same words head `packages/console-session/src/StaffGate.tsx`.

**The record.** `localStorage[<storageKey>] = {"v":2,"token":"<access>","refresh_token":"<refresh>"}`
under `invoice-os.ops-session` or `invoice-os.support-session`. A `v:1` record (`{v:1, operator}`)
reads as no record and logs one `console.warn`; it stays in storage and the next staff sign-in
overwrites it. The sign-in state sits in `sessionStorage['invoice-os.signInState']` on the
console's own origin, in the app's shape.

**Boot order.** The gate renders nothing until one of these steps ends it.
1. Standalone (`VITE_LANDING_URL` unset): render the console. No storage read, no request.
2. `?auth=start`: mint a fresh state and go to `<landing>/?state=<s>&console=<target>&signin=ready`.
3. `?handoff=<code>` (exactly one `handoff` parameter, 43 base64url characters): strip it at mount, then,
   with a live state in this tab, `POST /auth/exchange`. A staff token is stored and the console
   renders. A token without the claim is discarded unstored and the visitor goes to landing with
   `signin=not-staff`. Any failure goes to landing with `signin=failed` and leaves a stored
   record alone. With no live state the code is ignored and the gate continues at step 4.
4. A stored `v:2` record: `POST /auth/refresh` with its refresh token, 15 s timeout. A 200 with
   the staff claim stores the new pair and renders. A 200 without it clears the record and goes
   to landing with `signin=not-staff`. A 400 or 401 clears the record and goes to the front
   door. A network error, timeout, 429, 5xx or malformed 200 keeps the record and goes to
   landing with `signin=failed`.
5. Nothing stored: the front door, `<landing>/?state=<s>&console=<target>`. The state is reused
   when minted under 60 s ago.

With `VITE_LANDING_URL` set and `VITE_GATEWAY_URL` unset, steps 3 and 4 cannot run: a code
leaves with `signin=failed`, otherwise the gate leaves by the front door. A console with no gateway URL
therefore opens for nobody.

**What the renewal proves.** A load asks the gateway, not the browser, whether the session is
live: the refresh token exists in GoTrue, the account exists, its sessions are not revoked, and
the staff claim is minted now from the current `staff_members` row. The console decodes the
claim, unverified, from the token the gateway has just answered over TLS. A forged claim needs a
forged gateway answer. The cost is one GoTrue refresh grant per console load, and a stored
record whose access token expired hours ago loads normally.

**Revocation.** A sign-out anywhere and a staff cut-off delete the session's refresh token, so
the console's next load gets 401, clears its record and goes to the front door. A staff role
removal reaches the next load the same way: the refresh answers 200 without the claim, and the
visitor goes to landing with `signin=not-staff`. `/auth/refresh` is not cached, so the stale
window is 0 s from the next load. An open console tab keeps rendering until it reloads; what it
renders is mock data in the public bundle.

**Sign out.** The Sign out button posts the stored refresh token to `/auth/sign-out` (5 s,
never rejects; a failure logs `console.warn`), clears the record and goes to landing. It ends
every session of the account, so it also signs the account out of the app and the other
console. With no gateway the sign-out is local only.

**Ceilings.**
- `ceiling:` no timer renews an open console tab. When a console gains a backend, its requests
  must ask for a fresh token first, as the app's `fresh()` does.
- `ceiling:` no lock serialises two loads of one console. GoTrue answers the parent of the
  active refresh token with the active token, and only a token two or more generations old
  revokes the family (`TestIdP_RefreshRotationAndReuse`), which two overlapping loads do not
  reach.
- `ceiling:` a transient boot failure sends a still-valid staff user to sign in again; the
  next load after recovery succeeds with the kept record.
- `ceiling:` a customer's refused sign-in leaves its GoTrue session live in `auth.sessions`,
  reachable by nobody, until the account's next global sign-out or a cut-off.
- `ceiling:` a console's transient failure reuses landing's `failed` copy, "We couldn't open
  your workspace. Sign in again."

**The standalone build keeps no gate (VITE_LANDING_URL unset).** Today that costs nothing,
because both consoles are mock data in a public bundle, every deployed console sets
`VITE_LANDING_URL`, and a fork that lost it fails `smoke.spec.ts` "a visit with no session
redirects to the landing page". **The first console that reads real data must make the gate
fail closed when `VITE_LANDING_URL` is unset**, and must check the staff claim on the server.

**CORS.** The gateway's one origin list wraps `/api/`, `/auth/sign-in`, `/auth/exchange`,
`/auth/refresh`, `/auth/sign-out`, `/auth/register` and `/contacts/demo-request`, so console U2 lets browser JavaScript on the two console
origins call all of them, not only exchange, refresh and sign-out. `/auth/verify` is not wrapped. Every `/api/` call still needs a verified bearer, the session check and RLS; the
console origins serve only our own bundle, and the same token works from `curl`.

Guarded by the package's `boot.test.ts`, `StaffGate.dom.test.tsx`, `signOut.test.ts`,
`state.test.ts` and `session.test.ts`, each console's `App.test.tsx`, landing's `signIn.test.ts`
and `App.signIn.dom.test.tsx`, and the deployed `e2e/topology/auth.spec.ts` "deployed consoles:"
journeys (a staff session opens both consoles, a customer's session opens neither, a forged
record opens nothing, a load renews the stored session, signing out of one console ends the
other's).

## Opening registration in production (registration U1–U4)

These steps are separate from the first-time setup's U1–U4 above. Production writes are the
user's. Every write skips deploys, so it changes nothing until the next deploy of that
service. Each write is followed by its re-read; the re-read must print the expected value.

The commands name project `9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3` and the production
environment `6c864094-6a06-452f-8495-be77d8a94fe7`:

```
P=9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3
E=6c864094-6a06-452f-8495-be77d8a94fe7
```

| Step | When | Production write |
|---|---|---|
| U1 | any time after merge | gateway `AUTH_SITE_URL` |
| U2 | any time after merge | auth `GOTRUE_MAILER_URLPATHS_CONFIRMATION` |
| U3 | when registration opens: after AUTH-04 and AUTH-16 merge | auth `GOTRUE_DISABLE_SIGNUP=false`, landing `VITE_REGISTRATION_OPEN=true` |
| U4 | after U1–U3 have deployed | none: an end-to-end check by hand; step 5 may raise `AUTH_REGISTER_MIN_RESPONSE` |

Until U1 deploys, production's `POST /auth/register`, `GET /auth/verify` and `POST /auth/verify` answer 503
`registration is not configured`. Between U1 and U3, register answers 503
`registration is closed`. Neither affects any other route. From U1 on, a free-mail address
answers 400 with the policy message, also while signup is closed.

**U1 — gateway `AUTH_SITE_URL`:**

```
railway variables --set 'AUTH_SITE_URL=https://www.ascomply.com' -p "$P" -e "$E" -s gateway --skip-deploys
railway variables -p "$P" -e "$E" -s gateway --json | jq -r '.AUTH_SITE_URL'
# expected: https://www.ascomply.com
```

**U2 — auth `GOTRUE_MAILER_URLPATHS_CONFIRMATION`:**

```
railway variables --set 'GOTRUE_MAILER_URLPATHS_CONFIRMATION=https://api.ascomply.com/auth/verify' -p "$P" -e "$E" -s auth --skip-deploys
railway variables -p "$P" -e "$E" -s auth --json | jq -r '.GOTRUE_MAILER_URLPATHS_CONFIRMATION'
# expected: https://api.ascomply.com/auth/verify
```

**U3 — auth `GOTRUE_DISABLE_SIGNUP=false`. Do this only after AUTH-04 and AUTH-16 merge.** The
AUTH-00 decision S6 makes registration open with free-mail domains refused. AUTH-04 ships
that refusal; opening production before it admits free-mail registrants. AUTH-16 closes the
registration timing leak while GoTrue answers faster than `AUTH_REGISTER_MIN_RESPONSE` (see
Registration Ceilings and U4 step 5).

```
railway variables --set 'GOTRUE_DISABLE_SIGNUP=false' -p "$P" -e "$E" -s auth --skip-deploys
railway variables -p "$P" -e "$E" -s auth --json | jq -r '.GOTRUE_DISABLE_SIGNUP'
# expected: false
```

Show the entry on the landing. `VITE_REGISTRATION_OPEN` is a build argument, so the landing
needs a deploy to take it:

```
railway variables --set 'VITE_REGISTRATION_OPEN=true' -p "$P" -e "$E" -s landing --skip-deploys
railway variables -p "$P" -e "$E" -s landing --json | jq -r '.VITE_REGISTRATION_OPEN'
# expected: true
```

To close registration again, set `GOTRUE_DISABLE_SIGNUP` back to `true` the same way and
redeploy `auth`, and unset `VITE_REGISTRATION_OPEN` on `landing` and redeploy it.

**Deploy the writes.** The next push run on `main` deploys `gateway`, `auth` and `landing` with the new
values; a push that changes only `docs/**` or `*.md` starts no run (`paths-ignore`). To
deploy sooner, re-run the latest push `dev-env` run as a whole run
(`gh run rerun <id>`, not `--failed`); if that re-run gates on stale containers, push an
empty commit instead.

**U4 — check by hand with a real business mailbox:**
1. `curl -sS -w '\n%{time_total}\n' -X POST https://api.ascomply.com/auth/register -H 'Content-Type: application/json' -d '{"email":"<you>@<your-company-domain>","password":"<12+ characters>"}'`
   answers 202 `{"status":"verification_pending"}`, after at least the minimum (`2s` by default).
2. The mail arrives from `no-reply@ascomply.com`. Its link starts
   `https://api.ascomply.com/auth/verify?token=`.
3. Opening the link shows the confirm page. Clicking "Confirm my email" lands on
   `https://www.ascomply.com/?verified=1`. Opening the link again and clicking lands on
   `?verify=failed`.
4. `curl -sS -X POST https://api.ascomply.com/auth/register -H 'Content-Type: application/json' -d '{"email":"someone@gmail.com","password":"<12+ characters>"}'`
   answers 400 `{"error":"a business email address is required; personal email providers are not accepted"}`.
5. Time a real signup. A client-side `curl` time cannot separate GoTrue's time from the minimum, so read the gateway's
   `registration: signup timing` line for the step 1 request (Railway logs, service
   `gateway`): `upstream_ms` is how long GoTrue took, `min_ms` the minimum in force. When
   `upstream_ms` exceeds about three quarters of `min_ms`, or the line is WARN, raise the
   minimum, re-read it and deploy the gateway:

   ```
   railway variables --set 'AUTH_REGISTER_MIN_RESPONSE=<n>s' -p "$P" -e "$E" -s gateway --skip-deploys
   railway variables -p "$P" -e "$E" -s gateway --json | jq -r '.AUTH_REGISTER_MIN_RESPONSE'
   # expected: <n>s
   ```

   Deploy as in "Deploy the writes" above. Do not set a value below `2s`: the end-to-end
   assertion `REGISTER_MIN_MS = 2000` in `e2e/api/registration.spec.ts` fails on every PR
   fork, because a fork inherits production's value.

To check provisioning, sign in with the U4 account and redeem the code (the `curl` pair in
sign-in U3 below, with the real password), then post
`{"workspace_name":"<name>","display_name":"<you>"}` to `POST /api/tenancy/v1/workspaces`
with `Authorization: Bearer <access_token>`: it answers 201. The U4 account and its workspace
stay in production; no route deletes them.

## Branding the account mails in production (mail U1–U2)

Production writes are the user's. Each write skips deploys, so it changes nothing until
`auth` deploys, and is followed by a re-read that must print the expected value. `P` and `E`
are the project and production environment ids named under "Opening registration in
production". Until U1, GoTrue sends its default mail.

| Step | When | Production write |
|---|---|---|
| U1 | after the epic reaches `main` and the push run has deployed the gateway | auth `GOTRUE_MAILER_TEMPLATES_CONFIRMATION`, `GOTRUE_MAILER_SUBJECTS_CONFIRMATION` |
| U2 | after U1 | none: deploy `auth`, read the gate |

**U1 — check the template, then set both variables on `auth`:**

```
go run ./tools/prenv mail-template-check https://api.ascomply.com/emails/confirmation.html
# expected: ok https://api.ascomply.com/emails/confirmation.html
railway variables --set 'GOTRUE_MAILER_TEMPLATES_CONFIRMATION=https://api.ascomply.com/emails/confirmation.html' -p "$P" -e "$E" -s auth --skip-deploys
railway variables -p "$P" -e "$E" -s auth --json | jq -r '.GOTRUE_MAILER_TEMPLATES_CONFIRMATION'
# expected: https://api.ascomply.com/emails/confirmation.html
railway variables --set 'GOTRUE_MAILER_SUBJECTS_CONFIRMATION=Confirm your ASComply account' -p "$P" -e "$E" -s auth --skip-deploys
railway variables -p "$P" -e "$E" -s auth --json | jq -r '.GOTRUE_MAILER_SUBJECTS_CONFIRMATION'
# expected: Confirm your ASComply account
```

Do not run the `mail-template-check` line before the push run has deployed the gateway:
production answers 404 for the route until then.

**U2 — deploy `auth`.** Deploy as in "Deploy the writes" under "Opening registration in
production". Then read the push run's `fleet-gate` step "Gate on the account-mail templates":

```
# expected: ok https://api.ascomply.com/emails/confirmation.html
```

The same job's push-only step "Gate on the account-mail logo" loads
`https://api.ascomply.com/emails/mark.png` and expects 200 `image/*`.

To go back to GoTrue's default mail, unset both variables and deploy `auth`.

## Sending invite mail in production (invite U1–U3)

Production writes are the user's, after the epic reaches `main`. `P` and `E` are the project
and production environment ids named under "Opening registration in production". Until U1,
`tenancy` has no key and boots in `capture` mode: an invite is stored and no mail is sent.

| Step | When | Production write |
|---|---|---|
| U1 | after the epic reaches `main` and the push run has deployed | `tenancy` `RESEND_SENDING_KEY` |
| U2 | straight after U1 | none: seal in the dashboard, run the audit |
| U3 | after U2's audit exits 0 | none: deploy `tenancy`, read its boot line |

**The key.** In the Resend dashboard create an API key with permission "Sending access",
restricted to the `ascomply.com` domain. It can send mail and nothing else.

**U1 — pause PR pushes, then write the key on `tenancy`.** Pause PR pushes from here until U2's
audit exits 0: until the seal, the key is plain, and a PR run in that window forks it. A fork
made inside the window still discards it, because its posture `preview` stays `capture`. Run U1 and U2 back to
back. Pipe the key through stdin so it never reaches argv or shell history:

```
printf '%s' "$KEY" | railway variable set RESEND_SENDING_KEY --stdin --skip-deploys -p "$P" -e "$E" -s tenancy
railway variables -p "$P" -e "$E" -s tenancy --json | jq -r '.RESEND_SENDING_KEY | length'
# expected: the key's length; print the length, never the value
```

**U2 — seal, then audit.** In the dashboard, on production's `tenancy` service, seal
`RESEND_SENDING_KEY`. Seal nothing else: a seal cannot be undone. Then run the audit by hand
(read-only):

```
bash scripts/ci/railway-env.sh audit-sealed-variables
# expected: Sealed-variable audit clean: <n> of <m> variables in the source environment are sealed, all allowlisted: ... RESEND_SENDING_KEY ....
```

It must exit 0 and name `RESEND_SENDING_KEY` among the sealed names it allowed. Resume PR pushes
now. A PR run created before the qualified allowlist reached `main` fails at "Audit sealed
variables"; push to the PR, or re-run from a fresh event, instead of `gh run rerun`.

**U3 — deploy `tenancy`.** Deploy as in "Deploy the writes" under "Opening registration in
production". Then read `tenancy`'s boot line:

```
msg="tenancy: invite mail mode" mode=real
```

`mode=capture` means the key was not read: check U1's write and the deploy. Invite one real
address to prove the send end to end.

## Opening sign-in in production (sign-in U1–U3)

These steps are separate from the two U1–U4 lists above. Production writes are the user's.
The sign-in routes answer from the first deploy after AUTH-05 merges. Until U1 and U2 deploy,
production landing renders no sign-in form.

Production's gateway allows the app origin alone, and production's landing has no
`VITE_GATEWAY_URL`. `reconcile-urls` writes both on a PR fork only and refuses the persistent
environment, so production is written by hand. The commands use the same `P` and `E` as
"Opening registration":

```
P=9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3
E=6c864094-6a06-452f-8495-be77d8a94fe7
```

| Step | When | Production write |
|---|---|---|
| U1 | after merge, before U2 | gateway `CORS_ALLOWED_ORIGINS` gains the landing origin |
| U2 | after U1 | landing `VITE_GATEWAY_URL` |
| U3 | after U1 and U2 have deployed | none: `curl` checks |

**U1 — gateway `CORS_ALLOWED_ORIGINS`.** Read the current value first. If it holds more than
`https://app.ascomply.com`, keep every origin it holds and append the landing one.

```
railway variables -p "$P" -e "$E" -s gateway --json | jq -r '.CORS_ALLOWED_ORIGINS'
# expected before: https://app.ascomply.com
railway variables --set 'CORS_ALLOWED_ORIGINS=https://app.ascomply.com,https://www.ascomply.com' -p "$P" -e "$E" -s gateway --skip-deploys
railway variables -p "$P" -e "$E" -s gateway --json | jq -r '.CORS_ALLOWED_ORIGINS'
# expected: https://app.ascomply.com,https://www.ascomply.com
```

**U2 — landing `VITE_GATEWAY_URL`.** A `VITE_*` value is a build argument, so it takes effect
only when landing is rebuilt.

```
railway variables --set 'VITE_GATEWAY_URL=https://api.ascomply.com' -p "$P" -e "$E" -s landing --skip-deploys
railway variables -p "$P" -e "$E" -s landing --json | jq -r '.VITE_GATEWAY_URL'
# expected: https://api.ascomply.com
```

**Deploy the writes** as in "Opening registration": the next push run on `main` that changes
code, a whole-run re-run of the latest push `dev-env` run, or an empty commit.

**U3 — check by hand:**
1. The landing preflight is granted:
   ```
   curl -si -X OPTIONS https://api.ascomply.com/auth/sign-in -H 'Origin: https://www.ascomply.com' -H 'Access-Control-Request-Method: POST'
   ```
   It answers 204 with `access-control-allow-origin: https://www.ascomply.com`.
2. A wrong password is refused. `S` is any 43 base64url characters:
   ```
   S=$(openssl rand 32 | base64 | tr '+/' '-_' | tr -d '=')
   curl -sS -X POST https://api.ascomply.com/auth/sign-in -H 'Content-Type: application/json' -d "{\"email\":\"nobody@<your-company-domain>\",\"password\":\"wrong-password\",\"state\":\"$S\"}"
   ```
   It answers 401 `{"error":"invalid email or password"}`.
3. An unknown code is refused:
   ```
   curl -sS -X POST https://api.ascomply.com/auth/exchange -H 'Content-Type: application/json' -d "{\"code\":\"x\",\"state\":\"$S\"}"
   ```
   It answers 400 `{"error":"invalid or expired code"}`.
4. On `https://www.ascomply.com`, "Platform login" shows the sign-in form:
   Work email, Password and "Sign in →".
5. An unknown refresh token is refused:
   ```
   curl -sS -X POST https://api.ascomply.com/auth/refresh -H 'Content-Type: application/json' -d '{"refresh_token":"aaaaaaaaaaaa"}'
   ```
   It answers 401 `{"error":"invalid or expired refresh token"}`.

With a verified account, step 2 with the real password answers 200 `{"code":"…"}`, and step 3
with that code and the same `S` answers 200 `{"access_token":"…","refresh_token":"…"}`.
Step 5 with that `refresh_token` answers 200 with a new pair. A full sign-in through the
browser also needs a provisioned workspace, and so registration U3 and U4 above.

## Opening the consoles in production (console U1–U3)

These steps are separate from the first-time setup's U1–U4, registration U1–U4 and sign-in
U1–U3 above. Production writes are the user's. They use the same `P` and `E` as "Opening
registration". Console U2 extends the value that sign-in U1 left in the gateway's
`CORS_ALLOWED_ORIGINS`.

**Until the first staff account is granted (console U3), production's consoles open for
nobody.** After the merge deploy, every visitor to either console is sent to landing's sign-in,
and no production account can carry the staff claim. A fabricated storage
entry does not open a console either. The user accepted this (AUTH-11, CF1): the consoles hold mock
data only.

| Step | When | Production write |
|---|---|---|
| U1 | before the merge push | `VITE_GATEWAY_URL` on `ops-console` and `support-console` |
| U2 | after merge | gateway `CORS_ALLOWED_ORIGINS` gains the two console origins |
| U3 | after U1 and U2 have deployed | the first staff account, then its grant |

**U1 — `VITE_GATEWAY_URL` on both consoles.** It is a build argument, so write it before the
merge push builds them. A variable write triggers an unstamped rebuild.

```
railway variables --set 'VITE_GATEWAY_URL=https://api.ascomply.com' -p "$P" -e "$E" -s ops-console --skip-deploys
railway variables --set 'VITE_GATEWAY_URL=https://api.ascomply.com' -p "$P" -e "$E" -s support-console --skip-deploys
railway variables -p "$P" -e "$E" -s ops-console --json | jq -r '.VITE_GATEWAY_URL'
railway variables -p "$P" -e "$E" -s support-console --json | jq -r '.VITE_GATEWAY_URL'
# expected, both: https://api.ascomply.com
```

Without it a deployed console has no gateway to ask and opens for nobody.

**U2 — gateway `CORS_ALLOWED_ORIGINS`.** The console origin's preflight is refused until this
write. `reconcile_url_variables` refuses the persistent environment, so this is by hand. Read
the value first and keep every origin it holds.

```
railway variables -p "$P" -e "$E" -s gateway --json | jq -r '.CORS_ALLOWED_ORIGINS'
# expected before: https://app.ascomply.com,https://www.ascomply.com
railway variables --set 'CORS_ALLOWED_ORIGINS=https://app.ascomply.com,https://www.ascomply.com,https://ops.ascomply.com,https://sup.ascomply.com' -p "$P" -e "$E" -s gateway --skip-deploys
railway variables -p "$P" -e "$E" -s gateway --json | jq -r '.CORS_ALLOWED_ORIGINS'
# expected: https://app.ascomply.com,https://www.ascomply.com,https://ops.ascomply.com,https://sup.ascomply.com
```

This also lets browser calls from the two console origins reach `/api/` and the sign-in,
exchange, refresh and sign-out routes (Console sessions, CORS).

**Deploy the writes** as in "Opening registration".

**U3 — the first staff account.** Register it once production signup is open (registration
U3). Or set `GOTRUE_DISABLE_SIGNUP=false` on the `auth` service as in registration U3, register
and verify the account, then set it back to `true` the same way. Registration needs
registration U1 and U2 deployed (Opening registration). Then run the grant statement in
"Granting staff". Until then production's consoles open for nobody.

Check by hand, for each of `https://ops.ascomply.com` and `https://sup.ascomply.com`: the visit
goes to landing's sign-in, and a sign-in with the staff account returns to that console, which
opens. A console holds its own session, so the second console asks for its own sign-in.

## Sealed secrets

**Which are sealed, and why.** On production's `auth` service: `GOTRUE_JWT_KEYS` (the signing
key), `GOTRUE_JWT_SECRET` and `GOTRUE_SMTP_PASS` (the Resend key). On production's `tenancy`
service: `RESEND_SENDING_KEY` (the sending-only invite key), sealed after invite U2.
- A sealed variable is not copied into a fork, so a PR environment never receives
  production's signing key, JWT secret or Resend keys. `set-fork-auth` writes the fork's own
  `auth` values; a fork's `tenancy` has no key and runs in `capture` mode.
- The account-scoped `RAILWAY_API_TOKEN` that PR workflows hold cannot read them back.
- `AUTH_ADMIN_PASSWORD` is not sealed (see Variables).

**A seal cannot be undone.** Railway has no unseal. A wrongly sealed variable can only be
deleted and re-created. Sealing is a dashboard action: the variable's 3-dot menu → Seal. No
public API mutation seals a variable.

**A sealed value cannot be read.** Not in the dashboard, not through the API, not through
`railway variables` or `railway run`. Its name and its `isSealed` flag stay readable. No
script and no test in this repo reads a sealed value.

**A sealed value can be edited in the dashboard** (3-dot menu → edit), but not through the
Raw Editor. Whether a variable write (`variableUpsert` or `variableCollectionUpsert`) over a sealed variable succeeds, fails or unseals it is
unmeasured, so no script writes one.

**The audit allows exactly these four.** `audit-sealed-variables` (run by
prepare-env on every PR, and by hand after U3b and invite U2) passes when the only sealed variables in the
source environment are `auth:GOTRUE_JWT_KEYS`, `auth:GOTRUE_JWT_SECRET`, `auth:GOTRUE_SMTP_PASS` and
`tenancy:RESEND_SENDING_KEY`, each on the service before the colon. Any other sealed name, one of the four
on another service, one of the four environment-scoped, or any sealed variable in a source
environment where `auth` or `tenancy` cannot be resolved fails every PR.

**Still exposed, stated plainly:**
- Between U3b's write and the seal, production's values are plain, and a PR run in that
  window forks them. That is why U3b seals immediately, with PR pushes paused.
- Anyone with dashboard access to production can edit, but not read, the sealed values.

## Signing-key rotation

GoTrue reads `GOTRUE_JWT_KEYS` as a JSON array of JWKs. Exactly one may carry `sign` in
`key_ops`; every key must carry `"alg":"ES256"`, or GoTrue falls back to HS256.
`go run ./tools/prenv jwk-es256` prints a one-element array holding a fresh private key with
`"key_ops":["sign","verify"]`.

The general order is: add the new key as `verify`-only, deploy, switch `sign` to it, and
remove the old key after `GOTRUE_JWT_EXP` (3600 s) plus the verifier's stale window (1 h
cache TTL plus 6 h stale grace), so 8 h after the switch at the earliest.

**Production (sealed form).** The old private key cannot be read back, so the old key can
only re-enter a composed value as its public half, which is what the served JWKS holds.
Every step is a dashboard edit of `GOTRUE_JWT_KEYS` with a newly composed value.

1. Read the served JWKS from inside the private network, for example from the `auth`
   container (`railway ssh --service auth`, then fetch
   `http://localhost:8080/.well-known/jwks.json` with `wget -qO-`). Keep the current key's
   public JWK (`kty`, `crv`, `x`, `y`, `kid`, `alg`) and set its `key_ops` to `["verify"]`.
   The v2.197.0 image has `/bin/sh`, `wget` and `nc`. Unmeasured: whether `railway ssh`
   reaches the `auth` container.
2. Generate the new key with `go run ./tools/prenv jwk-es256`.
3. Compose `[<new private key, sign+verify>, <old public key, verify>]`, validate it (below),
   and paste it into `GOTRUE_JWT_KEYS` on production's `auth` in the dashboard (edit, not
   Raw Editor). The old private half is not recoverable, so there is no separate "new key
   verify-only" stage: this edit adds the new key and moves `sign` to it together.
4. Let the edit deploy `auth` so GoTrue loads the new set. A production variable change
   rebuilds the service from `main` without the build-SHA stamp; `auth` is exempt from the
   stamp check, so this is safe while `main` holds the deployed pin. The gateway verifier refetches the JWKS when a
   token names an unknown `kid` (at most every 30 s per issuer), so tokens signed by the new
   key verify without a gateway redeploy.
5. After at least 8 h, compose `[<new private key>]` again (you still hold it from step 2
   until this edit), validate it, and paste it as the whole value. Let it deploy `auth`. Then discard your copy of
   the private key.

**Validate every composed value before you paste it.** A malformed `GOTRUE_JWT_KEYS` makes
GoTrue fail at boot and log the whole value, private `d` included, in the fatal config error
(measured on v2.197.0). That paste would put the private key in the Railway deploy logs.
Keep the composed value in a file only you can read (`umask 077`), never on a command line.
Run this offline check on it. It prints `ok` or `REFUSED`, never the value:

```
jwt_keys_ok='def signs: (.key_ops | type) == "array" and any(.key_ops[]; . == "sign");
  type == "array"
  and all(.[]; type == "object" and .kty == "EC" and .crv == "P-256" and .alg == "ES256"
    and (.kid | type) == "string" and .kid != "")
  and ([.[] | select(has("d") and signs)] | length) == 1
  and all(.[]; (has("d") and signs) or ((has("d") | not) and (signs | not)))
  and ([.[].kid] | unique | length) == length'
jq -e "$jwt_keys_ok" composed.json >/dev/null 2>&1 && echo ok || echo REFUSED
pbpaste | jq -e "$jwt_keys_ok" >/dev/null 2>&1 && echo ok || echo REFUSED   # from the clipboard
```

It passes only an array in which exactly one key has `d` and `sign`, every other key has
neither, every key is EC P-256 with `alg` ES256 and a `kid`, and no two kids are equal.
`prenv jwk-check` refuses an array of more than one key, so it fits only step 5's value.
Delete `composed.json` after the paste.

`ceiling:` measured locally on image v2.197.0, 2026-09-24: GoTrue boots on
`[<new private key, sign+verify>, <old public key, verify>]`, its JWKS serves both kids, new
tokens carry the new kid, and the gateway verifier accepts old and new tokens. It refuses to
boot with two signing keys ("multiple signing keys detected"). Re-measure on a new image
version before the next rotation.

Rotating `GOTRUE_JWT_SECRET` or `GOTRUE_SMTP_PASS` is a single dashboard edit with a new
value (`openssl rand -hex 32`, or the new Resend key); the edit deploys `auth`.
