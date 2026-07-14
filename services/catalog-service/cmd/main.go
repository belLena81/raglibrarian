package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
)

type server struct {
	catalogv1.UnimplementedCatalogServiceServer
}

func (server) Check(ctx context.Context, _ *catalogv1.CheckRequest) (*catalogv1.CheckResponse, error) {
	if ctx.Err() != nil {
		return nil, status.Error(codes.Canceled, "request cancelled")
	}

	return &catalogv1.CheckResponse{Status: "SERVING"}, nil
}

func main() {
	addr := os.Getenv("CATALOG_GRPC_ADDR")
	if addr == "" {
		addr = ":50052"
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}

	creds, err := serverCredentials()
	if err != nil {
		panic(err)
	}

	grpcServer := grpc.NewServer(grpc.Creds(creds))
	catalogv1.RegisterCatalogServiceServer(grpcServer, server{})

	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)

	if err := grpcServer.Serve(listener); err != nil {
		panic(err)
	}
}

func serverCredentials() (credentials.TransportCredentials, error) {
	ca, err := os.ReadFile(os.Getenv("INTERNAL_TLS_CA_FILE")) // #nosec G703 -- operator-controlled runtime configuration
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, os.ErrInvalid
	}

	cert, err := tls.LoadX509KeyPair(
		os.Getenv("CATALOG_TLS_CERT_FILE"),
		os.Getenv("CATALOG_TLS_KEY_FILE"),
	)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}
