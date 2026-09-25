// Package auth is the JWT layer for ASComply: a config-driven verification
// middleware plus a dev/CI mock issuer that mints GoTrue-shaped tokens. The
// verification path is identical whether tokens come from the mock issuer or,
// after the M8 cutover, from Supabase GoTrue — only the issuer/JWKS config
// changes. The GoTrue claim contract the mock reproduces is pinned by the
// golden-token fixture under testdata/.
package auth

import "context"

// Identity is the verified caller extracted from a GoTrue-shaped JWT. It is the
// only thing the rest of the system consumes from a token; the tenant-context
// and RLS layers (M2-06/07) key off TenantID and Role.
type Identity struct {
	Subject  string // GoTrue "sub": the user id (a UUID)
	Role     string // GoTrue "role": a Postgres role, e.g. "authenticated"
	TenantID string // app_metadata.tenant_id: the tenant the caller acts within
	Email    string // GoTrue "email"; "" when the token carries none
}

type ctxKey int

const (
	ctxKeyIdentity ctxKey = iota
	ctxKeyTenantlessCaller
)

// WithIdentity returns a context carrying the verified identity. The middleware
// calls it after a successful Verify; business-logic tests call it directly as
// the cheap stub for "run as this tenant/role" without minting or verifying a
// token (the full mint→verify path is exercised only by this package's tests).
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKeyIdentity, id)
}

// IdentityFromContext returns the verified identity and whether one was present.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKeyIdentity).(Identity)
	return id, ok
}

// WithTenantlessCaller returns a context carrying a verified caller that has no
// tenant yet. It uses its own key so IdentityFromContext never sees one.
func WithTenantlessCaller(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKeyTenantlessCaller, id)
}

// TenantlessCallerFromContext returns the tenant-less caller and whether one was present.
func TenantlessCallerFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKeyTenantlessCaller).(Identity)
	return id, ok
}
