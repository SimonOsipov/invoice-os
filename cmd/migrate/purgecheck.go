package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// purgeCheckEnabled is true on a preview bootstrap run only; the grant run and production are off.
func purgeCheckEnabled(cfg db.ProvisionConfig) bool {
	return db.BootstrapEnabled(cfg.Environment, cfg.BootstrapFlag) &&
		platform.Posture(cfg.DeployEnv) == platform.PosturePreview
}

// purgeCheckStep runs check on a preview bootstrap run only; it never fails the job.
func purgeCheckStep(ctx context.Context, cfg db.ProvisionConfig, check func(context.Context, *slog.Logger), log *slog.Logger) {
	if purgeCheckEnabled(cfg) {
		check(ctx, log)
	}
}

// purgeCheck proves purge_tenant and DeleteTenantObjects on the preview's Cloud SQL and GCS.
// Log-only, like seedDemo: it logs one line, passed or failed, and returns nothing.
func purgeCheck(ctx context.Context, log *slog.Logger) {
	start := time.Now()
	var rows, objects int
	var step string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		rows, objects, step, err = runPurgeCheck(ctx)
	}()
	if err != nil {
		log.Error("migrate: tenant purge check", "outcome", "failed", "step", step, "error", err,
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	log.Info("migrate: tenant purge check", "outcome", "passed", "rows_removed", rows,
		"objects_deleted", objects, "duration_ms", time.Since(start).Milliseconds())
}

func runPurgeCheck(ctx context.Context) (rows, objects int, step string, err error) {
	step = "open"
	// The DSN's pool_max_conns applies; otherwise db.DefaultMaxConns (4).
	pool, err := db.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return 0, 0, step, err
	}
	defer pool.Close()
	cfg, err := document.ConfigFromEnv()
	if err != nil {
		return 0, 0, step, err
	}
	store, err := document.NewS3Store(cfg, nil)
	if err != nil {
		return 0, 0, step, err
	}
	return purgeCheckRun(ctx, pool, store)
}

// purgeCheckRun seeds a throwaway tenant, purges its objects and rows, and asserts both are gone.
func purgeCheckRun(ctx context.Context, pool *pgxpool.Pool, store document.ObjectStore) (rows, objects int, step string, err error) {
	tid, uid := uuid.NewString(), uuid.NewString()

	step = "seed"
	err = db.WithinTenantTx(ctx, pool, tid, func(tx pgx.Tx) error {
		policy, version := uuid.NewString(), uuid.NewString()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`SELECT public.provision_workspace($1, 'Purge check', 'firm', $2, 'Purge check', $3)`,
				[]any{tid, uid, "purge-check-" + tid + "@example.invalid"}},
			{`INSERT INTO approval_policies (id, tenant_id, name) VALUES ($1, $2, 'Purge check')`, []any{policy, tid}},
			{`INSERT INTO approval_policy_versions (id, tenant_id, policy_id, version) VALUES ($1, $2, $3, 1)`, []any{version, tid, policy}},
			{`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 1, 'approval')`, []any{tid, version}},
			{`UPDATE approval_policy_versions SET sealed = true WHERE id = $1`, []any{version}},
			{`INSERT INTO audit_log (actor, event) VALUES ('purge-check', 'purge.check')`, nil},
			{`INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, 'purge-check')`, []any{tid}},
			{`INSERT INTO river_job (kind, args, state, max_attempts, scheduled_at)
			  VALUES ('extraction_extract', jsonb_build_object('tenant_id', $1::text), 'scheduled', 1, now() + interval '1 day')`, []any{tid}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return fmt.Errorf("%.40s: %w", q.sql, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, step, err
	}

	step = "put objects"
	prefix := "tenants/" + tid + "/"
	for _, key := range []string{document.StorageKey(tid, "purge-check"), document.StorageKey(tid, "pages/purge-check/v1/p0001.png")} {
		if err = store.Put(ctx, key, bytes.NewReader([]byte("x")), 1); err != nil {
			return 0, 0, step, err
		}
	}

	step = "list objects"
	keys, err := store.List(ctx, prefix)
	if err != nil {
		return 0, 0, step, err
	}
	if len(keys) != 2 {
		return 0, 0, step, fmt.Errorf("listed %d objects, want 2", len(keys))
	}

	step = "delete objects"
	if objects, err = document.DeleteTenantObjects(ctx, store, tid); err != nil {
		return 0, objects, step, err
	}
	if objects != 2 {
		return 0, objects, step, fmt.Errorf("deleted %d objects, want 2", objects)
	}
	if keys, err = store.List(ctx, prefix); err != nil {
		return 0, objects, step, err
	}
	if len(keys) != 0 {
		return 0, objects, step, fmt.Errorf("%d objects left after delete", len(keys))
	}

	step = "purge"
	err = db.WithinTenantTx(ctx, pool, tid, func(tx pgx.Tx) error {
		var n int
		var who string
		if err := tx.QueryRow(ctx, `SELECT count(*), current_user FROM tenants WHERE id = $1`, tid).Scan(&n, &who); err != nil {
			return err
		}
		if n != 1 || who != "invoice_app" {
			return fmt.Errorf("control read: %d tenant rows as %s, want 1 as invoice_app", n, who)
		}
		return tx.QueryRow(ctx, `SELECT public.purge_tenant($1)`, tid).Scan(&rows)
	})
	if err != nil {
		return rows, objects, step, err
	}
	if rows != 1 {
		return rows, objects, step, fmt.Errorf("purge_tenant returned %d, want 1", rows)
	}

	step = "verify"
	err = db.WithinTenantTx(ctx, pool, tid, func(tx pgx.Tx) error {
		var left, enabled int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM tenants WHERE id = $1)
			+ (SELECT count(*) FROM audit_log WHERE tenant_id = $1)
			+ (SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1)
			+ (SELECT count(*) FROM river_job WHERE args @> jsonb_build_object('tenant_id', $1::text)),
			(SELECT count(*) FROM pg_trigger WHERE tgenabled = 'O' AND tgname IN
				('audit_log_no_update_delete', 'approval_policy_versions_seal_guard', 'approval_policy_steps_content_lock'))`,
			tid).Scan(&left, &enabled); err != nil {
			return err
		}
		if left != 0 || enabled != 3 {
			return fmt.Errorf("%d rows left, %d of 3 guard triggers enabled", left, enabled)
		}
		return nil
	})
	return rows, objects, step, err
}
