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
# invoice_tenant_reader — the M2-06 cross-tenant enumeration role (M2-07's suite is
# its first consumer). NOTE: an inline `# comment` here would land trailing spaces in
# the value and produce an invalid connection URL — keep the comment on its own line.
READER_PASSWORD   ?= reader

# Migrator URL for the local docker-compose Postgres (make dev-db). Kept separate
# from DATABASE_MIGRATION_URL so `make dev-db` always targets the compose DB on
# localhost regardless of .env, and the existing migrate-* guards stay untouched.
DEV_DB_MIGRATION_URL := postgres://invoice_migrator:$(MIGRATOR_PASSWORD)@localhost:5432/invoice_os?sslmode=disable

# The other per-role URLs against the same compose DB, for the RLS suite (make
# test-rls). Same rationale: always target localhost so `make test-rls` works right
# after `make dev-db` with no .env edits.
DEV_DB_APP_URL       := postgres://invoice_app:$(APP_PASSWORD)@localhost:5432/invoice_os?sslmode=disable
DEV_DB_READER_URL    := postgres://invoice_tenant_reader:$(READER_PASSWORD)@localhost:5432/invoice_os?sslmode=disable
DEV_DB_SUPERUSER_URL := postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable

# goose against Postgres as the migrator role.
GOOSE_MIGRATE := GOOSE_DRIVER=postgres GOOSE_MIGRATION_DIR=$(MIGRATIONS_DIR) \
	GOOSE_DBSTRING="$(DATABASE_MIGRATION_URL)" $(GOOSE)

.DEFAULT_GOAL := help
.PHONY: help db-bootstrap dev-db dev-db-down dev-db-reset migrate-up migrate-down migrate-reset migrate-status migrate-create test-rls

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

db-bootstrap: ## Create/rotate the non-superuser roles (runs as SUPERUSER; needs psql)
	@test -n "$(DATABASE_SUPERUSER_URL)" || { echo "DATABASE_SUPERUSER_URL is not set (set it in .env or the environment)"; exit 1; }
	psql "$(DATABASE_SUPERUSER_URL)" -v ON_ERROR_STOP=1 \
		-v migrator_password="$(MIGRATOR_PASSWORD)" \
		-v app_password="$(APP_PASSWORD)" \
		-v reader_password="$(READER_PASSWORD)" \
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

# ---- Local dev DB (docker-compose Postgres a solo engineer iterates against) ----

dev-db: ## One command: local Postgres up (compose) -> bootstrap roles -> migrate -> seed
	docker compose up -d --wait
	# Bootstrap the roles INSIDE the container: its psql is always present, so
	# no host psql client is required. -T disables the TTY so the file pipes to stdin.
	docker compose exec -T postgres psql -U postgres -d invoice_os -v ON_ERROR_STOP=1 \
		-v migrator_password="$(MIGRATOR_PASSWORD)" -v app_password="$(APP_PASSWORD)" \
		-v reader_password="$(READER_PASSWORD)" \
		< db/bootstrap.sql
	$(MAKE) migrate-up DATABASE_MIGRATION_URL="$(DEV_DB_MIGRATION_URL)"
	# Seed a couple of test tenants (M2-06). Runs as the in-container superuser, which
	# has BYPASSRLS — so the fixture inserts need no tenant context and no app INSERT
	# grant. Idempotent (ON CONFLICT DO NOTHING), so re-running `make dev-db` is safe.
	docker compose exec -T postgres psql -U postgres -d invoice_os -v ON_ERROR_STOP=1 \
		< db/seed.dev.sql
	@echo "Local dev DB ready on localhost:5432 (db invoice_os; app role invoice_app). Seeded tenants: see db/seed.dev.sql."

dev-db-down: ## Stop and remove the local dev Postgres container (keeps the data volume)
	docker compose down

dev-db-reset: ## Wipe the local dev Postgres (drop the data volume) and rebuild from empty
	docker compose down -v
	$(MAKE) dev-db

test-rls: ## Run the M2-07 adversarial RLS suite against the local dev DB (run `make dev-db` first)
	DATABASE_URL="$(DEV_DB_APP_URL)" \
	DATABASE_MIGRATION_URL="$(DEV_DB_MIGRATION_URL)" \
	DATABASE_SUPERUSER_URL="$(DEV_DB_SUPERUSER_URL)" \
	DATABASE_READER_URL="$(DEV_DB_READER_URL)" \
	go test -count=1 -run TestRLS ./internal/platform/db/...

.PHONY: guard-migration-url
guard-migration-url:
	@test -n "$(DATABASE_MIGRATION_URL)" || { echo "DATABASE_MIGRATION_URL is not set (set it in .env or the environment)"; exit 1; }
