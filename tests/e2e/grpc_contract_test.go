//go:build e2e

package e2e_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
)

func TestGRPCStandardHealthAndCatalogCheck(t *testing.T) {
	requireContractTests(t)
	identityConn := dialMTLS(t, envOr("IDENTITY_GRPC_ADDR", "identity-service:50051"), "identity-service", "EDGE_TLS_CERT_FILE", "EDGE_TLS_KEY_FILE")
	catalogConn := dialMTLS(t, envOr("CATALOG_GRPC_ADDR", "catalog-service:50052"), "catalog-service", "EDGE_TLS_CERT_FILE", "EDGE_TLS_KEY_FILE")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	identityHealth, err := grpc_health_v1.NewHealthClient(identityConn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, identityHealth.GetStatus())
	catalogHealth, err := grpc_health_v1.NewHealthClient(catalogConn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, catalogHealth.GetStatus())
	checked, err := catalogv1.NewCatalogServiceClient(catalogConn).Check(ctx, &catalogv1.CheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, "SERVING", checked.GetStatus())
}

func TestGRPCIdentityRejectsCatalogClientCertificate(t *testing.T) {
	requireContractTests(t)
	conn := dialMTLS(t, envOr("IDENTITY_GRPC_ADDR", "identity-service:50051"), "identity-service", "CATALOG_TLS_CERT_FILE", "CATALOG_TLS_KEY_FILE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := identityv1.NewIdentityServiceClient(conn).Register(ctx, &identityv1.RegisterRequest{Email: "rejected@example.test", Password: validPassword})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestGRPCIdentityRejectsUnknownClientCertificate(t *testing.T) {
	requireContractTests(t)
	conn := dialMTLS(t, envOr("IDENTITY_GRPC_ADDR", "identity-service:50051"), "identity-service", "UNKNOWN_TLS_CERT_FILE", "UNKNOWN_TLS_KEY_FILE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := identityv1.NewIdentityServiceClient(conn).Register(ctx, &identityv1.RegisterRequest{Email: "unknown@example.test", Password: validPassword})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestGRPCCatalogRejectsNonEdgeClientCertificates(t *testing.T) {
	requireContractTests(t)
	target := envOr("CATALOG_GRPC_ADDR", "catalog-service:50052")
	for _, certificate := range []struct {
		name    string
		certEnv string
		keyEnv  string
	}{
		{name: "catalog service", certEnv: "CATALOG_TLS_CERT_FILE", keyEnv: "CATALOG_TLS_KEY_FILE"},
		{name: "unknown client", certEnv: "UNKNOWN_TLS_CERT_FILE", keyEnv: "UNKNOWN_TLS_KEY_FILE"},
	} {
		t.Run(certificate.name, func(t *testing.T) {
			conn := dialMTLS(t, target, "catalog-service", certificate.certEnv, certificate.keyEnv)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, err := catalogv1.NewCatalogServiceClient(conn).ListBooks(ctx, &catalogv1.ListBooksRequest{})

			assert.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

func TestGRPCCatalogEnforcesForwardedActorPolicy(t *testing.T) {
	requireContractTests(t)
	conn := dialMTLS(t, envOr("CATALOG_GRPC_ADDR", "catalog-service:50052"), "catalog-service", "EDGE_TLS_CERT_FILE", "EDGE_TLS_KEY_FILE")
	client := catalogv1.NewCatalogServiceClient(conn)

	for _, request := range []*catalogv1.ListBooksRequest{
		{Actor: &catalogv1.Actor{UserId: "reader", Role: "reader", Status: "pending"}},
		{Actor: &catalogv1.Actor{UserId: "librarian", Role: "librarian", Status: "inactive"}},
		{Actor: &catalogv1.Actor{UserId: "unknown", Role: "unknown", Status: "active"}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.ListBooks(ctx, request)
		cancel()

		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	}
}

func TestGRPCDeadlineIsHonored(t *testing.T) {
	requireContractTests(t)
	conn := dialMTLS(t, envOr("CATALOG_GRPC_ADDR", "catalog-service:50052"), "catalog-service", "EDGE_TLS_CERT_FILE", "EDGE_TLS_KEY_FILE")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := catalogv1.NewCatalogServiceClient(conn).Check(ctx, &catalogv1.CheckRequest{})
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

func requireContractTests(t *testing.T) {
	t.Helper()
	if os.Getenv("GRPC_CONTRACT_TESTS") != "true" {
		t.Skip("set GRPC_CONTRACT_TESTS=true inside the Compose test network")
	}
}

func dialMTLS(t *testing.T, target, serverName, certEnv, keyEnv string) *grpc.ClientConn {
	t.Helper()
	ca, err := os.ReadFile(os.Getenv("INTERNAL_TLS_CA_FILE")) // #nosec G703 -- test-only configured fixture path
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(ca))
	certificate, err := tls.LoadX509KeyPair(os.Getenv(certEnv), os.Getenv(keyEnv))
	require.NoError(t, err)
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: serverName,
	})))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	return conn
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
