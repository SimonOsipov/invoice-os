# Build context MUST be the repo root: the pnpm workspace shares one root lockfile.

# ---- Build: install the whole workspace, build just this app ----
FROM mirror.gcr.io/library/node:22.23.5-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS build
WORKDIR /repo
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile
# Vite bakes VITE_* at build time; CI passes each value as a build arg.
ARG VITE_LANDING_URL
ENV VITE_LANDING_URL=$VITE_LANDING_URL
ARG VITE_GATEWAY_URL
ENV VITE_GATEWAY_URL=$VITE_GATEWAY_URL
# Empty Sentry DSN means Sentry off.
ARG VITE_SENTRY_DSN
ENV VITE_SENTRY_DSN=$VITE_SENTRY_DSN
ARG VITE_SENTRY_TEST_DIGEST
ENV VITE_SENTRY_TEST_DIGEST=$VITE_SENTRY_TEST_DIGEST
ARG VITE_GA_MEASUREMENT_ID
ENV VITE_GA_MEASUREMENT_ID=$VITE_GA_MEASUREMENT_ID
ARG VITE_HUBSPOT_PORTAL_ID
ENV VITE_HUBSPOT_PORTAL_ID=$VITE_HUBSPOT_PORTAL_ID
ARG BUILD_SHA
ENV VITE_BUILD_SHA=${BUILD_SHA}
RUN --mount=type=secret,id=sentry_auth_token,env=SENTRY_AUTH_TOKEN pnpm --filter @invoice-os/blog build

# ---- Server: the blog binary, built from the root go.mod ----
FROM mirror.gcr.io/library/golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS server
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/blog cmd/blog
COPY internal/blogweb internal/blogweb
COPY internal/platform internal/platform
RUN CGO_ENABLED=0 go build -o /out/blog ./cmd/blog

# ---- Serve: the blog binary over the built SPA ----
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS serve
COPY --from=server /out/blog /blog
COPY --from=build /repo/frontend/blog/dist /srv
# The commit under test, served at /build.txt for the deploy gate.
COPY --from=build /repo/internal/platform/buildsha.txt /srv/build.txt
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/blog"]
