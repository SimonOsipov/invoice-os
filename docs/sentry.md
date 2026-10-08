# Sentry

Ops page for error, trace and log reporting. The Sentry org is EU (`https://de.sentry.io`). Railway logs stay the store of record.

## What reports where

| Project | Deployable | Named by | Test trigger |
|---|---|---|---|
| `asc-backend` | 9 Go services: `gateway`, `tenancy`, `portfolio`, `invoice`, `validation`, `submission`, `dashboard`, `notifications`, `reconciliation` | `server_name` (`platform.Config.Service`) | `SENTRY_TEST_EVENT=true` at boot, in `platform.New` |
| `asc-backend` | `docling` | `server_name: docling` | `SENTRY_TEST_EVENT=true` at start, in `sentry_setup.init_sentry` |
| `asc-frontend` | `app`, `landing`, `ops-console`, `support-console`, `library` | tag `service` | `__ascSentryTest(<passphrase>)` in the browser console |

`auth` (GoTrue) has no Sentry SDK and is not a deployable here; `fleet-gate` exempts it by name.

The test event has the fingerprint `["sentry-test-event", <service>]`: one issue per deployable, and a repeat joins the same issue. The alert rule is Sentry's default "Send a notification for high priority issues" (decision #59), so a repeat joins the existing issue and sends no new email. Prove a retest by the issue's event count rising, or by its new event.

## Variables

Set in the Railway production environment only. Never seal them: a sealed variable does not fork, and `audit-sealed-variables` fails every PR run. Never write them through `scripts/ci/railway-env.sh` `upsert_variable`; it echoes values.

| Variable | Services | Value | Secret | Sealed | Blanked in forks |
|---|---|---|---|---|---|
| `SENTRY_DSN` | 9 Go services, `docling` | `<asc-backend DSN>` | no | never | yes (`set-sentry-off`) |
| `VITE_SENTRY_DSN` | 5 SPAs | `<asc-frontend DSN>` | no | never | yes |
| `SENTRY_AUTH_TOKEN` | 5 SPAs | organisation token `sntrys_…` | yes | never | yes |
| `SENTRY_TEST_EVENT` | 9 Go services, `docling` | `true` for the go-live deploy, then deleted | no | never | no |
| `VITE_SENTRY_TEST_DIGEST` | 5 SPAs | SHA-256 hex of the operator's passphrase | no (the passphrase is; never store it) | never | no |

The two test variables are not blanked in forks. The DSN is blank there, so they do nothing.

`SENTRY_TEST_EVENT`:
- Fires only on exactly `true` (`internal/platform/config.go`, `sidecar/docling/sentry_setup.py`). `1`, `True` and `yes` do nothing.
- Fires once per boot or start, and again on every boot while the container holds the variable.
- Deleting the variable changes nothing in the running container. Redeploy the service after the delete.
- Does nothing without a DSN.
- A crash-loop or health-check restart fires it too, so keep the window between step 8 and step 11 short.

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
3. In each project, keep the default alert "Send a notification for high priority issues" (it emails the user). In each project, open Settings → Security & Privacy and turn on the setting that prevents storing IP addresses. Evidence: the default alert exists in both projects, both settings are on.
4. In the organisation, read Settings → Spike Protection. The plan may be a trial: record the plan name with the state, and read it again after a trial ends. Evidence: the state and the plan name, reported to the agent session.
5. Open Settings → Developer Settings → Organization Tokens and create a token (`sntrys_…`). A user token (`sntryu_…`) makes the upload skip. Evidence: token created; the value goes nowhere else.
6. Generate the passphrase: `openssl rand -hex 24`. Keep it in a password manager. Compute its digest with the command under `VITE_SENTRY_TEST_DIGEST`. Evidence: the digest is ready.
7. In the Railway production environment (`6c864094-6a06-452f-8495-be77d8a94fe7`), write the variables. Never seal them. Pass `--skip-deploys` on every write.
   - On the 9 Go services and `docling`: `SENTRY_DSN` and `SENTRY_TEST_EVENT=true`.
   - On the 5 SPAs: `VITE_SENTRY_DSN`, `VITE_SENTRY_TEST_DIGEST` and `SENTRY_AUTH_TOKEN`.
   - Write the token with `railway variable set SENTRY_AUTH_TOKEN --stdin --skip-deploys -s <svc> -e <env id>`, so it never enters shell history or a log.
   - Evidence: `railway variable list -s <svc> -e <env id>` names each variable on each service (do not paste values).
