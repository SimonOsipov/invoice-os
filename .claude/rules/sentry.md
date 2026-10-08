---
paths:
  - "internal/platform/sentry*.go"
  - "internal/platform/config.go"
  - "packages/monitoring/**"
  - "sidecar/docling/sentry*.py"
  - "frontend/*/src/instrument.ts"
---
# Sentry

- Send Go services and `docling` to project `asc-backend`. Send the SPAs to project `asc-frontend`.
- Name every deployable in its events: `server_name` for a Go service and `docling`, tag `service` for an SPA.
- Treat an empty DSN as off. The SDK becomes a no-op.
- Call `sentry.Init` only in `initSentry`. Its `BeforeSend` hooks scrub every event.
- Set the Sentry variables in the Railway production environment only.
- Never seal a Sentry variable. A sealed variable does not fork, and `audit-sealed-variables` fails every PR run.
- Never write a Sentry variable through `upsert_variable`. It echoes values.
- Blank every Sentry DSN in PR forks with `set-sentry-off`. `fleet-gate` then sees `sentry` `off`.
- Exempt only `auth` from the Sentry fleet gate. GoTrue has no Sentry SDK.
- Keep the test-event fingerprint `["sentry-test-event", <service>]`. A repeat joins the same issue.
- Fire `SENTRY_TEST_EVENT` only on exactly the value `true`. `1`, `True` and `yes` do nothing.
- Expect `SENTRY_TEST_EVENT` to fire on every boot while the container holds it. Delete the variable and redeploy the service.
- Treat `VITE_SENTRY_TEST_DIGEST` as the SHA-256 hex of a passphrase. The client accepts exactly 64 hex characters, trimmed and lowercased.
- Never store the passphrase. The digest stays in the bundle until you delete the variable and rebuild the SPA.
- Rebuild an SPA to change its digest or DSN. A Railway redeploy re-serves the old bundle.
- Pass `SENTRY_AUTH_TOKEN` as a build `ARG`, never as `ENV`. Use an organisation token, because a user token makes the upload skip.
- Sample traces below `1.0` before spans pass about 2.5 million a month. The ceiling comment on `TracesSampleRate` states it.
- Treat a change to a trace sample rate or a scrubber as a privacy-page change.
