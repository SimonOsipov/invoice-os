package audit_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// ceiling: scratch-DB setup is copied across test files; extract a dbtest package before another copy.

// planScratchDB is the migrated audit_plan_<hex> database the plan corpus lives in, so the
// plan tests see a fresh database's planner stats instead of the grown dev one.
type planScratchDB struct {
	name    string
	appDSN  string
	fx      *fixture
	super   *pgxpool.Pool
	baseDSN string
}

var planScratch *planScratchDB

func dsnForDB(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

// envOr mirrors platform/db devDefaultGUCs: Bootstrap resets cluster-wide role passwords.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newPlanScratch() (*planScratchDB, error) {
	ctx := context.Background()
	baseSuper := os.Getenv("DATABASE_SUPERUSER_URL")
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	s := &planScratchDB{name: "audit_plan_" + hex.EncodeToString(b[:]), baseDSN: baseSuper, fx: &fixture{}}

	admin, err := pgx.Connect(ctx, baseSuper)
	if err != nil {
		return nil, fmt.Errorf("connect superuser: %w", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+s.name); err != nil {
		return nil, fmt.Errorf("create scratch database: %w", err)
	}
	planScratch = s // closePlanScratch drops it even if a later step fails

	dsns := map[string]string{}
	for k, env := range map[string]string{
		"super": "DATABASE_SUPERUSER_URL", "mig": "DATABASE_MIGRATION_URL", "app": "DATABASE_URL",
	} {
		if dsns[k], err = dsnForDB(os.Getenv(env), s.name); err != nil {
			return nil, fmt.Errorf("parse %s: %w", env, err)
		}
	}
	pw := db.RolePasswords{Migrator: envOr("MIGRATOR_PASSWORD", "migrator"), App: envOr("APP_PASSWORD", "app"), Reader: envOr("READER_PASSWORD", "reader"), AuthAdmin: envOr("AUTH_ADMIN_PASSWORD", "auth_admin")}
	if err := db.Bootstrap(ctx, dsns["super"], pw, dbsql.FS); err != nil {
		return nil, fmt.Errorf("bootstrap scratch: %w", err)
	}
	if err := db.MigrateUp(ctx, dsns["mig"], migrations.FS); err != nil {
		return nil, fmt.Errorf("migrate scratch: %w", err)
	}

	s.appDSN = dsns["app"]
	for dst, dsn := range map[**pgxpool.Pool]string{&s.super: dsns["super"], &s.fx.app: dsns["app"], &s.fx.mig: dsns["mig"]} {
		if *dst, err = pgxpool.New(ctx, dsn); err != nil {
			return nil, fmt.Errorf("scratch pool: %w", err)
		}
	}
	return s, nil
}

// closePlanScratch closes the scratch pools, then drops the database.
func closePlanScratch() {
	s := planScratch
	if s == nil {
		return
	}
	planScratch = nil
	for _, p := range []*pgxpool.Pool{s.super, s.fx.app, s.fx.mig} {
		if p != nil {
			p.Close()
		}
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, s.baseDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit suite: drop %s: connect: %v\n", s.name, err)
		return
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+s.name+` WITH (FORCE)`); err != nil {
		fmt.Fprintf(os.Stderr, "audit suite: drop %s: %v\n", s.name, err)
	}
}
