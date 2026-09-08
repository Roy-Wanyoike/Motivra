# Motivra service image — one image per deployable service.
#
# Build a service with:
#   docker build --build-arg SERVICE=identity -t motivra-identity:dev .
#
# SERVICE selects the cmd/<SERVICE> entrypoint. Allowed values are exactly
# the deployable services in cmd/ (ADR-0001: "cmd/<service>/main.go — one
# entrypoint per deployable service"): identity, vehicles, jobs, dispatch.
#
# Env contract: backend/platform/config.go (see .env.example). The server
# mounts liveness at GET /healthz and readiness at GET /readyz
# (backend/platform/server.go) — the healthcheck below uses the real
# liveness path.

# --- Build stage -------------------------------------------------------------
# golang image pinned to the go directive in go.mod (currently 1.27.1).
# Bump both together.
FROM golang:1.27.1-alpine AS build

ARG SERVICE=identity

# Fail fast (and loudly) on unknown services instead of silently producing
# a broken image; also rules out path traversal via --build-arg.
RUN case "$SERVICE" in \
      identity|vehicles|jobs|dispatch) ;; \
      *) echo "ERROR: SERVICE must be one of: identity, vehicles, jobs, dispatch (got '$SERVICE')" >&2; exit 1 ;; \
    esac

WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary: no CGO, trimmed paths, no debug info in the runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/motivra-"$SERVICE" ./cmd/"$SERVICE"

# --- Runtime stage -----------------------------------------------------------
FROM alpine:3.21

LABEL org.opencontainers.image.title="Motivra service" \
      org.opencontainers.image.description="Motivra backend service image (built from cmd/\${SERVICE})" \
      org.opencontainers.image.source="https://github.com/Roy-Wanyoike/Motivra"

# ca-certificates: outbound TLS (OTel exporter, NATS/Redis/Postgres TLS,
# future provider integrations). wget (busybox) powers the healthcheck.
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 motivra \
 && adduser -u 10001 -G motivra -D -H -s /sbin/nologin motivra

COPY --from=build /out/motivra-* /usr/local/bin/motivra-service

# Non-root, no shell. Numeric UID/GID so runAsNonRoot checks resolve.
USER 10001:10001

# Platform config default (backend/platform/config.go: MOTIVRA_PORT -> "8080").
ENV MOTIVRA_PORT=8080
EXPOSE 8080

# Liveness endpoint registered by platform.NewServer (backend/platform/server.go):
# GET /healthz. Readiness (dependency checks) is GET /readyz — compose and
# orchestrators that gate on dependencies should probe /readyz instead.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${MOTIVRA_PORT}/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/motivra-service"]
