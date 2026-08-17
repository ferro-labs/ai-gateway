# The one image the project serves, from one file and one build context.
#
#   docker build --target gateway -t ferrogw .
#
# Or, from the repository root: make docker-build.
#
# One container, one process. The dashboard is no longer a container of its
# own: the gateway compiles the bundle into its binary with go:embed and serves
# it from the same port as the API, so the node stage below exists only to
# produce that bundle for the Go stage to swallow.
#
# ── Stage order is load-bearing ──────────────────────────────────────────────
# "gateway" is last on purpose. Render's blueprint spec exposes dockerfilePath
# and dockerContext but no build target, so it always builds the final stage —
# and render.yaml deploys the gateway. Moving another stage below it would
# silently start deploying that one instead.
#
# The build context is the repository root, which is what lets one file cover
# both the frontend and the Go build. Docker reads a single .dockerignore from
# that root, so the dashboard's exclusions live there too, under web/ prefixes.

# ── Dashboard bundle ─────────────────────────────────────────────────────────

FROM node:22-alpine AS web-build

WORKDIR /src

# Manifests first so a source-only edit reuses the install layer.
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# ── Gateway ──────────────────────────────────────────────────────────────────

FROM golang:1.25.13-alpine AS gateway-build

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

# The Go inputs are listed rather than copied wholesale with `COPY . .`. Sharing
# one context with the dashboard means a `COPY . .` here would take web/ too, so
# editing a stylesheet would invalidate this layer and rerun the whole Go build.
# A new top-level Go package has to be added below, which fails the build with
# an unresolved import rather than quietly shipping without it.
COPY *.go ./
COPY cmd/ ./cmd/
COPY config/ ./config/
COPY internal/ ./internal/
COPY mcp/ ./mcp/
COPY models/ ./models/
COPY observability/ ./observability/
COPY pkg/ ./pkg/
COPY plugin/ ./plugin/
COPY providers/ ./providers/

# The bundle arrives from the node stage, not from the build context —
# .dockerignore excludes internal/webui/dist so a locally built copy cannot be
# sent, and this is the only thing that puts one there. It has to land inside
# internal/webui/ because go:embed cannot reference a path outside its own
# package directory.
#
# Last before the build on purpose: a frontend-only edit then invalidates just
# these two layers instead of re-running the source COPYs above, and a Go-only
# edit costs one cached inter-stage copy. Either way it stays below
# `go mod download`, the expensive layer neither should touch.
COPY --from=web-build /src/dist ./internal/webui/dist

# The whole link-flag string arrives as one build arg rather than a
# VERSION/COMMIT/DATE triple this file would have to restring into -X flags.
# The Makefile already builds that string for the host build (LDFLAGS), so
# `make docker-build` passes it verbatim and a source-built image stamps the
# same version the equivalent `make build` would — there is no second copy of
# the -X paths here to drift from it.
#
# The default is the bare strip: a plain `docker build` still produces the same
# small binary, it just reports the version package's own dev/none/unknown
# rather than claiming a release it is not.
#
# Declared here, immediately above its only use, because an ARG invalidates
# every layer below it and this one changes on every commit.
ARG LDFLAGS="-s -w"

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="${LDFLAGS}" -o /bin/ferrogw ./cmd/ferrogw

FROM alpine:3.24 AS gateway

RUN apk add --no-cache ca-certificates && \
    addgroup -S ferro && adduser -S ferro -G ferro && \
    mkdir -p /app && chown ferro:ferro /app

COPY --from=gateway-build --chown=ferro:ferro /bin/ferrogw /bin/ferrogw

WORKDIR /app

EXPOSE 8080

USER ferro

# /readyz, not /livez: an orchestrator that only knows the process is alive will
# route traffic to an instance whose every target is unroutable. wget is in
# busybox, so this adds no package.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/readyz || exit 1

ENTRYPOINT ["/bin/ferrogw"]
