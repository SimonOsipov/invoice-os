# Sentry

Ops page for error, trace and log reporting. The Sentry org is EU (`https://de.sentry.io`). Railway logs stay the store of record.

## What reports where

| Project | Deployable | Named by | Test trigger |
|---|---|---|---|
| `asc-backend` | 9 Go services: `gateway`, `tenancy`, `portfolio`, `invoice`, `validation`, `submission`, `dashboard`, `notifications`, `reconciliation` | `server_name` (`platform.Config.Service`) | `SENTRY_TEST_EVENT=true` at boot, in `platform.New` |
| `asc-backend` | `docling` | `server_name: docling` | `SENTRY_TEST_EVENT=true` at start, in `sentry_setup.init_sentry` |
| `asc-frontend` | `app`, `landing`, `ops-console`, `support-console` | tag `service` | `__ascSentryTest(<passphrase>)` in the browser console |

`auth` (GoTrue) has no Sentry SDK and is not a deployable here; `fleet-gate` exempts it by name.

The test event has the fingerprint `["sentry-test-event", <service>]`: one issue per deployable, and a repeat joins the same issue.

## Variables

Set in the Railway production environment only. Never seal them: a sealed variable does not fork, and `audit-sealed-variables` fails every PR run. Never write them through `scripts/ci/railway-env.sh` `upsert_variable`; it echoes values.

| Variable | Services | Value | Secret | Sealed | Blanked in forks |
|---|---|---|---|---|---|
| `SENTRY_DSN` | 9 Go services, `docling` | `<asc-backend DSN>` | no | never | yes (`set-sentry-off`) |
| `VITE_SENTRY_DSN` | 4 SPAs | `<asc-frontend DSN>` | no | never | yes |
| `SENTRY_AUTH_TOKEN` | 4 SPAs | organisation token `sntrys_…` | yes | never | yes |
| `SENTRY_TEST_EVENT` | 9 Go services, `docling` | `true` for the go-live deploy, then deleted | no | never | no |
| `VITE_SENTRY_TEST_DIGEST` | 4 SPAs | SHA-256 hex of the operator's passphrase | no (the passphrase is; never store it) | never | no |

The two test variables are not blanked in forks. The DSN is blank there, so they do nothing.

`SENTRY_TEST_EVENT`:
- Fires only on exactly `true` (`internal/platform/config.go`, `sidecar/docling/sentry_setup.py`). `1`, `True` and `yes` do nothing.
- Fires once per boot or start, and again on every boot while the container holds the variable.
- Deleting the variable changes nothing in the running container. Redeploy the service after the delete.
- Does nothing without a DSN.

`VITE_SENTRY_TEST_DIGEST`:
- The SHA-256 hex of the passphrase: `printf %s "$P" | shasum -a 256 | cut -d' ' -f1`.
- The build trims and lowercases it. It must be 64 hex characters, or nothing installs.
- Does nothing without `VITE_SENTRY_DSN`.
- It is baked into the bundle. It stays there until you delete the variable and rebuild the SPA.
- The browser call is `await __ascSentryTest('<passphrase>')` (`packages/monitoring/src/testEvent.ts`). It prints the event id, or `null` for a wrong passphrase.

## Go-live checklist

The user does every step. Production writes are the user's; an agent never writes production. Record the evidence named in each step.

1. Read the preconditions. Evidence: `epic/sentry` is in `main`; `GET https://api.ascomply.com/healthz/fleet` builds and each SPA's `/build.txt` equal a `main` commit that contains it; a PR run after SENTRY-01 printed `Sentry off` (run 37040249736).
2. In the EU Sentry org, create `asc-backend` (platform Go) and `asc-frontend` (platform React). Evidence: both project URLs.
3. In each project, add an issue alert "A new issue is created" that emails the user. In each project, open Settings → Security & Privacy and turn on the setting that prevents storing IP addresses. Evidence: both alerts exist, both settings are on.
4. In the organisation, read Settings → Spike Protection. Evidence: the state, reported to the agent session.
5. Open Settings → Developer Settings → Organization Tokens and create a token (`sntrys_…`). A user token (`sntryu_…`) makes the upload skip. Evidence: token created; the value goes nowhere else.
6. Generate the passphrase: `openssl rand -hex 24`. Keep it in a password manager. Compute its digest with the command under `VITE_SENTRY_TEST_DIGEST`. Evidence: the digest is ready.
7. In the Railway production environment (`6c864094-6a06-452f-8495-be77d8a94fe7`), write the variables. Never seal them. Pass `--skip-deploys` on every write.
   - On the 9 Go services and `docling`: `SENTRY_DSN` and `SENTRY_TEST_EVENT=true`.
   - On the 4 SPAs: `VITE_SENTRY_DSN`, `VITE_SENTRY_TEST_DIGEST` and `SENTRY_AUTH_TOKEN`.
   - Write the token with `railway variable set SENTRY_AUTH_TOKEN --stdin --skip-deploys -s <svc> -e <env id>`, so it never enters shell history or a log.
   - Evidence: `railway variable list -s <svc> -e <env id>` names each variable on each service (do not paste values).
8. Deploy once from CI.
   - Before it, record each SPA's entry asset name: `curl -s https://app.ascomply.com/ | grep -o 'assets/index-[^"]*\.js'`. Do the same for `www.`, `ops.` and `sup.`.
   - Re-run the whole push-to-main `Dev Env` run of `main`'s head: `gh run rerun <id>`, never `--failed`. It rebuilds all services with the new variables and a stamped release.
   - The SHA is the same, so the build-matched gates cannot tell old containers from new. Wait until every service shows a new SUCCESS deployment created after the re-run started, and each SPA's entry asset name has changed (the inlined DSN changes the bundle hash).
   - Watch one SPA build log. `[sentry-vite-plugin]` must upload. "No auth token provided" means the token did not reach the build. A stall in the upload means Sentry is unreachable.
   - Evidence: the run id, the deployment times, the old and new asset names; `/healthz/fleet` shows `"sentry":"on"` on the 9 Go services and `docling`.
9. The backends sent their test events at boot. In each SPA (`www.`, `app.`, `ops.`, `sup.ascomply.com`), open the browser console and run `await __ascSentryTest('<passphrase>')`. Call it once per page load. The SDK's `Dedupe` drops an identical second event on the same page while the call still prints an id, so reload before the next call. Evidence: the event id from each SPA.
10. Confirm one alert email arrived per deployable. Evidence: the emails.
11. Remove the test triggers.
    - On each backend: `railway variable delete SENTRY_TEST_EVENT -s <svc> -e <env id>`, then `railway redeploy -s <svc> -e <env id> -y`. A delete starts no deploy and the running container keeps the variable, so a plain restart fires again.
    - On each SPA: delete `VITE_SENTRY_TEST_DIGEST`. The next deploy ships bundles without the trigger.
    - To test again later, choose a new passphrase and set its digest.
    - To rotate or after a leak, delete the digest variable and deploy. A leaked passphrase can only send test events.
    - Evidence: neither variable is listed on any service.
12. Sign in to `app.ascomply.com` and validate one invoice, for the trace check. Evidence: the invoice id.
13. Tell the agent session. It reads the rest and fills "Go-live record".

## Where to look

- **Issues:** each project's issue stream, filtered by `environment:production` and `server_name` or `service`.
- **Traces and logs:** the issue's trace view, and Logs.
- **Usage:** Settings → Stats (errors, spans, logs).
- **Health:** `GET https://api.ascomply.com/healthz/fleet` (`sentry` per service).
- **Source map upload:** the SPA's Railway build log.
- **PR silence:** the deploy gate's `prepare-env` step "Blank every Sentry variable in the fork" and `fleet-gate` step "Gate on the fleet's Sentry state".

## Go-live record

OPEN until the agent fills it after go-live.

| Item | Issue link | Environment | Service name | Release | Stack readable / reads as source | Status |
|---|---|---|---|---|---|---|
| `gateway` | | | | | | OPEN — owed by the go-live read |
| `tenancy` | | | | | | OPEN — owed by the go-live read |
| `portfolio` | | | | | | OPEN — owed by the go-live read |
| `invoice` | | | | | | OPEN — owed by the go-live read |
| `validation` | | | | | | OPEN — owed by the go-live read |
| `submission` | | | | | | OPEN — owed by the go-live read |
| `dashboard` | | | | | | OPEN — owed by the go-live read |
| `notifications` | | | | | | OPEN — owed by the go-live read |
| `reconciliation` | | | | | | OPEN — owed by the go-live read |
| `docling` | | | | | | OPEN — owed by the go-live read |
| `app` | | | | | | OPEN — owed by the go-live read |
| `landing` | | | | | | OPEN — owed by the go-live read |
| `ops-console` | | | | | | OPEN — owed by the go-live read |
| `support-console` | | | | | | OPEN — owed by the go-live read |
| email confirmed | | | | | | OPEN — owed by the go-live read |
| trace (gateway → invoice → validation) | | | | | | OPEN — owed by the go-live read |
| PR run after go-live (`Sentry off`) | | | | | | OPEN — owed by the go-live read |

## Usage

OPEN until filled after go-live.

Method: Sentry Stats, 7 days from the go-live deploy, multiplied by 30/7.

| Signal | Quota | 7-day measured | 30-day projection | Share of quota |
|---|---|---|---|---|
| Errors | 5,000 / month | OPEN | OPEN | OPEN |
| Spans | 5,000,000 / month | OPEN | OPEN | OPEN |
| Logs | 5 GB / month | OPEN | OPEN | OPEN |

Code ceilings the numbers check: `TracesSampleRate` in `internal/platform/sentry.go`, and the landing and app `tracesSampleRate` in `packages/monitoring/src/options.ts`.
