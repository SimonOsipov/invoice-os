---
paths:
  - "internal/validation/**"
  - "migrations/*rules*"
---
# Validation rules

- Never grant `invoice_app` UPDATE on `rules`. Only the owner `invoice_migrator` switches a rule off.
- The kill switch writes no `audit_log` row. `audit_log` is per tenant and the switch is global.
- Write a per-line `cel` rule as `[!has(invoice.<list>) ||] invoice.<list>.all(x, <body on x only>)` with `target` = `<list>`. Any other shape reports one violation at the target, not the line.