8. Deploy once from CI.
   - Before it, record each SPA's entry asset name: `curl -s https://app.ascomply.com/ | grep -o 'assets/index-[^"]*\.js'`. Do the same for `www.`, `ops.`, `sup.` and `library.`.
   - Re-run the whole push-to-main `Dev Env` run of `main`'s head: `gh run rerun <id>`, never `--failed`. It rebuilds all services with the new variables and a stamped release.
   - The SHA is the same, so the build-matched gates cannot tell old containers from new. Wait until every service shows a new SUCCESS deployment created after the re-run started, and each SPA's entry asset name has changed (the inlined DSN changes the bundle hash).
   - Watch one SPA build log. `[sentry-vite-plugin]` must upload. "No auth token provided" means the token did not reach the build. A stall in the upload means Sentry is unreachable.
   - Evidence: the run id, the deployment times, the old and new asset names; `/healthz/fleet` shows `"sentry":"on"` on the 9 Go services and `docling`.
9. The backends sent their test events at boot. In each SPA (`www.`, `app.`, `ops.`, `sup.`, `library.ascomply.com`), open the browser console and run `await __ascSentryTest('<passphrase>')`. Call it once per page load. The SDK's `Dedupe` drops an identical second event on the same page while the call still prints an id, so reload before the next call. Evidence: the event id from each SPA.
10. Confirm one alert email arrived per deployable. Evidence: the emails. If no email arrives, read the issue's priority in Sentry and record it.
11. Remove the test triggers.
    - On each backend: `railway variable delete SENTRY_TEST_EVENT -s <svc> -e <env id>`, then `railway redeploy -s <svc> -e <env id> -y`. A delete starts no deploy and the running container keeps the variable, so a plain restart fires again.
    - On each SPA: delete `VITE_SENTRY_TEST_DIGEST`, then rebuild the five SPAs as in step 8: re-run the `Dev Env` run of `main`'s head with `gh run rerun <id>`, never `--failed`. Record each SPA's entry asset name before the re-run. A Railway `redeploy` re-serves the old bundle.
    - To test an SPA again later, choose a new passphrase, set its digest and rebuild the SPA as above: the digest is baked at build. The retest adds an event to the existing issue and sends no new email.
    - To rotate or after a leak, delete the digest variable and rebuild as above. A leaked passphrase can only send test events.
    - Evidence: neither variable is listed on any service. Each SPA's entry asset name differs from the one recorded before the re-run. `typeof window.__ascSentryTest === 'undefined'` in the console on `www.`, `app.`, `ops.`, `sup.` and `library.ascomply.com`.
12. Sign in to `app.ascomply.com` and validate one invoice, for the trace check. Evidence: the invoice id.
13. Tell the agent session. It reads the rest and fills "Go-live record". Also read:
    - `curl -sI` an entry `.js.map` on `app.ascomply.com`: expect 404 (SENTRY-08 F-2).
    - Landing (SENTRY-07): EU ingest host; consent absent or denied; no Sentry cookie or storage; page-load named `/` or `/privacy`; no ingest request off the production host.
    - Browser to gateway trace join (SENTRY-06 H1).
    - One extraction job transaction with a `docling` child span in one trace (H7); "no production upload in the window" is a valid result.
    - Moving ledger rows C23–C31 in `docs/privacy-policy-claims.md` to the observed date.

## Where to look

- **Issues:** each project's issue stream, filtered by `environment:production` and `server_name` or `service`.
- **Traces and logs:** the issue's trace view, and Logs.
- **Usage:** Settings → Stats (errors, spans, logs).
- **Health:** `GET https://api.ascomply.com/healthz/fleet` (`sentry` per service).
- **Source map upload:** the SPA's Railway build log.
- **PR silence:** the deploy gate's `prepare-env` pass `fork-vars-after-urls` (`set-sentry-off`) and `fleet-gate` step "Gate on the fleet's Sentry state".

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
| `library` | | | | | | OPEN — owed by the go-live read |
| email confirmed | | | | | | OPEN — owed by the go-live read |
| trace (gateway → invoice → validation) | | | | | | OPEN — owed by the go-live read |
| PR run after go-live (`Sentry off`) | | | | | | OPEN — owed by the go-live read |

## Usage

OPEN until filled after go-live.

Method: Sentry Stats, 7 days from the go-live deploy, multiplied by 30/7. Record the plan name with the reading. A trial plan has higher quotas: compare against the plan in force after the trial (the free-plan quotas below).

| Signal | Quota | 7-day measured | 30-day projection | Share of quota |
|---|---|---|---|---|
| Errors | 5,000 / month | OPEN | OPEN | OPEN |
| Spans | 5,000,000 / month | OPEN | OPEN | OPEN |
| Logs | 5 GB / month | OPEN | OPEN | OPEN |

Code ceilings the numbers check: `TracesSampleRate` in `internal/platform/sentry.go`, and the landing and app `tracesSampleRate` in `packages/monitoring/src/options.ts`.
