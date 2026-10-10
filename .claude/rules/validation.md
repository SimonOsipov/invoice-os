---
paths:
  - "cmd/validation/**"
  - "internal/validation/**"
  - "migrations/*rules*"
  - "migrations/*rule_set*"
---
# Validation rules

- Never grant `invoice_app` UPDATE on `rules`. Only the owner `invoice_migrator` switches a rule off.
- The kill switch writes no `audit_log` row. `audit_log` is per tenant and the switch is global.
- Write a per-line `cel` rule as `[!has(invoice.<list>) ||] invoice.<list>.all(x, <body on x only>)` with `target` = `<list>`. Any other shape reports one violation at the target, not the line.
- Register a staff route under `/v1/staff/`.
- The platform checks the rules role on a staff route. The handler repeats no check.
- Open the staff route transaction with `db.WithinStaffTx`.
- Read the actor with `auth.StaffFromContext`.
- Give a staff route an `exempt` verdict in `scRouteVerdicts`.
- Keep `PATCH /v1/rules/{key}` as `ToggleHandler`.
- Select a rule-set version only through `rule_set_version_for(date)`.
- Publish a version with one `UPDATE` that sets `sealed` and `effective_from`.
- Guard every `when` path with `has()`. A guard on an absent key fails the whole evaluation.
- Write a per-category tax rule as `tax_math` with `items`, `rate_by` and `rates`.
