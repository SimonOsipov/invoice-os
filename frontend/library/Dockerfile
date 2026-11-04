# syntax=docker/dockerfile:1
# Build context MUST be the repo root: the pnpm workspace shares one root lockfile.

# ---- Build: install the whole workspace, build just this app ----
FROM node:22-alpine AS build
WORKDIR /repo
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile
# Vite bakes VITE_* at build time; CI passes each value as a build arg.
ARG VITE_APP_URL
ENV VITE_APP_URL=$VITE_APP_URL
ARG VITE_LANDING_URL
ENV VITE_LANDING_URL=$VITE_LANDING_URL
# Empty Sentry DSN means Sentry off.
ARG VITE_SENTRY_DSN
ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN
ARG VITE_SENTRY_TEST_DIGEST
ENV VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST
ARG VITE_GA_MEASUREMENT_ID
ENV VITE_GA_MEASUREMENT_ID=$VITE_GA_MEASUREMENT_ID
ARG VITE_HUBSPOT_PORTAL_ID
ENV VITE_HUBSPOT_PORTAL_ID=$VITE_HUBSPOT_PORTAL_ID
ARG VITE_GATEWAY_URL
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
ARG VITE_LIBRARY_URL
ENV VITE_LIBRARY_URL=$VITE_LIBRARY_URL
ARG VITE_BLOG_URL
ENV VITE_BLOG_URL=$VITE_BLOG_URL
ARG VITE_OPS_URL
ENV VITE_OPS_URL=$VITE_OPS_URL
ARG VITE_SUPPORT_URL
ENV VITE_SUPPORT_URL=$VITE_SUPPORT_URL
ARG VITE_HUBSPOT_FORM_GUID
ENV VITE_HUBSPOT_FORM_GUID=$VITE_HUBSPOT_FORM_GUID
ARG VITE_REGISTRATION_OPEN
ENV VITE_REGISTRATION_OPEN=$VITE_REGISTRATION_OPEN
ARG BUILD_SHA
ENV VITE_BUILD_SHA=${BUILD_SHA}
# Build stage only, never ENV; empty skips the source-map upload.
ARG SENTRY_AUTH_TOKEN
RUN pnpm --filter @invoice-os/library build
# The landing not-found page answers a /library 404.
RUN pnpm --filter @invoice-os/landing exec vite build --base=/library/_landing/ --outDir=dist/library-404

# ---- Serve: Caddy static server + /health ----
FROM caddy:2-alpine AS serve
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /repo/frontend/library/dist /srv/library
# Serves / on the library host; any other missing path gets /srv/404.html.
COPY --from=build /repo/frontend/library/dist/index.html /srv/index.html
COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt
COPY --from=build /repo/internal/platform/buildsha.txt /srv/library/build.txt
COPY --from=build /repo/frontend/landing/dist/library-404/404.html /srv/404.html
COPY --from=build /repo/frontend/landing/dist/library-404/assets /srv/library/_landing/assets
COPY --from=build /repo/frontend/landing/dist/library-404/favicon.ico /repo/frontend/landing/dist/library-404/apple-touch-icon.png /srv/library/_landing/
