FROM golang:1.26.5-alpine AS builder
ARG SERVICE
WORKDIR /src

# Keep the build context explicit: operational files, local secrets, UI assets,
# and repository metadata never enter service image layers.
COPY go.work go.work.sum ./
COPY api/proto ./api/proto
COPY pkg ./pkg
COPY services ./services
COPY tools ./tools
COPY tests ./tests

# The Go base image is patch-version pinned. The Alpine package revision stays
# movable so patched repository packages remain consumable; the resulting
# service images are blocked by the pinned vulnerability scan gate.
# hadolint ignore=DL3018
RUN apk add --no-cache protobuf protobuf-dev \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1 \
    && PATH="$(go env GOPATH)/bin:$PATH" protoc -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/service ./services/${SERVICE}/cmd \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./tools/healthcheck

FROM builder AS contract-tests
RUN go -C /src/tests/e2e mod download \
    && go -C /src/services/identity-service mod download \
    && go -C /src/services/catalog-service mod download
ENTRYPOINT ["/bin/sh", "-ec"]
CMD ["grep -q '[^[:space:]]' \"$IDENTITY_POSTGRES_DSN_FILE\" && grep -q '[^[:space:]]' \"$IDENTITY_MIGRATION_POSTGRES_DSN_FILE\" && grep -q '[^[:space:]]' \"$CATALOG_POSTGRES_DSN_FILE\" && grep -q '[^[:space:]]' \"$CATALOG_RABBITMQ_URI_FILE\" && go -C /src/tests/e2e test -count=1 -v -tags=e2e -run '^TestGRPC' ./... && go -C /src/services/identity-service test -count=1 -v -tags=integration ./repository && go -C /src/services/identity-service test -count=1 -v -tags=integration ./migrations && go -C /src/services/catalog-service test -count=1 -v -tags=integration ./repository && go -C /src/services/catalog-service test -count=1 -v -tags=integration ./outbox"]

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/service /service
COPY --from=builder /bin/healthcheck /healthcheck
# The process starts with only SETUID/SETGID capabilities so it can read its
# root-owned 0400 Compose secrets and permanently drop to UID/GID 65532 before
# accepting traffic. The filesystem remains read-only at runtime.
# hadolint ignore=DL3002
USER root:root
ENTRYPOINT ["/service"]
