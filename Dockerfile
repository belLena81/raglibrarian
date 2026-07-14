FROM golang:1.26.5-alpine AS builder
ARG SERVICE
WORKDIR /src
COPY . .
RUN apk add --no-cache protobuf \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1 \
    && PATH="$(go env GOPATH)/bin:$PATH" protoc -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/service ./services/${SERVICE}/cmd

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/service /service
USER nonroot:nonroot
ENTRYPOINT ["/service"]
