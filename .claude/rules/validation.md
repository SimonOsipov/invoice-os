---
paths:
  - "cmd/validation/**"
  - "internal/validation/**"
  - "migrations/*rules*"
  - "migrations/*rule_set*"
---
# Validation rules

- Never grant `invoice_app` UPDATE on `rules`. The app role flips `enabled` only through `set_rule_enabled`.
- Switch a rule through `PATCH /v1/staff/rules/{key}`. It writes a `staff_audit_log` row in the same transaction through `audit.RecordStaff`, with event `validation.rule.disabled` or `validation.rule.enabled`.
- Run the owner `UPDATE` of the runbook only as break-glass. It writes no audit row.
- Write a per-line `cel` rule as `[!has(invoice.<list>) ||] invoice.<list>.all(x, <body on x only>)` with `target` = `<list>`. Any other shape reports one violation at the target, not the line.
- Register a staff route under `/v1/staff/`.
- The platform checks the rules role on a staff route. The handler repeats no check.
- Open the staff route transaction with `db.WithinStaffTx`.
- Read the actor with `auth.StaffFromContext`.
- Give a staff route an `exempt` verdict in `scRouteVerdicts`.
- Keep `PATCH /v1/rules/{key}` as `ToggleHandler`.
- Select a rule-set version only through `rule_set_version_for(date)`.
- Publish a version with one `UPDATE` that sets `sealed` and `effective_from`.
