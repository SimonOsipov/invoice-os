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
# (same mechanism the shared Go Dockerfile uses for SERVICE); declare it and promote it
# to an env var so `vite build` sees it. Unset → empty → the app runs as a pure
# client-side mock (frontend/app/src/auth.ts: gatewayBase() returns null), so the
# default showcase build and the smoke suite are unaffected.
ARG VITE_GATEWAY_URL
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
# Same mechanism, for Sign out's landing-page redirect (M4-21-10). Unset → landingBase()
# returns null (frontend/app/src/auth.ts) rather than routing to a hardcoded deploy.
ARG VITE_LANDING_URL
ENV VITE_LANDING_URL=$VITE_LANDING_URL
# Same mechanism, for the demo persona switcher. Unset → DEMO_MODE is false and src/demo is
# tree-shaken out, so a customer build cannot carry the control at all.
ARG VITE_DEMO_MODE
ENV VITE_DEMO_MODE=$VITE_DEMO_MODE
RUN pnpm --filter @invoice-os/app build

# ---- Serve: Caddy static server with SPA fallback + /health ----
FROM caddy:2-alpine AS serve
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/app/dist /srv
# The commit under test, stamped by scripts/ci/stamp-build-sha.sh before `railway up`
# and served at /build.txt. Caddy answers /health from memory before touching the
# filesystem, so it can never carry a build; the deploy gate reads this instead and
# blocks until the SPA under test is the one actually being served.
COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt
