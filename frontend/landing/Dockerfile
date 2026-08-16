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
# Vite bakes VITE_* at build time. Railway injects the service variable as a build arg
# (same mechanism the shared Go Dockerfile uses for SERVICE); declare + promote each to an
# env var so `vite build` sees it — without this an injected value is silently dropped and
# `vite build` bakes an empty string (M4-21-10). Unset → destUrl() returns null
# (frontend/landing/src/auth.ts) rather than routing to a hardcoded deploy, so the default
# showcase build and the smoke suite are unaffected.
ARG VITE_APP_URL
ENV VITE_APP_URL=$VITE_APP_URL
ARG VITE_OPS_URL
ENV VITE_OPS_URL=$VITE_OPS_URL
ARG VITE_SUPPORT_URL
ENV VITE_SUPPORT_URL=$VITE_SUPPORT_URL
ARG VITE_HUBSPOT_PORTAL_ID
ENV VITE_HUBSPOT_PORTAL_ID=$VITE_HUBSPOT_PORTAL_ID
ARG VITE_HUBSPOT_FORM_GUID
ENV VITE_HUBSPOT_FORM_GUID=$VITE_HUBSPOT_FORM_GUID
ARG VITE_GA_MEASUREMENT_ID
ENV VITE_GA_MEASUREMENT_ID=$VITE_GA_MEASUREMENT_ID
RUN pnpm --filter @invoice-os/landing build

# ---- Serve: Caddy static server with SPA fallback + /health ----
FROM caddy:2-alpine AS serve
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/landing/dist /srv
# The commit under test, stamped by scripts/ci/stamp-build-sha.sh before `railway up`
# and served at /build.txt. Caddy answers /health from memory before touching the
# filesystem, so it can never carry a build; the deploy gate reads this instead and
# blocks until the SPA under test is the one actually being served.
COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt
