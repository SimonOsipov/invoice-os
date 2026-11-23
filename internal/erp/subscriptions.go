package erp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
)

// SubscriptionInterval is how often the keeper re-ensures every vendor subscription.
const SubscriptionInterval = time.Hour

// SubscriptionKeeper ensures the vendor-side webhook subscription of each active connection.
type SubscriptionKeeper struct {
	Reader, Pool *pgxpool.Pool
	Registry     Registry
	Logger       *slog.Logger
}

type subTarget struct {
	id, system, key string
	creds           Credentials
}

// RunOnce reads each tenant's targets in one transaction, then calls the vendor with none held.
func (k *SubscriptionKeeper) RunOnce(ctx context.Context) error {
	var systems []string
	for name, c := range k.Registry {
		if c.Capabilities().Webhooks {
			systems = append(systems, name)
		}
	}
	if len(systems) == 0 {
		return nil
	}
	tenants, err := reconciliation.EnumerateTenants(ctx, k.Reader)
	if err != nil {
		return err
	}
	var errs []error
	fail := func(tenant, conn, system string, err error) {
		errs = append(errs, fmt.Errorf("erp subscriptions %s/%s: %w", tenant, conn, err))
		if k.Logger != nil {
			k.Logger.Error("erp subscriptions", "tenant_id", tenant, "connection_id", conn, "system", system, "err", err)
		}
	}
	for _, tenant := range tenants {
		var targets []subTarget
		err := db.WithinTenantTx(ctx, k.Pool, tenant, func(tx pgx.Tx) error {
			targets = nil
			if locked, err := db.WorkspaceLocked(ctx, tx); err != nil || locked {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id::text, system, external_key, grant_id::text FROM erp_connections WHERE status = 'active' AND system = ANY($1)`, systems)
			if err != nil {
				return err
			}
			defer rows.Close()
			type row struct {
				id, system string
				key, grant *string
			}
			var rs []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.system, &r.key, &r.grant); err != nil {
					return err
				}
				rs = append(rs, r)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			rows.Close()
			for _, r := range rs {
				if r.key == nil {
					continue
				}
				creds, err := credentialsFor(ctx, tx, r.grant)
				if err != nil {
					fail(tenant, r.id, r.system, err)
					continue
				}
				targets = append(targets, subTarget{id: r.id, system: r.system, key: *r.key, creds: creds})
			}
			return nil
		})
		if err != nil {
			fail(tenant, "", "", err)
			continue
		}
		for _, t := range targets {
			if err := k.Registry[t.system].EnsureSubscriptions(ctx, t.creds, t.key); err != nil {
				fail(tenant, t.id, t.system, err)
			}
		}
	}
	return errors.Join(errs...)
}
