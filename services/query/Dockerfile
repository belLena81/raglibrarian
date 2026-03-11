# ── Stage 1: build ────────────────────────────────────────────────────────────
# Use the official Go image. Pin the minor version so builds are reproducible.
# The builder stage is discarded after compilation — it never ships.
FROM golang:1.26-alpine AS builder

# Install git so `go mod download` can fetch modules that use git sources.
# ca-certificates is needed for HTTPS module downloads.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Copy the workspace file first so the module resolver knows the layout.
COPY go.work go.work.sum* ./

# Copy every module that the query service depends on.
# Listed in dependency order (leaves first) so Docker layer cache is reused
# when only service code changes, not shared packages.
COPY pkg/domain/go.mod   pkg/domain/go.sum*   pkg/domain/
COPY pkg/auth/go.mod     pkg/auth/go.sum*      pkg/auth/
COPY pkg/logger/go.mod   pkg/logger/go.sum*    pkg/logger/
COPY pkg/config/go.mod   pkg/config/go.sum*    pkg/config/
COPY services/metadata/go.mod services/metadata/go.sum* services/metadata/
COPY services/query/go.mod    services/query/go.sum*    services/query/

# Download dependencies before copying source so this layer is cached
# as long as go.mod/go.sum files do not change.
RUN go work sync && go mod download -modfile services/query/go.mod

# Copy all source code.
COPY pkg/       pkg/
COPY services/  services/

# Build a statically linked binary.
# CGO_ENABLED=0  — no C runtime dependency, works in scratch/distroless images.
# -trimpath      — removes local file paths from the binary (smaller + safer).
# -ldflags       — strips debug symbols and DWARF to reduce binary size.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /bin/query \
    ./services/query/cmd/main.go

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
# distroless/static has no shell, no package manager, no libc — minimal attack
# surface. The binary must be statically linked (CGO_ENABLED=0 above).
FROM gcr.io/distroless/static:nonroot

# Copy the CA bundle so outbound TLS calls (future: LLM API) work.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy only the compiled binary.
COPY --from=builder /bin/query /query

# Run as the nonroot user provided by distroless (uid 65532).
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/query"]