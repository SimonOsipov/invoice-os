# syntax=docker/dockerfile:1
# Build context MUST be the repo root: the pnpm workspace shares one root lockfile.

# ---- Build: install the whole workspace, build just this app ----
FROM node:22-alpine AS build
WORKDIR /repo
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile
# Vite bakes VITE_* at build time; Railway passes service variables as build args.
# Empty means Sentry off.
ARG VITE_SENTRY_DSN
ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN
ARG VITE_SENTRY_TEST_DIGEST
ENV VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST
ARG RAILWAY_GIT_COMMIT_SHA
ENV VITE_RAILWAY_GIT_COMMIT_SHA=$RAILWAY_GIT_COMMIT_SHA
# Build stage only, never ENV; empty skips the source-map upload.
ARG SENTRY_AUTH_TOKEN
RUN pnpm --filter @invoice-os/library build

# ---- Serve: Caddy static server with SPA fallback + /health ----
FROM caddy:2-alpine AS serve
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/library/dist /srv
# The commit under test, served at /build.txt for the deploy gate.
COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt
