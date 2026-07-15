// Package grpcauth authorizes verified internal gRPC peer identities.
package grpcauth

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Policy protects every method of one gRPC service for one verified peer SAN.
type Policy struct{ Service, DNSName string }

// UnaryServerInterceptor enforces policy while leaving unrelated services, such as health, untouched.
func UnaryServerInterceptor(policy Policy) grpc.UnaryServerInterceptor {
	if policy.Service == "" || policy.DNSName == "" {
		panic("grpcauth: service and DNS name are required")
	}
	prefix := "/" + policy.Service + "/"
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		peerInfo, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing peer identity")
		}
		tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
		if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing peer certificate")
		}
		if err := tlsInfo.State.PeerCertificates[0].VerifyHostname(policy.DNSName); err != nil {
			return nil, status.Error(codes.PermissionDenied, "caller is not authorized")
		}
		return handler(ctx, req)
	}
}
