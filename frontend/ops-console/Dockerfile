# syntax=docker/dockerfile:1
# Build context MUST be the repo root: the pnpm workspace shares one root
# lockfile and @invoice-os/design-tokens is resolved via workspace:*.

# ---- Build: install the whole workspace, build just this app ----
FROM node:22-alpine AS build
WORKDIR /repo
# Use the pnpm version pinned in root package.json#packageManager.
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile
# Vite bakes VITE_* at build time. Railway injects the service variable as a build arg;
# declare it and promote it to an env var so `vite build` sees it. Same mechanism as
# frontend/app/Dockerfile. Unset → landingBase() returns null (src/auth.ts) and Sign out
# stays put rather than routing to a hardcoded deploy, so the default showcase build and
# the smoke suite are unaffected.
ARG VITE_LANDING_URL
ENV VITE_LANDING_URL=$VITE_LANDING_URL
RUN pnpm --filter @invoice-os/ops-console build

# ---- Serve: Caddy static server with SPA fallback + /health ----
FROM caddy:2-alpine AS serve
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/ops-console/dist /srv
