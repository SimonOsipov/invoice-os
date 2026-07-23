// bootstrap.go — Go provisioning runner for db/bootstrap.sql and db/seed.dev.sql
// (M4-21-03). See db.go for the package doc.
package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// RolePasswords carries the three ascomply.*_password values Bootstrap sets
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

// prEnvironmentPattern matches a Railway PR-environment name in either
// documented shape: the bare "pr-<N>" or the repo-qualified "<repo>-pr-<N>"
// (QA F5), where <N> is one or more digits. Case-sensitive and fully anchored:
// "PR-42", "pr-", "pr-abc", "pr-42x" and "pr-42 " (trailing whitespace) must all
// fail this match — see TestBootstrapEnabledAllowlist.
var prEnvironmentPattern = regexp.MustCompile(`^(?:.+-)?pr-[0-9]+$`)

// provisionableEnvironment reports whether environment is one deploy-time DB
// provisioning is ever permitted against: exactly "development", or a Railway
// PR-environment name (see prEnvironmentPattern). Everything else — including
// "", "staging", "prod", "Production", "production", and any value with
// surrounding whitespace — is false. This is an ALLOWLIST, deliberately: see
// BootstrapEnabled's doc comment for the rationale.
func provisionableEnvironment(environment string) bool {
	if environment == "development" {
		return true
	}
	return prEnvironmentPattern.MatchString(environment)
}

// BootstrapEnabled reports whether deploy-time DB provisioning is permitted.
//
// ALLOWLIST, deliberately (QA F1, BLOCKER): flag == "true" AND environment is
// exactly "development" or a Railway PR-environment name in either documented
// shape (pr-<N> / <repo>-pr-<N>, QA F5) — never a blocklist. Deliberately NOT
// gateway.MockIssuerEnabled's `!= "production"` shape; see task-127 / Decision
// [allowlist-guard]. Blast radius of a false positive = superuser password
// rotation + demo-fixture insertion against a live database.
//
// Callers MUST pass the raw os.Getenv("ENVIRONMENT"), never app.Config.Environment
// (which substitutes "development" for an unset var — internal/platform/config.go:44)
// — otherwise the allowlist is defeated one layer up.
func BootstrapEnabled(environment, flag string) bool {
	return flag == "true" && provisionableEnvironment(environment)
}

// bootstrapConnectAttempts / bootstrapConnectBackoff bound the connect retry
// Bootstrap and Seed use against superuserDSN, for the cold-Postgres race in a
// freshly forked environment (a container that has only just started accepting
// connections). Bounded so an unreachable DB fails within seconds, never hangs
// (AC-6).
const (
	bootstrapConnectAttempts = 5
	bootstrapConnectBackoff  = 500 * time.Millisecond
)

// connectSuperuser opens a single pinned connection to dsn, retrying up to
// bootstrapConnectAttempts times with a fixed backoff between attempts. A
// pinned pgx.Connect (not a pool) is required by both callers: the GUCs
// Bootstrap sets via set_config are session-scoped, so a pool — which could
// hand statements to different physical connections — would silently break
// them.
func connectSuperuser(ctx context.Context, dsn string) (*pgx.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < bootstrapConnectAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("db: connect to superuser dsn: %w", ctx.Err())
			case <-time.After(bootstrapConnectBackoff):
			}
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("db: connect to superuser dsn after %d attempts: %w", bootstrapConnectAttempts, lastErr)
}

// validateRolePasswords rejects a RolePasswords with any empty field, naming
// the offending field, BEFORE Bootstrap opens any connection (AC-4).
func validateRolePasswords(pw RolePasswords) error {
	if pw.Migrator == "" {
		return errors.New("db: RolePasswords.Migrator is empty")
	}
	if pw.App == "" {
		return errors.New("db: RolePasswords.App is empty")
	}
	if pw.Reader == "" {
		return errors.New("db: RolePasswords.Reader is empty")
	}
	return nil
}

