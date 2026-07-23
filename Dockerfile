FROM golang:1.26.5-alpine AS builder
ARG SERVICE
ARG SERVICE_COMMAND=cmd
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
    && PATH="$(go env GOPATH)/bin:$PATH" protoc --experimental_allow_proto3_optional -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto api/proto/ingestion/v1/ingestion.proto api/proto/retrieval/v1/retrieval.proto api/proto/answer/v1/answer.proto \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/service ./services/${SERVICE}/${SERVICE_COMMAND} \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./tools/healthcheck

FROM builder AS contract-tests
RUN go -C /src/tests/e2e mod download \
    && go -C /src/services/identity-service mod download \
    && go -C /src/services/catalog-service mod download \
    && go -C /src/services/ingestion-service mod download \
    && go -C /src/services/retrieval-service mod download \
    && go -C /src/services/answer-service mod download
ENTRYPOINT ["/bin/sh", "-ec"]
CMD ["grep -q '[^[:space:]]' \"$IDENTITY_POSTGRES_DSN_FILE\" && grep -q '[^[:space:]]' \"$IDENTITY_MIGRATION_POSTGRES_DSN_FILE\" && grep -q '[^[:space:]]' \"$CATALOG_POSTGRES_DSN_FILE\" && grep -q '[^[:space:]]' \"$CATALOG_RABBITMQ_URI_FILE\" && grep -q '[^[:space:]]' \"$PGPASSFILE\" && grep -q '[^[:space:]]' \"$INGESTION_POSTGRES_DSN_FILE\" && test \"$CATALOG_MIGRATION_INTEGRATION\" = true && go -C /src/tests/e2e test -count=1 -v -tags=e2e -run '^TestGRPC' ./... && go -C /src/services/identity-service test -count=1 -v -tags=integration ./repository && go -C /src/services/identity-service test -count=1 -v -tags=integration ./migrations && go -C /src/services/catalog-service test -count=1 -v -tags=integration ./repository && go -C /src/services/catalog-service test -count=1 -v -tags=integration ./outbox && catalog_migration_tests=\"$(go -C /src/services/catalog-service test -tags=integration -list '^TestCatalogMigrationsRebuildCleanly$' ./migrations)\" && printf '%s\\n' \"$catalog_migration_tests\" | grep -qx 'TestCatalogMigrationsRebuildCleanly' && { catalog_migration_output=\"$(go -C /src/services/catalog-service test -count=1 -v -tags=integration -run '^TestCatalogMigrationsRebuildCleanly$' ./migrations 2>&1)\" || { status=$?; printf '%s\\n' \"${catalog_migration_output:-catalog migration integration test failed before producing output}\"; exit \"$status\"; }; printf '%s\\n' \"$catalog_migration_output\"; if printf '%s\\n' \"$catalog_migration_output\" | grep -q -- '^--- SKIP: TestCatalogMigrationsRebuildCleanly'; then echo 'Catalog migration integration test was skipped' >&2; exit 1; fi; } && go -C /src/services/ingestion-service test -count=1 -v -tags=integration ./..."]

FROM alpine:3.22 AS tokenizer
# The checksum pins the official cl100k_base vocabulary. It is fetched during
# the image build so workers never make runtime network calls on their hot path.
SHELL ["/bin/ash", "-o", "pipefail", "-c"]
RUN wget -q -O /cl100k_base.tiktoken https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken \
    && echo "223921b76ee99bde995b7ff738513eef100fb51d18c93597a113bcffe865b2a7  /cl100k_base.tiktoken" | sha256sum -c -

FROM builder AS ingestion-sandbox-builder
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/parser-sandbox ./services/ingestion-service/cmd/parser_sandbox \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/epub-parser ./services/ingestion-service/cmd/epub_parser

FROM alpine:3.22 AS ingestion-runtime
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates poppler-utils
COPY --from=builder /bin/service /service
COPY --from=builder /bin/healthcheck /healthcheck
COPY --from=ingestion-sandbox-builder /bin/parser-sandbox /parser-sandbox
COPY --from=ingestion-sandbox-builder /bin/epub-parser /usr/local/bin/epub-parser
COPY --from=tokenizer /cl100k_base.tiktoken /opt/raglibrarian/cl100k_base.tiktoken
# The worker reads root-owned 0400 secrets, then permanently drops to the
# configured non-root identity before consuming work.
# hadolint ignore=DL3002
USER root:root
ENTRYPOINT ["/service"]

FROM ingestion-runtime AS ingestion-lambda-runtime
# Lambda container images run the handler directly through aws-lambda-go's
# Runtime API client. Secrets must be mounted readable by this identity.
USER 65532:65532
ENTRYPOINT ["/service"]

FROM gcr.io/distroless/static:nonroot AS retrieval-runtime
COPY --from=builder /bin/service /service
COPY --from=builder /bin/healthcheck /healthcheck
# The process reads root-owned Compose secrets, then drops permanently to the
# configured numeric runtime identity before opening its private listeners.
# hadolint ignore=DL3002
USER root:root
ENTRYPOINT ["/service"]

FROM gcr.io/distroless/static:nonroot AS retrieval-lambda-runtime
COPY --from=builder /bin/service /service
USER 65532:65532
ENTRYPOINT ["/service"]

FROM gcr.io/distroless/static:nonroot AS service-runtime
COPY --from=builder /bin/service /service
COPY --from=builder /bin/healthcheck /healthcheck
# hadolint ignore=DL3002
USER root:root
ENTRYPOINT ["/service"]

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/service /service
COPY --from=builder /bin/healthcheck /healthcheck
# The process starts with only SETUID/SETGID capabilities so it can read its
# root-owned 0400 Compose secrets and permanently drop to UID/GID 65532 before
# accepting traffic. The filesystem remains read-only at runtime.
# hadolint ignore=DL3002
USER root:root
ENTRYPOINT ["/service"]
