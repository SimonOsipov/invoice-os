# Build context MUST be the repo root: the pnpm workspace shares one root
# lockfile and @invoice-os/design-tokens is resolved via workspace:*.

# ---- Build: install the whole workspace, build just this app ----
FROM mirror.gcr.io/library/node:22.23.5-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS build
WORKDIR /repo
# Use the pnpm version pinned in root package.json#packageManager.
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile
# Vite bakes VITE_* at build time. CI passes the value as a build arg;
# declare it and promote it to an env var so `vite build` sees it. Same mechanism as
# frontend/app/Dockerfile. Unset → landingBase() returns null (src/auth.ts): no staff gate,
# and Sign out stays put rather than routing to a hardcoded deploy.
ARG VITE_LANDING_URL
ENV VITE_LANDING_URL=$VITE_LANDING_URL
ARG VITE_GATEWAY_URL
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
# Empty means Sentry off; `set-sentry-off` blanks it in forks.
ARG VITE_SENTRY_DSN
ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN
# Operator test-event digest; empty leaves the trigger uninstalled.
ARG VITE_SENTRY_TEST_DIGEST
ENV VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST
# The release fallback when no release is stamped.
ARG BUILD_SHA
ENV VITE_BUILD_SHA=${BUILD_SHA}
# Build stage only, never ENV; empty skips the source-map upload.
ARG SENTRY_AUTH_TOKEN
RUN pnpm --filter @invoice-os/ops-console build

# ---- Serve: Caddy static server with SPA fallback + /health ----
FROM caddy:2.11.7-alpine@sha256:e70b164da81ef698b3ad19af799ea6086ce6d9e48ed4079eb9fe26f6fbf14ee5 AS serve
# 8080 needs no capability; a non-root exec of a file-capable binary fails under --cap-drop ALL.
RUN setcap -r /usr/bin/caddy
USER nobody:nobody
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/ops-console/csp.txt /etc/caddy/csp.txt
COPY --from=build /repo/frontend/ops-console/dist /srv
# The commit under test, stamped by scripts/ci/stamp-build-sha.sh before the image build
# and served at /build.txt. Caddy answers /health from memory before touching the
# filesystem, so it can never carry a build; the deploy gate reads this instead and
# blocks until the SPA under test is the one actually being served.
COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt
