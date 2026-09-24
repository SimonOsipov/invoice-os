# Identity provider (`auth`, supabase/auth)

The `auth` Railway service runs a pinned, unmodified `supabase/auth` (GoTrue) image. It
signs ES256 access tokens, serves their public keys at `/.well-known/jwks.json`, and
projects the tenant into `app_metadata.tenant_id` through the Postgres access-token hook.
It is private-network only (`http://auth.railway.internal:8080`); it has no public domain.
The gateway reaches it for JWKS and for the fleet probe.

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
5. On `schedule` and `workflow_dispatch` only, a finding also opens or updates the issue
   titled `supabase/auth patch due`. The issue is found by exact title, open or closed, so
   a finding never opens a second one; a closed one is reopened.

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
| `GOTRUE_DISABLE_SIGNUP` | `true` | No registration ships yet; only CI containers override it |
| `GOTRUE_MAILER_AUTOCONFIRM` | `false` | |
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

`AUTH_ADMIN_PASSWORD` stays unsealed: the `auth.DATABASE_URL` reference renders it, and
every fork overwrites it with its own.

## Script behaviour on writes

- `set-fork-auth`, `set-fork-auth-site` and `set-production-auth` write with
  `skipDeploys: true`, so a write never redeploys a service by itself.
- A secret is written through `upsert_secret_variable`, which prints
  `label.NAME = <redacted>` and never a value or a length.
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
- Set `dockerfilePath=sidecar/auth/Dockerfile`, `healthcheckPath=/.well-known/jwks.json`
  and `restartPolicyType=ON_FAILURE` on the instance, because `railwayConfigFile` is
  rejected for new services.
- Set instance `watchPatterns: []`.
- `deploymentTriggerDelete` any trigger `serviceCreate` attached.
- **No public domain. No variables.** The service stays undeployed until U3b and the first
  push after merge.
- **Needed before the PR's first deploy gate**, because forks inherit services from
  production and `expected_json` fails on a missing `auth`.
- Until done, the PR gate is red at prepare-env's `set-fork-auth` step, which cannot resolve
  a service named `auth`.
- This is the **only** production action the PR gate needs. The fork writes everything else
  itself.

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
Signup is off, so nothing sends mail yet; this gates registration, not the provider. Until
done, `GOTRUE_SMTP_PASS` stays unset on production, and GoTrue boots with the Resend host
configured but sends nothing. Once the key exists after U3b's seals, add it as a dashboard
edit on `auth` (`GOTRUE_SMTP_PASS`), then seal it: `set-production-auth` refuses to run once
any of its targets is sealed.

No GitHub secret is needed. The CI `idp` job and every fork generate their own keys at run
time. The production key exists only in the Railway variable U3b writes and then seals.

## Sealed secrets

**Which three are sealed, and why.** On production's `auth` service only:
`GOTRUE_JWT_KEYS` (the signing key), `GOTRUE_JWT_SECRET` and `GOTRUE_SMTP_PASS` (the Resend
key).
- A sealed variable is not copied into a fork, so a PR environment never receives
  production's signing key, JWT secret or Resend key. `set-fork-auth` writes the fork's own.
- The account-scoped `RAILWAY_API_TOKEN` that PR workflows hold cannot read them back.
- `AUTH_ADMIN_PASSWORD` is not sealed (see Variables).

**A seal cannot be undone.** Railway has no unseal. A wrongly sealed variable can only be
deleted and re-created. Sealing is a dashboard action: the variable's 3-dot menu → Seal. No
public API mutation seals a variable.

**A sealed value cannot be read.** Not in the dashboard, not through the API, not through
`railway variables` or `railway run`. Its name and its `isSealed` flag stay readable. No
script and no test in this repo reads a sealed value.

**A sealed value can be edited in the dashboard** (3-dot menu → edit), but not through the
Raw Editor. Whether `variableUpsert` over a sealed variable succeeds, fails or unseals it is
unmeasured, so no script writes one.

**The audit allows exactly these three on `auth`.** `audit-sealed-variables` (run by
prepare-env on every PR, and by hand after U3b) passes when the only sealed variables in the
source environment are `GOTRUE_JWT_KEYS`, `GOTRUE_JWT_SECRET` and `GOTRUE_SMTP_PASS` on the
`auth` service. Any other sealed name, one of the three on another service, one of the three
environment-scoped, or any sealed variable in a source environment where `auth` cannot be
resolved fails every PR.

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
