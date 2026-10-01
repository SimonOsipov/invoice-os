# Rule kill switch

Switch a validation rule off (or on) for every tenant, with no redeploy.

**Who:** the operator with production database access. `invoice_app` cannot write `rules`. Only the table owner `invoice_migrator` can.

## Steps

1. `railway ssh --service Postgres`, then `psql` against `invoice_os`.
2. `SET ROLE invoice_migrator;`
3. Run, with `<key>` and `false` (off) or `true` (on):

```sql
UPDATE rules r SET enabled = false FROM rule_set_versions v WHERE r.rule_set_version_id = v.id AND v.is_active AND r.key = '<key>';
```

4. Expect `UPDATE 1`. `UPDATE 0` means the active version has no such key. List the keys:

```sql
SELECT key FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE v.is_active;
```

## Effect

Every batch loads the rule set again, so the next batch honours the change. Sealed versions are not touched.

## Audit

The statement writes no `audit_log` row. `audit_log` is per tenant, and a global action belongs to no tenant. Record the change in your own ops log.

Pinned by `TestKillSwitch_E2E`.
