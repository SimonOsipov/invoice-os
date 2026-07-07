# syntax=docker/dockerfile:1
# Shared build for every Go service (M2-04). The build context MUST be the repo
# root: each binary shares the root go.mod/go.sum and the internal/ packages.
# Parameterized by a single build arg — the image is the static binary compiled
# from ./cmd/${SERVICE}. Do not add per-service Dockerfiles.
#
# Pairs with Dockerfile.dockerignore: BuildKit resolves the Dockerfile-adjacent
# ignore ahead of the root .dockerignore (which is tuned for the SPA images and
# excludes all Go source), so this build sees cmd/ and internal/ while the SPA
# builds keep their lean context. BuildKit is required (syntax directive above).

ARG SERVICE

# ---- Build: compile ./cmd/${SERVICE} into a static, CGO-free binary ----
# golang:1.26-alpine tracks the latest 1.26.x (>= the go.mod toolchain 1.26.4),
# so Go never has to download a toolchain at build time.
FROM golang:1.26-alpine AS build
WORKDIR /src
# Modules first: this layer caches across source-only changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG SERVICE
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test -n "${SERVICE}" || { echo "Dockerfile: SERVICE build arg is required" >&2; exit 1; }; \
    CGO_ENABLED=0 go build -o /out/service ./cmd/${SERVICE}

# ---- Run: distroless static (CA certs + nonroot user), binary only ----
# The service binds :$PORT on all interfaces (Railway private networking is
# IPv6); Railway health-checks /healthz (registered by the platform kit).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/service /service
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/service"]
