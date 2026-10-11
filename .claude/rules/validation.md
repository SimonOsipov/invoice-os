---
paths:
  - "cmd/validation/**"
  - "internal/validation/**"
  - "migrations/*rules*"
  - "migrations/*rule_set*"
---
# Validation rules

- Never grant `invoice_app` UPDATE on `rules`. The app role flips `enabled` on an in-force rule only through `set_rule_enabled`, and on a draft rule only through `rule_draft_put_rule`.
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
- Publish a golden version from the Support Console desk (`POST /v1/staff/rule-versions/draft/publish`). It runs one `UPDATE` that sets `sealed`, `effective_from` and `published_at`.
- Write rule content only through the `rule_draft_*` functions. Each has one Go caller, `internal/validation/staff_drafts.go`.
- Keep one unsealed version. `rule_set_versions_one_draft` enforces it.
- Guard every `when` path with `has()`. A guard on an absent key fails the whole evaluation.
- Write a per-category tax rule as `tax_math` with `items`, `rate_by` and `rates`.
