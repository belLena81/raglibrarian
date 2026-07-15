FROM golang:1.26.5-alpine AS builder
ARG SERVICE
WORKDIR /src
COPY . .
RUN apk add --no-cache protobuf \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1 \
    && PATH="$(go env GOPATH)/bin:$PATH" protoc -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/service ./services/${SERVICE}/cmd \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./tools/healthcheck

FROM builder AS contract-tests
ENTRYPOINT ["/bin/sh", "-ec"]
CMD ["cd /src/tests/e2e && go test -v -tags=e2e -run '^TestGRPC' ./... && cd /src/services/identity-service && go test -v -tags=integration ./repository"]

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/service /service
COPY --from=builder /bin/healthcheck /healthcheck
# Compose file-backed secrets preserve their host-side 0600 permissions. The
# service loads only its assigned secrets, then drops to this image's 65532
# non-root identity before accepting traffic. Health probes remain isolated
# processes and need root only to read the same service-specific probe files.
USER root:root
ENTRYPOINT ["/service"]
