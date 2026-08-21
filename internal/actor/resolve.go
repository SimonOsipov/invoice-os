package actor

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Resolve maps every stored audit_log actor string to a display Label in at most
// one round trip: "system" and anything failing the UUID gate are classified in
// Go and never bound; the rest are de-duplicated on their normalised uuid and
// looked up in a single `= ANY($1::uuid[])` over memberships.
//
// Scope is the caller's and nothing else's: Resolve opens no transaction, sets no
// GUC and writes no tenant_id predicate. The memberships tenant_isolation policy
// on tx's connection is the only filter, so a tx with no app.current_tenant
// resolves nothing and a superuser tx would resolve across tenants.
//
// UNIMPLEMENTED (AUDIT-02-02, Stage 2.5). Returns zero values on purpose so the
// specs in resolve_test.go fail on value diffs rather than on a build error.
func Resolve(ctx context.Context, tx pgx.Tx, subjects []string) (map[string]Label, error) {
	return nil, nil
}
