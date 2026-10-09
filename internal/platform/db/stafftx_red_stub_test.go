package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// RED STUB (ENGI-11-05). When stafftx.go lands:
//  1. delete ErrNotStaff and WithinStaffTx below;
//  2. delete this file and put StaffCtxForTest in export_test.go with this body, which needs auth.RequireRulesRole:
//     h := auth.RequireRulesRole(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() }))
//     h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/staff/x", nil).WithContext(auth.WithIdentity(context.Background(), id)))
//     if ctx == nil { t.Fatal("RequireRulesRole refused the staff caller") }
var ErrNotStaff = errors.New("red stub: ErrNotStaff")

func WithinStaffTx(context.Context, *pgxpool.Pool, func(pgx.Tx) error) error {
	return errors.New("red stub: WithinStaffTx not implemented")
}

// StaffCtxForTest returns the context a handler sees behind RequireRulesRole for id.
func StaffCtxForTest(_ *testing.T, id auth.Identity) context.Context {
	return auth.WithIdentity(context.Background(), id)
}
