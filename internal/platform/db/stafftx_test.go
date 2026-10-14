package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestRLS_StaffActor_MissingOrMalformedSubjectIsNotStaff(t *testing.T) {
	if _, err := db.StaffActor(context.Background()); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("no staff context: err = %v, want db.ErrNotStaff", err)
	}
	if _, err := db.StaffActor(db.StaffCtxForTest(t, auth.Identity{Subject: "abc", Role: "authenticated", Staff: true})); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("subject abc: err = %v, want db.ErrNotStaff", err)
	}
	want := uuid.New()
	got, err := db.StaffActor(db.StaffCtxForTest(t, auth.Identity{Subject: want.String(), Role: "authenticated", Staff: true}))
	if err != nil || got != want {
		t.Errorf("uuid subject: got %v, %v; want %v, nil", got, err, want)
	}
}

func TestRLS_StaffReadError_OnlyTheFunctionsOwnRefusalIsNotStaff(t *testing.T) {
	refusal := "staff_read_tenants: actor is not staff"
	cases := []struct {
		name     string
		err      error
		notStaff bool
	}{
		{"the function's refusal", &pgconn.PgError{Code: "42501", Message: refusal}, true},
		{"the refusal, wrapped", fmt.Errorf("w: %w", &pgconn.PgError{Code: "42501", Message: refusal}), true},
		{"a lost function grant", &pgconn.PgError{Code: "42501", Message: "permission denied for function staff_read_tenants"}, false},
		{"a lost column grant", &pgconn.PgError{Code: "42501", Message: "permission denied for table tenants"}, false},
		{"the refusal text under another SQLSTATE", &pgconn.PgError{Code: "P0001", Message: refusal}, false},
		{"the refusal text from a non-database error", errors.New(refusal), false},
		{"the refusal text with a suffix after it", &pgconn.PgError{Code: "42501", Message: refusal + " (retry)"}, false},
		{"the refusal text without its separator", &pgconn.PgError{Code: "42501", Message: "actor is not staff"}, false},
		{"an unrelated error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := db.StaffReadError("op", tc.err)
			if got == nil {
				t.Fatal("db.StaffReadError returned nil for an error")
			}
			if errors.Is(got, db.ErrNotStaff) != tc.notStaff {
				t.Errorf("errors.Is(ErrNotStaff) = %v, want %v (got %v)", !tc.notStaff, tc.notStaff, got)
			}
			if !tc.notStaff && !errors.Is(got, tc.err) {
				t.Errorf("a server fault must keep its cause: %v does not wrap %v", got, tc.err)
			}
		})
	}
	if got := db.StaffReadError("op", nil); got != nil {
		t.Errorf("db.StaffReadError(nil) = %v, want nil", got)
	}
}
