---
paths:
  - "internal/erp/**"
  - "cmd/portfolio/main.go"
---
# ERP connectors

- Build connectors only through `erp.RegistryFromEnv`.
- Run the fake connector on a preview and the nightly; locally only with `ERP_FAKE=true`. Refuse to boot a hosted deployment with `ERP_FAKE` set. Name the variable in a boot error, never its value.
- Never let a request choose the connector mode.
- Check the admin role as the first statement of every ERP connection write transaction.
- Let every member who can see the client list its connections. Scope the list by the seam's client scope only; answer a client outside it 404.
- Keep tokens out of Postgres: refuse a grant with secret bytes until a secret store exists.
- Disconnect sets `disconnected`, clears `grant_id` and deletes a grant no connection references. Never delete a connection or an invoice.
- Serialise a grant's release on its advisory lock. Refuse to release a grant whose `secret_ref` is set until a secret deleter exists.
