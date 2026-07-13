FROM golang:1.26-alpine AS builder
ARG SERVICE
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/service ./services/${SERVICE}/cmd

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/service /service
USER nonroot:nonroot
ENTRYPOINT ["/service"]
