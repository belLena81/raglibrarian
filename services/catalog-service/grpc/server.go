package cataloggrpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
)

// Server exposes Catalog's health-only contract until catalog capabilities are delivered.
type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
}

// NewServer constructs the Catalog gRPC adapter.
func NewServer() *Server { return &Server{} }

// Check reports the Catalog process contract status.
func (*Server) Check(ctx context.Context, _ *catalogv1.CheckRequest) (*catalogv1.CheckResponse, error) {
	if ctx.Err() != nil {
		return nil, status.Error(codes.Canceled, "request cancelled")
	}
	return &catalogv1.CheckResponse{Status: "SERVING"}, nil
}
