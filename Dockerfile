# Shared build for every Go service (M2-04). The build context MUST be the repo
# root: each binary shares the root go.mod/go.sum and the internal/ packages.
# Parameterized by a single build arg — the image is the static binary compiled
# from ./cmd/${SERVICE}. Do not add a per-service Dockerfile for a GO service: that is
# what this file is for, and a second one would fork the build contract. Non-Go services
# carry their own, which was already true of the four SPAs before it was written down
# (frontend/<app>/Dockerfile) and is true of the Python sidecar (sidecar/<svc>/Dockerfile).
#
# Pairs with Dockerfile.dockerignore: BuildKit resolves the Dockerfile-adjacent
# ignore ahead of the root .dockerignore (which is tuned for the SPA images and
# excludes all Go source), so this build sees cmd/ and internal/ while the SPA
# builds keep their lean context.
#
# No `# syntax=` directive: it makes BuildKit fetch its frontend from Docker Hub
# before every build. Nothing here needs a frontend newer than the built-in one.

ARG SERVICE

# ---- Build: compile ./cmd/${SERVICE} into a static, CGO-free binary ----
# Pinned to the go.mod version; TestGoPins_RepoAgree fails when the two differ.
# Go never downloads a toolchain at build time.
# Pulled through mirror.gcr.io, not docker.io: no Docker Hub rate limit.
FROM mirror.gcr.io/library/golang:1.26.10-alpine AS build
WORKDIR /src
# Modules first: this COPY+download layer is reused across source-only changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SERVICE
# cmd/${SERVICE}/build.tags is optional; gateway's is empty on main and stamped only for PR deploys and the CI canary.
RUN test -n "${SERVICE}" || { echo "Dockerfile: SERVICE build arg is required" >&2; exit 1; }; \
    CGO_ENABLED=0 go build -tags "$(cat "cmd/${SERVICE}/build.tags" 2>/dev/null)" -o /out/service ./cmd/${SERVICE}

# ---- Run: distroless static (CA certs + nonroot user), binary only ----
# The service binds :$PORT on all interfaces; Cloud Run injects PORT.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/service /service
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/service"]
