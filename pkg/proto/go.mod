// Shared protobuf definitions and generated gRPC stubs.
// No business logic — only generated code and its direct dependencies.

module github.com/belLena81/raglibrarian/pkg/proto

go 1.26

require (
    google.golang.org/grpc       v1.70.0
    google.golang.org/protobuf   v1.36.5

    // googleapis/rpc provides status.proto and common gRPC error types
    google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f
)