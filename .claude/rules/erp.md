---
paths:
  - "internal/erp/**"
  - "cmd/portfolio/main.go"
  - "cmd/worker/main.go"
---
# ERP connectors

- Build connectors only through `erp.RegistryFromEnv`.
- Run the fake connector on a preview and the nightly; locally only with `ERP_FAKE=true`. Refuse to boot a hosted deployment with `ERP_FAKE` set to true. Name the variable in a boot error, never its value.
- Never let a request choose the connector mode.
- Check the admin role as the first statement of every ERP connection write transaction, except a system job's.
- Let every member who can see the client list its connections. Scope the list by the seam's client scope only; answer a client outside it 404.
- Keep tokens out of Postgres. A grant's secret lives in the `erp.SecretStore`; Postgres holds only the secret id.
- Build the store only through `erp.SecretsFromEnv`: nil for an empty registry, `MemorySecrets` locally, Secret Manager on a preview, the nightly and hosted. A failed metadata read is a boot error.
- Name every secret with `erp.SecretName(scope, grantID)`: `erp-grant-<uuid>` in production and locally, `erp-grant-<slot>-<uuid>` on a preview or the nightly. A preview or nightly boot needs `ERP_SECRET_SCOPE` (`slot-1`, `slot-2`, `nightly`); a hosted boot refuses it. Name the variable in a boot error, never its value.
- Label a preview's secrets `preview-pr` (the PR number, or `nightly`) so teardown deletes them. Put no payload in an error.
- Build a connector with `ERP_REDIRECT_URL` (absolute, https outside local), read only when the registry is non-empty. A missing or invalid value is a boot error that names the variable.
- Build a state with `erp.NewState` and parse it with `erp.ParseState`; Postgres stores only its sha256. Render the fake consent page with `strings.NewReplacer` and `html.EscapeString`.
- Complete the consent only in `Store.Complete`: tx1 consumes the single-use state and re-checks the admin, the workspace lock and the pending row; the exchange and the secret create hold no transaction; tx2 repeats the admin and lock checks and activates. Delete the secret inline when tx2 fails or the key collides, unless a re-read shows the commit applied; if the re-read fails, keep it. Log no code, state, token or vendor reply. Keep the gateway-served pieces (mode, state shape, fake consent page) in `internal/erp/erpweb`: the gateway must not link Secret Manager.
- Disconnect sets `disconnected`, clears `grant_id` and deletes a grant no connection references. Never delete a connection or an invoice.
- Serialise a grant's release on its advisory lock. Release a grant only through `releaseGrant`: when no connection uses it, it deletes the row and enqueues `erp_secret_delete` in the same transaction. Only that job deletes the secret (NOT_FOUND counts as done) and records `erp.token.deleted`; never delete a secret on the request path.
- Delete the tenant's ERP secrets in the workspace purge, before `purge_tenant`: every `erp_grants.secret_ref` and every `erp_secret_delete` job ref of the tenant (`erp.TenantSecretRefs`). NOT_FOUND counts as deleted; a failed delete stops the purge at step `secrets`; a ref without `erp-grant-` stops it with no store call.
- Refresh a grant only in `RefreshWorker`: the claim (`version + 1` under `OncePerJob`) comes first, then Access, `Refresh` and `AddVersion` with no transaction held, then one transaction for `expires_at`, `erp.token.refreshed` and the next job (`erp_refresh:<grant>:<version>`, at expiry − 5 min). Retry `AddVersion` in the attempt: the vendor may have spent the refresh token. A failed disable of the old version is logged, not retried.
- End a grant only through the reconsent update (`active` → `reconsent`): on `ErrGrantRevoked` (job cancelled) and on the 8th failed attempt (`exhausted`, error returned). The status change is the once-guard; never call `OncePerJob` a second time in a job. Put no token in an event, error or log.
- Create an ERP draft only through `importer.CreateDrafts`. Never write an `invoices` row from `internal/erp`: link a duplicate number, mark a change on the document, never re-sign or submit. Take a document's client only from its connection row.
- Pull from the stored cursor; enqueue each ingest through `EnqueueTx` with `IngestKey` and save the cursor in the same transaction. Pull and ingest only an active connection.
- A bad cursor nulls the cursor and keeps `ingest_epoch`; only a skipped ingest bumps it.
- Map an ERP document only through `erp.Apply` with the connector's `DefaultMapping` merged with the client's overrides; apply lookups after `ContentHash`.
- Save a mapping or lookup only through `Store.SaveMapping` or `Store.SaveLookups`: the admin check first, values checked against `nrs_codes`, one `erp.mapping.changed`.
