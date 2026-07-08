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
# Optional build-time API gateway URL. Railway passes the `app` service's
# VITE_GATEWAY_URL variable as a build arg; Vite bakes it into the bundle so the
# deployed app performs the real M2-13 round trip (mint → GET /api/tenancy/v1/me).
# Unset → empty → the app is a pure client-side mock with no backend calls.
ARG VITE_GATEWAY_URL=""
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
RUN pnpm --filter @invoice-os/app build

# ---- Serve: Caddy static server with SPA fallback + /health ----
FROM caddy:2-alpine AS serve
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/app/dist /srv
