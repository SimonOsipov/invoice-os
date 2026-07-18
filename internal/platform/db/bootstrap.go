// bootstrap.go — Go provisioning runner for db/bootstrap.sql and db/seed.dev.sql
// (M4-21-03). See db.go for the package doc.
//
// MODE-A STUB (task-127, Test-first: yes): this file exists only so
// internal/platform/db/bootstrap_test.go and seed_test.go — the architect's
// pre-authored Test Specs — compile and fail on ASSERTIONS (the correct RED state
// for a test-first subtask) instead of on missing symbols. Every declaration below
// is the smallest possible compilable stand-in: a correct name/signature/type, and
// a zero value or a "not implemented" error body. None of it is the real
// implementation, and none of it should be read as a design decision beyond what's
// needed to compile. The executor replaces every body here per task-127's
// Implementation Plan and removes this notice.
package db

import (
	"context"
	"errors"
	"io/fs"
)

// RolePasswords carries the three fiscalbridge.*_password values Bootstrap sets
// via set_config before executing db/bootstrap.sql. All three are required —
// Bootstrap validates them before opening any connection (AC-4).
type RolePasswords struct {
	Migrator string
	App      string
	Reader   string
}

// BootstrapAdvisoryLockKey is the fixed, project-scoped pg_advisory_lock key
// Bootstrap holds for the entire db/bootstrap.sql execution (QA F7), so two
// concurrent gateway boots serialize on CREATE ROLE rather than racing it. An
// arbitrary fixed int64 — its value carries no meaning beyond "non-zero, and not
// reused by any other advisory lock in this codebase". Exported so tests can
// assert the lock is actually acquired/released without hardcoding the key a
// second time.
const BootstrapAdvisoryLockKey int64 = 84210001

// BootstrapEnabled reports whether deploy-time DB provisioning is permitted.
//
// STUB (Mode A): always returns false. The real implementation is an ALLOWLIST
// (QA F1, BLOCKER): flag == "true" AND environment is exactly "development" or a
// Railway PR-environment name in either documented shape (pr-<N> / <repo>-pr-<N>,
// QA F5) — never a blocklist. Deliberately NOT gateway.MockIssuerEnabled's
// `!= "production"` shape; see task-127 / Decision [allowlist-guard].
// Callers MUST pass the raw os.Getenv("ENVIRONMENT"), never app.Config.Environment
// (which substitutes "development" for an unset var — internal/platform/config.go:44)
// — otherwise the allowlist is defeated one layer up.
func BootstrapEnabled(environment, flag string) bool {
	return false
}

// Bootstrap provisions a fresh/empty database: connects as superuser, holds
// BootstrapAdvisoryLockKey for the duration, sets the three fiscalbridge.*
// password GUCs, and executes bootstrap.sql read from fsys.
//
// STUB (Mode A): always returns a non-nil "not implemented" error and touches no
// connection, no lock, and no statement. See task-127's Implementation Plan for
// the real behavior (single pinned pgx.Connect with a bounded connect retry,
// pg_advisory_lock held across the whole execution, password validation before
// any connection is opened, one argument-less Exec so pgx uses the simple
// protocol bootstrap.sql's multi-statement body requires).
func Bootstrap(ctx context.Context, superuserDSN string, pw RolePasswords, fsys fs.FS) error {
	return errors.New("db: Bootstrap not implemented")
}

// Seed applies db/seed.dev.sql as the superuser (required: tenants is FORCE RLS,
// so invoice_app cannot insert the fixtures).
//
// STUB (Mode A): always returns a non-nil "not implemented" error.
func Seed(ctx context.Context, superuserDSN string, fsys fs.FS) error {
	return errors.New("db: Seed not implemented")
}
