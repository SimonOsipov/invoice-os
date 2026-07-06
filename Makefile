# Migration / DB tooling for invoice-os (M2-01).
#
# goose is pinned via the go.mod `tool` directive, so `go tool goose` is the exact
# same version locally and in CI. Migrations run as the MIGRATOR role
# (DATABASE_MIGRATION_URL); the one-time role bootstrap runs as the SUPERUSER
# (DATABASE_SUPERUSER_URL). The app role (DATABASE_URL) is never used here.
# Set the DATABASE_* URLs in .env (gitignored) or your environment; see docs/migrations.md §1.

# Load local overrides from .env if present (gitignored).
-include .env
export

GOOSE          := go tool goose
MIGRATIONS_DIR := migrations

# Dev-default role passwords for `db-bootstrap`. Overridden by .env / the
# environment / the command line; real values live only in Railway.
MIGRATOR_PASSWORD ?= migrator
APP_PASSWORD      ?= app

# goose against Postgres as the migrator role.
GOOSE_MIGRATE := GOOSE_DRIVER=postgres GOOSE_MIGRATION_DIR=$(MIGRATIONS_DIR) \
	GOOSE_DBSTRING="$(DATABASE_MIGRATION_URL)" $(GOOSE)

.DEFAULT_GOAL := help
.PHONY: help db-bootstrap migrate-up migrate-down migrate-reset migrate-status migrate-create

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

db-bootstrap: ## Create/rotate the non-superuser roles (runs as SUPERUSER; needs psql)
	@test -n "$(DATABASE_SUPERUSER_URL)" || { echo "DATABASE_SUPERUSER_URL is not set (set it in .env or the environment)"; exit 1; }
	psql "$(DATABASE_SUPERUSER_URL)" -v ON_ERROR_STOP=1 \
		-v migrator_password="$(MIGRATOR_PASSWORD)" \
		-v app_password="$(APP_PASSWORD)" \
		-f db/bootstrap.sql

migrate-up: guard-migration-url ## Apply all pending migrations (as migrator)
	$(GOOSE_MIGRATE) up

migrate-down: guard-migration-url ## Roll back the latest migration (as migrator)
	$(GOOSE_MIGRATE) down

migrate-reset: guard-migration-url ## Roll back ALL migrations (as migrator; used by the CI round-trip)
	$(GOOSE_MIGRATE) reset

migrate-status: guard-migration-url ## Show applied/pending migration state (as migrator)
	$(GOOSE_MIGRATE) status

migrate-create: ## Scaffold a timestamped migration: make migrate-create name=<slug>
	@test -n "$(name)" || { echo "usage: make migrate-create name=<slug>"; exit 1; }
	$(GOOSE) -dir $(MIGRATIONS_DIR) create $(name) sql

.PHONY: guard-migration-url
guard-migration-url:
	@test -n "$(DATABASE_MIGRATION_URL)" || { echo "DATABASE_MIGRATION_URL is not set (set it in .env or the environment)"; exit 1; }
