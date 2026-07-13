package main

import (
	"context"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
)

type server struct { catalogv1.UnimplementedCatalogServiceServer }
func (server) Check(ctx context.Context, _ *catalogv1.CheckRequest) (*catalogv1.CheckResponse, error) {
	if ctx.Err() != nil { return nil, status.Error(codes.Canceled, "request cancelled") }
	return &catalogv1.CheckResponse{Status: "SERVING"}, nil
}
func main() {
	addr := os.Getenv("CATALOG_GRPC_ADDR"); if addr == "" { addr = ":50052" }
	lis, err := net.Listen("tcp", addr); if err != nil { panic(err) }
	grpcServer := grpc.NewServer(); catalogv1.RegisterCatalogServiceServer(grpcServer, server{}); if err = grpcServer.Serve(lis); err != nil { panic(err) }
}
