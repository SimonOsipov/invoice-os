---
paths:
  - "internal/validation/**"
  - "migrations/*rules*"
---
# Validation rules

- Never grant `invoice_app` UPDATE on `rules`. Only the owner `invoice_migrator` switches a rule off.
- The kill switch writes no `audit_log` row. `audit_log` is per tenant and the switch is global.
