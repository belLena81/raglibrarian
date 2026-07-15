package grpcauth_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/grpcauth"
)

func TestInterceptorAuthorizesVerifiedDNSSAN(t *testing.T) {
	interceptor := grpcauth.UnaryServerInterceptor(grpcauth.Policy{Service: "identity.v1.IdentityService", DNSName: "edge-api"})
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tlsState("edge-api")}})
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/identity.v1.IdentityService/Login"}, func(context.Context, any) (any, error) { return "ok", nil })
	assert.NoError(t, err)
}

func TestInterceptorRejectsWrongDNSSAN(t *testing.T) {
	interceptor := grpcauth.UnaryServerInterceptor(grpcauth.Policy{Service: "identity.v1.IdentityService", DNSName: "edge-api"})
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tlsState("catalog-service")}})
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/identity.v1.IdentityService/Login"}, func(context.Context, any) (any, error) { return nil, nil })
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestInterceptorLeavesHealthServiceToMTLSChainValidation(t *testing.T) {
	interceptor := grpcauth.UnaryServerInterceptor(grpcauth.Policy{Service: "identity.v1.IdentityService", DNSName: "edge-api"})
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, func(context.Context, any) (any, error) { return "ok", nil })
	assert.NoError(t, err)
}

func tlsState(dnsName string) tls.ConnectionState {
	return tls.ConnectionState{PeerCertificates: []*x509.Certificate{{DNSNames: []string{dnsName}}}}
}
