---
paths: ["internal/erp/**", "cmd/portfolio/main.go"]
---
# ERP connectors

- Build connectors only through `erp.RegistryFromEnv`.
- Run the fake connector on a preview and the nightly; locally only with `ERP_FAKE=true`. Refuse to boot a hosted deployment with `ERP_FAKE` set. Name the variable in a boot error, never its value.
- Never let a request choose the connector mode.