// Bootstrap provisions a fresh/empty database: connects as superuser on a
// single pinned connection, holds BootstrapAdvisoryLockKey for the duration
// (QA F7 — proven empirically necessary: concurrent bootstrap without it raises
// Postgres's "tuple concurrently updated", SQLSTATE XX000), sets the three
// ascomply.* password GUCs, and executes bootstrap.sql read from fsys in a
// single argument-less Exec (so pgx uses the simple protocol its multi-statement
// body requires — passing any argument would switch to the extended protocol,
// under which multi-statement is illegal).
//
// The connection is superuser-only, dedicated to this call, and closed before
// Bootstrap returns — it is never retained or threaded onto any request-path
// pool (QA F3): read once, use once, discard.
func Bootstrap(ctx context.Context, superuserDSN string, pw RolePasswords, fsys fs.FS) (err error) {
	if err := validateRolePasswords(pw); err != nil {
		return err
	}

	conn, err := connectSuperuser(ctx, superuserDSN)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, BootstrapAdvisoryLockKey); err != nil {
		return fmt.Errorf("db: acquire bootstrap advisory lock: %w", err)
	}
	// Released on every path — success or error — before the connection closes.
	// A fresh background context is used here (not ctx) so the unlock is still
	// attempted even if ctx has already been cancelled/timed out. A failed
	// unlock surfaces as Bootstrap's own error ONLY when the call would
	// otherwise report success (err == nil here refers to the function's named
	// return, already set by every earlier return statement above) — an error
	// already in flight takes priority, since that's the more actionable one for
	// the caller. Either way, closing the connection below releases the
	// (session-scoped) lock server-side as a backstop even if this Exec itself
	// failed to reach the server.
	defer func() {
		if _, unlockErr := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, BootstrapAdvisoryLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("db: release bootstrap advisory lock: %w", unlockErr)
		}
	}()

	// set_config($1, $2, false): name and is_local are ours (hardcoded / a fixed
	// literal), never user input, and value is always bound as `text` — a type
	// Postgres accepts unconditionally, so it can never be the argument an
	// "invalid input syntax" error echoes back. Empirically confirmed: a forced
	// set_config failure's error text contains only the offending argument, not
	// an unrelated valid one — so wrapping this error can't leak a password.
	for _, kv := range []struct{ name, value string }{
		{"ascomply.migrator_password", pw.Migrator},
		{"ascomply.app_password", pw.App},
		{"ascomply.reader_password", pw.Reader},
	} {
		if _, err := conn.Exec(ctx, `SELECT set_config($1, $2, false)`, kv.name, kv.value); err != nil {
			return fmt.Errorf("db: set_config(%s): %w", kv.name, err)
		}
	}

	sql, err := fs.ReadFile(fsys, "bootstrap.sql")
	if err != nil {
		return fmt.Errorf("db: read bootstrap.sql: %w", err)
	}

	// Argument-less Exec: pgx uses the simple query protocol, which permits the
	// multi-statement DO $$ … $$ body bootstrap.sql requires. Passing any
	// argument here would switch pgx to the extended protocol, under which
	// multi-statement is illegal.
	if _, err := conn.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("db: execute bootstrap.sql: %w", err)
	}
	return nil
}

// Seed applies db/seed.dev.sql as the superuser (required: tenants is FORCE
// RLS, so invoice_app cannot insert the fixtures). Like Bootstrap, it connects
// on a single dedicated connection that is closed before Seed returns.
func Seed(ctx context.Context, superuserDSN string, fsys fs.FS) error {
	conn, err := connectSuperuser(ctx, superuserDSN)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	sql, err := fs.ReadFile(fsys, "seed.dev.sql")
	if err != nil {
		return fmt.Errorf("db: read seed.dev.sql: %w", err)
	}

	if _, err := conn.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("db: execute seed.dev.sql: %w", err)
	}
	return nil
}
