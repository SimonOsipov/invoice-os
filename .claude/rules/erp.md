---
paths:
  - "internal/erp/**"
  - "cmd/portfolio/main.go"
---
# ERP connectors

- Build connectors only through `erp.RegistryFromEnv`.
- Run the fake connector on a preview and the nightly; locally only with `ERP_FAKE=true`. Refuse to boot a hosted deployment with `ERP_FAKE` set to true. Name the variable in a boot error, never its value.
- Never let a request choose the connector mode.
- Check the admin role as the first statement of every ERP connection write transaction.
- Let every member who can see the client list its connections. Scope the list by the seam's client scope only; answer a client outside it 404.
- Keep tokens out of Postgres. A grant's secret lives in the `erp.SecretStore`; Postgres holds only the secret id.
- Build the store only through `erp.SecretsFromEnv`: nil for an empty registry, `MemorySecrets` locally, Secret Manager on a preview, the nightly and hosted. A failed metadata read is a boot error.
- Name every secret with `erp.SecretName(scope, grantID)`: `erp-grant-<uuid>` in production and locally, `erp-grant-<slot>-<uuid>` on a preview or the nightly. A preview or nightly boot needs `ERP_SECRET_SCOPE` (`slot-1`, `slot-2`, `nightly`); a hosted boot refuses it. Name the variable in a boot error, never its value.
- Label a preview's secrets `preview-pr` (the PR number, or `nightly`) so teardown deletes them. Put no payload in an error.
- Build a connector with `ERP_REDIRECT_URL` (absolute, https outside local), read only when the registry is non-empty. A missing or invalid value is a boot error that names the variable.
- Build a state with `erp.NewState` and parse it with `erp.ParseState`; Postgres stores only its sha256. Render the fake consent page with `strings.NewReplacer` and `html.EscapeString`.
- Disconnect sets `disconnected`, clears `grant_id` and deletes a grant no connection references. Never delete a connection or an invoice.
- Serialise a grant's release on its advisory lock. Release a grant only through `releaseGrant`: when no connection uses it, it deletes the row and enqueues `erp_secret_delete` in the same transaction. Only that job deletes the secret (NOT_FOUND counts as done) and records `erp.token.deleted`; never delete a secret on the request path.
