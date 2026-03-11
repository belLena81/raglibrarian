# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Workspace file first — establishes the module graph for go work sync.
COPY go.work go.work.sum* ./

# go.mod/go.sum for every module the metadata service depends on, leaves first.
# pkg/proto is included because metadata imports metadatapb from it.
COPY pkg/domain/go.mod    pkg/domain/go.sum*    pkg/domain/
COPY pkg/auth/go.mod      pkg/auth/go.sum*       pkg/auth/
COPY pkg/logger/go.mod    pkg/logger/go.sum*     pkg/logger/
COPY pkg/config/go.mod    pkg/config/go.sum*     pkg/config/
COPY pkg/proto/go.mod     pkg/proto/go.sum*      pkg/proto/
COPY services/metadata/go.mod services/metadata/go.sum* services/metadata/

# Cache the dependency download layer separately from source.
RUN go work sync && go mod download -modfile services/metadata/go.mod

# Copy all source after deps are cached.
COPY pkg/       pkg/
COPY services/  services/

# Statically linked binary — required for distroless/static runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /bin/metadata \
    ./services/metadata/cmd/main.go

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
# distroless/static: no shell, no libc, minimal CVE surface.
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/metadata /metadata

USER nonroot:nonroot

# gRPC port — must match GRPC_ADDR env var in docker-compose.yml.
EXPOSE 9090

ENTRYPOINT ["/metadata"]