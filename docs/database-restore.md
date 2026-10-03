# Database restore

Restore the production Postgres to a point in time, into a sibling service, and prove the copy is whole.

**Who:** the operator with Railway access to project `ASComply`. Every step marked **Production write — the operator runs it** changes production or creates or deletes a service. Run those by hand and keep the terminal transcript. Every other step is a read.

Fixed IDs used below:

| Thing | ID |
|---|---|
| Project `ASComply` | `9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3` |
| Environment `production` | `6c864094-6a06-452f-8495-be77d8a94fe7` |
| Service `Postgres` | `98723af0-50ca-42a4-a56a-3e0438b9ce8a` |
| Volume `postgres-volume` | `b3b5bcf5-6a4d-4871-970a-5e72aa9d7efa` |

The production database is named `railway`, not `invoice_os`. Commands read it from `$PGDATABASE` on the container. Production Postgres is private-only: reach it with `railway ssh`.

## Shell setup

Paste once per terminal. Run from the repo root. `rsql` runs SQL read-only on a service, `rc_run` runs `db/restore-check.sql`.

```sh
REMOTE_PSQL='PGOPTIONS=-cdefault_transaction_read_only=on psql -q -At -v ON_ERROR_STOP=1 -U "$PGUSER" -d "$PGDATABASE"'

rsql() {  # $1 = service name, $2 = SQL
  railway ssh -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s "$1" \
    "echo $(printf '%s\n' "$2" | base64 | tr -d '\n') | base64 -d | $REMOTE_PSQL"
}

rc_run() {  # $1 = service name, $2 = output prefix; reads RC_TENANT and RC_CUTOFF
  local b64 rc
  [ -r db/restore-check.sql ] || { echo "rc_run: run from the repo root (db/restore-check.sql not found)" >&2; return 2; }
  b64=$({ printf "SET restore_check.tenant = '%s';\nSET restore_check.cutoff = '%s';\n" "$RC_TENANT" "$RC_CUTOFF"; cat db/restore-check.sql; } | base64 | tr -d '\n')
  railway ssh -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s "$1" \
    "echo $b64 | base64 -d | $REMOTE_PSQL" >"$2.out" 2>"$2.err"
  rc=$?
  echo "$2 exit=$rc stdout_lines=$(wc -l <"$2.out" | tr -d ' ') stderr_restore_check=$(grep -c restore-check "$2.err")"
}
```

`railway ssh` joins its arguments and loses one level of quoting. That is why the SQL travels base64-encoded, and why `PGOPTIONS` carries no space or quote. Every statement of `rsql` and `rc_run` starts in a read-only transaction: a write fails with `cannot execute ... in a read-only transaction`.

## 1. What exists

- **Point-in-time recovery (PITR):** enabled on 2026-10-02 at about 18:11 UTC by the user (`pitr enable --json`: `enabled` true, `bucketWired` true, `isHaCluster` false). Bucket: `Postgres-PITR`, read back with `railway bucket list -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production --json`. The restart took `Postgres` deployment `5d63c4d5-cc7a-4169-86cc-66e6ddeb33ea` (created 18:11:00Z) from `DEPLOYING` at 18:11:43 to `SUCCESS` at 18:12:19; it is the post-enable deployment id. Read it: `deploymentId` of `railway service status -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s Postgres --json`. First base backup: pgbackrest label `20261002-181212F`, full, 18:12:12Z to 18:13:12Z, LSN start `0/35000028`, stop `0/350003E0`. The restore range starts at 18:13:12Z. `pg_stat_archiver` showed `archived_count` 3 and `failed_count` 0, `archive_mode` on, and `pitr status` showed `archiverHealthy` true. `/healthz/fleet` was ok with all 11 services up. Current state: `railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr status --json`.
- **Backup schedule:** none. On 2026-10-02, `railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr schedule list` printed `No backup schedule configured for Postgres.`, and with `--json` printed `[]`.
- **Manual backup:** one, `Pre-Security-Patch Backup`, id `c3db8156-43c2-4f8e-b3af-4e128e033d0d`, taken 2026-08-22, expired 2026-09-21 (`referencedMB` 248). List: `railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a --json pitr backup list`. It is a volume backup, not a PITR source.
- **Never drill with a volume restore.** `pitr backup restore` and a restore from the dashboard Backups tab overwrite the selected database in place: a new volume replaces production's volume. That is a production event, not a drill. Use only `pitr restore` (section 3), which creates a sibling service and never touches the source.
- **PR forks.** A PR environment is forked from production, so a fork made after the enable copies `WAL_ARCHIVE_*`. The values are `${{Postgres-PITR.*}}` references, and each fork gets its own `Postgres-PITR` bucket instance. `scripts/ci/railway-env.sh reconcile-fork` writes `""` to every `WAL_ARCHIVE_*` variable of the fork's Postgres before that Postgres has a deployment, then reads them back. Railway keeps the references. The step then passes only when the fork's rendered `WAL_ARCHIVE_BUCKET` differs from production's. It fails when they match, or when either side is empty or unreadable. When the fork's Postgres already has a deployment, the step writes nothing and runs the same compare. A fork that failed after its Postgres booted may have archived into production's bucket: delete it (`railway-env.sh delete-environment pr-<N>` or `dev-env-teardown.yml`) and tell the coordinator before you re-run. The step never prints a `WAL_ARCHIVE_*` value.

## 2. Enable PITR

Enabling restarts production Postgres once, with a short outage. Pick the time. Check `/healthz/fleet` afterwards.

**Precondition, for this enable and any later one:** the fork step is on the base of every PR that can fork. A PR runs the `railway-env.sh` of its head merged into its base, so a base without the step lets forks copy `WAL_ARCHIVE_*`. A draft PR forks nothing. Check:

```sh
git fetch origin && for b in origin/main $(git branch -r --list 'origin/epic/*'); do printf '%s ' "$b"; git grep -cF 'fork_bucket=$(rendered_bucket "$env_id")' "$b" -- scripts/ci/railway-env.sh || echo MISSING; done
```

A base that prints `MISSING` needs a hold on PR environments, agreed with the coordinator, before the enable.

**Production write — the operator runs it.** Enable:

```sh
railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr enable --json
```

Effects: Railway creates the bucket `Postgres-PITR`, sets the `WAL_ARCHIVE_*` variables on `Postgres` (they reference the bucket's credentials) and redeploys `Postgres` once. The restore window starts at the first base backup after the enable. It is not retroactive.

Health reads, in this order:

```sh
railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr status --json
railway service status -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s Postgres --json
rsql Postgres 'select archived_count, failed_count, last_failed_wal from pg_stat_archiver'
railway ssh -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s Postgres "su postgres -s /bin/sh -c 'pgbackrest --stanza=main info --output=json'"
curl -fsS https://api.ascomply.com/healthz/fleet
```

Expect: `bucketWired` true, `Postgres` deployment `SUCCESS`, `failed_count` 0, a base backup in `pgbackrest info`, `/healthz/fleet` ok.

Failure branches. Stop at the first one and roll back with the command below:

- `Postgres` is not `SUCCESS`, or `/healthz/fleet` is not ok, within 10 min.
- `bucketWired` stays false.
- `failed_count` rises for 15 min.
- No base backup appears within 2 h.

**Production write — the operator runs it.** Rollback. It stages the removal of the archive variables and the deletion of the bucket, and its deploy is a second Postgres restart:

```sh
railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr disable --yes --json
```

Afterwards, record the `Postgres` `deploymentId` (the enable changed it) in section 1.

## 3. Restore to a sibling

1. Pick a time outside Railway's auto-update window for `Postgres`: Saturday 10:00 to Sunday 18:00 (the timezone of that schedule is unverified).
2. Ask the coordinator to hold PR environments. While the sibling exists, no PR environment is created or redeployed: a fork made then would copy the sibling.
3. Read the restore range. `range_end` is the `Restorable up to` line of the text output (JSON: `live.maxRestoreTime`):

   ```sh
   railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr status
   ```

4. Read the stop LSN of the newest base backup (`[0].backup[-1].lsn.stop`):

   ```sh
   railway ssh -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s Postgres "su postgres -s /bin/sh -c 'pgbackrest --stanza=main info --output=json'"
   ```

5. Set `T = range_end - 2 min`, in RFC 3339 with `Z`. A target past the last archived WAL can fail with `recovery ended before configured recovery target was reached`.
6. **Production write — the operator runs it.** Create the sibling. Keep `date -u` on the same line: its output is `t_request`.

   ```sh
   date -u +%FT%TZ; railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr restore --at <T> --new-service-name Postgres-drill-<YYYYMMDD> --yes --json
   ```

   `pitr restore --json` returns only `root`, `targetTimestamp` and `workflowId`. Take `SIBLING_ID` from the `id` of the entry whose `name` is the `--new-service-name`:

   ```sh
   railway service list -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production --json
   ```

   Cross-check `t_request` with `railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a history --json`.
7. Prove the sibling is a distinct server before you poll: production also returns `f` from `pg_is_in_recovery()`. Run the query on both services and compare. The sibling's start time must be after `t_request`, and its address and LSN must differ from `Postgres`:

   ```sh
   rsql Postgres-drill-<YYYYMMDD> 'select pg_postmaster_start_time(), inet_server_addr(), pg_current_wal_lsn()'
   rsql Postgres 'select pg_postmaster_start_time(), inet_server_addr(), pg_current_wal_lsn()'
   ```

   Then poll every 30 s until it prints `f`. A connection error means not ready. The first `f` is `t_ready`. Give up after 30 min: keep the sibling, run `railway service logs -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s Postgres-drill-<YYYYMMDD>`, and tell the coordinator.

   ```sh
   rsql Postgres-drill-<YYYYMMDD> 'select pg_is_in_recovery()'
   ```

## 4. Verify the restored copy

`db/restore-check.sql` prints a fingerprint of one tenant. It needs a role that bypasses RLS: `railway ssh` runs as the container superuser `postgres`. It runs inside `BEGIN READ ONLY`, so it writes nothing.

**Pick the tenant.** If production holds a tenant outside the four demo tenants (`aaaaaaaa-…`, `bbbbbbbb-…`, `11111111-…`, `22222222-…`), use the one with the most invoices. Otherwise use `Okafor & Partners`, `11111111-1111-1111-1111-111111111111`.

```sh
rsql Postgres 'select t.id, t.name, count(i.id) from tenants t left join invoices i on i.tenant_id = t.id group by 1, 2 order by 3 desc, 1'
```

**Run it.** The cutoff is `T`. It carries a time of day and an explicit offset, for example `2026-10-02T14:00:00Z`. A date-only value is refused. Run the same tenant and cutoff on both services:

```sh
RC_TENANT=11111111-1111-1111-1111-111111111111
RC_CUTOFF=<T>
rc_run Postgres rc-prod
rc_run Postgres-drill-<YYYYMMDD> rc-sib
```

**Treat a non-zero exit, or any stderr line containing `restore-check`, as a failure.** Do not run the file with plain `psql -f`: it prints the final `SELECT` and exits 0 even after the guard failed. `psql -q -At -v ON_ERROR_STOP=1` stops at the guard, so a failure prints nothing on stdout and exits non-zero.

**Pass rule.** All of these hold:

```sh
for f in rc-prod rc-sib; do for k in role tenant table goose; do printf '%s %s %s\n' "$f" "$k" "$(grep -c "^$k | " "$f.out")"; done; done
diff rc-prod.out rc-sib.out && echo DIFF-EMPTY
```

- Both `rc_run` lines show `exit=0`, `stderr_restore_check=0`.
- Each file has exactly 1 `role`, 1 `tenant`, 5 `table` and 1 `goose` row.
- `diff` prints nothing.

The `role` row on both is `role | postgres | t | t`.

**Negative control.** A tenant that does not exist must fail. Run it once per environment before you trust a pass. The subshell leaves `RC_TENANT` untouched:

```sh
for svc in Postgres Postgres-drill-<YYYYMMDD>; do
  ( RC_TENANT=$(uuidgen | tr 'A-Z' 'a-z'); rc_run "$svc" rc-neg-"$svc"; cat rc-neg-"$svc".out rc-neg-"$svc".err )
done
```

Expect a non-zero exit, `stdout_lines=0`, and stderr naming `restore-check: tenant <uuid> not found`.

**The `auth` schema.** GoTrue owns `auth`. The file does not read `auth.users`, because the CI migrations database has no GoTrue tables. Run this on both services and compare by eye. The `owner | auth | …` rows in the main output cover ownership only:

```sh
rsql Postgres "select count(*), md5(coalesce(string_agg(id::text, ',' order by id), '')) from auth.users"
rsql Postgres-drill-<YYYYMMDD> "select count(*), md5(coalesce(string_agg(id::text, ',' order by id), '')) from auth.users"
```

**Replay.** Production writes continuously (River), so the restore replayed WAL past the base backup. Read the replay position on the sibling, then compare with the stop LSN from section 3, step 4. Never compare LSNs as strings:

```sh
rsql Postgres-drill-<YYYYMMDD> 'select pg_last_wal_replay_lsn()'
rsql Postgres-drill-<YYYYMMDD> "select '<replay>'::pg_lsn > '<stop>'::pg_lsn"
```

Expect `t`.

**A non-empty `diff`.** Keep the sibling and record the diff. The only benign cause is a production write after `T`. Four forms of it exist. Tell them apart by the `table` rows that differ, and by the `count` field:

- **New rows** created after `T`: production's count is higher. A cutoff 1 h earlier clears them.
- **Rows committed after `T` with `created_at` at or before `T`** (a back-dated or late-committed row): production's count is higher, and an earlier cutoff does not clear them.
- **Deletes after `T` of rows created before `T`**: production's count is lower, and an earlier cutoff does not clear them.
- **Updates to older rows**: both counts are equal and only the hash differs.

For a count difference, list the ids on both services and compare: `rsql <service> "select id from <table> where tenant_id = '<tenant>' and created_at <= '<T>' order by id"`. An id only on the sibling is a delete after `T`. An id only on production is a late commit. The `goose` and `owner` rows are not scoped by the cutoff: a diff there is benign only if a migration ran on production after `T`. Any diff counts as benign only if you can name the production write after `T` that caused it. Otherwise it is a restore defect: stop and tell the coordinator.

## 5. Clean up

Delete only by ID, and only the sibling. Steps 3 and 4 are production writes.

1. Read the sibling's volume ID before you delete the service. Take `SIBLING_VOLUME_ID` from the `id` of the entry whose `serviceName` is the sibling:

   ```sh
   railway volume -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production list --json
   ```

2. Set both IDs, then assert each is a UUID and neither is production's. An empty ID passes a "not production" test alone, and an empty `-s` falls back to the linked service:

   ```sh
   SIBLING_ID=<id from the service list in section 3 step 6>
   SIBLING_VOLUME_ID=<id from step 1>
   uuid='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
   [ "$(printf '%s\n%s\n' "$SIBLING_ID" "$SIBLING_VOLUME_ID" | grep -Ec "$uuid")" = 2 ] && [ "$SIBLING_ID" != 98723af0-50ca-42a4-a56a-3e0438b9ce8a ] && [ "$SIBLING_VOLUME_ID" != b3b5bcf5-6a4d-4871-970a-5e72aa9d7efa ] && echo IDS-OK
   ```

   Stop unless it prints `IDS-OK`.
3. **Production write — the operator runs it.** Delete the service. Add `--2fa-code <code>` if 2FA is on:

   ```sh
   railway service delete -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s "$SIBLING_ID" --yes
   ```

   It prints only `> Select a service to delete <name>`, with no confirmation. Step 5's re-read is the evidence.

4. **Production write — the operator runs it.** Delete its volume. Add `--2fa-code <code>` if 2FA is on:

   ```sh
   railway volume -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production delete -v "$SIBLING_VOLUME_ID" --yes
   ```

   It prints `Volume "<name>" deleted`, but the volume stays listed (see step 5).

5. Read again. The sibling is not listed. Its volume is either not listed, or listed with `isPendingDeletion: true`, `serviceName` null and a `deletedAt` purge time 48 h later. That volume is attached to no service, so a fork cannot copy it. `Postgres` and `postgres-volume` remain, and PITR is still enabled:

   ```sh
   railway service list -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production --json
   railway volume -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production list --json
   railway postgres -p 9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3 -e production -s 98723af0-50ca-42a4-a56a-3e0438b9ce8a pitr status --json
   ```

6. Tell the coordinator to lift the hold on PR environments.

## 6. Open gaps

- **The bucket and its keys.** The `source-documents` bucket holds the files behind `documents.storage_key` and `extraction_page_images.storage_key`. No volume or PITR restore covers it. A restored database can point at keys the bucket no longer has.
- **Volume against PITR archive.** Volume backups die with the volume. The PITR archive lives in the `Postgres-PITR` bucket, outside the volume, so it survives losing the volume but not losing the project. Restores land in the same project only. Whether deleting the `Postgres` service also deletes that bucket is unverified.
- **Logical-dump roles, passwords and ownership.** A dump restore needs `db/bootstrap.sql` first. The cluster has four login roles (`invoice_app`, `invoice_migrator`, `invoice_tenant_reader`, `supabase_auth_admin`), the NOLOGIN role `auth_hook_reader`, and the schema `auth`, which GoTrue migrates outside goose. Ownership is split: `supabase_auth_admin` owns `auth`; `invoice_migrator` owns most of `public`; `auth_hook_reader` owns `custom_access_token_hook`. A dump with `--no-owner` would, by reasoning not by test, break the next migration: `ALTER TABLE` needs ownership and goose runs as `invoice_migrator`. Passwords are not in a dump. A PITR restore is physical and carries roles, passwords and ownership.
- **`river_job` re-submission.** Restoring `river_job` rows to an earlier point can re-submit invoices to FIRS. Latent today: only the `mock` adapter is registered and production has no `river_job` or `idempotency_keys` rows. `idempotency_keys` partly covers it; that is a hazard, not a proven safety property, once a real adapter ships.
- **Cutover never performed.** Pointing the services at a restored copy has not been drilled.
- **Restore-then-migrate never performed.** The `goose` and `owner` rows prove the copy carries production's apply order and ownership. Running a migration on the copy has not been done.
- **`auth` compared by one line only.** The `auth.users` count and hash in section 4. Sessions, identities and refresh tokens are not compared.

## 7. Drill record

Definitions:

- `range_end`: the `Restorable up to` time in `pitr status`, read just before the restore.
- `T`: `range_end - 2 min`.
- `t_request`: the `date -u` printed on the same line as `pitr restore`.
- `t_ready`: the first 30 s poll where `pg_is_in_recovery()` on the sibling returns `f`.
- **Restore duration:** `t_ready - t_request`.
- **Data age:** `t_request - T`.
- Archive lag: `t_request - range_end`.
- Newest invoice `created_at`: the newest `recent` row of the named tenant in the copy.

The 2026-10-02 `table` hashes use the earlier whole-text form. Later drills use the per-row hash (`md5` of the concatenated per-row `md5`s), so do not compare hashes across the two forms.

| Date | Operator | `range_end` | `T` | Base-backup time and stop LSN | `t_request` | `t_ready` | Restore duration | Data age | Archive lag | Sibling replay LSN | Newest invoice `created_at` | Diff result | Sibling name and ID | Sibling volume ID | Deleted at |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 2026-10-02 | The user ran the writes (typed with `!`); the lead ran the reads | 18:21:54.193149Z | 18:19:54Z | `20261002-181212F`, 18:13:12Z, stop LSN `0/350003E0` | 18:22:36Z | 18:24:05Z | 1 min 29 s | 2 min 42 s | 41.8 s | `0/3C000F98` | 2026-09-23T11:27:52.788696Z | Empty | `Postgres-drill-20261002`, `f835699e-af4c-4a62-9dab-232beab086f6` | `5d342760-65a9-4835-8b66-63cb475d3593` | Service 18:29Z; volume soft-deleted 18:29:29Z, purge 2026-10-04T18:29:29Z |
