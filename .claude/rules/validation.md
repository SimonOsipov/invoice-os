---
paths:
  - "cmd/validation/**"
  - "internal/validation/**"
  - "migrations/*rules*"
---
# Validation rules

- Never grant `invoice_app` UPDATE on `rules`. Only the owner `invoice_migrator` switches a rule off.
- The kill switch writes no `audit_log` row. `audit_log` is per tenant and the switch is global.
- Register a staff route under `/v1/staff/`.
- The platform checks the rules role on a staff route. The handler repeats no check.
- Open the staff route transaction with `db.WithinStaffTx`.
- Read the actor with `auth.StaffFromContext`.
- Give a staff route an `exempt` verdict in `scRouteVerdicts`.
- Keep `PATCH /v1/rules/{key}` as `ToggleHandler`.
